#!/usr/bin/env python3
"""Build and retain the source-bound native config-paths differential."""
from __future__ import annotations

import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import platform
import re
import subprocess
import sys
from typing import Any

sys.dont_write_bytecode = True

ROOT = Path(__file__).resolve().parents[2]
FOUNDATION = "47dd417ec5632c8e6a2edb32ead09db09e929ed9"
ORIGIN = "https://github.com/danieljustus/symaira-desktop.git"
COREKIT = "github.com/danieljustus/symaira-corekit"
COREKIT_VERSION = "v0.18.2"
GO_VERSION = "go1.26.9"
RUST_VERSION = "1.98.0"
CASE_COUNT = 75
UNIX_CASE_COUNT = 16

INDEX_STATUS = ROOT / "scripts/rust-port/index-status"
sys.path.insert(0, str(INDEX_STATUS))
_capture_spec = importlib.util.spec_from_file_location("config_paths_native_capture", INDEX_STATUS / "capture.py")
if _capture_spec is None or _capture_spec.loader is None:
    raise RuntimeError("cannot load the existing native process-tree/evidence helpers")
_capture_module = importlib.util.module_from_spec(_capture_spec)
_capture_spec.loader.exec_module(_capture_module)
_terminate_process_tree = _capture_module._terminate_process_tree
sha256_file = _capture_module.sha256_file


class GateFailure(RuntimeError):
    pass


def digest_bytes(content: bytes) -> str:
    return hashlib.sha256(content).hexdigest()


def write_json(path: Path, value: Any) -> None:
    encoded = (json.dumps(value, indent=2, sort_keys=True) + "\n").encode("utf-8")
    temporary = path.with_name(path.name + ".tmp")
    with temporary.open("xb") as stream:
        stream.write(encoded)
        stream.flush()
        os.fsync(stream.fileno())
    os.replace(temporary, path)


def git(root: Path, *args: str) -> str:
    result = subprocess.run(
        ["git", "-C", str(root), *args],
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        timeout=30,
        check=False,
    )
    if result.returncode != 0:
        detail = result.stderr.decode("utf-8", errors="replace").strip()
        raise GateFailure(f"git {' '.join(args)} failed ({result.returncode}): {detail}")
    return result.stdout.decode("utf-8", errors="replace").strip()


def _record_path(path: Path) -> dict[str, Any]:
    if path.is_symlink():
        value: dict[str, Any] = {"kind": "symlink", "target": os.readlink(path)}
        if path.is_file():
            value["sha256"] = sha256_file(path)
        return value
    if path.is_file():
        return {"kind": "file", "sha256": sha256_file(path)}
    if path.is_dir():
        commit = None
        if (path / ".git").exists():
            try:
                commit = git(path, "rev-parse", "HEAD")
            except GateFailure:
                commit = "unavailable"
        return {"kind": "directory", "git_head": commit}
    raise GateFailure(f"source input is missing or not a regular file: {path}")


def repository_snapshot(root: Path) -> dict[str, Any]:
    root = root.resolve(strict=True)
    commit = git(root, "rev-parse", "HEAD")
    tree = git(root, "rev-parse", "HEAD^{tree}")
    paths = subprocess.check_output(
        ["git", "-C", str(root), "ls-files", "--cached", "--others", "--exclude-standard", "-z"]
    ).split(b"\0")
    files: dict[str, Any] = {}
    for raw in paths:
        if not raw:
            continue
        relative = os.fsdecode(raw)
        path = root / relative
        if path.is_dir() and not path.is_symlink():
            files[relative] = _record_path(path)
            for directory, subdirs, names in os.walk(path, followlinks=False):
                base = Path(directory)
                subdirs[:] = sorted(
                    name for name in subdirs
                    if name != ".git" and not (base / name).is_symlink()
                )
                for name in sorted(names):
                    nested = base / name
                    files[nested.relative_to(root).as_posix()] = _record_path(nested)
                for name in sorted(
                    name for name in os.listdir(base)
                    if (base / name).is_symlink() and name != ".git"
                ):
                    nested = base / name
                    files[nested.relative_to(root).as_posix()] = _record_path(nested)
        else:
            files[relative] = _record_path(path)
    return {
        "root": str(root),
        "commit": commit,
        "tree": tree,
        "branch": git(root, "branch", "--show-current"),
        "status": git(root, "status", "--porcelain=v1", "--untracked-files=all"),
        "file_count": len(files),
        "files": dict(sorted(files.items())),
    }


