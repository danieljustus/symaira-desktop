"""VALUE-002 harness controls; these tests never measure a benchmark.

The positive control is a real, structurally complete *smoke* capture (4
samples, not evidence) kept as a fixture; every negative control tampers with
a copy and must be rejected by the fail-closed validator.
"""

import copy
import io
import json
import os
import subprocess
import tarfile
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import value001
import value002

ROOT = Path(__file__).resolve().parents[2]
FIXTURE = ROOT / "docs/rust-port/results/value002-smoke-fixture.json"


def load_fixture():
    return json.loads(FIXTURE.read_text(encoding="utf-8"))


def revalidate(result):
    value002.validate_result(result, allow_smoke=True)


class ProfileTests(unittest.TestCase):
    def test_final_profile_pins_100_samples_and_20_warmups(self):
        for samples, warmups in ((99, 20), (100, 19), (4, 2)):
            with self.subTest(samples=samples, warmups=warmups):
                with self.assertRaises(value002.HarnessError):
                    with value002.profile_constants("final", samples, warmups):
                        pass
        with value002.profile_constants("final", 100, 20):
            self.assertEqual(value001.MIN_SAMPLES, 100)

    def test_smoke_profile_restores_value001_constants(self):
        with value002.profile_constants("smoke", 4, 2):
            self.assertEqual(value001.CURRENT_SAMPLES, 4)
        after = (
            value001.MIN_SAMPLES,
            value001.CURRENT_SAMPLES,
            value001.CURRENT_WARMUPS,
            value001.MIN_ORDER_COHORT_SAMPLES,
        )
        self.assertEqual(after, (100, 100, 20, 50))

    def test_thresholds_are_the_value001_thresholds(self):
        self.assertEqual(value001.MAXIMUM_LATENCY_REGRESSION, 0.10)
        self.assertEqual(value001.MAXIMUM_LATENCY_INTERVAL_WIDTH, 0.40)
        self.assertEqual(value001.MEDIAN_INTERVAL_CONFIDENCE, 0.95)
        thresholds = load_fixture()["thresholds"]
        self.assertEqual(thresholds["minimum_improvement"], 0.20)
        self.assertEqual(thresholds["maximum_latency_regression"], 0.10)
        self.assertEqual(thresholds["latency_interval_width_limit"], 0.40)


class QuietHostTests(unittest.TestCase):
    PS = "\n".join(
        [
            "  100 /bin/zsh -lic cd repo; cargo build --release; xcodebuild test",
            "  101 /Users/daniel/.rustup/toolchains/1.98.0/bin/cargo test -p symdesk-ingest",
            "  102 /Users/daniel/.rustup/toolchains/1.98.0/bin/rustc --crate-name x",
            "  103 /usr/bin/xcodebuild -scheme SymDesk",
            "  104 /Users/daniel/Library/Caches/go-build/46/abc-d/portgen --check",
            "  105 /usr/bin/swift-frontend -frontend",
            "  106 python3 scripts/rust-port/value002.py run",
            "  107 /usr/bin/pgrep -fl cargo",
        ]
    )

    def test_only_real_build_executables_count(self):
        found = value002.parse_build_processes(self.PS, own_pids=set())
        self.assertEqual(sorted(line.split()[0] for line in found), ["101", "102", "103", "104", "105"])

    def test_own_processes_are_excluded(self):
        found = value002.parse_build_processes(self.PS, own_pids={101, 102, 103, 104, 105})
        self.assertEqual(found, [])

    def test_final_run_refuses_a_busy_host_before_creating_output(self):
        busy = {
            "load1": 30.0, "load5": 1.0, "load15": 1.0, "uptime": "", "captured_at": "x",
            "build_processes": [], "quiet": False,
        }
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "out.json"
            record = Path(directory) / "record.json"
            record.write_text(json.dumps(load_fixture()["build_record"]))
            args = value002.parse_args(
                [
                    "run", "--root", str(ROOT), "--build-record", str(record), "--go-archive", "g",
                    "--rust-archive", "r", "--contract-tools", directory, "--output", str(output),
                    "--binary-dir", directory,
                ]
            )
            with patch.object(value002, "host_snapshot", return_value=busy):
                with self.assertRaisesRegex(value002.HarnessError, "host is not quiet"):
                    value002.run_value002(args)
            self.assertFalse(output.exists())
            self.assertFalse(Path(str(output) + ".incomplete").exists())


