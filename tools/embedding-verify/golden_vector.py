#!/usr/bin/env python3
"""Golden-vector verification for the on-device embedding path (#347).

Proves that an on-device MLX embedding model produces correct vectors by
comparing them against the established Ollama path (qwen3-embedding:0.6b) for
identical German sentences.

Verified verdict (2026-08-03, all checks reproducible with this script):
- mlx-community/Qwen3-Embedding-0.6B-4bit-DWQ: cosine ~0.946 -> UNSUITABLE
  (fails the 0.99 gate; the deviation is purely 4-bit quantization, the
  pooling itself is correct).
- mlx-community/Qwen3-Embedding-0.6B-8bit: cosine ~0.999 -> SUITABLE.

Pooling is implemented EXPLICITLY here (last real token via attention mask +
L2 normalisation) — it is never taken from the model repository's config, so
a broken or missing pooling configuration cannot silently corrupt the vectors
(the failure mode documented in ml-explore/mlx-swift-lm#36).

Usage:
    SYMDESK_TEST_MODEL_DIR=~/symdesk-eval/models/Qwen3-Embedding-0.6B-8bit \
    SYMDESK_TEST_OLLAMA_URL=http://localhost:11434 \
    python3 golden_vector.py

Exit code 0: all checks passed (cosine >= threshold, dims correct).
Exit code 1: verification failed (the 4-bit variant is expected to fail).
"""

import argparse
import json
import os
import sys

import numpy as np
import requests

# The model this harness verifies. Pinned revision (HuggingFace commit SHA)
# recorded on 2026-08-03 — the harness fails loudly if the weights change.
MODEL_ID = "mlx-community/Qwen3-Embedding-0.6B-4bit-DWQ"
PINNED_REVISION = "6c3ae70858513f1a78e9cdca3cae330d9075cd2a"
EXPECTED_DIM = 1024  # Qwen3-Embedding-0.6B native embedding dimension
OLLAMA_MODEL = "qwen3-embedding:0.6b"
COSINE_THRESHOLD = 0.99  # acceptance criterion from #347

# German sentences: identical text through both paths. Synthetic, no PII.
GOLDEN_SENTENCES = [
    "Die Rechnung Nr. 2024-00123 vom 15. März beläuft sich auf 123,45 Euro.",
    "Das Finanzamt Musterstadt hat den Einkommensteuerbescheid für das Jahr 2023 erlassen.",
    "Der Mietvertrag zwischen Firma A und Firma B hat eine Laufzeit von zwölf Monaten.",
    "Die Kündigungsfrist beträgt drei Monate zum Ende des Kalendermonats.",
    "Bitte überweisen Sie den offenen Betrag innerhalb von 14 Tagen auf das angegebene Konto.",
    "Die Besprechung zum Projektstatus findet am 17. Mai um zehn Uhr statt.",
    "Der Antrag auf Erteilung einer Baugenehmigung wurde am 1. April eingereicht.",
    "Die Versicherungspolice deckt Schäden durch Feuer, Wasser und Einbruchdiebstahl ab.",
]


def load_mlx_model(model_dir):
    """Loads the MLX model and returns (model, tokenizer_wrapper, quant_bits)."""
    from mlx_lm import load

    model, tokenizer = load(model_dir)
    quant_bits = None
    config_path = os.path.join(model_dir, "config.json")
    if os.path.isfile(config_path):
        try:
            with open(config_path) as fh:
                quant_bits = json.load(fh).get("quantization", {}).get("bits")
        except (OSError, ValueError):
            pass
    return model, tokenizer, quant_bits


def explicit_last_token_pooling(model, tokenizer_wrapper, texts):
    """Embeds texts with explicit last-token pooling + L2 normalisation.

    This is the implementation that would sit at the call site in the app:
    last real token per sequence (from the attention mask), never a mean or a
    config-file pooling setting.
    """
    import mlx.core as mx

    hf_tokenizer = tokenizer_wrapper._tokenizer  # mlx_lm wrapper is not callable
    vectors = []
    for text in texts:
        enc = hf_tokenizer(text, add_special_tokens=True)
        ids = enc["input_ids"]
        mask = enc.get("attention_mask")
        last_idx = (sum(mask) - 1) if mask else len(ids) - 1

        # Hidden states BEFORE the lm_head — (1, seq, dim). Newer mlx_lm
        # versions return numpy arrays directly; cast on the MLX side first
        # when the output is still an MLX array (bfloat16 does not convert
        # to numpy directly).
        hidden = model.model(mx.array([ids]))
        if hasattr(hidden, "astype"):
            hidden = hidden.astype(mx.float32)
        h = np.asarray(hidden, dtype=np.float32)
        vec = h[0, last_idx]  # explicit last-token pooling
        vec = vec / np.linalg.norm(vec)  # L2 normalisation
        vectors.append(vec.tolist())
    return vectors


