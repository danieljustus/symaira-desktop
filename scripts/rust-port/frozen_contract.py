#!/usr/bin/env python3
"""Go-independent Rust contract aggregate (issue #1153).

Every Make `*-differential` target either replays committed Go-oracle fixtures
through `$(CARGO) test` or compares two live binaries. This runner executes each
distinct Rust replay with every `go` executable removed from PATH, inside a
disposable HOME/XDG/TMP root, and requires the exact recorded test counts from
`frozen-contract.json`. Targets without a Rust replay must be declared
`live_only`; a new differential target that is neither fails closed.
"""
import argparse
import json
import os
from pathlib import Path
import re
import shlex
import shutil
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[2]
MANIFEST = Path(__file__).with_name("frozen-contract.json")
TARGET = re.compile(r"^([A-Za-z0-9_.-]+-differential):(?!=)")
RESULT = re.compile(r"^test result: (\w+)\. (\d+) passed; (\d+) failed; (\d+) ignored;", re.M)
GO_NAMES = ("go", "go.exe")


def differential_targets(makefile):
    """Map each `*-differential` target to its `$(CARGO) test` commands."""
    targets, current = {}, None
    for line in makefile.splitlines():
        match = TARGET.match(line)
        if match:
            current = match.group(1)
            if current in targets:
                raise ValueError(f"duplicate Make target {current}")
            targets[current] = []
        elif line.startswith("\t") and current:
            command = line.strip()
            if command.startswith("$(CARGO) test"):
                command = command.replace("$(CARGO)", "cargo", 1)
                if "$" in command:
                    raise ValueError(f"{current}: unexpanded variable in {command!r}")
                targets[current].append(command)
        elif line and not line.startswith("\t"):
            current = None
    return targets


def plan(targets, manifest):
    """Return ordered unique replay commands; reject unclassified targets."""
    live_only = manifest["live_only"]
    replays = manifest["replays"]
    errors, commands = [], []
    for name, cargo in targets.items():
        if cargo and name in live_only:
            errors.append(f"{name}: has a Rust replay but is declared live_only")
        elif not cargo and name not in live_only:
            errors.append(f"{name}: no Rust replay and not declared live_only")
        for command in cargo:
            if command not in commands:
                commands.append(command)
    errors += [f"{name}: stale live_only entry" for name in live_only if name not in targets]
    errors += [f"{command!r}: missing recorded counts" for command in commands if command not in replays]
    errors += [f"{command!r}: stale replay entry" for command in replays if command not in commands]
    return commands, errors


def go_free_env(root):
    """Return an environment with no `go` on PATH and private user roots."""
    env = dict(os.environ)
    home = Path.home()
    env.setdefault("CARGO_HOME", str(home / ".cargo"))
    env.setdefault("RUSTUP_HOME", str(home / ".rustup"))
    keep = [entry for entry in env.get("PATH", "").split(os.pathsep)
            if entry and not any(Path(entry, name).is_file() for name in GO_NAMES)]
    env["PATH"] = os.pathsep.join(keep)
    for name in ("GOROOT", "GOPATH", "GOTOOLCHAIN", "GOFLAGS", "GOWORK", "GOENV"):
        env.pop(name, None)
    for name, sub in (("HOME", "home"), ("USERPROFILE", "home"), ("XDG_CONFIG_HOME", "xdg-config"),
                      ("XDG_DATA_HOME", "xdg-data"), ("XDG_CACHE_HOME", "xdg-cache"),
                      ("XDG_STATE_HOME", "xdg-state"), ("TMPDIR", "tmp"), ("TMP", "tmp"), ("TEMP", "tmp")):
        path = Path(root, sub)
        path.mkdir(exist_ok=True)
        env[name] = str(path)
    if shutil.which("go", path=env["PATH"]):
        raise RuntimeError("go is still reachable through PATH")
    return env


def counts(output):
    """Sum libtest result lines; a run without any result line is not evidence."""
    results = RESULT.findall(output)
    if not results:
        raise ValueError("no libtest result lines")
    if any(status != "ok" for status, *_ in results):
        raise ValueError("a test binary did not report ok")
    return {key: sum(int(r[i]) for r in results) for i, key in ((1, "passed"), (2, "failed"), (3, "ignored"))}


def check_skips(command, skips, env):
    """Each live-only skip must name exactly one listed test of this command."""
    listed = subprocess.run(shlex.split(command) + ["--", "--list", "--format", "terse"], cwd=ROOT, env=env,
                            capture_output=True, text=True)
    if listed.returncode:
        raise RuntimeError(f"{command}: --list failed\n{listed.stderr[-4000:]}")
    names = [line[:-len(": test")] for line in listed.stdout.splitlines() if line.endswith(": test")]
    for skip in skips:
        hits = [name for name in names if skip in name]
        if len(hits) != 1:
            raise RuntimeError(f"{command}: live-only skip {skip!r} matches {hits}")


