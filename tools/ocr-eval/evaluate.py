#!/usr/bin/env python3
"""Evaluates OCR output against the German reference corpus ground truth.

Implements CER (character error rate) and WER (word error rate) via
Levenshtein distance plus a field-presence check (all ground-truth
number-like tokens must appear in the hypothesis). Mirrors the metrics
used by symaira-ingest's internal/ocr benchmark.
"""
import json
import os
import re
import sys

CORPUS = {
    "invoice-001": "Rechnung\nNr. 2024-00123\nDatum: 15.03.2024\nBetrag: 123,45 EUR\nKundennummer: K-98765",
    "letter-authority": "Finanzamt Musterstadt\nSteuernummer: 123/456/78901\nAktenzeichen: AB-2024-00123\nDatum: 01.04.2024\nBetreff: Einkommensteuerbescheid 2023",
    "multi-column-001": "Spalte A\tSpalte B\tSpalte C\nArtikel 1\t10,00\t100,00\nArtikel 2\t20,00\t200,00\nGesamt\t\t300,00",
    "form-001": "Anmeldung\nName: Max Mustermann\nGeburtsdatum: 01.01.1980\nAdresse: Musterstrasse 1, 12345 Musterstadt\nTelefon: 0123-456789",
    "poor-scan": "Vertrag\nZwischen Firma A und Firma B\nVertragsnummer: V-2024-00987\nLaufzeit: 12 Monate\nKündigungsfrist: 3 Monate",
    "handwriting-note": "Notiz\nBesprechung am 10.05.2024\nTeilnehmer: Mueller, Schmidt\nThema: Projektstatus Q2\nNaechster Termin: 17.05.2024",
}


def levenshtein(a, b):
    if len(a) < len(b):
        a, b = b, a
    previous = list(range(len(b) + 1))
    for i, ca in enumerate(a, 1):
        current = [i]
        for j, cb in enumerate(b, 1):
            current.append(min(previous[j] + 1, current[j - 1] + 1, previous[j - 1] + (ca != cb)))
        previous = current
    return previous[-1]


def normalize(text):
    # Fold case and whitespace for CER/WER; keep digits and punctuation.
    return re.sub(r"\s+", " ", text.strip()).lower()


def cer(ref, hyp):
    r, h = normalize(ref), normalize(hyp)
    if not r:
        return 0.0 if not h else 1.0
    return levenshtein(r, h) / len(r)


def wer(ref, hyp):
    r, h = normalize(ref).split(), normalize(hyp).split()
    if not r:
        return 0.0 if not h else 1.0
    return levenshtein(r, h) / len(r)


def field_hits(ref, hyp):
    """Fraction of ground-truth number-like tokens present in the hypothesis."""
    numbers = set(re.findall(r"\b[0-9][0-9.,/-]*[0-9]\b|[A-Z]-?[0-9]{3,}", ref))
    if not numbers:
        return 1.0
    h = normalize(hyp)
    hits = sum(1 for n in numbers if n.lower() in h)
    return hits / len(numbers)


def main():
    results_dir = sys.argv[1]  # dir with <docid>.txt hypothesis files
    out = []
    for doc_id, ground_truth in CORPUS.items():
        hyp_path = os.path.join(results_dir, f"{doc_id}.txt")
        hypothesis = ""
        if os.path.exists(hyp_path):
            hypothesis = open(hyp_path, encoding="utf-8", errors="replace").read()
        out.append({
            "id": doc_id,
            "cer": round(cer(ground_truth, hypothesis), 4),
            "wer": round(wer(ground_truth, hypothesis), 4),
            "field_hits": round(field_hits(ground_truth, hypothesis), 4),
            "hypothesis": hypothesis.strip()[:300],
        })
    mean_cer = sum(o["cer"] for o in out) / len(out)
    mean_wer = sum(o["wer"] for o in out) / len(out)
    mean_fields = sum(o["field_hits"] for o in out) / len(out)
    print(json.dumps({"documents": out, "mean_cer": round(mean_cer, 4),
                      "mean_wer": round(mean_wer, 4), "mean_field_hits": round(mean_fields, 4)}, indent=2))


if __name__ == "__main__":
    main()