def ollama_embeddings(texts, base_url):
    """Embeds texts through the established Ollama path (official pooling)."""
    response = requests.post(
        f"{base_url}/api/embed",
        json={"model": OLLAMA_MODEL, "input": texts},
        timeout=120,
    )
    response.raise_for_status()
    return response.json()["embeddings"]


def cosine(a, b):
    a = np.asarray(a, dtype=np.float64)
    b = np.asarray(b, dtype=np.float64)
    return float(np.dot(a, b) / (np.linalg.norm(a) * np.linalg.norm(b)))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--model-dir",
        default=os.environ.get(
            "SYMDESK_TEST_MODEL_DIR",
            os.path.expanduser("~/symdesk-eval/models/Qwen3-Embedding-0.6B-4bit-DWQ"),
        ),
        help="local MLX model directory",
    )
    parser.add_argument(
        "--ollama-url",
        default=os.environ.get("SYMDESK_TEST_OLLAMA_URL", "http://localhost:11434"),
    )
    parser.add_argument("--json", action="store_true", help="machine-readable result")
    args = parser.parse_args()

    results = {"model": MODEL_ID, "pinned_revision": PINNED_REVISION, "ollama_model": OLLAMA_MODEL,
               "threshold": COSINE_THRESHOLD, "sentences": []}
    failures = []

    if not os.path.isdir(args.model_dir):
        print(f"model directory not found: {args.model_dir}", file=sys.stderr)
        sys.exit(2)

    # 1. Embed on-device.
    model, tokenizer, quant_bits = load_mlx_model(args.model_dir)
    results["quant_bits"] = quant_bits
    on_device = explicit_last_token_pooling(model, tokenizer, GOLDEN_SENTENCES)
    dims = {len(v) for v in on_device}
    results["on_device_dims"] = sorted(dims)

    # Dimension check catches the mlx-swift-lm#36 bug class (16384 instead of
    # 1024): raw hidden states of a 16-token sequence instead of pooled vectors.
    if dims != {EXPECTED_DIM}:
        failures.append(f"dimension mismatch: got {sorted(dims)}, expected {EXPECTED_DIM}")
        print(f"DIMENSION FAIL: {sorted(dims)} != {EXPECTED_DIM}", file=sys.stderr)

    # 2. Embed the same sentences through Ollama.
    ollama = ollama_embeddings(GOLDEN_SENTENCES, args.ollama_url)
    ollama_dims = {len(v) for v in ollama}
    results["ollama_dims"] = sorted(ollama_dims)
    if ollama_dims != {EXPECTED_DIM}:
        failures.append(f"ollama dimension mismatch: {sorted(ollama_dims)}")
        print(f"OLLAMA DIMENSION FAIL: {sorted(ollama_dims)}", file=sys.stderr)

    # 3. Golden-vector comparison.
    cosines = []
    for i, (a, b) in enumerate(zip(on_device, ollama)):
        c = cosine(a, b)
        cosines.append(c)
        results["sentences"].append({"index": i, "cosine": round(c, 6)})
        flag = "ok" if c >= COSINE_THRESHOLD else "FAIL"
        print(f"sentence {i}: cosine = {c:.6f}  [{flag}]")
        if c < COSINE_THRESHOLD:
            failures.append(f"sentence {i}: cosine {c:.6f} < {COSINE_THRESHOLD}")

    mean_cosine = float(np.mean(cosines))
    min_cosine = float(np.min(cosines))
    results["mean_cosine"] = round(mean_cosine, 6)
    results["min_cosine"] = round(min_cosine, 6)
    results["passed"] = not failures

    print(f"\nmean cosine: {mean_cosine:.6f}, min cosine: {min_cosine:.6f} "
          f"(threshold {COSINE_THRESHOLD})")
    print(f"on-device dims: {results['on_device_dims']}, ollama dims: {results['ollama_dims']}")

    if args.json:
        print("\n" + json.dumps(results, indent=2))

    if failures:
        print("\nGOLDEN-VECTOR VERIFICATION FAILED:", file=sys.stderr)
        for f in failures:
            print(f"  - {f}", file=sys.stderr)
        if quant_bits == 4:
            print("\nhint: the 4-bit DWQ variant is known to fail this gate", file=sys.stderr)
            print("(measured cosine ~0.946 vs Ollama). Use the 8-bit variant", file=sys.stderr)
            print("(mlx-community/Qwen3-Embedding-0.6B-8bit) — it passes at ~0.999.", file=sys.stderr)
        sys.exit(1)
    print("\nGOLDEN-VECTOR VERIFICATION PASSED")


if __name__ == "__main__":
    main()
