package ocr

import (
	"errors"
	"testing"
)

func TestRunnerWithLanguageCopiesToolsAndResetsValidationCache(t *testing.T) {
	runner := &Runner{
		Tesseract:           "/opt/tesseract",
		PDFToPPM:            "/opt/pdftoppm",
		SIPS:                "/usr/bin/sips",
		OCRLang:             "eng",
		langCacheLoaded:     true,
		validatedLangKey:    "eng",
		validatedLang:       "eng",
		validatedLangCached: true,
	}

	localized := runner.WithLanguage("deu+eng")
	if localized == runner {
		t.Fatal("WithLanguage returned the original runner")
	}
	if localized.OCRLang != "deu+eng" {
		t.Errorf("OCRLang = %q, want deu+eng", localized.OCRLang)
	}
	if localized.Tesseract != runner.Tesseract || localized.PDFToPPM != runner.PDFToPPM || localized.SIPS != runner.SIPS {
		t.Errorf("WithLanguage did not preserve tool paths: %#v", localized)
	}
	if localized.langCacheLoaded || localized.validatedLangCached || localized.validatedLangKey != "" || localized.validatedLang != "" {
		t.Errorf("WithLanguage carried validation state: %#v", localized)
	}
}

func TestVLMRunnerWithLanguageCopiesFallbackConfiguration(t *testing.T) {
	fallback := DefaultRunner("eng")
	original := &VLMRunner{
		Ollama:      unavailableOllamaClient{err: errors.New("test client")},
		OllamaModel: "vision-model",
		Prompt:      "custom prompt",
		Fallback:    fallback,
	}

	localized := original.WithLanguage("deu")
	if localized == original {
		t.Fatal("WithLanguage returned the original VLM runner")
	}
	if localized.Ollama != original.Ollama || localized.OllamaModel != original.OllamaModel || localized.Prompt != original.Prompt {
		t.Errorf("WithLanguage changed VLM configuration: %#v", localized)
	}
	if localized.Fallback == nil || localized.Fallback == fallback || localized.Fallback.OCRLang != "deu" {
		t.Errorf("WithLanguage did not copy the localized fallback: %#v", localized.Fallback)
	}
}