class LinkedVaultTests(unittest.TestCase):
    def test_link_targets_are_distinct_and_never_self(self):
        for index in range(value001.DOC_COUNT):
            a, b, c = value002.link_targets(index)
            self.assertEqual(len({index, a, b, c}), 4, index)

    def test_linked_content_keeps_the_value001_anchor_and_adds_links(self):
        content = value002.linked_document_content(42)
        self.assertIn(value001.SEARCH_TOKEN, content)
        self.assertTrue(content.startswith("---\ntitle: Value Gate Note 00042\n"))
        a, b, c = value002.link_targets(42)
        self.assertIn(f'related: "[[note-{c:05d}]]"\n---\n', content)
        self.assertTrue(content.endswith(f"links [[note-{a:05d}]] [[note-{b:05d}]]\n"))

    def test_expected_semantics_come_from_the_generator(self):
        expected = value002.expected_linked()
        self.assertEqual(len(expected["edges"]), 2 * value001.DOC_COUNT)
        self.assertEqual(len(expected["nodes"]), value001.DOC_COUNT)
        self.assertEqual(expected["target"], "cohort-042/note-00042.md")
        self.assertEqual(len(expected["backlinks"]), 2)
        self.assertEqual({prop for _src, prop in expected["inverse"]}, {"_link", "related"})

    @staticmethod
    def graph_json(expected, drop=None, add=None):
        edges = sorted(expected["edges"])
        if drop:
            edges.remove(drop)
        if add:
            edges.append(add)
        return json.dumps(
            {
                "nodes": [{"id": path, "label": title} for path, title in sorted(expected["nodes"].items())],
                "edges": [{"source": s, "target": t} for s, t in edges],
            }
        )

    def test_graph_validator_has_negative_controls(self):
        expected = value002.expected_linked()
        value002.validate_graph(self.graph_json(expected), "", expected)
        missing = sorted(expected["edges"])[0]
        with self.assertRaisesRegex(value002.HarnessError, "graph edges differ"):
            value002.validate_graph(self.graph_json(expected, drop=missing), "", expected)
        extra = ("cohort-000/note-00000.md", "cohort-000/note-00000.md")
        with self.assertRaisesRegex(value002.HarnessError, "graph edges differ"):
            value002.validate_graph(self.graph_json(expected, add=extra), "", expected)
        with self.assertRaisesRegex(value002.HarnessError, "stderr"):
            value002.validate_graph(self.graph_json(expected), "noise", expected)

    def test_backlinks_and_inverse_validators_have_negative_controls(self):
        expected = value002.expected_linked()
        value002.validate_backlinks(json.dumps(expected["backlinks"]), "", expected)
        with self.assertRaisesRegex(value002.HarnessError, "backlinks mismatch"):
            value002.validate_backlinks(json.dumps(expected["backlinks"][:1]), "", expected)
        entries = [
            {"source": src, "title": expected["nodes"][src], "property": prop} for src, prop in expected["inverse"]
        ]
        value002.validate_inverse(json.dumps(entries), "", expected)
        wrong = copy.deepcopy(entries)
        wrong[0]["property"] = "related" if wrong[0]["property"] == "_link" else "_link"
        with self.assertRaisesRegex(value002.HarnessError, "relations inverse mismatch"):
            value002.validate_inverse(json.dumps(wrong), "", expected)
        wrong = copy.deepcopy(entries)
        wrong[0]["title"] = "forged"
        with self.assertRaisesRegex(value002.HarnessError, "title mismatch"):
            value002.validate_inverse(json.dumps(wrong), "", expected)

    def test_duplicates_validator_rejects_empty_and_foreign_paths(self):
        expected = value002.expected_linked()
        member = {"path": "cohort-006/note-00006.md", "title": "t", "similarity": 98}
        group = [{"path": "cohort-000/note-00000.md", "title": "t", "members": [member]}]
        value002.validate_duplicates(json.dumps(group), "", expected)
        with self.assertRaisesRegex(value002.HarnessError, "no groups"):
            value002.validate_duplicates("[]", "", expected)
        group[0]["path"] = "elsewhere.md"
        with self.assertRaisesRegex(value002.HarnessError, "not within the vault"):
            value002.validate_duplicates(json.dumps(group), "", expected)

    def test_symroom_version_contract(self):
        value002.validate_symroom_version("symroom 0.12.2\n", "")
        with self.assertRaises(value002.HarnessError):
            value002.validate_symroom_version("symroom 0.12.3\n", "")


