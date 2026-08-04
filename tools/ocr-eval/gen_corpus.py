#!/usr/bin/env python3
"""Generates the German OCR reference corpus as PNG images (#350).

Replicates symaira-ingest's internal/ocr GermanReferenceCorpus (same document
set, same ground truth) as standalone renders so the on-device OCR port can be
evaluated without touching the symaira-ingest repository. All documents are
synthetic — no real personal data.
"""
import os
import sys

from PIL import Image, ImageDraw, ImageFont

# Same ground truth as symaira-ingest internal/ocr/corpus.go
CORPUS = [
    ("invoice-001", "Rechnung",
     "Rechnung\nNr. 2024-00123\nDatum: 15.03.2024\nBetrag: 123,45 EUR\nKundennummer: K-98765"),
    ("letter-authority", "Behoerdenbrief",
     "Finanzamt Musterstadt\nSteuernummer: 123/456/78901\nAktenzeichen: AB-2024-00123\nDatum: 01.04.2024\nBetreff: Einkommensteuerbescheid 2023"),
    ("multi-column-001", "Mehrspaltiger Text",
     "Spalte A\tSpalte B\tSpalte C\nArtikel 1\t10,00\t100,00\nArtikel 2\t20,00\t200,00\nGesamt\t\t300,00"),
    ("form-001", "Formular",
     "Anmeldung\nName: Max Mustermann\nGeburtsdatum: 01.01.1980\nAdresse: Musterstrasse 1, 12345 Musterstadt\nTelefon: 0123-456789"),
    ("poor-scan", "Schlechter Scan",
     "Vertrag\nZwischen Firma A und Firma B\nVertragsnummer: V-2024-00987\nLaufzeit: 12 Monate\nKündigungsfrist: 3 Monate"),
    ("handwriting-note", "Handschrift-Anteil",
     "Notiz\nBesprechung am 10.05.2024\nTeilnehmer: Mueller, Schmidt\nThema: Projektstatus Q2\nNaechster Termin: 17.05.2024"),
]


def font(size):
    for candidate in (
        "/System/Library/Fonts/Supplemental/Arial.ttf",
        "/System/Library/Fonts/Supplemental/Arial Unicode.ttf",
        "/Library/Fonts/Arial.ttf",
    ):
        if os.path.exists(candidate):
            return ImageFont.truetype(candidate, size)
    return ImageFont.load_default()


def render(doc_id, category, text, out_dir):
    img = Image.new("RGB", (640, 480), "white")
    draw = ImageDraw.Draw(img)
    f = font(28)
    draw.text((24, 24), text, fill="black", font=f, spacing=10)
    path = os.path.join(out_dir, f"{doc_id}.png")
    img.save(path)
    return path


def main():
    out_dir = sys.argv[1] if len(sys.argv) > 1 else "."
    os.makedirs(out_dir, exist_ok=True)
    for doc_id, category, text in CORPUS:
        path = render(doc_id, category, text, out_dir)
        print(f"{doc_id}\t{category}\t{path}")


if __name__ == "__main__":
    main()
