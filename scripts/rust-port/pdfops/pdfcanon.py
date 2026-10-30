#!/usr/bin/env python3
"""Canonical, writer-independent description of a PDF's reachable object graph.

Used on BOTH sides of the RENDER-002 PDF differentials: the Go generators run it
on the PDF pdfcpu really wrote, the Rust tests run it on the PDF the Rust port
wrote, and the two JSON documents must be equal. The tool is a measurement, not
an oracle: every number it prints comes from `qpdf --json`.

Normalisations (declared; everything else is compared exactly):
  * Reachable tree from /Root only. /Info, /ID, trailer, xref/object-stream
    containers, free objects and the header version are ignored.
  * Indirect references are expanded in place (sharing is not compared; a
    duplicated and a shared identical object canonicalise equally). /Parent and
    /P back references are dropped, a genuine cycle becomes {"$cycle": true}.
  * Numbers: integral reals equal integers; other reals are rounded to 5
    decimals (pdfcpu prints 12 decimals, `%.5f`, or shortest 'f' formats, and
    Go on arm64 fuses multiply-adds, so the last ulp is host dependent).
  * Streams: /Length is dropped; Flate/LZW/ASCII filters are decoded by qpdf and
    their /Filter and /DecodeParms entries dropped (compressor output differs);
    other filters (DCT) stay and their raw bytes are compared. Text streams
    (content streams) are compared as text with every decimal token rounded the
    same way as numbers; binary streams by length and SHA-256.
  * Strings keep qpdf's "u:" / "b:" encoding.
"""
import base64
import hashlib
import json
import re
import subprocess
import sys

DROP_KEYS = {"/Parent", "/P", "/Length"}
DECODED_FILTERS = {"/FlateDecode", "/LZWDecode", "/ASCII85Decode", "/ASCIIHexDecode"}
REF = re.compile(r"^(\d+) (\d+) R$")
DECIMAL = re.compile(r"-?\d+\.\d+")
NUMBER_DECIMALS = 5


def norm_number(x):
    if isinstance(x, bool):
        return x
    if isinstance(x, int):
        return x
    r = round(x)
    if abs(x - r) < 10 ** -(NUMBER_DECIMALS + 1):
        return int(r)
    return round(x, NUMBER_DECIMALS)


def norm_decimal_token(m):
    v = round(float(m.group(0)), NUMBER_DECIMALS)
    if abs(v - round(v)) < 10 ** -(NUMBER_DECIMALS + 1):
        return str(int(round(v)))
    return repr(v)


def stream_node(dict_node, data):
    node = {"dict": dict_node, "len": len(data)}
    try:
        text = data.decode("ascii")
        if len(data) <= 1 << 20 and all(c in "\t\n\r" or 32 <= ord(c) < 127 for c in text):
            node["text"] = DECIMAL.sub(norm_decimal_token, text)
            return node
    except UnicodeDecodeError:
        pass
    node["sha256"] = hashlib.sha256(data).hexdigest()
    return node


class Canon:
    def __init__(self, objects):
        self.objects = objects
        self.memo = {}
        self.active = set()

    def value(self, v):
        if isinstance(v, bool) or v is None or isinstance(v, str) and not REF.match(v):
            return v
        if isinstance(v, (int, float)):
            return norm_number(v)
        if isinstance(v, str):
            return self.ref(v)
        if isinstance(v, list):
            return [self.value(x) for x in v]
        if isinstance(v, dict):
            return {k: self.value(x) for k, x in sorted(v.items()) if k not in DROP_KEYS}
        raise TypeError(type(v))

    def ref(self, text):
        if text in self.memo:
            return self.memo[text]
        if text in self.active:
            return {"$cycle": True}
        entry = self.objects.get("obj:" + text)
        if entry is None:
            return {"$missing": text}
        self.active.add(text)
        try:
            if "stream" in entry:
                s = entry["stream"]
                d = {k: x for k, x in s.get("dict", {}).items()}
                filt = d.get("/Filter")
                names = [filt] if isinstance(filt, str) else (filt or [])
                if names and all(n in DECODED_FILTERS for n in names):
                    d.pop("/Filter", None)
                    d.pop("/DecodeParms", None)
                data = base64.b64decode(s.get("data", ""))
                node = stream_node(self.value(d), data)
            else:
                node = self.value(entry["value"])
        finally:
            self.active.discard(text)
        self.memo[text] = node
        return node


def canon(path):
    proc = subprocess.run(
        ["qpdf", "--json=2", "--json-stream-data=inline", "--decode-level=generalized", path],
        capture_output=True,
    )
    if proc.returncode not in (0, 3):
        return {"error": "qpdf", "code": proc.returncode, "stderr": proc.stderr.decode("utf-8", "replace")[:300]}
    doc = json.loads(proc.stdout)
    objects = doc["qpdf"][1]
    trailer = objects["trailer"]["value"]
    root = trailer.get("/Root")
    if not root:
        return {"error": "no-root"}
    return {"root": Canon(objects).ref(root)}


def main():
    sys.stdout.write(json.dumps(canon(sys.argv[1]), sort_keys=True, separators=(",", ":")))


if __name__ == "__main__":
    main()