def directory_snapshot(directory: Path) -> dict[str, Any]:
    directory = directory.resolve(strict=True)
    if not directory.is_dir():
        raise GateFailure(f"dependency source directory is not a directory: {directory}")
    files: dict[str, Any] = {}
    for current, subdirs, names in os.walk(directory, followlinks=False):
        base = Path(current)
        symlinks = [name for name in subdirs if (base / name).is_symlink()]
        subdirs[:] = sorted(name for name in subdirs if name not in {".git"} and name not in symlinks)
        for name in symlinks:
            link = base / name
            files[link.relative_to(directory).as_posix()] = _record_path(link)
        for name in sorted(names):
            path = base / name
            if path.name == ".git":
                continue
            files[path.relative_to(directory).as_posix()] = _record_path(path)
    return {
        "directory": str(directory),
        "file_count": len(files),
        "files": dict(sorted(files.items())),
    }


def decode_json_stream(content: bytes) -> list[dict[str, Any]]:
    text = content.decode("utf-8")
    decoder = json.JSONDecoder()
    values: list[dict[str, Any]] = []
    offset = 0
    while offset < len(text):
        while offset < len(text) and text[offset].isspace():
            offset += 1
        if offset == len(text):
            break
        value, offset = decoder.raw_decode(text, offset)
        if not isinstance(value, dict):
            raise GateFailure("expected a JSON object in Go module inventory")
        values.append(value)
    return values


def go_module_sources(packages: list[dict[str, Any]]) -> list[dict[str, Any]]:
    sources = []
    seen: set[tuple[str, str, str]] = set()
    for package in packages:
        module = package.get("Module")
        if not isinstance(module, dict) or module.get("Main"):
            continue
        directory = module.get("Dir")
        if not directory:
            raise GateFailure(f"Go package module has no resolved source directory: {module.get('Path')}")
        source = directory_snapshot(Path(directory))
        key = (str(module.get("Path")), str(module.get("Version")), source["directory"])
        if key in seen:
            continue
        seen.add(key)
        sources.append({
            "path": module.get("Path"),
            "version": module.get("Version"),
            "sum": module.get("Sum", ""),
            "replace": module.get("Replace"),
            **source,
        })
    return sorted(sources, key=lambda source: (str(source["path"]), str(source["version"]), str(source["directory"])))


def cargo_package_sources(metadata: dict[str, Any]) -> list[dict[str, Any]]:
    workspace = set(metadata.get("workspace_members", []))
    package_sources = []
    seen: set[str] = set()
    for package in metadata.get("packages", []):
        manifest = Path(package["manifest_path"]).resolve(strict=True)
        package_root = manifest.parent
        if package.get("id") in workspace:
            continue
        source = directory_snapshot(package_root)
        key = source["directory"]
        if key in seen:
            continue
        seen.add(key)
        package_sources.append({
            "id": package.get("id"),
            "name": package.get("name"),
            "version": package.get("version"),
            "source": package.get("source"),
            **source,
        })
    return sorted(package_sources, key=lambda source: (str(source["name"]), str(source["version"]), str(source["directory"])))


def validate_cargo_metadata(metadata: dict[str, Any], root: Path, target_dir: Path) -> dict[str, Any]:
    root = root.resolve(strict=True)
    target_dir = target_dir.resolve()
    if Path(metadata.get("workspace_root", "")).resolve() != root:
        raise GateFailure("Cargo metadata workspace root is not the exact candidate checkout")
    if Path(metadata.get("target_directory", "")).resolve() != target_dir:
        raise GateFailure("Cargo metadata target directory is not the isolated external output")
    expected_manifest = (root / "crates/symdesk-cli/Cargo.toml").resolve(strict=True)
    packages = [package for package in metadata.get("packages", []) if package.get("name") == "symdesk-cli"]
    if len(packages) != 1 or Path(packages[0]["manifest_path"]).resolve() != expected_manifest:
        raise GateFailure("Cargo metadata did not identify the candidate symdesk-cli manifest")
    targets = [
        target for target in packages[0].get("targets", [])
        if target.get("name") == "symdesk" and "bin" in target.get("kind", [])
    ]
    expected_source = (root / "crates/symdesk-cli/src/main.rs").resolve(strict=True)
    if len(targets) != 1 or Path(targets[0]["src_path"]).resolve() != expected_source:
        raise GateFailure("Cargo metadata did not identify the candidate symdesk binary target")
    return {
        "workspace_root": str(root),
        "manifest": str(expected_manifest),
        "target_source": str(expected_source),
        "target_directory": str(target_dir),
        "package_id": packages[0].get("id"),
        "binary_target": targets[0].get("name"),
        "resolved_package_count": len(metadata.get("packages", [])),
        "workspace_members": metadata.get("workspace_members", []),
    }


