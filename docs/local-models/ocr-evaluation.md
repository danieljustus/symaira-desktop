# On-Device OCR Evaluation — PaddleOCR-VL Swift Port (#350)

Stand: 2026-08-03 · Repo: `mlx-community/paddleocr-vl.swift` ·
Reproduzierbar mit `tools/ocr-eval/`

## Kurzfassung

**Der Fremd-Port kann das offizielle Modell `PaddlePaddle/PaddleOCR-VL` nicht
laden** — der Port implementiert einen älteren, vereinfachten Vision-Encoder,
die offiziellen Gewichte haben eine andere Struktur. Die Ausführung auf dem
deutschen Referenzkorpus ist damit nicht möglich, ohne den Encoder neu zu
implementieren. **Empfehlung: Option 1 und 3 (übernehmen/vendorn) verwerfen,
Option 2 (offizielles MLXVLM) zurückstellen, bestehenden Tesseract-Pfad
behalten.** Die Entscheidung ist mit echten Lauf- und Strukturbelegen
dokumentiert, nicht nach Modellkarte getroffen.

## Was tatsächlich ausgeführt wurde

1. **Build** (Apple Silicon, macOS 27): `swift build -c release` →
   **erfolgreich** (135 s; einzige Auffälligkeit: eine
   `AddPreconcurrencyImport`-Warnung). Das offene Issue #1 im Port-Repo
   („Failed to load the default metallib") trat nicht auf.
2. **Lauf gegen den deutschen Referenzkorpus** (6 synthetische Dokumente aus
   `symaira-ingest` `internal/ocr`: Rechnung, Behördenbrief, Mehrspaltiger
   Text, Formular, Schlechter Scan, Handschrift-Anteil; Ground Truth
   identisch übernommen, Bilder via `tools/ocr-eval/gen_corpus.py`
   reproduzierbar gerendert):
   - Alle 6 Dokumente scheitern identisch beim Gewichts-Laden:
     `Error: Unhandled keys ["vision_model"] ... NaViTVisionEncoder`
   - Nach Korrektur des offensichtlichen Prefix-Mappings
     (`visual.` → leeren Prefix; der Port mappte fälschlich `visual.` →
     `vision_model.`, was `vision_model.vision_model.*` erzeugte):
     `Unhandled keys ["embeddings", "encoder", "head"]` — die
     Submodul-Namen passen weiterhin nicht.
3. **Strukturanalyse der offiziellen Gewichte** (Safetensors-Header,
   `tools/ocr-eval/safetensors_header.py`, Revision `main` und
   `4760b0ec59a9` vom Nov 2025 sind **identisch**, 620 Tensoren):

| Ebene | Offizielles Modell (transformers-Stil) | Port-Erwartung (CLIP-Stil) | Kompatibel? |
|---|---|---|---|
| Patch-Embedding | `embeddings.patch_embedding.*` | `patch_embed.proj.*` | mechanisch mappbar |
| Positions-Embedding | `embeddings.position_embedding.*`, `packing_position_embedding.*` | separat via `loadSpecialWeights` | teilweise (Packing fehlt) |
| Encoder-Layer | `encoder.layers.N.*` | `layers.N.*` | mechanisch mappbar |
| **Attention** | `self_attn.k_proj/q_proj/v_proj/out_proj` (getrennt) | `self_attn.qkv` (fusioniert) + `proj` | **strukturell inkompatibel** |
| **Head** | `head.attention.*` + `head.layernorm.*` (Attention-Pooling-Head) | `post_layernorm` | **im Port nicht implementiert** |

Der Port implementiert einen älteren/vereinfachten Vision-Encoder (fusioniertes
QKV, kein Attention-Pooling-Head, kein NaViT-Packing). Die offiziellen
Gewichte zum Port-Zeitpunkt (Nov 2025) hatten bereits dieselbe Struktur wie
heute — der Port wurde also nie gegen die offiziellen Gewichte getestet.

## Optionen-Bewertung

| Option | Bewertung | Entscheidung |
|---|---|---|
| **1. Fremd-Port übernehmen** (SPM-Dependency) | 2 Commits, seit 2025-11-28 unverändert (8 Monate), 18 Stars, 1 offenes Issue; Repo-Zuordnung widersprüchlich (Org `mlx-community`, README verweist auf persönliches `lulzx`); lädt Gewichte unkontrolliert in den Bibliotheks-Standardcache (verletzt #348); **kann das offizielle Modell nicht laden** | **Ablehnen** |
| **2. Offizielles MLXVLM-Vision-Modell** (Qwen2.5-VL etc.) | Von Apple getragen (mlx-swift-lm), aber generisches VLM: schwächer bei Layout/Tabellen/Formularen — genau die Dokumentklassen des Korpus | Zurückstellen; erst mit Messung gegen denselben Korpus entscheiden |
| **3. Vendorn (Fork + eigene Wartung)** | Erfordert, den Vision-Encoder gegen die aktuelle Architektur neu zu implementieren (Split-QKV, Attention-Head, Packing) — praktisch das Modell neu schreiben | **Ablehnen** (Aufwand ≫ Nutzen bei 0.9B-Modell; Tesseract-Pfad erfüllt den Bedarf) |

## Lizenzlage (getrennt)

- **Code** (Port): MIT (`LICENSE` im Port-Repo).
- **Gewichte** (PaddlePaddle/PaddleOCR-VL): Apache-2.0 (`LICENSE` im
  Modell-Repo; Commit `53484eb1138a` „Create LICENSE").
- Kein Lizenzkonflikt; die Trennung ist für eine spätere Übernahme
  unkritisch, aber die Gewichte würden über den #348-Ablageort + gepinnte
  Revision laufen müssen (der Port lädt sie heute ungepinnt in den
  HF-Standardcache, ~1,9 GB).

## Ausfall-Verhalten / Degradation

Es existiert kein on-device-Pfad in der App; der bestehende OCR-Pfad
(Tesseract via `internal/selfhost/worker.go` / symingest) bleibt unverändert
der produktive Weg. Ein zukünftiger on-device-Pfad MUSS bei Ausfall auf
diesen Weg degradieren statt zu brechen — Architektur-Anforderung, im
Modell-Download-Design (#348) bereits verankert (Download-Fehler → Status
`failed` statt Crash).

## Nächste Schritte (Empfehlung)

1. Issue schließen mit diesem Ergebnis; kein PR, der den Port einführt.
2. Falls On-Device-OCR wieder aufgenommen wird: Option 2 zuerst messen
   (MLXVLM + Qwen2.5-VL-3B o. ä. gegen `tools/ocr-eval`-Korpus, CER/WER wie
   `evaluate.py`), bevor erneut über einen Port nachgedacht wird.
3. Korpus + Auswertung (`tools/ocr-eval/`) bleiben als Messinfrastruktur im
   Repo erhalten.

## Reproduzierbarkeit

```bash
# Korpus erzeugen (6 PNGs):
python3 tools/ocr-eval/gen_corpus.py /tmp/ocr-corpus/pngs

# Port bauen (Stand 2026-08-03, Revision b31d5ab):
git clone https://github.com/mlx-community/paddleocr-vl.swift && cd paddleocr-vl.swift
swift build -c release

# Lauf (erwartet: Fehler beim Gewichts-Laden):
.build/release/PaddleOCRVLCLI ocr /tmp/ocr-corpus/pngs/invoice-001.png --output /tmp/out.txt

# Auswertung der Hypothesen (CER/WER/Field-Hits):
python3 tools/ocr-eval/evaluate.py /tmp/results
```
