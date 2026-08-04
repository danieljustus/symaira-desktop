# On-Device Embedding Verification — Golden Vectors vs. Ollama

Stand: 2026-08-03 · Issue #347 · Harness: `tools/embedding-verify/golden_vector.py`

## Verdict

| Pfad | Cosinus vs. Ollama (`qwen3-embedding:0.6b`) | Dims | Tauglich? |
|---|---|---|---|
| `mlx-community/Qwen3-Embedding-0.6B-4bit-DWQ` (MLX, on-device) | **0.947** (min 0.924) | 1024 ✓ | **Nein** — verfehlt das 0,99-Gate |
| `mlx-community/Qwen3-Embedding-0.6B-8bit` (MLX, on-device) | **0.9993** (min 0.9991) | 1024 ✓ | **Ja** — bestanden |

Die Abweichung des 4-Bit-Pfads ist **nachweislich reine Quantisierung**, kein
Pooling- oder Tokenizer-Fehler: 4-Bit vs. 8-Bit direkt gemessen = 0.946, und
8-Bit vs. Ollama = 0.999. Das im Issue geforderte Ergebnis ist damit
**negativ für 4-Bit, positiv für 8-Bit** — der dokumentierte Rückfallpfad
(„eigene 8-Bit-Konvertierung") bestätigt sich; eine eigene Konvertierung ist
nicht nötig, die 8-Bit-Konvertierung existiert bereits und besteht das Gate.

## Upstream-Blocker: aufgelöst (Plan-Risiko R5 war veraltet)

Der Plan (2026-08-03) ging von offenem `ml-explore/mlx-swift-lm#36` aus. Die
Nachprüfung ergab: **Das Issue wurde am 2026-01-15 geschlossen** (7 Kommentare).
Auflösung: Der Fehler lag **nicht in MLXEmbedders**, sondern in der
Drittanbieter-Aggregation von Vecturakit. Maintainer David Koski bestätigte,
dass `embedder-tool` + `mlx-swift >= 0.30.2` mit `Qwen3-Embedding-0.6B-4bit-DWQ`
korrekt arbeiten. Zusätzlich belegt im Quelltext von mlx-swift-lm
(`Libraries/MLXEmbedders/Models/Qwen3.swift`):
`Qwen3Model.poolingStrategy = .last` — die Bibliothek poolt Qwen3-Embedding
offiziell über den letzten Token.

## Methodik

1. **Identischer deutscher Text** durch beide Pfade (8 synthetische Sätze,
   keine personenbezogenen Daten; siehe `GOLDEN_SENTENCES` im Harness).
2. **On-device** (MLX auf Apple Silicon): `model.model(input_ids)` →
   Hidden States **vor** `lm_head`, **explizites Last-Token-Pooling** über die
   Attention-Mask (`last_idx = sum(mask) - 1`), **L2-Normalisierung** — alles
   im eigenen Harness-Code, nie aus der Modell-Repo-Config (das ist die
   Anforderung „Pooling explizit am Aufrufort" aus #347).
3. **Ollama-Pfad** als etablierte Referenz: `/api/embed` mit
   `qwen3-embedding:0.6b` (offizielles Pooling der Engine).
4. **Dimensions-Check** (1024) fängt die Fehlerklasse aus #36 ab (16384 =
   rohe Hidden States statt gepoolter Vektoren).
5. **Cosinus-Schwelle 0.99** pro Satz; Harness-Exit-Code 0 nur bei Bestehen.

Zusätzliche Diagnose (im Repo nachvollziehbar): Mean-Pooling und CLS-Pooling
liefern vs. Ollama nur 0.578 bzw. 0.170 — Ollama verwendet also genau das
Last-Token-Pooling, das der Harness explizit implementiert. Tokenizer sind
byte-identisch mit dem Original-Repo (`Qwen/Qwen3-Embedding-0.6B`,
SHA-256-Vergleich `tokenizer.json`). Ollama ist über Aufrufe hinweg stabil
(cos = 1.0).

## Recall@10-Abweichung (4-Bit vs. unquantisiert)

- Gemessen hier: mittlerer Cosinus 4-Bit vs. Ollama = 0.947; 8-Bit vs.
  Ollama = 0.9993. Die Vektorabweichung der 4-Bit-Stufe ist damit beziffert
  (~5 % Cosinus-Verlust).
- Unabhängige Retrieval-Messung aus symseek #302 (gleiches Modell,
  deutscher Korpus, 10 Paraphrase-Queries ohne lexikalische Überlappung):
  **MRR@10 4-Bit-DWQ = 0.950 vs. unquantisiert = 1.000** (NDCG@10 0.963).
  Das bestätigt: 4-Bit kostet ~5 % Retrieval-Qualität, 8-Bit ist praktisch
  verlustfrei.

