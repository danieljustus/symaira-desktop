#!/usr/bin/env python3
"""Generate bounded hostile PDF inputs; these are inputs, not oracle outputs.

Usage: hostile_inputs.py DIRECTORY QPDF. The directory must not already exist.
The qpdf flags intentionally use weak RC4 and a fixed ID for test fixtures only.
"""
import argparse
import hashlib
import json
from pathlib import Path
import subprocess


def document(objects):
    data = bytearray(b'%PDF-1.4\n')
    offsets = [0]
    for number, obj in enumerate(objects, 1):
        offsets.append(len(data))
        data.extend(f'{number} 0 obj\n'.encode() + obj + b'\nendobj\n')
    start = len(data)
    data.extend(f'xref\n0 {len(offsets)}\n0000000000 65535 f \n'.encode())
    for offset in offsets[1:]:
        data.extend(f'{offset:010d} 00000 n \n'.encode())
    data.extend(f'trailer\n<< /Root 1 0 R /Size {len(offsets)} >>\nstartxref\n{start}\n%%EOF\n'.encode())
    return bytes(data)


def corpus():
    catalog = b'<< /Type /Catalog /Pages 2 0 R >>'
    page = b'<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 300] /Resources << >> /Contents 4 0 R >>'
    stream = b'<< /Length 0 >>\nstream\n\nendstream'
    valid = document([catalog, b'<< /Type /Pages /Count 1 /Kids [3 0 R] >>', page, stream])
    variants = {
        'valid.pdf': valid,
        'huge-count.pdf': document([catalog, b'<< /Type /Pages /Count 2147483647 /Kids [3 0 R] >>', page, stream]),
        'pages-loop.pdf': document([catalog, b'<< /Type /Pages /Count 1 /Kids [2 0 R] >>']),
        'kids-cycle.pdf': document([catalog, b'<< /Type /Pages /Count 1 /Kids [3 0 R] >>', b'<< /Type /Pages /Parent 2 0 R /Count 1 /Kids [2 0 R] >>']),
        'huge-stream.pdf': document([catalog, b'<< /Type /Pages /Count 1 /Kids [3 0 R] >>', page, b'<< /Length 2147483647 >>\nstream\nabc\nendstream']),
        'truncated-stream.pdf': document([catalog, b'<< /Type /Pages /Count 1 /Kids [3 0 R] >>', page, b'<< /Length 100 >>\nstream\nabc']),
        'missing-xref.pdf': valid[:valid.index(b'xref\n')],
        'malformed-xref.pdf': valid.replace(b'0000000000 65535 f', b'not-an-xref-entry!!'),
    }
    chain = [catalog] + [f'<< /Type /Pages /Count 1 /Kids [{i + 1} 0 R] >>'.encode() for i in range(2, 514)]
    chain.append(b'<< /Type /Page /Parent 513 0 R /MediaBox [0 0 200 300] >>')
    variants['deep-pages.pdf'] = document(chain)
    return variants


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('directory', type=Path)
    parser.add_argument('qpdf', type=Path)
    args = parser.parse_args()
    args.directory.mkdir(parents=True, exist_ok=False)
    for name, data in corpus().items():
        if len(data) > 128 * 1024:
            raise RuntimeError('fixture exceeds size bound')
        (args.directory / name).write_bytes(data)
    subprocess.run([str(args.qpdf), '--static-id', '--allow-weak-crypto', '--encrypt', 'user', 'owner', '128', '--use-aes=n', '--', str(args.directory / 'valid.pdf'), str(args.directory / 'encrypted.pdf')], check=True, timeout=15, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    manifest = {p.name: {'bytes': p.stat().st_size, 'sha256': hashlib.sha256(p.read_bytes()).hexdigest()} for p in sorted(args.directory.glob('*.pdf'))}
    (args.directory / 'manifest.json').write_text(json.dumps(manifest, indent=2, sort_keys=True) + '\n')
    print(json.dumps(manifest, sort_keys=True))


if __name__ == '__main__':
    main()
