#!/usr/bin/env python3
"""Build two source revisions and exercise a disposable Rust/Go SymRoom handoff."""

import argparse
import hashlib
import json
import os
import platform
from pathlib import Path
import shutil
import stat
import subprocess
import tempfile
import sys


def run(args, *, cwd, env=None):
    result = subprocess.run(
        [str(arg) for arg in args], cwd=cwd, env=env, text=True,
        stdout=subprocess.PIPE, stderr=subprocess.PIPE,
    )
    if result.returncode:
        raise RuntimeError(
            f"{args[0]} exited {result.returncode}\n{result.stdout}{result.stderr}"
        )
    return result


def source_revision(root, ref):
    return run(["git", "rev-parse", "--verify", f"{ref}^{{commit}}"], cwd=root).stdout.strip()


def sha256(path):
    digest = hashlib.sha256()
    with open(path, "rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def ensure_report_safe(report, sensitive_key, failure, cleanup_error):
    report_text = json.dumps(report)
    error_text = "; ".join(
        part for part in (str(failure) if failure else None, cleanup_error) if part
    )
    if sensitive_key and (sensitive_key in report_text or sensitive_key in error_text):
        raise RuntimeError("source-bound report contains identity private key; report suppressed")


def select_msvc_linker(where_output):
    return next((path.strip() for path in where_output.splitlines()
                 if "\\vc\\tools\\msvc\\" in path.casefold()
                 and path.casefold().endswith("\\link.exe")), None)


def msvc_linker_from_installation(installation, target, host):
    if not installation:
        return None
    tools = Path(installation) / "VC" / "Tools" / "MSVC"
    for version in sorted(tools.glob("*"), reverse=True):
        for host_name in (host, "Hostx64", "HostARM64"):
            linker = version / "bin" / host_name / target / "link.exe"
            if linker.is_file():
                return str(linker)
    return None


def parse_msvc_library_environment(output):
    allowed = {"LIB", "LIBPATH", "INCLUDE", "VCToolsInstallDir", "WindowsSdkDir",
               "WindowsSDKVersion", "UniversalCRTSdkDir"}
    return {name: value for line in output.splitlines()
            for name, separator, value in [line.partition("=")]
            if separator and name in allowed}


def msvc_library_environment(installation, target, temp, system_root):
    if not installation:
        return {}
    dev_cmd = Path(installation) / "Common7" / "Tools" / "VsDevCmd.bat"
    if not dev_cmd.is_file():
        return {}
    arch = "arm64" if target == "arm64" else "amd64"
    batch = temp / "msvc-env.cmd"
    batch.write_text(
        f'@echo off\ncall "{dev_cmd}" -no_logo -arch={arch} -host_arch={arch} >nul\n'
        'if errorlevel 1 exit /b 1\nset\n', encoding="utf-8",
    )
    result = subprocess.run(
        [str(system_root / "System32" / "cmd.exe"), "/d", "/c", str(batch)],
        cwd=temp, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
    )
    if result.returncode:
        return {}
    return parse_msvc_library_environment(result.stdout)


def is_git_posix_bin(path):
    return path.replace("/", "\\").casefold().endswith("\\git\\usr\\bin")


def build_environment(temp, rustc):
    home = temp / "build-home"
    data = temp / "build-data"
    config = temp / "build-config"
    cache = temp / "build-cache"
    cargo_home = temp / "cargo-home"
    paths = (home, data, config, cache, cargo_home)
    for path in paths:
        path.mkdir(parents=True, exist_ok=True)

    # Native compilers and linkers are installed on the runner and may live
    # outside fixed system directories. Keep PATH for tool discovery, while
    # clearing HOME and the credential/user-state environment.
    path_entries = os.environ.get("PATH", "").split(os.pathsep)
    if os.name == "nt":
        # Git Bash ships a GNU link.exe. Rust can discover the MSVC linker
        # through the installed toolchain, but only if that GNU binary is
        # absent from the isolated build PATH.
        path_entries = [path for path in path_entries if not is_git_posix_bin(path)]
        system_root = Path(os.environ.get("SystemRoot", r"C:\Windows"))
        path_entries.extend((system_root / "System32", system_root))

    env = {
        "HOME": str(home),
        "USERPROFILE": str(home),
        "XDG_DATA_HOME": str(data),
        "XDG_CONFIG_HOME": str(config),
        "XDG_CACHE_HOME": str(cache),
        "TMPDIR": str(temp / "build-tmp"),
        "TEMP": str(temp / "build-tmp"),
        "TMP": str(temp / "build-tmp"),
        "PATH": os.pathsep.join(str(path) for path in path_entries),
        "CARGO_HOME": str(cargo_home),
        "CARGO_TARGET_DIR": str(temp / "cargo-target"),
        "RUSTC": str(rustc),
        "RUSTUP_HOME": os.environ.get("RUSTUP_HOME", str(Path.home() / ".rustup")),
        "GOCACHE": str(temp / "go-cache"),
        "GOMODCACHE": str(temp / "go-mod-cache"),
        "GOPATH": str(temp / "go-path"),
        "GOTMPDIR": str(temp / "go-tmp"),
        "GOENV": "off",
        "GOTOOLCHAIN": "local",
        "CGO_ENABLED": "0",
        "GOSUMDB": "sum.golang.org",
        "TZ": "UTC",
    }
    if os.name == "nt":
        env["SystemRoot"] = str(system_root)
        env["WINDIR"] = str(system_root)
        linker_paths = subprocess.run(["where.exe", "link.exe"], text=True,
                                      stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        linker = select_msvc_linker(linker_paths.stdout)
        target = "arm64" if platform.machine().casefold() in ("arm64", "aarch64") else "x64"
        vswhere = shutil.which("vswhere.exe") or str(
            Path(os.environ.get("ProgramFiles(x86)", r"C:\Program Files (x86)"))
            / "Microsoft Visual Studio" / "Installer" / "vswhere.exe"
        )
        installation = ""
        if Path(vswhere).is_file():
            installation = subprocess.run(
                [vswhere, "-latest", "-products", "*", "-property", "installationPath"],
                text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            ).stdout.strip()
        if not linker:
            host = "HostARM64" if target == "arm64" else "Hostx64"
            linker = msvc_linker_from_installation(installation, target, host)
        if linker and Path(linker).is_file():
            triple_arch = "AARCH64" if target == "arm64" else "X86_64"
            env[f"CARGO_TARGET_{triple_arch}_PC_WINDOWS_MSVC_LINKER"] = linker
            env["PATH"] = os.pathsep.join((str(Path(linker).parent), env["PATH"]))
            if not os.environ.get("LIB"):
                env.update(msvc_library_environment(installation, target, temp, system_root))
        # MSVC discovery and its library search paths are part of the native
        # toolchain environment. Dropping them lets Git's GNU link.exe win.
        for name in (
            "INCLUDE", "LIB", "LIBPATH", "VCINSTALLDIR", "VCToolsInstallDir",
            "WindowsSdkDir", "WindowsSDKVersion", "UniversalCRTSdkDir",
            "VSCMD_ARG_HOST_ARCH", "VSCMD_ARG_TGT_ARCH",
        ):
            if value := os.environ.get(name):
                env[name] = value
    for name in ("build-tmp", "go-cache", "go-mod-cache", "go-path", "go-tmp"):
        (temp / name).mkdir(parents=True, exist_ok=True)
    return env


def cleanup_worktrees(root, paths, env, git):
    listed = subprocess.run(
        [git, "worktree", "list", "--porcelain"], cwd=root, env=env,
        text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
    )
    if listed.returncode:
        return [f"cannot inspect worktrees: {listed.stderr.strip()}"]
    registered = {
        Path(line.removeprefix("worktree ")).resolve()
        for line in listed.stdout.splitlines() if line.startswith("worktree ")
    }
    failures = []
    for path in paths:
        resolved = path.resolve()
        if resolved not in registered:
            if path.exists():
                failures.append(f"unregistered worktree path preserved: {path}")
            continue
        status = subprocess.run(
            [git, "-C", str(path), "status", "--porcelain", "--ignored", "--untracked-files=all"],
            cwd=root, env=env, text=True,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        )
        if status.returncode:
            failures.append(f"cannot inspect worktree {path}: {status.stderr.strip()}")
            continue
        if status.stdout:
            failures.append(f"modified worktree preserved: {path}")
            continue
        removed = subprocess.run(
            [git, "worktree", "remove", str(path)], cwd=root, env=env,
            text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        )
        if removed.returncode:
            failures.append(f"could not remove clean worktree {path}: {removed.stderr.strip()}")
    return failures


def remove_temp_tree(path):
    def make_writable_and_retry(function, failed_path, _error):
        failed = Path(failed_path)
        for candidate in (failed.parent, failed):
            try:
                mode = os.lstat(candidate).st_mode
            except FileNotFoundError:
                continue
            if stat.S_ISLNK(mode):
                continue
            if os.name == "nt":
                os.chmod(candidate, mode | stat.S_IWUSR)
            else:
                os.chmod(candidate, mode | stat.S_IWUSR, follow_symlinks=False)
        function(failed_path)

    shutil.rmtree(path, onerror=make_writable_and_retry)


def extract_git_blobs(root, revision, destination, env, git, excluded_prefixes=()):
    """Materialize regular blobs without asking Git for Windows to unpack invalid names."""
    tree = subprocess.run(
        [git, "ls-tree", "-r", "-z", revision], cwd=root, env=env,
        stdout=subprocess.PIPE, stderr=subprocess.PIPE,
    )
    if tree.returncode:
        raise RuntimeError(f"git ls-tree exited {tree.returncode}: {tree.stderr.decode(errors='replace')}")
    entries = []
    for entry in tree.stdout.split(b"\0"):
        if not entry:
            continue
        meta, path_bytes = entry.split(b"\t", 1)
        mode, kind, object_id = meta.split(b" ")
        path = path_bytes.decode("utf-8")
        parts = path.split("/")
        if path.startswith("/") or any(part in ("", ".", "..") for part in parts):
            raise RuntimeError(f"unsafe Git tree path: {path!r}")
        if any(path.startswith(prefix) for prefix in excluded_prefixes):
            continue
        if kind != b"blob" or mode not in (b"100644", b"100755"):
            continue
        entries.append((path, object_id, mode))

    destination.mkdir(parents=True)
    process = subprocess.Popen(
        [git, "cat-file", "--batch"], cwd=root, env=env,
        stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
    )
    try:
        for path, object_id, mode in entries:
            process.stdin.write(object_id + b"\n")
            process.stdin.flush()
            header = process.stdout.readline().split()
            if len(header) != 3 or header[0] != object_id or header[1] != b"blob":
                raise RuntimeError(f"unexpected Git blob header for {path!r}: {header!r}")
            remaining = int(header[2])
            target = destination.joinpath(*path.split("/"))
            target.parent.mkdir(parents=True, exist_ok=True)
            with target.open("wb") as output:
                while remaining:
                    block = process.stdout.read(min(remaining, 1024 * 1024))
                    if not block:
                        raise RuntimeError(f"truncated Git blob for {path!r}")
                    output.write(block)
                    remaining -= len(block)
            if process.stdout.read(1) != b"\n":
                raise RuntimeError(f"missing Git blob delimiter for {path!r}")
            if mode == b"100755":
                target.chmod(target.stat().st_mode | stat.S_IXUSR)
        process.stdin.close()
        if process.wait() != 0:
            raise RuntimeError(f"git cat-file failed: {process.stderr.read().decode(errors='replace')}")
    finally:
        if process.poll() is None:
            process.kill()
            process.wait()
        process.stdin.close()
        process.stdout.close()
        process.stderr.close()


def invoke(binary, args, work, room, identity, label, expected_code=0):
    env = {
        "HOME": str(work / "home"),
        "USERPROFILE": str(work / "home"),
        "XDG_DATA_HOME": str(work / "data"),
        "XDG_CONFIG_HOME": str(work / "config"),
        "TMPDIR": str(work / "tmp"),
        "SYMROOM_ROOM_DIR": str(room),
        "TZ": "UTC",
        "LC_ALL": "C",
        "LANG": "C",
        "PATH": str(work / "path"),
    }
    if identity:
        env["SYMROOM_DEFAULT_IDENTITY"] = "rollback"
        env["SYMROOM_IDENTITY_KEY"] = identity
    for name in ("home", "data", "config", "tmp", "path"):
        (work / name).mkdir(parents=True, exist_ok=True)
    result = subprocess.run(
        [str(binary), *args], cwd=work, env=env, text=True,
        stdout=subprocess.PIPE, stderr=subprocess.PIPE,
    )
    if result.returncode != expected_code:
        raise RuntimeError(
            f"{label} exited {result.returncode}, expected {expected_code}\n"
            f"{result.stdout}{result.stderr}"
        )
    return {
        "step": label,
        "args": ["symroom", *args],
        "exit_code": result.returncode,
        "stdout": result.stdout,
        "stderr": result.stderr,
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go-ref", default="v0.12.2", help="older Go fallback Git revision")
    parser.add_argument("--rust-ref", default="HEAD", help="Rust candidate Git revision")
    parser.add_argument("--report", type=Path, help="write the source-bound JSON report here")
    options = parser.parse_args()

    root = Path(__file__).resolve().parents[2]
    go_revision = source_revision(root, options.go_ref)
    rust_revision = source_revision(root, options.rust_ref)
    report = {
        "schema_version": 1,
        "result": "PASS",
        "host": {"system": platform.system(), "machine": platform.machine()},
        "sources": {
            "rust": {"ref": options.rust_ref, "commit": rust_revision},
            "go_fallback": {"ref": options.go_ref, "commit": go_revision},
        },
        "cleanup": {"status": "pending"},
        "steps": [],
    }

    cargo = shutil.which("cargo")
    rustc = shutil.which("rustc")
    go = shutil.which("go")
    git = shutil.which("git")
    if not cargo or not rustc or not go or not git:
        raise RuntimeError("cargo, rustc, go, and git must be available on the host PATH")
    temp = Path(tempfile.mkdtemp(prefix="symroom-rollback-"))
    rust_tree, go_tree = temp / "rust-source", temp / "go-source"
    build_env = build_environment(temp, rustc)
    failure = None
    sensitive_key = None
    try:
        run([git, "worktree", "add", "--detach", rust_tree, rust_revision], cwd=root, env=build_env)
        if os.name == "nt":
            # Git for Windows rejects the frozen tag's Notion test fixture filenames.
            extract_git_blobs(root, go_revision, go_tree, build_env, git,
                              ("internal/ingest/internal/notionimport/testdata/fixture/",))
        else:
            run([git, "worktree", "add", "--detach", go_tree, go_revision], cwd=root, env=build_env)
        rust_bin, go_bin = temp / "symroom-rust", temp / "symroom-go"
        suffix = ".exe" if os.name == "nt" else ""
        rust_bin = rust_bin.with_suffix(suffix) if suffix else rust_bin
        go_bin = go_bin.with_suffix(suffix) if suffix else go_bin
        run([cargo, "build", "--locked", "--release", "-p", "symroom-cli",
             "--manifest-path", rust_tree / "Cargo.toml"], cwd=rust_tree, env=build_env)
        rust_output = temp / "cargo-target" / "release" / f"symroom{suffix}"
        shutil.copy2(rust_output, rust_bin)
        report["toolchain"] = {
            "go": run([go, "version"], cwd=go_tree, env=build_env).stdout.strip(),
            "rustc": run([rustc, "--version"], cwd=rust_tree, env=build_env).stdout.strip(),
            "cargo": run([cargo, "--version"], cwd=rust_tree, env=build_env).stdout.strip(),
        }
        run([go, "build", "-trimpath", "-buildvcs=false", "-o", go_bin, "./cmd/symroom"],
            cwd=go_tree, env=build_env)
        report["binaries"] = {
            "rust": {"sha256": sha256(rust_bin)},
            "go_fallback": {
                "sha256": sha256(go_bin),
                "provenance": "local build from the selected Go source revision; not a public release artifact",
            },
        }
        room, work = temp / "room", temp / "runtime"

        # Rust creates the identity and signed room state in isolated HOME/XDG paths.
        report["steps"].append(invoke(rust_bin, ["identity", "create", "rollback"],
                                      work, room, None, "rust creates identity"))
        identity_file = work / "data/symroom/identities/rollback.json"
        identity_data = json.loads(identity_file.read_text())
        sensitive_key = identity_data["private_key"]
        key = sensitive_key
        report["steps"].append(invoke(rust_bin, ["init", str(room), "--identity", "rollback"],
                                      work, room, key, "rust initializes room"))
        report["steps"].append(invoke(rust_bin, ["note", "--identity", "rollback", "written by Rust"],
                                      work, room, key, "rust writes signed note"))

        # The older Go binary must verify and read Rust's event before mutating the same copy.
        go_verify = invoke(go_bin, ["verify"], work, room, key, "older Go verifies Rust journal")
        report["steps"].append(go_verify)
        go_log = invoke(go_bin, ["log"], work, room, key, "older Go reads Rust journal")
        report["steps"].append(go_log)
        if "written by Rust" not in go_log["stdout"]:
            raise RuntimeError("older Go fallback did not read the Rust-authored note")

        tampered = temp / "tampered-room"
        shutil.copytree(room, tampered)
        segment = next((tampered / "journal").glob("*.jsonl"))
        lines = segment.read_text().splitlines()
        event = json.loads(lines[-1])
        event["body"]["text"] = "tampered note body"
        lines[-1] = json.dumps(event, separators=(",", ":"))
        segment.write_text("\n".join(lines) + "\n")
        tamper_result = invoke(
            go_bin, ["verify"], work, tampered, key,
            "older Go rejects tampered signature", expected_code=1,
        )
        report["steps"].append(tamper_result)
        if "signature verification failed" not in tamper_result["stdout"]:
            raise RuntimeError("older Go did not reject the tampered signed body")

        report["steps"].append(invoke(go_bin, ["note", "--identity", "rollback", "written by Go fallback"],
                                      work, room, key, "older Go appends signed note"))

        rust_verify = invoke(rust_bin, ["verify"], work, room, key,
                             "Rust verifies mixed-version journal")
        report["steps"].append(rust_verify)
        rust_log = invoke(rust_bin, ["log"], work, room, key,
                          "Rust reads mixed-version journal")
        report["steps"].append(rust_log)
        if "written by Rust" not in rust_log["stdout"] or "written by Go fallback" not in rust_log["stdout"]:
            raise RuntimeError("Rust did not read both signed notes after Go rollback")

        journal = room / "journal"
        report["room"] = {
            "journal_files": {
                path.name: sha256(path) for path in sorted(journal.glob("*.jsonl"))
            },
            "journal_sha256": sha256_bytes(b"".join(
                path.read_bytes() for path in sorted(journal.glob("*.jsonl"))
            )),
        }
    except BaseException as error:
        failure = error
    cleanup_failures = cleanup_worktrees(root, [rust_tree] if os.name == "nt" else [rust_tree, go_tree], build_env, git)
    cleanup_error = None
    if cleanup_failures:
        report["cleanup"] = {
            "status": "FAIL",
            "workspace": str(temp),
            "details": cleanup_failures,
        }
        cleanup_error = "temporary workspace preserved at " + str(temp) + ": " + "; ".join(cleanup_failures)
    else:
        try:
            remove_temp_tree(temp)
            report["cleanup"] = {"status": "PASS", "workspace_removed": True}
        except OSError as error:
            report["cleanup"] = {
                "status": "FAIL",
                "workspace": str(temp) if temp.exists() else None,
                "details": [str(error)],
            }
            cleanup_error = f"could not remove temporary workspace {temp}: {error}"

    report["test_result"] = "PASS" if failure is None else "FAIL"
    report["result"] = "PASS" if failure is None and cleanup_error is None else "FAIL"
    if failure:
        report["failure"] = str(failure)
    ensure_report_safe(report, sensitive_key, failure, cleanup_error)
    encoded = json.dumps(report, indent=2, sort_keys=True) + "\n"
    if options.report:
        options.report.write_text(encoded)
    print(encoded, end="")
    if failure or cleanup_error:
        message = "; ".join(part for part in (str(failure) if failure else None, cleanup_error) if part)
        raise RuntimeError(message)


def sha256_bytes(value):
    return hashlib.sha256(value).hexdigest()


if __name__ == "__main__":
    main()
