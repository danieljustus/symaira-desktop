#!/usr/bin/env python3
"""Record the Go PDF adapter observations for a Rust frozen replay.

`verify.py` (render-pdfops-differential) built the Go oracle drivers and the
Rust probes and compared them live. After the Go deletion (RUST-019) nothing
may need Go, so this script runs only the *Go* drivers through the same three
corpora and commits what they observed:

* pdfops: split/merge/rotate/parse over `verify.corpus()` with the controlled
  `pdf_tool_stub` standing in for Poppler/qpdf (`internal/ingest/testdata/portpdfops`);
* press: the Typst profile/discovery corpus (`internal/pdf/testdata/portpress`);
* safety: process-tree, output-flood and publication-race checks against a
  Python tool shim (`safety.py`).

`crates/symdesk-render/tests/pdfops_frozen.rs` rebuilds each world, runs the
Rust probes and compares the normalized observations. The stub is the Rust
example `pdf_tool_stub`; it is an input to both languages, not an oracle.

Usage: record.py [--check] [--stub PATH] [--output FILE]
Without --stub the example is built with `cargo build -p symdesk-render
--example pdf_tool_stub --locked`.
"""
import argparse
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import time

sys.dont_write_bytecode = True
HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[2]
sys.path[:0] = [str(HERE), str(ROOT / "scripts/rust-port")]
import verify as pdfverify  # noqa: E402  (corpus, snapshot, digest)
from press.verify import press_cases  # noqa: E402
from safety import TOOL, safety_cases  # noqa: E402

DEFAULT_OUTPUT = ROOT / "testdata/port/render/pdfops-recording.json"
NAMES = pdfverify.NAMES
TOOLS = pdfverify.TOOLS
STUB_INFO = "Title: fixture\nPages: 4\n"
REQUEST_KEYS = {"operation", "layer", "input", "inputs", "output", "pages", "degrees", "cancel", "cancel_marker"}


def normalizer(*pairs):
    """Replace variable roots, then the random output-staging suffix.

    Applied to the JSON text exactly like `verify.normalized` (which this
    extends with the stub/tool directories); the Rust twin does the same on
    its own serialization.
    """
    def apply(value, root):
        text = json.dumps(value, ensure_ascii=False)
        for needle, token in ((str(root), "<ROOT>"), *pairs):
            text = text.replace(needle, token)
        text = re.sub(r'\.symdesk-pdf-[^/\\" ]+', ".symdesk-pdf-<TEMP>", text)
        return json.loads(text)
    return apply


def run(binary, cwd, env, stdin, timeout=40):
    process = subprocess.run([str(binary)], cwd=cwd, env=env, input=stdin, capture_output=True, timeout=timeout)
    if process.returncode or process.stderr:
        raise RuntimeError(f"{binary.name}: exit {process.returncode}\n{process.stderr.decode(errors='replace')}")
    return process.stdout