class BuildRecordTests(unittest.TestCase):
    @staticmethod
    def make_repo(directory):
        repo = Path(directory) / "repo"
        repo.mkdir()
        env = {
            **os.environ, "GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t",
            "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t",
        }
        for command in (["init", "-q"], ["config", "commit.gpgsign", "false"]):
            subprocess.run(["git", *command], cwd=repo, check=True, env=env)
        (repo / "a.txt").write_text("a")
        subprocess.run(["git", "add", "."], cwd=repo, check=True, env=env)
        subprocess.run(["git", "commit", "-qm", "init"], cwd=repo, check=True, env=env)
        artifact = Path(directory) / "artifact"
        artifact.write_bytes(b"binary")
        return repo, artifact, env

    def test_failed_builds_and_dirty_trees_are_not_recordable(self):
        with tempfile.TemporaryDirectory() as directory:
            repo, artifact, _env = self.make_repo(directory)
            record = Path(directory) / "record.json"
            with self.assertRaisesRegex(value002.HarnessError, "failed build"):
                value002.record_build(record, repo, "go-symdesk", "cmd", 1, [artifact], {}, "go")
            (repo / "a.txt").write_text("changed")
            with self.assertRaisesRegex(value002.HarnessError, "clean candidate tree"):
                value002.record_build(record, repo, "go-symdesk", "cmd", 0, [artifact], {}, "go")
            self.assertFalse(record.exists())

    def test_record_binds_the_candidate_and_rejects_a_second_commit(self):
        with tempfile.TemporaryDirectory() as directory:
            repo, artifact, env = self.make_repo(directory)
            record = Path(directory) / "record.json"
            value002.record_build(record, repo, "go-symdesk", "cmd", 0, [artifact], {"K": "V"}, "go1")
            stored = json.loads(record.read_text())
            self.assertEqual(stored["candidate"]["commit"], value001.git_output(repo, ["rev-parse", "HEAD"]))
            self.assertEqual(stored["entries"][0]["artifacts"][0]["sha256"], value001.sha256_file(artifact))
            (repo / "a.txt").write_text("next")
            subprocess.run(["git", "commit", "-qam", "next"], cwd=repo, check=True, env=env)
            with self.assertRaisesRegex(value002.HarnessError, "different candidate"):
                value002.record_build(record, repo, "go-symroom", "cmd", 0, [artifact], {}, "go1")

    def test_record_validation_requires_every_build_and_a_clean_tracked_diff(self):
        record = load_fixture()["build_record"]
        value002.validate_build_record(record)
        for name in value002.REQUIRED_BUILDS:
            broken = copy.deepcopy(record)
            broken["entries"] = [e for e in broken["entries"] if e["name"] != name]
            with self.subTest(missing=name), self.assertRaisesRegex(value002.HarnessError, "misses required"):
                value002.validate_build_record(broken)
        broken = copy.deepcopy(record)
        broken["candidate"]["tracked_diff_sha256"] = "1" * 64
        with self.assertRaisesRegex(value002.HarnessError, "dirty tracked tree"):
            value002.validate_build_record(broken)
        broken = copy.deepcopy(record)
        broken["entries"][0]["exit_code"] = 2
        with self.assertRaisesRegex(value002.HarnessError, "did not succeed"):
            value002.validate_build_record(broken)


