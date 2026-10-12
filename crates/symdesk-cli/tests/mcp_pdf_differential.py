#!/usr/bin/env python3
"""Source-prepared PDF MCP probe, NOT an oracle capture or a build wrapper.

After scheduler/native and secure-runtime admission, compare the two separately
source/compiler-bound symdesk executables. Go uses the real mcp StartServer and
canonical tools registry, never a duplicated handler. No fixture is rewritten.
Retain raw stdout/stderr/exit and every runtime tree, including failed cases.
Unix process-group cleanup only; Windows native admission remains separate.
"""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import platform
import signal
import stat
import subprocess
import sys
from typing import Any

sys.dont_write_bytecode = True
import mcp_pdf_transport
ROOT = Path(__file__).resolve().parents[3]
PDF_NAMES = ["desk_split_pdf", "desk_merge_pdf", "desk_rotate_pdf",
             "split_pdf", "merge_pdf", "rotate_pdf"]


def cases():
    """Input-only corpus, using mcpgen's request/raw_input/framed case shape."""
    result = []

    def add(case_id, request, **kw):
        case = dict(id=case_id, request=request, allow_write=True,
                    tools=False, success=False, response_count=1)
        case.update(kw)
        result.append(case)

    def call(case_id, name, raw, notification=False, **kw):
        request = '{"jsonrpc":"2.0",' + ('' if notification else '"id":1,')
        request += '"method":"tools/call","params":{"name":' + json.dumps(name)
        if raw is not None:
            request += ',"arguments":' + raw
        add(case_id, request + '}}', response_count=0 if notification else 1, **kw)

    for name in PDF_NAMES:
        for label, raw in [("missing", None), ("empty", "{}"), ("null", "null"),
                           ("array", "[]"), ("scalar", "false")]:
            call(name + "-" + label, name, raw)
        call(name + "-disabled", name, '{"input":false}')
        result[-1]["allow_write"] = False
    for index, raw in enumerate([
        '{"degrees":90.0}', '{"degrees":9e1}', '{"degrees":9223372036854775808}',
        '{"degrees":-9223372036854775809}', '{"degrees":false,"degrees":90}',
        '{"pages":false,"input":1}', '{"input":null,"output":"out.pdf"}',
        '{"input":"a.pdf","input":null,"output":"out.pdf","degrees":0}',
        '{"input":"missing.pdf","output":"out.pdf","degrees":0}',
        '{"degrees":"90"}', '{"unknown":false,"DEGREES":0}', '{"degrees":1e999}',
    ]):
        call(f"rotate-validation-{index}", "desk_rotate_pdf", raw)
    for index, raw in enumerate([
        '{"inputs":"a"}', '{"inputs":[false]}', '{"inputs":[null,null]}',
        '{"output":false,"inputs":[]}', '{"inputs":["a.pdf","b.pdf"]}',
        '{"inputs":["a.pdf","b.pdf"],"inputs":null}',
    ]):
        call(f"merge-validation-{index}", "desk_merge_pdf", raw)
    call("split-missing-tool-before-input", "desk_split_pdf",
         '{"input":"missing.pdf","at":"1","output_dir":"parts"}')
    for allowed in [False, True]:
        add("catalog-" + str(allowed), '{"jsonrpc":"2.0","id":1,"method":"tools/list"}', catalog=True)
        result[-1]["allow_write"] = allowed
        for index, params in enumerate(['{"name":true}',
                                        '{"name":"desk_split_pdf","_meta":false}',
                                        '[]', '{}']):
            add(f"params-{allowed}-{index}",
                '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":' + params + '}', protocol_error=True)
            result[-1]["allow_write"] = allowed
    add("case-folded-params", '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"NAME":"desk_rotate_pdf","name":null,"ARGUMENTS":{}}}')
    call("framed-error", "desk_rotate_pdf", '{"degrees":90.0}', framed=True)
    for name in PDF_NAMES:
        if "split" in name:
            raw = '{"input":"a.pdf","at":"1,3","output_dir":"parts"}'
            expected = [["one"], ["two", "three"], ["four"]]
        elif "merge" in name:
            raw = '{"inputs":["b.pdf","a.pdf"],"output":"out/result.pdf"}'
            expected = [["five", "six", "one", "two", "three", "four"]]
        else:
            raw = '{"input":"a.pdf","output":"out/result.pdf","degrees":90,"pages":"1,3"}'
            expected = [["one", "two", "three", "four"]]
        call(name + "-real", name, raw, expected_pages=expected)
        result[-1].update(tools=True, success=True)
        call(name + "-real-disabled", name, raw)
        result[-1].update(tools=True, allow_write=False)
        for framed in [False, True]:
            for allowed in [False, True]:
                call(f"{name}-notification-{framed}-{allowed}", name, raw,
                     notification=True, tools=True, allow_write=allowed,
                     framed=framed, success=allowed, expected_pages=expected)
    call("merge-duplicate-null-element", "desk_merge_pdf",
         '{"inputs":["a.pdf","b.pdf"],"inputs":[null,"b.pdf"],"output":"out/result.pdf"}',
         expected_pages=[["one", "two", "three", "four", "five", "six"]])
    result[-1].update(tools=True, success=True)
    call("rotate-escaped-output", "desk_rotate_pdf",
         json.dumps({"input": "a.pdf", "output": "out/<&>\u2028.pdf", "degrees": -90}),
         expected_pages=[["one", "two", "three", "four"]], framed=True)
    result[-1].update(tools=True, success=True)
    for operation, raw in [
        ("merge", '{"inputs":["a.pdf","b.pdf"],"output":"a.pdf"}'),
        ("rotate", '{"input":"a.pdf","output":"a.pdf","degrees":90}'),
        ("split", '{"input":"a.pdf","output_dir":"parts","at":"1"}'),
    ]:
        call(operation + "-existing-output", "desk_" + operation + "_pdf", raw,
             existing_split=operation == "split")
        result[-1]["tools"] = True
    deep = '[' * 200 + '0' + ']' * 200
    for name, field in [("desk_search", "query"), ("desk_ask", "query"), ("desk_ls", "dir")]:
        for label, unknown in [("overflow", "1e999"), ("deep", deep)]:
            for first in [True, False]:
                fields = [json.dumps(field) + ':false', '"unknown":' + unknown]
                if not first:
                    fields.reverse()
                call(f"{name}-{label}-{first}", name, '{' + ','.join(fields) + '}', allow_write=False)
        call(name + "-missing-arguments", name, None, allow_write=False)
    for name in ["desk_search", "desk_ask"]:
        for label, raw in [("null", "null"), ("valid-empty", '{"query":""}'),
                           ("empty-with-ignored-overflow", '{"query":"","unknown":1e999}')]:
            call(name + "-" + label, name, raw, allow_write=False)
    for index, raw in enumerate([r'"\ud800"', r'"\udc00"', r'"\ud83d\ude00"', r'"\\ud800"']):
        for name in PDF_NAMES:
            call(f"{name}-surrogate-{index}", name, '{"input":' + raw + '}')
        add(f"name-surrogate-{index}", '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":' + raw + '}}', protocol_error=True)
    for allowed in [False, True]:
        for index, params in enumerate([
            '{"name":"desk_rotate_pdf","_meta":{"a":[{"n":1e999}]},"arguments":{}}',
            '{"name":false,"_meta":{"n":1e999}}',
            '{"_meta":{"n":1e999},"name":false}',
            '{"_meta":{"n":1e999},"_meta":null,"name":"desk_rotate_pdf"}',
            '{"_meta":{"n":-1e999},"name":"desk_rotate_pdf"}',
        ]):
            add(f"meta-overflow-{allowed}-{index}",
                '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":' + params + '}',
                allow_write=allowed, protocol_error=True)
    add("meta-valid-surrogates-underflow", r'{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"desk_rotate_pdf","_meta":{"n":1e-999,"text":"1e999\ud800","\udc00":true},"arguments":{}}}')
    ids = [case["id"] for case in result]
    if len(set(ids)) != len(ids) or not ids:
        raise ValueError("empty/duplicate case inventory")
    return result


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def private_file(path, data):
    with path.open("xb") as stream:
        stream.write(data)