def validate_capture_report(report: dict[str, Any], host_os: str, expected_head: str) -> dict[str, Any]:
    if report.get("head") != expected_head or report.get("head_after") != expected_head:
        raise GateFailure("producer report does not bind both source observations to candidate HEAD")
    if report.get("candidate_clean") is not True or report.get("git_status_before") or report.get("git_status_after"):
        raise GateFailure("producer report does not prove an unchanged clean candidate checkout")
    if report.get("go_source", {}).get("commit") != FOUNDATION:
        raise GateFailure("producer Go source identity is not the pinned foundation commit")
    for field in ("rust_source", "harness_source", "source_after"):
        identity = report.get(field, {})
        if identity.get("commit") != expected_head or not identity.get("tree"):
            raise GateFailure(f"producer {field} identity does not bind candidate HEAD and tree")
    go_binary = report.get("go_binary", {})
    if go_binary.get("go_version") != GO_VERSION or go_binary.get("vcs_revision") != FOUNDATION or go_binary.get("vcs_modified") != "false":
        raise GateFailure("producer Go binary is not a clean Go 1.26.9 foundation build")
    harness_binary = report.get("harness_binary", {})
    if harness_binary.get("go_version") != GO_VERSION or harness_binary.get("vcs_revision") != expected_head or harness_binary.get("vcs_modified") != "false":
        raise GateFailure("differential harness binary is not a clean exact-HEAD Go build")
    if report.get("go_corekit_source") != report.get("go_corekit_source_after"):
        raise GateFailure("producer CoreKit identity changed during capture")
    if report.get("mutation_control", {}).get("comparator_rejected") is not True or report.get("mutation_control", {}).get("retained_input_unchanged") is not True:
        raise GateFailure("producer mutation control did not reject without mutating retained evidence")

    cases = report.get("cases")
    if not isinstance(cases, list) or len(cases) != CASE_COUNT or report.get("declared_case_count") != CASE_COUNT:
        raise GateFailure(f"producer declared case inventory is not exactly {CASE_COUNT}")
    host_is_windows = host_os.lower().startswith("win")
    expected_executed = CASE_COUNT - UNIX_CASE_COUNT if host_is_windows else CASE_COUNT
    expected_skipped = UNIX_CASE_COUNT if host_is_windows else 0
    ids: list[str] = []
    executed = 0
    skipped = 0
    for case in cases:
        case_id = case.get("id")
        if not isinstance(case_id, str) or not case_id or case.get("input", {}).get("id") != case_id:
            raise GateFailure("producer case identity is missing or differs from its input identity")
        ids.append(case_id)
        if case.get("platform") == "unix" and host_is_windows:
            if case.get("result") != "not_applicable" or case.get("go") is not None or case.get("rust") is not None:
                raise GateFailure(f"Windows Unix-only case was not explicitly marked not applicable: {case_id}")
            skipped += 1
            continue
        if case.get("result") != "passed" or not isinstance(case.get("go"), dict) or not isinstance(case.get("rust"), dict):
            raise GateFailure(f"applicable real-process pair did not pass: {case_id}")
        executed += 1
    if len(set(ids)) != CASE_COUNT:
        raise GateFailure("producer case inventory contains duplicate IDs")
    if (executed, skipped) != (expected_executed, expected_skipped):
        raise GateFailure(f"native case applicability is {executed}/{skipped}; expected {expected_executed}/{expected_skipped}")
    for field, expected in (("executed_case_count", expected_executed), ("skipped_platform_case_count", expected_skipped), ("passed_case_count", expected_executed)):
        if report.get(field) != expected:
            raise GateFailure(f"producer {field} is {report.get(field)!r}; expected {expected}")
    return {"declared": CASE_COUNT, "executed_pairs": executed, "not_applicable": skipped, "case_ids": ids}


def require_sources_unchanged(before: dict[str, Any], after: dict[str, Any]) -> None:
    if before != after:
        raise GateFailure("one or more candidate, oracle, or dependency source inputs changed during the gate")


