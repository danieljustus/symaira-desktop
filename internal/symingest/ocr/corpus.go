// Package ocr provides a German document reference corpus for OCR benchmarking.
package ocr

import (
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// CorpusEntry represents one document in the reference corpus.
type CorpusEntry struct {
	// ID is a short stable identifier, e.g. "invoice-001".
	ID string

	// Category describes the document type.
	Category string

	// GroundTruth is the expected OCR output for this document.
	GroundTruth string

	// Tags describe document characteristics for grouped evaluation.
	Tags []string
}

// CorpusEntryWithImage is a CorpusEntry whose rendered image is available on disk.
type CorpusEntryWithImage struct {
	CorpusEntry
	ImagePath string // absolute path to the generated PNG
}

// GermanReferenceCorpus returns the standard German document reference corpus.
// All entries are synthetic — they contain no real personal data.
func GermanReferenceCorpus() []CorpusEntry {
	return []CorpusEntry{
		{
			ID:          "invoice-001",
			Category:    "Rechnung",
			GroundTruth: "Rechnung\nNr. 2024-00123\nDatum: 15.03.2024\nBetrag: 123,45 EUR\nKundennummer: K-98765",
			Tags:        []string{"invoice", "numbers", "date"},
		},
		{
			ID:          "letter-authority",
			Category:    "Behoerdenbrief",
			GroundTruth: "Finanzamt Musterstadt\nSteuernummer: 123/456/78901\nAktenzeichen: AB-2024-00123\nDatum: 01.04.2024\nBetreff: Einkommensteuerbescheid 2023",
			Tags:        []string{"authority", "case-number", "tax-id"},
		},
		{
			ID:          "multi-column-001",
			Category:    "Mehrspaltiger Text",
			GroundTruth: "Spalte A\tSpalte B\tSpalte C\nArtikel 1\t10,00\t100,00\nArtikel 2\t20,00\t200,00\nGesamt\t\t300,00",
			Tags:        []string{"table", "columns", "numbers"},
		},
		{
			ID:          "form-001",
			Category:    "Formular",
			GroundTruth: "Anmeldung\nName: Max Mustermann\nGeburtsdatum: 01.01.1980\nAdresse: Musterstrasse 1, 12345 Musterstadt\nTelefon: 0123-456789",
			Tags:        []string{"form", "personal-fields", "date"},
		},
		{
			ID:          "poor-scan",
			Category:    "Schlechter Scan",
			GroundTruth: "Vertrag\nZwischen Firma A und Firma B\nVertragsnummer: V-2024-00987\nLaufzeit: 12 Monate\nKündigungsfrist: 3 Monate",
			Tags:        []string{"contract", "numbers", "scan-quality"},
		},
		{
			ID:          "handwriting-note",
			Category:    "Handschrift-Anteil",
			GroundTruth: "Notiz\nBesprechung am 10.05.2024\nTeilnehmer: Mueller, Schmidt\nThema: Projektstatus Q2\nNaechster Termin: 17.05.2024",
			Tags:        []string{"handwriting", "date", "names"},
		},
	}
}

// isTesseractLikelyAvailable returns true when tesseract is found on PATH.
func isTesseractLikelyAvailable() bool {
	// We check for tesseract in the PATH without shelling out to --list-langs.
	// The benchmark test will skip gracefully when tesseract is missing.
	for _, name := range []string{"tesseract"} {
		for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
			p := filepath.Join(dir, name)
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0111 != 0 {
				return true
			}
		}
	}
	return false
}

// renderTextToImage renders multiline text onto an RGBA image using the basic font.
func renderTextToImage(text string) *image.RGBA {
	lines := strings.Split(text, "\n")
	face := basicfont.Face7x13
	lineHeight := face.Height + 2 // 2px padding between lines
	width := 800
	height := len(lines)*lineHeight + 40

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)

	y := 20
	for _, line := range lines {
		if line == "" {
			y += lineHeight
			continue
		}
		drawer := &font.Drawer{
			Dst:  img,
			Src:  &image.Uniform{color.Black},
			Face: face,
			Dot:  fixed.P(20, y+face.Metrics().Ascent.Ceil()),
		}
		drawer.DrawString(line)
		y += lineHeight
	}
	return img
}

// GenerateCorpus renders all corpus entries to PNG images in dir.
// Returns the entries with their image paths.
func GenerateCorpus(dir string) ([]CorpusEntryWithImage, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	corpus := GermanReferenceCorpus()
	var out []CorpusEntryWithImage
	for _, e := range corpus {
		img := renderTextToImage(e.GroundTruth)
		imgPath := filepath.Join(dir, e.ID+".png")
		f, err := os.Create(imgPath)
		if err != nil {
			return nil, err
		}
		if err := png.Encode(f, img); err != nil {
			f.Close()
			return nil, err
		}
		f.Close()
		out = append(out, CorpusEntryWithImage{
			CorpusEntry: e,
			ImagePath:   imgPath,
		})
	}
	return out, nil
}