def frame_body(raw, framed, response_count=1):
    if response_count == 0:
        if raw:
            raise AssertionError("notification emitted protocol bytes")
        return None
    if response_count != 1:
        raise AssertionError("unsupported declared response count")
    if not framed:
        rows = raw.split(b"\n")
        if len(rows) != 2 or rows[-1] != b"":
            raise AssertionError("expected exactly one LF-terminated response")
        return rows[0]
    header, body = raw.split(b"\r\n\r\n", 1)
    if header != f"Content-Length: {len(body)}".encode():
        raise AssertionError("framed length mismatch")
    return body


def main():
    p = argparse.ArgumentParser(description=__doc__)
    for name in ("go-binary", "rust-binary", "runtime-root", "output", "pdfinfo", "qpdf", "pdftotext"):
        p.add_argument("--" + name, required=True, type=Path)
    args = p.parse_args()
    if os.name != "posix":
        p.error("Unix-only probe; Windows requires its separately admitted native runner")
    binaries = {"go": args.go_binary.resolve(strict=True), "rust": args.rust_binary.resolve(strict=True)}
    tools = {name: getattr(args, name).resolve(strict=True) for name in ("pdfinfo", "qpdf", "pdftotext")}
    runtime, output = args.runtime_root.absolute(), args.output.absolute()
    # No fallback roots, no HOME inherited and no permission repair. Scheduler
    # must additionally admit mount ownership/ACLs and binary provenance.
    for root in (runtime, output):
        if root.exists() or root.is_symlink():
            raise RuntimeError("fresh retained run roots required")
        if not root.parent.is_dir() or root.parent.resolve() != root.parent:
            raise RuntimeError("existing canonical parent required")
    current = runtime.parent
    while current != current.parent:
        meta = current.stat()
        if meta.st_mode & 0o022:
            raise RuntimeError(f"insecure runtime ancestor: {current}")
        current = current.parent
    runtime.mkdir(mode=0o700)
    output.mkdir(mode=0o700)
    pdf_dir = ROOT / "scripts/rust-port/pdfops"
    sys.path.insert(0, str(pdf_dir))
    spec = importlib.util.spec_from_file_location("mcp_pdf_real_inputs", pdf_dir / "real_tools.py")
    if spec is None or spec.loader is None:
        raise RuntimeError("existing real-PDF input helper unavailable")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    corpus = cases()
    report = {"passed": False, "source_only_preparation": False,
              "native_os": platform.system(), "native_arch": platform.machine(),
              "source_class": "actual Go/Rust production MCP; real tools on explicitly synthetic PDF inputs",
              "binaries": {name: {"path": str(path), "sha256": digest(path)} for name, path in binaries.items()},
              "tools": {name: {"path": str(path), "sha256": digest(path)} for name, path in tools.items()},
              "sources_before": module.source_manifest(), "declared_cases": corpus,
              "commands": [], "cases": [], "provenance_limit": "external admission must bind executables to source/compiler receipts"}

    def run(argv, label, cwd, env, data=None):
        entry: dict[str, Any] = dict(argv=[str(a) for a in argv], cwd=str(cwd), exit=None, timed_out=False, label=label)
        report["commands"].append(entry)
        if data is not None:
            private_file(output / (label + ".stdin"), data)
        with (output / (label + ".stdout")).open("xb") as out, (output / (label + ".stderr")).open("xb") as err:
            process = subprocess.Popen(argv, cwd=cwd, env=env, stdin=subprocess.PIPE if data is not None else subprocess.DEVNULL,
                                       stdout=out, stderr=err, start_new_session=True)
            try:
                process.communicate(data, timeout=75)
            except subprocess.TimeoutExpired:
                entry["timed_out"] = True
            finally:
                # Own group only, including descendants whose leader exited.
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                process.wait(timeout=5)
                entry["exit"] = process.returncode
        return (output / (label + ".stdout")).read_bytes(), (output / (label + ".stderr")).read_bytes(), entry

    def snapshot(root, label, env, baseline):
        tree, page_text = {}, []
        for index, path in enumerate(sorted(root.rglob("*"))):
            relative = path.relative_to(root).as_posix()
            meta = path.lstat()
            item: dict[str, Any] = {"mode": stat.S_IMODE(meta.st_mode)}
            if path.is_symlink():
                item.update(type="symlink", target=os.readlink(path))
            elif path.is_dir():
                item["type"] = "directory"
            else:
                item.update(type="file", sha256=digest(path))
                if relative not in baseline and path.suffix == ".pdf":
                    raw, err, command = run([tools["qpdf"], "--json", "--json-stream-data=inline", path], f"{label}-pdf-{index}", root, env)
                    if command["exit"] != 0 or command["timed_out"] or err:
                        raise AssertionError("output PDF validation failed (warnings are not success)")
                    parsed = json.loads(raw)
                    for block in parsed.get("qpdf", []):
                        if isinstance(block, dict) and "trailer" in block:
                            block["trailer"].get("value", {}).pop("/ID", None)
                    # Only nondeterministic trailer IDs are excluded, like the
                    # existing real_tools diagnostic; raw output/hash is retained.
                    item.pop("sha256")
                    item["pdf_semantic"] = parsed
                    text, err, command = run([tools["pdftotext"], "-layout", path, "-"], f"{label}-text-{index}", root, env)
                    if command["exit"] != 0 or command["timed_out"] or err:
                        raise AssertionError("output text extraction failed")
                    page_text.append([piece.strip() for piece in text.decode().split("\f") if piece.strip()])
            tree[relative] = item
        return tree, page_text

    try:
        for case in corpus:
            observations, errors = [], []
            for language, binary in binaries.items():
                root = runtime / (case["id"] + "-" + language)
                root.mkdir(mode=0o700)
                env = {"PATH": "", "TZ": "UTC", "LC_ALL": "C", "LANG": "C", "NO_COLOR": "1"}
                for key in ("HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "TMPDIR", "TMP", "TEMP"):
                    child = root / key
                    child.mkdir(mode=0o700)
                    env[key] = str(child)
                bindir = root / "bin"
                bindir.mkdir(mode=0o700)
                if case["tools"]:
                    for name, path in tools.items():
                        (bindir / name).symlink_to(path)
                    env["PATH"] = str(bindir)
                env["SYMDESK_VAULT"] = str(root)
                for name, labels in [("a.pdf", ["one", "two", "three", "four"]), ("b.pdf", ["five", "six"])]:
                    private_file(root / name, module.pdf(labels))
                    _, err, command = run([tools["qpdf"], "--check", root / name], case["id"] + "-" + language + "-input-" + name, root, env)
                    if command["exit"] != 0 or command["timed_out"] or err:
                        raise AssertionError("synthetic input is not a clean real-tool PDF")
                if case.get("existing_split"):
                    (root / "parts").mkdir(mode=0o700)
                    private_file(root / "parts/part-001.pdf", b"existing output sentinel")
                baseline = {path.relative_to(root).as_posix(): digest(path) for path in root.rglob("*") if path.is_file() and not path.is_symlink()}
                before, _ = snapshot(root, "before", env, baseline)
                body = case["request"].encode()
                data = f"Content-Length: {len(body)}\r\n\r\n".encode() + body if case.get("framed") else body + b"\n"
                argv = [binary, "mcp", "--allow-write=" + str(case["allow_write"]).lower()]
                label = case["id"] + "-" + language
                raw, stderr, command = run(argv, label, root, env, data)
                observation: dict[str, Any] = dict(exit=command["exit"], timed_out=command["timed_out"], stderr=stderr.decode(errors="replace"))
                try:
                    response_body = frame_body(raw, case.get("framed", False), case["response_count"])
                    response = json.loads(response_body) if response_body is not None else None
                    if command["exit"] != 0 or command["timed_out"] or stderr:
                        raise AssertionError("MCP process did not complete cleanly")
                    if response is None:
                        observation["notification_response_bytes"] = len(raw)
                    elif case.get("catalog"):
                        pdfs = [t for t in response["result"]["tools"] if t["name"] in PDF_NAMES]
                        if [t["name"] for t in pdfs] != (PDF_NAMES if case["allow_write"] else []):
                            raise AssertionError("PDF catalog admission/order mismatch")
                        observation["pdf_catalog"] = pdfs
                    else:
                        assert response_body is not None
                        observation["raw_response_body"] = response_body.decode()
                        params = json.loads(case["request"]).get("params")
                        requested_name = params.get("name") if isinstance(params, dict) else None
                        if case.get("protocol_error") or (not case["allow_write"] and requested_name in PDF_NAMES):
                            if "error" not in response:
                                raise AssertionError("expected protocol rejection")
                        elif response["result"]["isError"] is not (not case["success"]):
                            raise AssertionError("wrong tool success/error result")
                    after, pages = snapshot(root, label, env, baseline)
                    if any(not (root / name).is_file() or digest(root / name) != sha for name, sha in baseline.items()):
                        raise AssertionError("protected input/output changed")
                    if not case["allow_write"] and after != before:
                        raise AssertionError("read-only call changed filesystem")
                    if case["success"] and pages != case["expected_pages"]:
                        raise AssertionError("produced page order/text mismatch")
                    observation["filesystem"] = after
                except (AssertionError, KeyError, ValueError) as error:
                    errors.append(language + ": " + str(error))
                observations.append(module.normalized(observation, root))
            private_file(output / (case["id"] + ".json"), json.dumps(observations, indent=2).encode())
            try:
                module.compare(*observations)
            except AssertionError as error:
                errors.append(str(error))
            report["cases"].append(dict(id=case["id"], passed=not errors, errors=errors))
        report["transport"] = mcp_pdf_transport.verify(
            runtime, output, binaries, tools, module.pdf, run, snapshot, frame_body)
        report["passed"] = ([c["id"] for c in report["cases"]] == [c["id"] for c in corpus]
                            and all(c["passed"] is True for c in report["cases"])
                            and report["transport"]["passed"] is True)
    finally:
        report["sources_after"] = module.source_manifest()
        report["sources_unchanged"] = report["sources_before"] == report["sources_after"]
        report["identities_unchanged"] = all(digest(Path(item["path"])) == item["sha256"]
                                              for category in ("tools", "binaries") for item in report[category].values())
        report["passed"] = report["passed"] and report["sources_unchanged"] and report["identities_unchanged"]
        private_file(output / "report.json", (json.dumps(report, indent=2) + "\n").encode())
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
