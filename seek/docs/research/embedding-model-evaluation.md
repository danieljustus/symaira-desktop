# Embedding Model Evaluation — Qwen3-Embedding-0.6B vs. nomic-embed-text

**Issue:** #302 · **Date:** 2026-07-31 · **Corpus:** `testdata/bench/corpus-de/`
(8 German documents, 10 paraphrase queries with zero keyword overlap)

## Setup

- Harness: `internal/bench` (Hit Rate@k, MRR, NDCG@10; hybrid mode = BM25 + vector + RRF, identical results in vector-only mode — the delta is purely embedding quality).
- Queries are semantic paraphrases (no document vocabulary) so the evaluation measures German semantic retrieval, not keyword echo.
- Ollama 0.32.5; MLX via `mlx-lm` 0.29.1 (Apple Silicon), last-token hidden state before the LM head (Qwen3-Embedding causal + last-token pooling).
- Matryoshka truncation uses Ollama's `dimensions` body parameter (`/api/embed`); `options.num_dim` is ignored by Ollama.

## Results (aggregate)

| Variant | Runtime | Dims | HR@1 | HR@10 | MRR | NDCG@10 |
|---|---|---|---|---|---|---|
| nomic-embed-text | Ollama (Q8_0) | 768 | 0.600 | 1.000 | **0.689** | **0.787** |
| qwen3-embedding:0.6b | Ollama (Q8_0, unquantized reference) | 1024 | 1.000 | 1.000 | **1.000** | **1.000** |
| qwen3-embedding:0.6b | Ollama (Q8_0), Matryoshka-pinned | 768 | 1.000 | 1.000 | **1.000** | **1.000** |
| qwen3-embedding:0.6b-4bit (DWQ) | MLX (`mlx-community/Qwen3-Embedding-0.6B-4bit-DWQ`) | 1024 | 0.900 | 1.000 | **0.950** | **0.963** |
| qwen3-embedding:0.6b-8bit | MLX (`mlx-community/Qwen3-Embedding-0.6B-8bit`) | 1024 | 1.000 | 1.000 | **1.000** | **1.000** |

Per-query detail: nomic misses rank 1 on 4 of 10 queries (train travel, home
office, train delay compensation, solar self-consumption). The 4-bit MLX
variant misses rank 1 on exactly one query (solar self-consumption, MRR 0.5
on that query); the 8-bit MLX variant is indistinguishable from unquantized.

## Decision: switch the default to `qwen3-embedding:0.6b`

1. **Clear quality win on German semantics:** MRR 0.689 → 1.000, NDCG@10 0.787 → 1.000 on a corpus that targets exactly the reported weakness (primarily English-trained model on German content).
2. **Dimension stays 768:** Ollama's `dimensions` parameter pins the Matryoshka output to 768 with no measurable loss on this corpus. `defaultEmbeddingDim` and `symaira-memory`'s `EmbeddingDim = 768` remain untouched. Requires Ollama supporting `dimensions` (verified on 0.32.5; older Ollama versions silently return the model's native 1024 dims, which the mixed-embedding-space guard detects and blocks until re-index).
3. **4-bit MLX variant is production-viable for symaira-desktop:** −0.050 MRR / −0.037 NDCG@10 vs. unquantized on this corpus.
4. **Apache-2.0 license, 32K context** (vs. 8K), multilingual (100+ languages).
5. **Cost:** model download ~639 MB (vs. 274 MB); full re-index required once (model name changes, so the mixed-embedding-space guard blocks cross-scoring until then).

## Migration notes (for existing installs)

- **Re-index:** after upgrading, run `./symseek index <folder>` (or the watch
  daemon) once per indexed folder. Old chunks carry `embedding_model =
  nomic-embed-text`; new ones carry `qwen3-embedding:0.6b`. Search is blocked
  with a clear error until the index is rebuilt (mixed-embedding-space guard).
- **Rollback:** set `model = "nomic-embed-text"` in `~/.config/symseek/config.toml`
  and re-index. `embedding_dim = 768` is valid for both models.
- **Other models:** users who set a custom `model` keep it; set
  `embedding_dim = 0` to restore auto-detection of the model's native
  dimension.

## Follow-up

- Migration to `corekit/embedkit` (shared embedding layer) is deferred until
  embedkit exists; the measurement does not depend on it (issue #302).
- `ollamakit` should expose the `dimensions` parameter upstream so the local
  transport in `internal/engine` can be removed (filed as corekit issue).