class ArchiveTests(unittest.TestCase):
    @staticmethod
    def archive(directory, names):
        path = Path(directory) / "a.tar.gz"
        with tarfile.open(path, "w:gz") as tar:
            for name in names:
                data = name.encode()
                info = tarfile.TarInfo(name)
                info.size = len(data)
                tar.addfile(info, io.BytesIO(data))
        return path

    def test_extract_requires_exactly_the_release_members(self):
        with tempfile.TemporaryDirectory() as directory:
            good = self.archive(directory, value002.ARCHIVE_MEMBERS)
            records = value002.extract_archive(good, Path(directory) / "out")
            self.assertEqual(set(records), set(value002.ARCHIVE_MEMBERS))
            self.assertTrue(os.access(Path(directory) / "out/symdesk", os.X_OK))
            bad_sets = (
                value002.ARCHIVE_MEMBERS[:-1],
                value002.ARCHIVE_MEMBERS + ("extra",),
                value002.ARCHIVE_MEMBERS[:-1] + ("../symroom",),
            )
            for names in bad_sets:
                with self.subTest(names=names), self.assertRaisesRegex(value002.HarnessError, "differ from"):
                    value002.extract_archive(self.archive(directory, names), Path(directory) / "bad")


class ValidatorTests(unittest.TestCase):
    def setUp(self):
        self.result = load_fixture()

    def test_fixture_is_a_valid_smoke_capture_but_not_evidence(self):
        revalidate(self.result)
        with self.assertRaisesRegex(value002.HarnessError, "smoke capture is not evidence"):
            value002.validate_result(self.result)

    def assert_rejected(self, mutate, pattern=None):
        result = copy.deepcopy(self.result)
        mutate(result)
        with self.assertRaises(value002.HarnessError) as caught:
            revalidate(result)
        if pattern:
            self.assertRegex(str(caught.exception), pattern)

    def test_top_level_and_identity_tampering(self):
        self.assert_rejected(lambda r: r.update(extra=1), "top-level schema")
        self.assert_rejected(lambda r: r.update(benchmark="VALUE-001"), "benchmark")
        self.assert_rejected(lambda r: r["runner"].update(pairing="other"), "pairing")
        self.assert_rejected(lambda r: r.update(passed=not r["passed"]), "passed flag")

    def test_final_profile_requires_exact_counts(self):
        self.assert_rejected(lambda r: r["runner"].update(profile="final"), "exactly 100 samples")

    def test_quiet_host_is_enforced_for_final_captures(self):
        host = copy.deepcopy(self.result["host"])
        value002.validate_host(host, "smoke")
        mutations = (
            lambda h: h["load_before"].update(load1=6.0),
            lambda h: h["load_before"].update(build_processes=["1 cargo build"]),
            lambda h: h["load_before"].update(quiet=False),
        )
        for mutate in mutations:
            broken = copy.deepcopy(host)
            broken["load_before"].update(load1=1.0, build_processes=[], quiet=True)
            mutate(broken)
            with self.assertRaisesRegex(value002.HarnessError, "quiet host"):
                value002.validate_host(broken, "final")
        broken = copy.deepcopy(host)
        broken["load_before"].update(load1=1.0, build_processes=[], quiet=True)
        value002.validate_host(broken, "final")
        broken["quiet_limit_load1"] = 60.0
        with self.assertRaisesRegex(value002.HarnessError, "quiet limit"):
            value002.validate_host(broken, "final")

    def test_binaries_must_be_the_recorded_builds_and_archive_members(self):
        self.assert_rejected(lambda r: r["binaries"]["go"]["symdesk"].update(sha256="2" * 64), "archive|recorded")
        self.assert_rejected(lambda r: r["binaries"]["rust"]["symroom"].update(sha256="3" * 64), "archive|recorded")
        self.assert_rejected(lambda r: r["archives"]["rust"].update(sha256="4" * 64), "recorded build artifact")
        self.assert_rejected(lambda r: r["archives"].update(published_go_bytes=1), "published Go archive")
        self.assert_rejected(lambda r: r["archives"].update(rust_vs_go_built=0.5), "not recomputed")

    def test_candidate_must_match_and_only_harness_files_may_differ(self):
        self.assert_rejected(lambda r: r["repository"].update(candidate_commit="5" * 40), "candidate")

        def non_harness(r):
            r["repository"]["changed_since_candidate"] = ["crates/symdesk-cli/src/main.rs"]
            r["repository"]["non_harness_changes"] = ["crates/symdesk-cli/src/main.rs"]

        self.assert_rejected(non_harness, "predate non-harness")
        self.assert_rejected(lambda r: r["repository"].update(changed_since_candidate=["go.mod"]), "not recomputed")

    def test_workloads_are_complete_deterministic_and_paired(self):
        for name in value002.NEW_WORKLOADS:
            with self.subTest(missing=name):
                self.assert_rejected(lambda r, name=name: r["workloads"].pop(name), "workload set")
        self.assert_rejected(
            lambda r: r["workloads"]["graph"]["output_sha256"].update(rust="6" * 64), "not recorded as identical"
        )
        self.assert_rejected(lambda r: r["workloads"]["graph"].update(outputs_equal=False), "not recorded as identical")
        self.assert_rejected(lambda r: r["workloads"]["index-build"].update(setup_runs=0), "cold index")
        self.assert_rejected(lambda r: r["workloads"]["graph"]["pair"]["rust"]["raw"].__setitem__(0, 0.0), "invalid raw|raw")
        self.assert_rejected(
            lambda r: r["workloads"]["graph"]["pair"]["rust"].update(pair_order=["go-rust"] * 4), "balanced|aligned|raw"
        )

    def test_slowed_samples_must_agree_with_their_summaries(self):
        def slow(r):
            pair = r["workloads"]["graph"]["pair"]
            pair["rust"]["raw"] = [v * 3 for v in pair["rust"]["raw"]]

        self.assert_rejected(slow, "raw samples|maximum")

    def test_representative_slice_is_validated_by_value001(self):
        self.assert_rejected(lambda r: r["representative"]["metrics"]["startup"]["go"].update(p95=999.0), "p95|raw")
        self.assert_rejected(
            lambda r: r["representative"]["thresholds"].update(
                latency_pass=not r["representative"]["thresholds"]["latency_pass"]
            ),
            "latency pass",
        )
        self.assert_rejected(lambda r: r["representative"]["contracts"][0].update(exit_code=1), "contract record")
        self.assert_rejected(lambda r: r["representative"]["repository"].update(head="7" * 40), "different checkout")
        self.assert_rejected(
            lambda r: r["representative"]["binaries"]["rust"].update(sha256="8" * 64), "not the measured symdesk"
        )

    def test_sustained_load_is_recomputed_and_error_free(self):
        self.assert_rejected(lambda r: r["sustained"]["runs"][0].update(errors=1), "errors")
        self.assert_rejected(lambda r: r["sustained"]["runs"][0].update(http_requests=1), "every declared request")
        self.assert_rejected(lambda r: r["sustained"]["runs"][0]["max_rss_bytes"].update(combined=1), "series")
        self.assert_rejected(lambda r: r["sustained"]["max_rss_bytes"].update(go=1), "recomputed")
        self.assert_rejected(lambda r: r["sustained"].update(http_cycle=["healthz"]), "declared mix")

        def same_order(r):
            for run in r["sustained"]["runs"]:
                run["order"] = "go-rust"

        self.assert_rejected(same_order, "alternate")
        self.assert_rejected(lambda r: r["sustained"]["runs"].pop(), "alternating rounds")

    def test_threshold_edits_are_rejected(self):
        edits = (
            ("minimum_improvement", 0.05),
            ("maximum_latency_regression", 0.5),
            ("latency_interval_width_limit", 9.0),
            ("improvement_pass", False),
        )
        for key, value in edits:
            with self.subTest(key=key):
                self.assert_rejected(lambda r, key=key, value=value: r["thresholds"].update({key: value}), "not recomputed")