def run_logged_command(
    report: dict[str, Any],
    logs_dir: Path,
    name: str,
    command: list[str],
    cwd: Path,
    *,
    env: dict[str, str],
    launch_env: dict[str, str],
    launcher: str | None = None,
    timeout: float = 900,
) -> bytes:
    index = len(report["commands"])
    stem = re.sub(r"[^A-Za-z0-9_.-]+", "-", name).strip("-") or f"stage-{index}"
    stdout_path = logs_dir / f"{index:02d}-{stem}.stdout.log"
    stderr_path = logs_dir / f"{index:02d}-{stem}.stderr.log"
    actual_command = list(command)
    process_env = dict(env)
    if launcher:
        if os.name == "nt" or not Path("/usr/bin/env").is_file():
            raise GateFailure("the local build launcher requires POSIX /usr/bin/env")
        assignments = [f"{key}={value}" for key, value in sorted(env.items())]
        actual_command = [launcher, "/usr/bin/env", "-i", *assignments, *command]
        process_env = dict(launch_env)
    record: dict[str, Any] = {
        "name": name,
        "argv": command,
        "invocation": actual_command,
        "cwd": str(cwd.resolve()),
        "stdout_file": str(stdout_path.relative_to(logs_dir.parent)),
        "stderr_file": str(stderr_path.relative_to(logs_dir.parent)),
        "exit_code": None,
        "timed_out": False,
        "launch_error": None,
        "process_cleanup": "not-started",
    }
    report["commands"].append(record)
    try:
        with stdout_path.open("xb") as stdout, stderr_path.open("xb") as stderr:
            try:
                process = subprocess.Popen(
                    actual_command,
                    cwd=cwd,
                    env=process_env,
                    stdin=subprocess.DEVNULL,
                    stdout=stdout,
                    stderr=stderr,
                    start_new_session=os.name != "nt",
                    creationflags=subprocess.CREATE_NEW_PROCESS_GROUP if os.name == "nt" else 0,
                    close_fds=True,
                )
            except OSError as error:
                record["launch_error"] = f"{type(error).__name__}: {error}"
            else:
                try:
                    record["exit_code"] = process.wait(timeout=timeout)
                except subprocess.TimeoutExpired:
                    record["timed_out"] = True
                    record["process_cleanup"] = _terminate_process_tree(process, env)
                    record["exit_code"] = process.returncode
                else:
                    record["process_cleanup"] = _terminate_process_tree(process, env)
                finally:
                    if process.poll() is None:
                        record["process_cleanup"] = _terminate_process_tree(process, env)
    except BaseException:
        record["launch_error"] = record.get("launch_error") or "runner interrupted while capturing command"
        raise
    stdout_bytes = stdout_path.read_bytes()
    stderr_bytes = stderr_path.read_bytes()
    record["stdout_bytes"] = len(stdout_bytes)
    record["stdout_sha256"] = digest_bytes(stdout_bytes)
    record["stderr_bytes"] = len(stderr_bytes)
    record["stderr_sha256"] = digest_bytes(stderr_bytes)
    if record["timed_out"] or record["launch_error"] or record["exit_code"] != 0:
        raise GateFailure(
            f"{name} failed (exit={record['exit_code']}, timed_out={record['timed_out']}); "
            f"raw logs retained at {stdout_path} and {stderr_path}"
        )
    return stdout_bytes


def sanitized_environment(operator_env: dict[str, str]) -> dict[str, str]:
    allowed = {
        "PATH", "HOME", "USERPROFILE", "CARGO_HOME", "RUSTUP_HOME", "GOROOT", "GOPATH",
        "GOMODCACHE", "GOCACHE", "SystemRoot", "SYSTEMROOT", "WINDIR", "windir",
        "COMSPEC", "ComSpec", "PATHEXT", "SystemDrive", "DEVELOPER_DIR", "SDKROOT",
        "ProgramFiles", "ProgramFiles(x86)", "LANG", "LC_ALL", "CC", "CXX",
        "PKG_CONFIG_PATH", "LD_LIBRARY_PATH", "DYLD_LIBRARY_PATH", "DYLD_FALLBACK_LIBRARY_PATH",
    }
    return {key: value for key, value in operator_env.items() if key in allowed}