## Quantisierungsstufe als Teil der Raum-Identität

Die Quantisierungsstufe ist Bestandteil der Embedding-Raum-Identität und muss
mitgeführt werden, damit ein gemischter Index nicht stillschweigend
inkompatible Vektoren mischt:

- In `symaira-seek` lebt die Raum-Identität im Embedding-Space-Guard
  (Dimension + Modell pro Chunk; gemischte Räume blockieren die Suche bis
  zum Reindex). Die Quantisierungsstufe gehört in denselben Schlüssel.
- `symdesk` pusht selbst keine Vektoren (docs/PLAN.md: „symdesk besitzt ALLE
  Embeddings nicht — es pusht nur Text"), die Raum-Identität wird also an
  symseek delegiert; dort ist die Quantisierungsstufe beim Modell-Wechsel
  auf 8-Bit zu dokumentieren.
- Der Harness gibt die Quantisierungsstufe aus `config.json` aus
  (`quant_bits`), damit jeder Messlauf sie mitschreibt.

## Konsequenz für die App

1. **Katalog-Eintrag (#348-Mechanik):** `ModelCatalog` erhält den 8-Bit-Pfad
   als ersten Eintrag (gepinnte Revision + Prüfsumme, Download über den
   app-eigenen Ablageort). Die 4-Bit-Variante wird **nicht** aufgenommen.
2. **Swift-Anbindung (Folgearbeit, hier nicht Teil des Umfangs):** MLXEmbedders
   in `SymDeskCore` mit explizitem Pooling-Aufruf (Last-Token via
   Attention-Mask + L2) — das Swift-Gegenstück zu
   `explicit_last_token_pooling` im Harness. Der Harness bleibt das
   Verifikationsziel für diese Anbindung (Golden-Vector-Test gegen Ollama).
3. **Recalls:** der Golden-Vector-Test ist als CI-/Script-Ziel nutzbar
   (benötigt Modell + Ollama, daher nicht im Standard-Testlauf).

## Reproduzierbarkeit

```bash
# Modelle (gepinnte Revisionen, 2026-08-03):
#   mlx-community/Qwen3-Embedding-0.6B-4bit-DWQ @ 6c3ae70858513f1a78e9cdca3cae330d9075cd2a
#   mlx-community/Qwen3-Embedding-0.6B-8bit    @ siehe SHA des Repos zum Messdatum
# Ablage (nur für die Verifikation, nicht der App-Ablageort):
mkdir -p ~/symdesk-eval/models && cd ~/symdesk-eval/models
# ... Dateien via https://huggingface.co/<repo>/resolve/<sha>/<datei> laden

# Venv + Lauf:
python3 -m venv /tmp/mlx-verify-venv
/tmp/mlx-verify-venv/bin/pip install -r tools/embedding-verify/requirements.txt
SYMDESK_TEST_MODEL_DIR=~/symdesk-eval/models/Qwen3-Embedding-0.6B-8bit \
  /tmp/mlx-verify-venv/bin/python tools/embedding-verify/golden_vector.py

# Erwartung: PASS (mean cosine ~0.9993). Mit der 4-Bit-Variante: FAIL (~0.947).
```

Messumgebung: Apple Silicon (arm64), macOS 27, mlx_lm (Python), Ollama mit
`qwen3-embedding:0.6b`.