class GateArithmeticTests(unittest.TestCase):
    def setUp(self):
        self.result = load_fixture()

    def thresholds(self, mutate):
        result = copy.deepcopy(self.result)
        mutate(result)
        with value002.profile_constants("smoke", 4, 2):
            return value002.compute_thresholds(
                result["representative"], result["binaries"], result["workloads"], result["sustained"]
            )

    @staticmethod
    def equal_sizes(r):
        for member in ("symdesk", "symroom"):
            r["binaries"]["rust"][member]["bytes"] = r["binaries"]["go"][member]["bytes"]

    def test_size_reduction_is_the_combined_dual_binary_figure(self):
        go = self.result["binaries"]["go"]
        rust = self.result["binaries"]["rust"]
        total_go = go["symdesk"]["bytes"] + go["symroom"]["bytes"]
        total_rust = rust["symdesk"]["bytes"] + rust["symroom"]["bytes"]
        thresholds = self.thresholds(lambda r: None)
        self.assertEqual(thresholds["combined_binary_size_reduction"], (total_go - total_rust) / total_go)
        self.assertIn("combined_binary_size", thresholds["improvement_basis"])

    def test_improvement_needs_size_or_representative_rss(self):
        t = self.thresholds(self.equal_sizes)
        self.assertEqual(t["combined_binary_size_reduction"], 0.0)
        self.assertIn("representative_rss", t["improvement_basis"])

        def no_rss(r):
            self.equal_sizes(r)
            r["representative"]["thresholds"]["representative_rss_reduction_max"] = 0.19

        t = self.thresholds(no_rss)
        self.assertFalse(t["improvement_pass"])
        self.assertEqual(t["improvement_basis"], [])
        self.assertFalse(value002.overall_pass(t))

    def test_sustained_rss_does_not_replace_the_declared_rss_criterion(self):
        def only_sustained(r):
            self.equal_sizes(r)
            r["representative"]["thresholds"]["representative_rss_reduction_max"] = 0.0
            r["sustained"]["rss_reduction"] = 0.9

        self.assertFalse(self.thresholds(only_sustained)["improvement_pass"])

    def test_a_significant_regression_in_a_new_workload_fails_the_latency_gate(self):
        def regress(r):
            pair = r["workloads"]["duplicates"]["pair"]
            pair["rust"]["raw"] = [v * 1.5 for v in pair["go"]["raw"]]
            # isolate the new workload: only it enters the interval decision
            for name in list(r["workloads"]):
                if name != "duplicates":
                    del r["workloads"][name]

        result = copy.deepcopy(self.result)
        regress(result)
        with value002.profile_constants("smoke", 4, 2):
            failure = value001.order_stratified_latency_failure(value002.workload_intervals(result["workloads"]))
        self.assertIn("workload.duplicates", failure)
        self.assertIn("exceeds the 0.10 latency ceiling", failure)
        # and the same arithmetic flips the overall gate
        t = self.thresholds(lambda r: r["workloads"]["duplicates"]["pair"]["rust"].update(
            raw=[v * 1.5 for v in r["workloads"]["duplicates"]["pair"]["go"]["raw"]]))
        self.assertFalse(t["latency_pass"])
        self.assertFalse(value002.overall_pass(t))

    def test_new_workload_intervals_use_the_value001_rule(self):
        t = self.thresholds(lambda r: None)
        for name in value002.NEW_WORKLOADS:
            self.assertIn(f"workload.{name}", t["latency_order_regression_intervals"])
        self.assertIn("http.snapshot", t["latency_order_regression_intervals"])

    def test_failed_contract_or_sustained_run_fails_the_gate(self):
        t = self.thresholds(lambda r: r["representative"]["contracts"][0].update(exit_code=1))
        self.assertFalse(t["contracts_pass"])
        self.assertFalse(value002.overall_pass(t))
        t = self.thresholds(lambda r: r["sustained"]["runs"][0].update(errors=3))
        self.assertFalse(t["sustained_pass"])
        self.assertFalse(value002.overall_pass(t))