def private_env(world, **extra):
    env = {"TZ": "UTC", "LC_ALL": "C"}
    for key in ("HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "TMPDIR", "TMP", "TEMP"):
        directory = world / ("env-" + key.lower())
        directory.mkdir(mode=0o700, exist_ok=True)
        env[key] = str(directory)
    env.update(extra)
    return env


def record_pdfops(go, stub, base):
    tools = base / "tools"
    tools.mkdir()
    for name in NAMES:
        shutil.copy2(stub, tools / name)
    normalize = normalizer((str(tools), "<TOOLS>"), (str(stub), "<STUB>"))
    results = []
    for case in pdfverify.corpus():
        world = base / "pdfops" / case["id"]
        world.mkdir(parents=True, mode=0o700)
        (world / "sub").mkdir(mode=0o700)
        for name in ("a.pdf", "b.pdf"):
            (world / name).write_bytes(f"%PDF-fixture-{name}\n".encode())
            (world / name).chmod(0o600)
        for key in ("home", "tmp", "config", "data", "cache", "state"):
            (world / key).mkdir(mode=0o700)
        if case.get("existing"):
            (world / "out").mkdir(mode=0o755)
            (world / "out/result.pdf").write_text("previous")
            (world / "out/result.pdf").chmod(0o640)
        env = {"TZ": "UTC", "LC_ALL": "C", "PATH": str(tools), "HOME": str(world / "home"), "USERPROFILE": str(world / "home"),
               "TMPDIR": str(world / "tmp"), "TMP": str(world / "tmp"), "TEMP": str(world / "tmp"),
               "XDG_CONFIG_HOME": str(world / "config"), "XDG_DATA_HOME": str(world / "data"),
               "XDG_CACHE_HOME": str(world / "cache"), "XDG_STATE_HOME": str(world / "state"),
               "PDF_STUB_MODE": case.get("mode", ""), "PDF_STUB_TOOL": case.get("tool", ""),
               "PDF_STUB_INFO": case.get("info", STUB_INFO)}
        request = {k: v for k, v in case.items() if k in REQUEST_KEYS}
        paths: dict[str, str] = dict(zip(TOOLS, NAMES))
        if case.get("missing"):
            paths[case["missing"]] = "missing-tool"
        if case.get("empty"):
            paths[case["empty"]] = ""
        request["tools"] = paths
        before = pdfverify.snapshot(world)
        response = json.loads(run(go["pdfops"], world, env, json.dumps(request).encode()))
        trace = [json.loads(line) for line in (world / "trace.jsonl").read_bytes().split(b"\n") if line] if (world / "trace.jsonl").exists() else []
        after = pdfverify.snapshot(world)
        for name in ("a.pdf", "b.pdf"):
            assert before[name] == after[name], "input changed"
        assert not list((world / "tmp").iterdir()), "workspace leaked"
        assert not list(world.rglob(".symdesk-pdf-*")), "output stage leaked"
        results.append({
            "id": case["id"], "request": request, "existing": bool(case.get("existing")),
            "stub": {"mode": env["PDF_STUB_MODE"], "tool": env["PDF_STUB_TOOL"], "info": env["PDF_STUB_INFO"]},
            "observation": normalize({"response": response, "trace": trace, "before": before, "after": after}, world),
        })
    return results


def record_press(go, stub, base):
    normalize = normalizer((str(stub), "<STUB>"))
    results = []
    for case in press_cases():
        world = base / "press" / case["id"]
        world.mkdir(parents=True, mode=0o700)
        bindir = world / "bin"
        bindir.mkdir(mode=0o700)
        tool = bindir / "typst"
        if not case.get("absent"):
            shutil.copy2(stub, tool)
        env = private_env(world, PATH=str(bindir), PDF_STUB_TOOL="typst", PDF_STUB_MODE=case.get("mode", ""),
                          PDF_STUB_INFO=case.get("info", "typst 0.15.0 (abcdef)\n"))
        steps = [{k: (v.replace("TOOL", "<TOOL>").replace("STUB", "<STUB>") if isinstance(v, str) else v) for k, v in step.items()}
                 for step in case["steps"]]
        live = [{k: (v.replace("<TOOL>", str(tool)).replace("<STUB>", str(stub)) if isinstance(v, str) else v) for k, v in step.items()}
                for step in steps]
        response = json.loads(run(go["press"], world, env, json.dumps(live).encode()))
        assert isinstance(response, list) and len(response) == len(steps)
        trace = [json.loads(line) for line in (world / "trace.jsonl").read_bytes().split(b"\n") if line] if (world / "trace.jsonl").exists() else []
        assert not list((world / "env-tmpdir").iterdir()), "discovery capture leaked"
        results.append({
            "id": case["id"], "steps": steps, "absent": bool(case.get("absent")),
            "stub": {"mode": env["PDF_STUB_MODE"], "info": env["PDF_STUB_INFO"]},
            "observation": normalize({"response": response, "trace": trace}, world),
        })
    return results


def alive(pid):
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    return True


def record_safety(go, base):
    normalize = normalizer()
    results = []
    for operation, mode in safety_cases():
        world = base / "safety" / f"{operation}-{mode}"
        world.mkdir(parents=True, mode=0o700)
        output = "parts/part-001.pdf" if operation == "split" else "out/result.pdf"
        env = private_env(world, PATH="", SAFETY_MODE=mode, SAFETY_OUTPUT=output)
        original = b"controlled input; input preservation is byte-exact\n"
        for name in ("a.pdf", "b.pdf"):
            (world / name).write_bytes(original)
        tool = world / ("typst" if operation == "press" else "qpdf")
        tool.write_text("#!<PYTHON>\n" + TOOL)
        tool.chmod(0o700)
        info = world / "pdfinfo"
        info.write_text("#!<PYTHON>\nprint('Pages: 4')\n")
        info.chmod(0o700)
        for path in (tool, info):  # materialize the interpreter for this world
            path.write_text(path.read_text().replace("<PYTHON>", sys.executable))
        ready = "ready" if mode == "descendant-cancel" else ""
        if operation == "press":
            request = [dict(operation="detect", name="<TOOL>", cancel_marker=ready)]
            live = [dict(operation="detect", name=str(tool), cancel_marker=ready)]
        else:
            request = dict(operation=operation, layer="api", input="a.pdf", inputs=["a.pdf", "b.pdf"],
                           output="parts" if operation == "split" else "out/result.pdf", pages="1", degrees=90,
                           tools={"QPDF": "<TOOL>", "PDFInfo": "<INFO>", "PDFSeparate": "unused", "PDFUnite": "unused"},
                           cancel_marker=ready)
            live = json.loads(json.dumps(request).replace("<TOOL>", str(tool)).replace("<INFO>", str(info)))
        binary = go["press" if operation == "press" else "pdfops"]
        started = time.monotonic()
        response = json.loads(run(binary, world, env, json.dumps(live).encode()))
        assert time.monotonic() - started < 8, "bounded operation exceeded eight seconds"
        pids = []
        if mode.startswith("descendant-"):
            pids = [int((world / name).read_text()) for name in ("ready", "descendant-ready")]
            deadline = time.monotonic() + 2
            while any(alive(pid) for pid in pids) and time.monotonic() < deadline:
                time.sleep(0.005)
            assert not any(alive(pid) for pid in pids), "product left an owned process alive"
        for pid_file in ("leader", "ready", "descendant-ready"):
            if (world / pid_file).is_file() and alive(int((world / pid_file).read_text())):
                os.kill(int((world / pid_file).read_text()), 9)
        for name in ("a.pdf", "b.pdf"):
            assert (world / name).read_bytes() == original, "input changed"
        assert not list(world.rglob(".symdesk-pdf-*")), "output staging leaked"
        results.append({"id": f"safety-{operation}-{mode}", "operation": operation, "mode": mode,
                        "request": request, "observation": normalize(response, world)})
    return results


def build_go(base):
    go = {}
    for key, package in (("pdfops", "./internal/ingest/testdata/portpdfops"), ("press", "./internal/pdf/testdata/portpress")):
        binary = base / f"{key}-go"
        subprocess.run(["go", "build", "-o", str(binary), package], cwd=ROOT, check=True, env=dict(os.environ, GOTOOLCHAIN="go1.26.10"))
        go[key] = binary
    identity = json.loads(subprocess.check_output([str(go["pdfops"]), "--identity"]))
    press_identity = json.loads(subprocess.check_output([str(go["press"]), "--identity"]))
    reference = json.loads((HERE / "reference-v2.json").read_text())
    assert identity == press_identity and identity["oracle"] == reference["oracle"] and identity["compiler"] == "go1.26.10", identity
    return go, identity


def find_stub(explicit):
    if explicit:
        return Path(explicit).resolve(strict=True)
    cargo = shutil.which("cargo") or str(Path.home() / ".cargo/bin/cargo")
    target = Path(os.environ.get("CARGO_TARGET_DIR", ROOT / "target"))
    # The fixture check runs under a toolchain-only PATH; cargo still needs the
    # system linker driver and its own proxies.
    env = dict(os.environ, PATH=os.pathsep.join([os.environ.get("PATH", ""), str(Path.home() / ".cargo/bin"), "/usr/bin", "/bin"]))
    subprocess.run([cargo, "build", "-p", "symdesk-render", "--example", "pdf_tool_stub", "--locked"], cwd=ROOT, check=True, env=env)
    return (target / "debug/examples/pdf_tool_stub").resolve(strict=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--check", action="store_true")
    parser.add_argument("--stub")
    parser.add_argument("--output", type=Path, default=DEFAULT_OUTPUT)
    args = parser.parse_args()
    stub = find_stub(args.stub)
    with tempfile.TemporaryDirectory(prefix="symdesk-pdfops-record-") as temporary:
        base = Path(os.path.realpath(temporary))
        go, identity = build_go(base)
        document = {
            "schema_version": 1,
            "oracle": identity["oracle"],
            "compiler": identity["compiler"],
            "stub_default_info": STUB_INFO,
            "safety_tool": TOOL,
            "pdfops": record_pdfops(go, stub, base),
            "press": record_press(go, stub, base),
            "safety": record_safety(go, base),
        }
    encoded = (json.dumps(document, indent=1, ensure_ascii=False) + "\n").encode()
    if args.check:
        if not args.output.is_file() or args.output.read_bytes() != encoded:
            print(f"{args.output} drifted from the live Go PDF adapters", file=sys.stderr)
            return 1
        print(f"PASS {args.output.name} matches the live Go PDF adapters "
              f"({len(document['pdfops'])} pdfops, {len(document['press'])} press, {len(document['safety'])} safety)")
        return 0
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_bytes(encoded)
    print(f"recorded {args.output} ({len(document['pdfops'])} pdfops, {len(document['press'])} press, {len(document['safety'])} safety)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
