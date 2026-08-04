# OCR-Eval — German corpus + evaluation tooling (#350)

Reproducible evaluation infrastructure for on-device OCR candidates against
the German reference corpus from `symaira-ingest` (`internal/ocr`).

## Components

- `gen_corpus.py` — renders the 6 synthetic German documents (Rechnung,
  Behördenbrief, Mehrspaltiger Text, Formular, Schlechter Scan,
  Handschrift-Anteil) as PNGs. Ground truth is identical to
  `symaira-ingest/internal/ocr/corpus.go`. No personal data.
- `evaluate.py` — CER / WER (Levenshtein) + field-hit rate (ground-truth
  number-like tokens must appear in the hypothesis) per document; JSON
  summary with means. Mirrors symaira-ingest's benchmark metrics.
- `safetensors_header.py` — lists tensor names of a safetensors file by
  reading only the header (range request); used to diff model structures
  without downloading multi-GB weights.

## Usage

```bash
# 1. Generate the corpus
python3 tools/ocr-eval/gen_corpus.py /tmp/ocr-corpus/pngs

# 2. Run your OCR candidate on each PNG, write /tmp/results/<id>.txt

# 3. Evaluate
python3 tools/ocr-eval/evaluate.py /tmp/results
```

## Result of the #350 evaluation (2026-08-03)

The candidate port (`mlx-community/paddleocr-vl.swift`) builds but cannot
load the official `PaddlePaddle/PaddleOCR-VL` weights (vision-encoder
structure mismatch: split-QKV attention + attention-pooling head vs the
port's fused-QKV/simple-layernorm implementation) — all 6 corpus documents
fail at weight load. Full analysis and option matrix:
`docs/local-models/ocr-evaluation.md`.
