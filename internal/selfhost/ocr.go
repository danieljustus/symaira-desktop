package selfhost

import (
	"context"
	"fmt"
	"strings"

	"github.com/danieljustus/symaira-desktop/internal/ingest"
	ingestapi "github.com/danieljustus/symaira-ingest/api"
)

// processViaIngest runs the absorbed extraction pipeline over input and
// returns its text along with the engine (and model, for a VLM) that produced
// it. Nothing is written to a vault, an archive, or the document store: the
// worker only wants the text, and the server owns the note it becomes.
func (w *Worker) processViaIngest(ctx context.Context, input string) (text, engine, model string, err error) {
	mode, err := w.ocrMode()
	if err != nil {
		return "", "", "", err
	}

	opts := ingestapi.Options{OCRLang: w.cfg.OCRLanguage}
	if mode == "ollama" {
		opts.OllamaBaseURL = w.cfg.OllamaURL
		opts.OllamaModel = w.cfg.OllamaModel
	} else {
		// An explicit tesseract choice must mean Tesseract, even when the
		// user's symingest configuration names a VLM model.
		opts.DisableVLM = true
	}

	result, err := ingest.ExtractTextFunc(ctx, input, opts)
	if err != nil {
		return "", "", "", fmt.Errorf("OCR failed: %w", err)
	}

	engine = strings.TrimSpace(result.Engine)
	if engine == "" {
		engine = mode
	}
	if mode == "ollama" {
		model = w.cfg.OllamaModel
	}
	return result.Text, engine, model, nil
}

// ocrMode resolves the configured engine choice to "tesseract" or "ollama".
func (w *Worker) ocrMode() (string, error) {
	mode := strings.ToLower(strings.TrimSpace(w.cfg.Engine))
	if mode == "" || mode == "auto" {
		if w.cfg.OllamaModel != "" {
			return "ollama", nil
		}
		return "tesseract", nil
	}
	if mode != "tesseract" && mode != "ollama" {
		return "", fmt.Errorf("unsupported OCR engine %q; supported engines are tesseract and ollama", w.cfg.Engine)
	}
	if mode == "ollama" && w.cfg.OllamaModel == "" {
		return "", fmt.Errorf("--ollama-model is required for the Ollama engine")
	}
	return mode, nil
}
