package ocr

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/ingest/internal/extract"
)

func TestValidateLanguagesHandlesDefaultsFailuresAndEmptyParts(t *testing.T) {
	dir := t.TempDir()
	failingTesseract := writeFakeBin(t, dir, "tesseract-failing", `
if [ "$1" = "--list-langs" ]; then
  echo "could not list languages" >&2
  exit 1
fi
`)

	fallback := &Runner{Tesseract: failingTesseract, OCRLang: "deu"}
	if got, err := fallback.validateLanguages(context.Background()); err != nil || got != "deu" {
		t.Fatalf("failed language listing = (%q, %v), want original language", got, err)
	}

	defaultLanguage := &Runner{Tesseract: failingTesseract}
	if got, err := defaultLanguage.validateLanguages(context.Background()); err != nil || got != "eng" {
		t.Fatalf("empty OCR language = (%q, %v), want eng", got, err)
	}

	missingTool := &Runner{Tesseract: filepath.Join(dir, "missing-tesseract"), OCRLang: "eng"}
	if _, err := missingTool.validateLanguages(context.Background()); err == nil {
		t.Fatal("validateLanguages accepted a missing tesseract")
	}
	if _, err := (&Runner{OCRLang: "eng"}).validateLanguages(context.Background()); err == nil {
		t.Fatal("validateLanguages accepted an empty tesseract path")
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&Runner{Tesseract: failingTesseract}).validateLanguagesFor(cancelled, "eng"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled validation error = %v, want context.Canceled", err)
	}

	availableTesseract := writeFakeBin(t, dir, "tesseract-available", `
if [ "$1" = "--list-langs" ]; then
  echo "List of available languages (2):"
  echo "eng"
  echo "deu"
  exit 0
fi
`)
	withEmptyPart := &Runner{Tesseract: availableTesseract}
	if got, err := withEmptyPart.validateLanguagesFor(context.Background(), "eng++"); err != nil || got != "eng++" {
		t.Fatalf("language with empty component = (%q, %v), want eng++,", got, err)
	}
}

func TestExtractImageRetriesWithDetectedGermanLanguage(t *testing.T) {
	dir := t.TempDir()
	tess := writeFakeBin(t, dir, "tesseract-retry", `
if [ "$1" = "--list-langs" ]; then
  echo "List of available languages (2):"
  echo "eng"
  echo "deu"
  exit 0
fi
if [ "$2" = "eng" ]; then
  echo "Der Vertrag wurde zwischen den Parteien geschlossen und ist gültig. Die Beiträge für die Krankenversicherung werden monatlich gezahlt. Wir können die Änderung nicht übernehmen, wenn die Frist schon abgelaufen ist."
else
  echo "retry text"
fi
`)
	img := filepath.Join(dir, "scan.png")
	if err := os.WriteFile(img, []byte("not a real image"), 0o600); err != nil {
		t.Fatal(err)
	}

	runner := &Runner{Tesseract: tess, OCRLang: "eng"}
	result, err := runner.Extract(context.Background(), img, extract.KindPNG)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if result.Language != "deu+eng" {
		t.Fatalf("Language = %q, want deu+eng after German retry", result.Language)
	}
	if !strings.Contains(result.Text, "retry text") {
		t.Fatalf("Text = %q, want retry output", result.Text)
	}
}

func TestLanguageRetrySkipsWhenGermanModelIsUnavailable(t *testing.T) {
	dir := t.TempDir()
	tess := writeFakeBin(t, dir, "tesseract-only-eng", `
if [ "$1" = "--list-langs" ]; then
  echo "List of available languages (1):"
  echo "eng"
  exit 0
fi
`)
	germanText := "Der Vertrag wurde zwischen den Parteien geschlossen und ist gültig. Die Beiträge für die Krankenversicherung werden monatlich gezahlt. Wir können die Änderung nicht übernehmen, wenn die Frist schon abgelaufen ist."

	if got := (&Runner{Tesseract: tess}).languageRetry(context.Background(), "eng", germanText); got != "" {
		t.Fatalf("languageRetry = %q, want no retry without German traineddata", got)
	}
}

func TestExtractImageRejectsUnavailableConfiguredLanguage(t *testing.T) {
	dir := t.TempDir()
	tess := writeFakeBin(t, dir, "tesseract-only-eng", `
if [ "$1" = "--list-langs" ]; then
  echo "List of available languages (1):"
  echo "eng"
  exit 0
fi
`)
	img := filepath.Join(dir, "scan.png")
	if err := os.WriteFile(img, []byte("not a real image"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := (&Runner{Tesseract: tess, OCRLang: "fra"}).Extract(context.Background(), img, extract.KindPNG); err == nil {
		t.Fatal("Extract accepted an unavailable OCR language")
	}
}

func TestNewEngineSelectsVLMAndLocalizedFallback(t *testing.T) {
	if runner, ok := NewEngine("deu", "", "").(*Runner); !ok || runner.OCRLang != "deu" {
		t.Fatalf("NewEngine without model = %#v, want localized Runner", runner)
	}
	engine := NewEngine("deu", "http://127.0.0.1:11434", "vision-model")
	vlm, ok := engine.(*VLMRunner)
	if !ok {
		t.Fatalf("NewEngine with model = %T, want *VLMRunner", engine)
	}
	if vlm.OllamaModel != "vision-model" || vlm.Fallback == nil || vlm.Fallback.OCRLang != "deu" {
		t.Fatalf("VLM engine configuration = %#v", vlm)
	}
}