def expected_counts(entry):
    """Return (passed, ignored) for this platform; platform-gated tests differ."""
    if "by_platform" not in entry:
        return entry["passed"], entry["ignored"]
    if sys.platform not in entry["by_platform"]:
        raise RuntimeError(f"no recorded counts for platform {sys.platform}")
    counts = entry["by_platform"][sys.platform]
    return counts["passed"], counts["ignored"]


def record_counts(entry, got):
    if "by_platform" in entry:
        entry["by_platform"][sys.platform] = {"passed": got["passed"], "ignored": got["ignored"]}
    else:
        entry.update(passed=got["passed"], ignored=got["ignored"])


def replay(command, entry, env):
    skips = entry.get("live_only_tests", [])
    if skips:
        check_skips(command, skips, env)
    argv = shlex.split(command)
    if skips:
        argv += ["--"] + [arg for skip in skips for arg in ("--skip", skip)]
    run = subprocess.run(argv, cwd=ROOT, env=env, capture_output=True, text=True)
    output = run.stdout + run.stderr
    if run.returncode:
        raise RuntimeError(f"{command}: exit {run.returncode}\n{output[-8000:]}")
    got = counts(run.stdout)
    if got["passed"] == 0:
        raise RuntimeError(f"{command}: zero passing cases")
    return got, len(skips)


def mutate(raw, control):
    """Return fixture bytes with one recorded Go observation changed."""
    if "append" in control:
        return raw + control["append"].encode()
    document = json.loads(raw)
    *parents, leaf = control["increment"]
    node = document
    for key in parents:
        node = node[key]
    node[leaf] += 1
    return json.dumps(document, indent=2).encode() + b"\n"


def mutation_control(control, env):
    """A frozen replay must fail once its Go-recorded fixture is mutated."""
    fixture = ROOT / control["fixture"]
    dirty = subprocess.run(["git", "status", "--porcelain", "--", control["fixture"]], cwd=ROOT,
                           capture_output=True, text=True, check=True).stdout
    if dirty:
        raise RuntimeError(f"{control['fixture']}: refusing to mutate a modified fixture")
    original = fixture.read_bytes()
    try:
        fixture.write_bytes(mutate(original, control))
        run = subprocess.run(shlex.split(control["command"]), cwd=ROOT, env=env, capture_output=True, text=True)
    finally:
        fixture.write_bytes(original)
    if run.returncode == 0:
        raise RuntimeError(f"mutation control passed unexpectedly: {control['command']} with mutated {control['fixture']}")
    if "test result: FAILED" not in run.stdout:
        raise RuntimeError(f"mutation control failed before any test ran: {control['command']}\n{run.stderr[-4000:]}")
    print(f"ok mutation rejected  {control['fixture']} -> {control['command']}", flush=True)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--record", action="store_true", help="rewrite recorded counts from this Go-free run")
    parser.add_argument("--plan", action="store_true", help="validate classification only; run nothing")
    args = parser.parse_args(argv)

    manifest = json.loads(MANIFEST.read_text())
    targets = differential_targets((ROOT / "Makefile").read_text())
    commands, errors = plan(targets, manifest)
    if args.record:
        errors = [e for e in errors if "recorded counts" not in e and "stale replay" not in e]
        manifest["replays"] = {c: manifest["replays"].get(c, {}) for c in commands}
    if errors:
        print("\n".join(errors), file=sys.stderr)
        return 1
    if args.plan:
        print(f"PASS plan: {len(commands)} Rust replays, {len(manifest['live_only'])} live-only targets")
        return 0

    total = live_total = 0
    with tempfile.TemporaryDirectory(prefix="symdesk-frozen-contract-") as root:
        env = go_free_env(root)
        for command in commands:
            entry = manifest["replays"][command]
            got, skipped = replay(command, entry, env)
            if args.record:
                record_counts(entry, got)
            elif (got["passed"], got["ignored"]) != expected_counts(entry):
                want = expected_counts(entry)
                print(f"FAIL {command}: got {got['passed']} passed/{got['ignored']} ignored, "
                      f"recorded {want[0]}/{want[1]} on {sys.platform}", file=sys.stderr)
                return 1
            total += got["passed"]
            live_total += got["passed"] + skipped
            print(f"ok {got['passed']:>5} (+{skipped} live-only)  {command}", flush=True)
        for control in manifest["mutation_controls"]:
            mutation_control(control, env)
    if args.record:
        MANIFEST.write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n")
    print(f"PASS frozen contract: {len(commands)} replays, {total} cases without Go "
          f"({live_total} in live mode), {len(manifest['live_only'])} live-only targets, {len(manifest['mutation_controls'])} mutation controls rejected")
    return 0


if __name__ == "__main__":
    sys.exit(main())