class ValidatorCliTests(unittest.TestCase):
    def run_cli(self, *extra):
        script = ROOT / "scripts/rust-port/validate_value002.py"
        env = {**os.environ, "PYTHONDONTWRITEBYTECODE": "1"}
        return subprocess.run(
            ["python3", "-I", str(script), str(FIXTURE), *extra], capture_output=True, text=True, env=env, check=False
        )

    def test_cli_rejects_a_smoke_capture_and_a_malformed_candidate(self):
        candidate = load_fixture()["build_record"]["candidate"]["commit"]
        result = self.run_cli("--candidate", candidate)
        self.assertEqual(result.returncode, 1)
        self.assertIn("smoke capture is not evidence", result.stderr)
        result = self.run_cli("--candidate", "abc")
        self.assertEqual(result.returncode, 1)
        self.assertIn("40-hex", result.stderr)


class CanonicalTests(unittest.TestCase):
    def test_canonical_sha_is_order_independent_for_keys_only(self):
        self.assertEqual(value002.canonical_sha({"a": 1, "b": 2}), value002.canonical_sha({"b": 2, "a": 1}))
        self.assertNotEqual(value002.canonical_sha([1, 2]), value002.canonical_sha([2, 1]))

    def test_nondeterministic_or_divergent_outputs_abort_the_measurement(self):
        counter = {"n": 0}

        def flaky(command, **_kwargs):
            counter["n"] += 1
            out = f"go-{counter['n']}" if "go-bin" in command[0] else "rust"
            return subprocess.CompletedProcess(command, 0, stdout=out, stderr="")

        with patch.object(subprocess, "run", side_effect=flaky):
            with self.assertRaisesRegex(value002.HarnessError, "not deterministic"):
                value002.measure_workload(
                    ["go-bin"], ["rust-bin"], {}, {}, lambda out, err: out, lambda out, err: out, None, None, 2, 4, 0
                )

        def divergent(command, **_kwargs):
            out = "go" if "go-bin" in command[0] else "rust"
            return subprocess.CompletedProcess(command, 0, stdout=out, stderr="")

        with patch.object(subprocess, "run", side_effect=divergent):
            with self.assertRaisesRegex(value002.HarnessError, "outputs differ"):
                value002.measure_workload(
                    ["go-bin"], ["rust-bin"], {}, {}, lambda out, err: out, lambda out, err: out, None, None, 2, 4, 0
                )

    def test_cold_reset_removes_index_state_and_fails_if_it_survives(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            state = home / "home/.local/share/symdesk"
            state.mkdir(parents=True)
            (state / "retrieval.db").write_text("x")
            sidecar = home / "sidecar.db"
            sidecar.write_text("x")
            Path(str(sidecar) + "-wal").write_text("x")
            value002.cold_reset(home, sidecar)()
            self.assertFalse(state.exists())
            self.assertFalse(sidecar.exists())
            self.assertFalse(Path(str(sidecar) + "-wal").exists())


if __name__ == "__main__":
    unittest.main()
