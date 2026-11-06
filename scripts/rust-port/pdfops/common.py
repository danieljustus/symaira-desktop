"""Helpers shared by the Go-free PDF replay harnesses (extracted from the
retired live Go/Rust verifier in RUST-019)."""
import json
from pathlib import Path
import re

ROOT = Path(__file__).resolve().parents[3]


def normalized(value, root):
    # Only isolated case roots and random MkdirTemp basenames are variable.
    # Output bytes, ordering, status, errors and ordinary paths stay untouched.
    raw = json.dumps(value, ensure_ascii=False).replace(str(root), '<ROOT>')
    raw = re.sub(r'\.symdesk-pdf-[^/\\" ]+', '.symdesk-pdf-<TEMP>', raw)
    return json.loads(raw)


def compare(expected, actual):
    if type(expected) is not type(actual): raise AssertionError('JSON type mismatch')
    if isinstance(expected, dict):
        if expected.keys() != actual.keys(): raise AssertionError('JSON fields mismatch')
        for key in expected: compare(expected[key], actual[key])
    elif isinstance(expected, list):
        if len(expected) != len(actual): raise AssertionError('JSON list length mismatch')
        for left, right in zip(expected, actual): compare(left, right)
    elif expected != actual: raise AssertionError(f'value mismatch: {expected!r} != {actual!r}')


def pdf(labels):
    """Small valid uncompressed PDF, with actual xref offsets, not oracle output."""
    objects = [b'<< /Type /Catalog /Pages 2 0 R >>', b'', b'<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>']
    kids = []
    for label in labels:
        page_id = len(objects) + 1
        kids.append(f'{page_id} 0 R')
        objects.append(f'<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 400] /Resources << /Font << /F1 3 0 R >> >> /Contents {page_id+1} 0 R >>'.encode())
        text = f'BT /F1 18 Tf 30 200 Td ({label}) Tj ET\n'.encode()
        objects.append(f'<< /Length {len(text)} >>\nstream\n'.encode()+text+b'endstream')
    objects[1] = f'<< /Type /Pages /Count {len(labels)} /Kids [{" ".join(kids)}] >>'.encode()
    result = bytearray(b'%PDF-1.4\n')
    offsets = [0]
    for index, obj in enumerate(objects, 1):
        offsets.append(len(result)); result.extend(f'{index} 0 obj\n'.encode()+obj+b'\nendobj\n')
    start = len(result)
    result.extend(f'xref\n0 {len(offsets)}\n0000000000 65535 f \n'.encode())
    for offset in offsets[1:]: result.extend(f'{offset:010d} 00000 n \n'.encode())
    result.extend(f'trailer\n<< /Root 1 0 R /Size {len(offsets)} >>\nstartxref\n{start}\n%%EOF\n'.encode())
    return bytes(result)