def private_environment(output: Path, operator_env: dict[str, str], launcher: str | None) -> tuple[dict[str, str], dict[str, str]]:
    runtime = output / "runtime"
    names = ("HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "TMPDIR", "TMP", "TEMP", "APPDATA", "LOCALAPPDATA")
    private_paths = {}
    for name in names:
        path = runtime / name.lower()
        path.mkdir(parents=True, exist_ok=False)
        private_paths[name] = str(path.resolve())
    operator_home = operator_env.get("HOME") or operator_env.get("USERPROFILE")
    if not operator_home:
        raise GateFailure("operator HOME/USERPROFILE is unavailable for launcher bootstrap")
    operator_home = str(Path(operator_home).expanduser().resolve())
    cargo_home = str(Path(operator_env.get("CARGO_HOME", str(Path(operator_home) / ".cargo"))).expanduser().resolve())
    rustup_home = str(Path(operator_env.get("RUSTUP_HOME", str(Path(operator_home) / ".rustup"))).expanduser().resolve())
    if launcher and (not Path(cargo_home).is_dir() or not Path(rustup_home).is_dir()):
        raise GateFailure("local Cargo/Rustup homes must be existing installed caches")
    env = sanitized_environment(operator_env)
    env.update(private_paths)
    env.update(
        CARGO_HOME=cargo_home,
        RUSTUP_HOME=rustup_home,
        RUSTUP_TOOLCHAIN=RUST_VERSION,
        GOENV="off",
        GOWORK="off",
        GOTOOLCHAIN=GO_VERSION,
        GOFLAGS="-mod=readonly",
        CGO_ENABLED="0",
        PYTHONDONTWRITEBYTECODE="1",
    )
    if launcher:
        env.update(CARGO_NET_OFFLINE="true", GOPROXY="off")
    return env, dict(operator_env)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--evidence-dir", required=True, type=Path)
    parser.add_argument("--build-launcher", type=Path, help="local dev-external launcher; omitted on hosted CI")
    parser.add_argument("--oracle-worktree", type=Path, help="explicit clean pinned Go foundation checkout for local policy builds")
    args = parser.parse_args()
    output = args.evidence_dir.expanduser().absolute()
    launcher = str(args.build_launcher.expanduser().resolve()) if args.build_launcher else None
    if launcher and not Path(launcher).is_file():
        parser.error(f"build launcher does not exist: {launcher}")
    if output.exists():
        parser.error(f"evidence directory already exists; refusing overwrite: {output}")
    if output == ROOT or ROOT in output.parents:
        parser.error("evidence and build outputs must be outside the candidate checkout")
    output.parent.mkdir(parents=True, exist_ok=True)
    output.mkdir(mode=0o700)
    for child in ("commands", "binaries", "worktrees", "runtime"):
        (output / child).mkdir(mode=0o700)
    write_json(output / "sources.before.json", {"status": "not_captured"})
    write_json(output / "sources.after.json", {"status": "not_captured"})

    host_env = dict(os.environ)
    env: dict[str, str] = {}
    launch_env = host_env
    runner: dict[str, Any] = {
        "schema_version": 1,
        "passed": False,
        "candidate_root": str(ROOT.resolve()),
        "host_os": platform.system(),
        "host_arch": platform.machine(),
        "foundation_commit": FOUNDATION,
        "go_version_required": GO_VERSION,
        "rust_version_required": RUST_VERSION,
        "build_launcher": launcher,
        "commands": [],
    }
    before: dict[str, Any] | None = None
    source_roots: dict[str, Any] = {}
    failure: str | None = None
    try:
        env, launch_env = private_environment(output, host_env, launcher)
        root = ROOT.resolve(strict=True)
        if Path(git(root, "rev-parse", "--show-toplevel")).resolve() != root:
            raise GateFailure("runner is not executing from the exact candidate repository root")
        if git(root, "remote", "get-url", "origin") != ORIGIN:
            raise GateFailure("candidate origin does not match the pinned repository")
        head = git(root, "rev-parse", "HEAD")
        if re.fullmatch(r"[0-9a-f]{40}", head) is None:
            raise GateFailure("candidate HEAD is not a full immutable commit")
        status = git(root, "status", "--porcelain=v1", "--untracked-files=all")
        if status:
            raise GateFailure("native gate requires a clean committed candidate before source capture")
        git(root, "merge-base", "--is-ancestor", FOUNDATION, head)
        branch = git(root, "branch", "--show-current")
        if not branch:
            if host_env.get("GITHUB_ACTIONS") != "true":
                raise GateFailure("detached candidate is accepted only inside the isolated GitHub Actions checkout")
            branch = f"native-config-paths-{head[:12]}"
            git(root, "switch", "--create", branch, head)
            if git(root, "rev-parse", "HEAD") != head:
                raise GateFailure("temporary native candidate branch changed the immutable candidate HEAD")
        if branch == "main":
            raise GateFailure("producer refuses main; native gate requires an explicitly isolated candidate branch")
        if git(root, "status", "--porcelain=v1", "--untracked-files=all"):
            raise GateFailure("candidate became dirty while establishing its native branch")
        runner.update({"candidate_head": head, "candidate_tree": git(root, "rev-parse", "HEAD^{tree}"), "candidate_branch": branch})

        oracle_created = args.oracle_worktree is None
        if args.oracle_worktree is not None:
            oracle = args.oracle_worktree.expanduser().resolve(strict=True)
            if oracle == root:
                raise GateFailure("candidate checkout cannot double as the immutable Go oracle")
        else:
            if launcher:
                raise GateFailure("local policy builds require an explicitly verified --oracle-worktree under an approved repo path")
            oracle = output / "worktrees/oracle"
            git(root, "worktree", "add", "--detach", str(oracle), FOUNDATION)
            oracle = oracle.resolve(strict=True)
        if Path(git(oracle, "rev-parse", "--show-toplevel")).resolve() != oracle:
            raise GateFailure("oracle worktree root does not match its explicitly owned path")
        if git(oracle, "remote", "get-url", "origin") != ORIGIN:
            raise GateFailure("oracle checkout origin does not match the pinned repository")
        if git(oracle, "rev-parse", "HEAD") != FOUNDATION or git(oracle, "status", "--porcelain=v1", "--untracked-files=all"):
            raise GateFailure("Go oracle checkout is not the exact clean immutable foundation")
        runner["oracle_worktree"] = str(oracle)
        runner["oracle_branch"] = git(oracle, "branch", "--show-current")
        runner["oracle_worktree_created"] = oracle_created

        operator_home = host_env.get("HOME") or host_env.get("USERPROFILE")
        if not operator_home:
            raise GateFailure("operator HOME/USERPROFILE is unavailable for Go cache discovery")
        probe_env = sanitized_environment(host_env)
        probe_env.update(HOME=operator_home, USERPROFILE=operator_home, GOENV="off", GOWORK="off", GOTOOLCHAIN=GO_VERSION, GOPROXY="off")
        go_env_output = run_logged_command(
            runner, output / "commands", "go-environment-probe",
            ["go", "env", "GOPATH", "GOMODCACHE", "GOCACHE", "GOVERSION"], root,
            env=probe_env, launch_env=launch_env, launcher=launcher, timeout=60,
        ).decode("utf-8").splitlines()
        if len(go_env_output) != 4 or go_env_output[3] != GO_VERSION:
            raise GateFailure(f"Go environment is not the pinned {GO_VERSION}: {go_env_output!r}")
        gopath, gomodcache, gocache, _ = go_env_output
        if launcher and any(not Path(path).is_dir() for path in (gopath, gomodcache, gocache)):
            raise GateFailure("local Go module/build caches must already exist; no internal fallback or cache creation")
        env.update(GOPATH=gopath, GOMODCACHE=gomodcache, GOCACHE=gocache)

        corekit_bytes = run_logged_command(
            runner, output / "commands", "resolve-pinned-corekit-source",
            ["go", "list", "-m", "-json", COREKIT], oracle,
            env=env, launch_env=launch_env, launcher=launcher,
        )
        corekit_values = decode_json_stream(corekit_bytes)
        if len(corekit_values) != 1:
            raise GateFailure("go list -m -json did not return one CoreKit module identity")
        corekit = corekit_values[0]
        if corekit.get("Path") != COREKIT or corekit.get("Version") != COREKIT_VERSION or corekit.get("Replace"):
            raise GateFailure(f"CoreKit module path/version/replacement differs from the pin: {corekit}")
        corekit_dir = Path(corekit.get("Dir", "")).resolve(strict=True)
        if corekit_dir.name != "symaira-corekit@v0.18.2" or not corekit.get("Sum"):
            raise GateFailure("CoreKit source directory or checksum is not the actual pinned module")
        runner["corekit_module"] = {"path": COREKIT, "version": COREKIT_VERSION, "sum": corekit["Sum"], "directory": str(corekit_dir)}

        cli_packages = decode_json_stream(run_logged_command(
            runner, output / "commands", "resolve-go-cli-build-inputs",
            ["go", "list", "-deps", "-json", "./cmd/symdesk"], oracle,
            env=env, launch_env=launch_env, launcher=launcher,
        ))
        harness_packages = decode_json_stream(run_logged_command(
            runner, output / "commands", "resolve-go-harness-test-inputs",
            ["go", "-C", str(root), "list", "-deps", "-test", "-json", "./scripts/rust-port/cmd/config-paths-diff", "./scripts/rust-port/internal/diff"], root,
            env=env, launch_env=launch_env, launcher=launcher,
        ))
        go_packages = cli_packages + harness_packages
        go_modules_before = go_module_sources(go_packages)
        if not any(
            module["path"] == COREKIT and module["version"] == COREKIT_VERSION
            and module["directory"] == str(corekit_dir) and module["sum"] == corekit["Sum"]
            for module in go_modules_before
        ):
            raise GateFailure("Go CLI/test package input closure omits the exact queried CoreKit module")

        cargo_target = output / "cargo-target"
        cargo_target.mkdir(mode=0o700)
        metadata_bytes = run_logged_command(
            runner, output / "commands", "cargo-metadata-candidate-workspace",
            ["cargo", "metadata", "--format-version", "1", "--locked", "--manifest-path", str(root / "Cargo.toml")], root,
            env={**env, "CARGO_TARGET_DIR": str(cargo_target)}, launch_env=launch_env, launcher=launcher,
        )
        metadata = json.loads(metadata_bytes)
        cargo_identity = validate_cargo_metadata(metadata, root, cargo_target)
        runner["cargo_workspace"] = cargo_identity

        go_version = run_logged_command(
            runner, output / "commands", "go-version-pinned", ["go", "version"], oracle,
            env=env, launch_env=launch_env, launcher=launcher, timeout=60,
        ).decode("utf-8", errors="replace").strip()
        rust_version = run_logged_command(
            runner, output / "commands", "rustc-version-pinned", ["rustc", "--version"], root,
            env={**env, "CARGO_TARGET_DIR": str(cargo_target)}, launch_env=launch_env, launcher=launcher, timeout=60,
        ).decode("utf-8", errors="replace").strip()
        if not go_version.startswith(f"go version {GO_VERSION} "):
            raise GateFailure(f"Go executable version differs from pin: {go_version}")
        if not rust_version.startswith(f"rustc {RUST_VERSION} "):
            raise GateFailure(f"Rust compiler version differs from pin: {rust_version}")
        runner["toolchains"] = {"go": go_version, "rustc": rust_version}

        source_roots = {
            "candidate": root,
            "oracle": oracle,
            "go_packages": go_packages,
            "cargo_packages": metadata,
        }
        before = {
            "schema_version": 1,
            "candidate": repository_snapshot(root),
            "oracle": repository_snapshot(oracle),
            "go_modules": go_modules_before,
            "cargo_packages": cargo_package_sources(metadata),
        }
        if before["candidate"]["status"] or before["oracle"]["status"]:
            raise GateFailure("source inventory began from a dirty candidate or oracle worktree")
        write_json(output / "sources.before.json", before)
        runner["source_inventory"] = {
            "candidate_files": before["candidate"]["file_count"],
            "oracle_files": before["oracle"]["file_count"],
            "go_module_directories": len(before["go_modules"]),
            "cargo_package_directories": len(before["cargo_packages"]),
        }

        test_log = run_logged_command(
            runner, output / "commands", "focused-native-runner-tests",
            [sys.executable, str(root / "scripts/rust-port/test_config_paths_native.py")], root,
            env=env, launch_env=launch_env, timeout=120,
        )
        runner["focused_tests_stdout_sha256"] = digest_bytes(test_log)
        go_test = run_logged_command(
            runner, output / "commands", "go-producer-and-diff-tests",
            ["go", "test", "-count=1", "-v", "./scripts/rust-port/cmd/config-paths-diff", "./scripts/rust-port/internal/diff"], root,
            env=env, launch_env=launch_env, launcher=launcher,
        )
        runner["go_test_stdout_sha256"] = digest_bytes(go_test)
        run_logged_command(
            runner, output / "commands", "go-producer-and-diff-vet",
            ["go", "vet", "./scripts/rust-port/cmd/config-paths-diff", "./scripts/rust-port/internal/diff"], root,
            env=env, launch_env=launch_env, launcher=launcher,
        )

        suffix = ".exe" if os.name == "nt" else ""
        go_binary = output / "binaries" / f"symdesk-go{suffix}"
        rust_binary = cargo_target / "debug" / f"symdesk{suffix}"
        harness_binary = output / "binaries" / f"config-paths-diff{suffix}"
        go_git_dir = git(oracle, "rev-parse", "--absolute-git-dir")
        candidate_git_dir = git(root, "rev-parse", "--absolute-git-dir")
        go_build_env = {**env, "GIT_DIR": go_git_dir, "GIT_WORK_TREE": str(oracle)}
        candidate_go_env = {**env, "GIT_DIR": candidate_git_dir, "GIT_WORK_TREE": str(root)}
        run_logged_command(
            runner, output / "commands", "build-go-foundation-oracle",
            ["go", "build", "-o", str(go_binary), "./cmd/symdesk"], oracle,
            env=go_build_env, launch_env=launch_env, launcher=launcher,
        )
        run_logged_command(
            runner, output / "commands", "build-candidate-rust-cli",
            ["cargo", "build", "--manifest-path", str(root / "crates/symdesk-cli/Cargo.toml"), "--target-dir", str(cargo_target), "--locked", "--package", "symdesk-cli", "--bin", "symdesk"], root,
            env={**env, "CARGO_TARGET_DIR": str(cargo_target)}, launch_env=launch_env, launcher=launcher,
        )
        run_logged_command(
            runner, output / "commands", "build-candidate-differential-producer",
            ["go", "build", "-o", str(harness_binary), "./scripts/rust-port/cmd/config-paths-diff"], root,
            env=candidate_go_env, launch_env=launch_env, launcher=launcher,
        )
        for binary in (go_binary, rust_binary, harness_binary):
            if not binary.is_file() or binary.stat().st_size == 0:
                raise GateFailure(f"expected freshly built executable is missing or empty: {binary}")
        runner["binaries"] = {
            "go_oracle": {"path": str(go_binary), "sha256": sha256_file(go_binary), "source_commit": FOUNDATION},
            "rust_candidate": {"path": str(rust_binary), "sha256": sha256_file(rust_binary), "source_commit": head},
            "differential_producer": {"path": str(harness_binary), "sha256": sha256_file(harness_binary), "source_commit": head},
        }

        run_logged_command(
            runner, output / "commands", "capture-real-config-paths-differential",
            [str(harness_binary), "--go-binary", str(go_binary), "--rust-binary", str(rust_binary),
             "--repo-root", str(root), "--rust-manifest", str(root / "crates/symdesk-cli/Cargo.toml"),
             "--corekit-dir", str(corekit_dir), "--evidence-dir", str(output / "capture")], root,
            env=env, launch_env=launch_env, timeout=600,
        )
        producer_report_path = output / "capture/report.json"
        if not producer_report_path.is_file():
            raise GateFailure("config-paths producer exited successfully without its retained report")
        producer_report = json.loads(producer_report_path.read_bytes())
        runner["capture"] = validate_capture_report(producer_report, platform.system(), head)
        runner["producer_report"] = str(producer_report_path)
        runner["producer_report_sha256"] = sha256_file(producer_report_path)

        if git(root, "rev-parse", "HEAD") != head or git(root, "branch", "--show-current") != branch:
            raise GateFailure("candidate branch or HEAD changed during native execution")
        if git(root, "status", "--porcelain=v1", "--untracked-files=all"):
            raise GateFailure("candidate checkout became dirty during native execution")
        if git(oracle, "rev-parse", "HEAD") != FOUNDATION or git(oracle, "status", "--porcelain=v1", "--untracked-files=all"):
            raise GateFailure("immutable Go oracle checkout changed during native execution")
        if any(sha256_file(binary) != identity["sha256"] for binary, identity in zip(
            (go_binary, rust_binary, harness_binary), runner["binaries"].values()
        )):
            raise GateFailure("one or more executable bytes changed after capture")
    except Exception as error:
        failure = f"{type(error).__name__}: {error}"
    finally:
        active_exception = sys.exc_info()[1]
        if failure is None and active_exception is not None:
            failure = f"{type(active_exception).__name__}: gate interrupted before completion"
        if before is not None:
            try:
                after = {
                    "schema_version": 1,
                    "candidate": repository_snapshot(source_roots["candidate"]),
                    "oracle": repository_snapshot(source_roots["oracle"]),
                    "go_modules": go_module_sources(source_roots["go_packages"]),
                    "cargo_packages": cargo_package_sources(source_roots["cargo_packages"]),
                }
                write_json(output / "sources.after.json", after)
                require_sources_unchanged(before, after)
                runner["sources_unchanged"] = True
            except Exception as error:
                runner["sources_unchanged"] = False
                if failure is None:
                    failure = f"{type(error).__name__}: {error}"
                runner["source_verification_error"] = f"{type(error).__name__}: {error}"
        elif not (output / "sources.after.json").exists():
            write_json(output / "sources.after.json", {"status": "not_captured", "failure": failure})
        runner["failure"] = failure
        runner["passed"] = failure is None and runner.get("sources_unchanged") is True and "capture" in runner
        write_json(output / "gate.json", runner)

    if failure:
        print(f"FAIL config-paths native gate: {failure}; evidence={output}", file=sys.stderr)
        return 1
    print(
        f"PASS config-paths native gate: {runner['capture']['executed_pairs']}/"
        f"{runner['capture']['declared']} real pairs on {runner['host_os']}/{runner['host_arch']}; "
        f"evidence={output}",
        flush=True,
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
