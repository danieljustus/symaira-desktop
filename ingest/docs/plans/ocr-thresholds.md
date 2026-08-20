# OCR Engine Thresholds

Letztes Update: siehe Git-History dieser Datei.

## Zweck

Dieses Dokument definiert die Schwellwerte, ab denen eine OCR-Engine als Ersatz für den aktuellen Default (Tesseract) in Frage kommt. Die Schwellwerte basieren auf Messungen gegen das deutsche Referenzkorpus (`internal/ocr/corpus.go`).

## Messmetriken

| Metrik | Beschreibung | Ziel |
|--------|-------------|------|
| **CER** | Character Error Rate (Levenshtein auf Zeichenebene / Referenzlänge) | < 0.02 |
| **WER** | Word Error Rate (Levenshtein auf Wortebene / Referenzwortanzahl) | < 0.05 |
| **FieldCER** | CER nur auf Zahlen- und Aktenzeichenfeldern | < 0.01 |

## Tesseract Baseline

Die Baseline wird im Benchmark-Test `TestOCRBenchmark` automatisch gemessen und ausgegeben. Der Test ist reproduzierbar:

```bash
go test -run TestOCRBenchmark -v ./internal/ocr/
```

Die aktuelle Baseline wird hier dokumentiert, sobald der erste Durchlauf vorliegt.

| Metrik | Tesseract (deu+eng) |
|--------|---------------------|
| CER | (zu messen) |
| WER | (zu messen) |
| FieldCER | (zu messen) |

## Schwellwerte für Default-Ablösung

Eine neue Engine kommt als Default in Frage, wenn ALLE folgenden Bedingungen erfüllt sind:

1. **CER < 0.02** — Zeichengenauigkeit besser als Tesseract auf dem Gesamtkorpus
2. **WER < 0.05** — Wortgenauigkeit besser als Tesseract
3. **FieldCER < 0.01** — Zahlen- und Aktenzeichenfelder MÜSSEN präziser sein
4. **Keine Regression** — Keine einzelne Dokumentkategorie (Rechnung, Behördenbrief, etc.) darf sich verschlechtern
5. **Halluzinationsfrei** — Keine falschen, aber plausibel aussehenden Werte in Zahlenfeldern (VLM-spezifisches Risiko)

Solange diese Bedingungen nicht erfüllt sind, bleibt Tesseract der Default.

## Reproduzierbarkeit

Der Benchmark-Lauf ist mit einem Kommando reproduzierbar:

```bash
# Gesamter Benchmark inkl. Korpus-Generierung und Tesseract-Durchlauf
go test -run TestOCRBenchmark -v ./internal/ocr/

# Nur Korpus-Generierung und Validierung (ohne Tesseract)
go test -run TestCorpus -v ./internal/ocr/
```

Voraussetzung: `tesseract` mit Sprachpaket `deu` muss auf dem PATH verfügbar sein. Ohne Tesseract wird der Benchmark-Test automatisch übersprungen.
