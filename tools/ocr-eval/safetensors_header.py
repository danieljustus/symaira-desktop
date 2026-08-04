#!/usr/bin/env python3
"""Lists tensor names in a safetensors file by reading only its header."""
import json
import struct
import sys
import urllib.request


def header_keys(url, limit=10**6):
    req = urllib.request.Request(url, headers={"Range": f"bytes=0-{limit}"})
    data = urllib.request.urlopen(req, timeout=60).read()
    n = struct.unpack("<Q", data[:8])[0]
    header = json.loads(data[8:8 + n])
    return sorted(k for k in header if k != "__metadata__"), header.get("__metadata__")


def main():
    url = sys.argv[1]
    keys, meta = header_keys(url)
    prefix = sys.argv[2] if len(sys.argv) > 2 else ""
    hits = [k for k in keys if prefix in k]
    print(f"total tensors: {len(keys)}, prefix '{prefix}': {len(hits)}")
    for k in hits[:15]:
        print(" ", k)
    if meta:
        print("metadata:", json.dumps(meta)[:200])


if __name__ == "__main__":
    main()
