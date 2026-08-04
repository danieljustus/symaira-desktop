# Embedding-Verify — Golden-Vector-Harness (#347)

Verifies that an on-device MLX embedding model produces correct vectors by
comparing them with the established Ollama path for identical German text.

## Verdict (2026-08-03)

| Model | Cosine vs. Ollama | Dims | Suitable |
|---|---|---|---|
| `Qwen3-Embedding-0.6B-4bit-DWQ` | 0.947 (min 0.924) | 1024 | **No** — fails the 0.99 gate |
| `Qwen3-Embedding-0.6B-8bit` | 0.9993 (min 0.9991) | 1024 | **Yes** |

The 4-bit deviation is purely quantization (4-bit vs 8-bit cosine = 0.946).
Full write-up: `docs/local-models/embedding-verification.md`.

## Setup

```bash
python3 -m venv /tmp/mlx-verify-venv
/tmp/mlx-verify-venv/bin/pip install -r requirements.txt

# Download the 8-bit model (pinned revision, see the docs) to a local dir,
# e.g. ~/symdesk-eval/models/Qwen3-Embedding-0.6B-8bit
```

## Run

```bash
SYMDESK_TEST_MODEL_DIR=~/symdesk-eval/models/Qwen3-Embedding-0.6B-8bit \
SYMDESK_TEST_OLLAMA_URL=http://localhost:11434 \
/tmp/mlx-verify-venv/bin/python golden_vector.py
```

Prerequisites:

- Apple Silicon Mac (MLX)
- Ollama running with `qwen3-embedding:0.6b` pulled
- Model files on disk (never auto-downloaded by the harness)

Exit codes: `0` = passed, `1` = verification failed, `2` = model dir missing.
`--json` emits a machine-readable result.

## What it checks

1. **Explicit pooling** — last real token via attention mask + L2
   normalisation, implemented in `explicit_last_token_pooling()`. Never taken
   from the model repository config (the mlx-swift-lm#36 failure mode).
2. **Dimensions** — must be 1024 for Qwen3-Embedding-0.6B (catches the
   "16384 raw hidden states" bug class).
3. **Golden vectors** — identical German sentences through MLX and Ollama;
   per-sentence cosine must be >= 0.99.
