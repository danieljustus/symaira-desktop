"""Producer negative controls only; these tests never measure a benchmark."""

import copy
import json
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

import value001


ROOT = Path(__file__).resolve().parents[2]
RESULTS = ROOT / "docs/rust-port/results"
REQUIRED_GATES = (
    "startup",
    "search",
    "mcp",
    "http",
    "mcp.initialize",
    "mcp.tools-list",
    "mcp.desk_status",
    "mcp.desk_ls",
    "mcp.desk_search",
    "http.healthz",
    "http.status",
    "http.snapshot",
    "http.file-read",
    "http.file-range",
    "http.file-missing",
    "http.file-traversal",
)


def metric_pair(metrics, name):
    category, _, operation = name.partition(".")
    metric = metrics[category]
    return metric["operations"][operation] if operation else metric



class ProducerGateTests(unittest.TestCase):
    def setUp(self):
        self.original = json.loads(
            (RESULTS / "value001-operations-5088972a.json").read_text()
        )
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.go = Path(temporary.name) / "go-size-only"
        self.rust = Path(temporary.name) / "rust-size-only"
        # Inert size inputs, never executables. All build/Git queries are mocked.
        self.go.write_bytes(b"g" * 100)
        self.rust.write_bytes(b"r" * 50)

    def build(self, metrics):
        source = {
            "commit": self.original["binaries"]["go"]["source"],
            "go_version": "unit-test metadata",
            "binary_sha256": value001.sha256_file(self.go),
            "build_command": "unit-test size input; no build",
            "build_elapsed_ms": 10**12,
        }
        with (
            patch.object(
                value001, "git_output", return_value=self.original["repository"]["head"]
            ),
            patch.object(value001, "git_bytes", return_value=b""),
            patch.object(value001, "tool_version", return_value="unit-test metadata"),
        ):
            return value001.build_result(
                ROOT,
                self.go,
                self.rust,
                source,
                "unit-test size input; no build",
                self.original["contracts"],
                metrics,
                {
                    "documents": 10000,
                    "bytes": self.original["vault"]["generated_bytes"],
                    "search_match_count": 100,
                },
                100,
                20,
                self.original["index_preparation"],
                # These metrics are replayed from a pre-declaration capture,
                # so the report must declare that capture's summation rather
                # than the producer's current default.
                value001.summation_of(self.original),
                # Replayed metrics are evaluated under the estimator of the
                # capture they came from, so these gates keep testing what
                # they were written to test.
                value001.latency_estimator_of(self.original),
                # A historical replay must explicitly retain schema-3
                # semantics; schema 4 cannot emit the pooled estimator.
                value001.SCHEMA3_VERSION,
            )

    def test_alternate_go_source_is_rejected_before_build_or_output(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "current.json"
            args = SimpleNamespace(
                go_binary=None,
                go_source_commit="0" * 40,
                root=ROOT,
                rust_binary=self.rust,
                rust_build_command="unit-test",
                output=output,
                samples=100,
                warmups=20,
                rss_interval_ms=0.0,
            )
            with patch.object(value001, "parse_args", return_value=args), patch.object(
                value001, "build_go_oracle"
            ) as build:
                with self.assertRaisesRegex(
                    value001.HarnessError,
                    "--go-source-commit must equal CURRENT_BEHAVIOUR_ORACLE",
                ):
                    value001.main()
            build.assert_not_called()
            self.assertFalse(output.exists())

    def test_each_required_gate_enforces_unchanged_ceiling(self):
        for name in REQUIRED_GATES:
            for factor, passes in ((1.099, True), (1.101, False)):
                with self.subTest(gate=name, factor=factor):
                    metrics = copy.deepcopy(self.original["metrics"])
                    pair = metric_pair(metrics, name)
                    go = pair["go"]
                    pair["rust"] = value001.summary(
                        [sample * factor for sample in go["raw"]],
                        go["unit"],
                        go["warmup_samples"],
                        go["pair_order"],
                        value001.summation_of(self.original),
                    )
                    result = self.build(metrics)
                    thresholds = result["thresholds"]
                    self.assertEqual(
                        set(thresholds["latency_regressions"]), set(REQUIRED_GATES)
                    )
                    self.assertEqual(thresholds["maximum_latency_regression"], 0.10)
                    self.assertAlmostEqual(
                        thresholds["latency_regressions"][name], factor - 1
                    )
                    self.assertTrue(thresholds["improvement_pass"])
                    self.assertTrue(thresholds["contracts_pass"])
                    self.assertIs(thresholds["latency_pass"], passes)
                    self.assertIs(result["passed"], passes)
                    if "." in name:
                        category = name.split(".")[0]
                        self.assertLessEqual(
                            thresholds["latency_regressions"][category], 0.10
                        )
                        self.assertEqual(
                            metrics[category]["rust"],
                            self.original["metrics"][category]["rust"],
                        )

    def test_altered_summary_identity_is_rejected_without_normalization(self):
        for name in REQUIRED_GATES:
            for side in ("go", "rust"):
                for field in ("min", "mean", "p50", "p95", "p99"):
                    with self.subTest(gate=name, side=side, field=field):
                        metrics = copy.deepcopy(self.original["metrics"])
                        metric_pair(metrics, name)[side][field] *= 0.5
                        before = copy.deepcopy(metrics)
                        with self.assertRaisesRegex(
                            value001.HarnessError,
                            f"{side}\\.{field} does not match raw samples",
                        ):
                            self.build(metrics)
                        self.assertEqual(metrics, before)

    def test_each_missing_required_operation_is_rejected(self):
        for name in REQUIRED_GATES:
            if "." not in name:
                continue
            with self.subTest(gate=name):
                metrics = copy.deepcopy(self.original["metrics"])
                category, operation = name.split(".")
                del metrics[category]["operations"][operation]
                with self.assertRaisesRegex(
                    value001.HarnessError, f"{category}\\.operations are incomplete"
                ):
                    self.build(metrics)

    def test_historical_resumption_raw_metrics_still_fail(self):
        capture = json.loads((RESULTS / "value001-resume-956bd3e.json").read_text())
        before = copy.deepcopy(capture)
        result = self.build(capture["metrics"])
        regressions = result["thresholds"]["latency_regressions"]
        self.assertLessEqual(regressions["http"], 0.10)
        self.assertEqual(
            {name for name, ratio in regressions.items() if ratio > 0.10},
            {"http.file-read", "http.file-missing"},
        )
        self.assertFalse(result["thresholds"]["latency_pass"])
        self.assertFalse(result["passed"])
        self.assertEqual(capture, before)


class AggregatePairOrderTests(unittest.TestCase):
    """Schema-5 HTTP samples have a fixed, rotated operation/order schedule."""

    def test_http_round_schedule_rotates_operations_and_orders(self):
        self.assertEqual(
            value001.http_round_schedule(1),
            (
                ("status", "go-rust"),
                ("snapshot", "rust-go"),
                ("file-read", "go-rust"),
                ("file-range", "rust-go"),
                ("file-missing", "go-rust"),
                ("file-traversal", "rust-go"),
                ("healthz", "rust-go"),
            ),
        )
        aggregate = value001.expected_http_pair_orders(
            value001.CURRENT_WARMUPS, value001.CURRENT_SAMPLES
        )
        self.assertEqual(len(aggregate), value001.CURRENT_SAMPLES * len(value001.HTTP_OPERATION_NAMES))
        self.assertEqual(aggregate.count("go-rust"), aggregate.count("rust-go"))
        for name in value001.HTTP_OPERATION_NAMES:
            orders = value001.expected_http_operation_orders(
                name, value001.CURRENT_WARMUPS, value001.CURRENT_SAMPLES
            )
            self.assertEqual(orders.count("go-rust"), orders.count("rust-go"))

    def test_measure_http_records_the_predeclared_rotated_order(self):
        rounds, warmups = 4, 1

        def fake_summary(values, unit, warmups_arg, pair_orders):
            return {"raw": list(values), "pair_order": list(pair_orders)}

        clock = iter(range(10_000_000, 10_000_000 + 10_000 * 1000, 1000))
        events = []

        class FakeServer:
            process = None
            vault = None

            def __init__(self, name):
                self.name = name

            def request_raw(self, _method, path, *_args, **_kwargs):
                events.append((self.name, path))
                return 200, b"", {}

        with patch.object(value001, "summary", fake_summary), patch.object(
            value001, "validate_http", lambda *a, **k: None
        ), patch.object(
            value001, "http_operation",
            lambda name: {"method": "GET", "path": "/" + name, "auth": None},
        ), patch.object(
            value001, "rss_bytes", lambda _pid: 1
        ), patch.object(
            value001.time, "perf_counter_ns", lambda: next(clock)
        ), patch.object(
            value001.time, "sleep", lambda seconds: events.append(("idle", seconds))
        ):
            go, rust = FakeServer("go"), FakeServer("rust")
            go.process = rust.process = type("P", (), {"pid": 1})()
            http, _rss = value001.measure_http(go, rust, {}, {}, warmups, rounds, 0)

        expected_events = []
        for index in range(warmups + rounds):
            priming_sides = ("go", "rust") if index % 2 == 0 else ("rust", "go")
            for side in priming_sides:
                expected_events.extend(((side, "/healthz"), ("idle", value001.HTTP_CONTROL_IDLE_SECONDS)))
            for operation, order in value001.http_round_schedule(index):
                sides = ("go", "rust") if order == "go-rust" else ("rust", "go")
                for side in sides:
                    expected_events.extend(((side, "/" + operation), ("idle", value001.HTTP_CONTROL_IDLE_SECONDS)))
        self.assertEqual(events, expected_events)
        expected_aggregate = value001.expected_http_pair_orders(warmups, rounds)
        self.assertEqual(
            http["operation_order"],
            value001.expected_http_operation_order(warmups, rounds),
        )
        self.assertEqual(http["go"]["pair_order"], expected_aggregate)
        self.assertEqual(http["rust"]["pair_order"], expected_aggregate)
        for name in value001.HTTP_OPERATION_NAMES:
            expected = value001.expected_http_operation_orders(name, warmups, rounds)
            self.assertEqual(http["operations"][name]["go"]["pair_order"], expected)
            self.assertEqual(http["operations"][name]["rust"]["pair_order"], expected)

    def test_request_raw_closes_each_connection(self):
        captured = {}

        class Response:
            status = 200
            headers = {}

            def read(self, _limit):
                return b""

            def __enter__(self):
                return self

            def __exit__(self, *_args):
                return False

        def fake_urlopen(request, timeout):
            captured["request"] = request
            self.assertEqual(timeout, 5.0)
            return Response()

        server = object.__new__(value001.RunningServer)
        server.base = "http://127.0.0.1:4242"
        server.binary = SimpleNamespace(name="symdesk")
        with patch.object(value001.urllib.request, "urlopen", fake_urlopen):
            server.request_raw("GET", "/healthz", auth="none")
        self.assertEqual(captured["request"].get_header("Connection"), "close")


class McpListScopeTests(unittest.TestCase):
    def test_measure_mcp_scopes_desk_ls_to_the_search_cohort(self):
        manifest = {
            "expected_paths": [value001.document_path(index) for index in range(value001.DOC_COUNT)],
            "expected_titles": {},
            "search_paths": [],
            "search_match_count": 0,
        }
        expected = value001.expected_vault_semantics(manifest)
        captured: list[str] = []
        metric = {
            side: {"raw": [1.0] * 100, "pair_order": ["go-rust", "rust-go"] * 50}
            for side in ("go", "rust")
        }

        def measure(*_args, **kwargs):
            captured.append(kwargs["input_data"])
            return metric

        with patch.object(value001, "measure_process_pair", measure):
            value001.measure_mcp(
                Path("/go"),
                Path("/rust"),
                {},
                {},
                expected,
                Path("/go-vault"),
                Path("/rust-vault"),
                1,
                100,
            )

        request = json.loads(captured[3])
        self.assertEqual(request["params"]["arguments"], {"dir": "cohort-042/"})
        self.assertEqual(len(expected["mcp_ls_paths"]), 100)
        self.assertTrue(all(path.startswith("cohort-042/") for path in expected["mcp_ls_paths"]))


if __name__ == "__main__":
    unittest.main(verbosity=2)
