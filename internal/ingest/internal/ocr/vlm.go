package ocr

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"github.com/danieljustus/symaira-corekit/llmkit"
	"github.com/danieljustus/symaira-desktop/internal/ingest/internal/extract"
)

// ollamaClient is the minimal Ollama surface VLMRunner needs.
type ollamaClient interface {
	Generate(ctx context.Context, model, prompt string, callback func(llmkit.GenerateResponse) error, opts ...llmkit.GenerateOption) error
}

type unavailableOllamaClient struct{ err error }

func (c unavailableOllamaClient) Generate(context.Context, string, string, func(llmkit.GenerateResponse) error, ...llmkit.GenerateOption) error {
	return c.err
}

// VLMRunner implements extract.Engine using a local vision-language model
// via Ollama. Tesseract remains the fallback when Ollama is unreachable or
// the model is not found.
type VLMRunner struct {
	// Ollama is the configured Ollama client; tests can inject a fake.
	Ollama ollamaClient

	// OllamaModel is the model name passed to Ollama, e.g. "paddleocr-vl:0.9b".
	OllamaModel string

	// Prompt is the OCR instruction sent with the image. If empty,
	// a sensible default is used.
	Prompt string

	// Fallback is the Tesseract runner used when Ollama cannot process
	// the image. Must not be nil.
	Fallback *Runner
}

// NewVLMRunner creates a VLMRunner with the given Ollama config and a
// Tesseract fallback. baseURL, model and ocrLang may be empty; defaults
// are applied.
func NewVLMRunner(baseURL, model, ocrLang string, fallback *Runner) *VLMRunner {
	if fallback == nil {
		fallback = DefaultRunner(ocrLang)
	}

	ollama := newOllamaClient(baseURL)
	return &VLMRunner{
		Ollama:      ollama,
		OllamaModel: model,
		Fallback:    fallback,
	}
}

func newOllamaClient(baseURL string) ollamaClient {
	desc, ok := llmkit.Lookup("ollama")
	if !ok {
		return unavailableOllamaClient{err: fmt.Errorf("ollama provider descriptor missing from embedded registry")}
	}
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	client, err := llmkit.NewClient(desc, "", llmkit.WithBaseURL(baseURL))
	if err != nil {
		return unavailableOllamaClient{err: err}
	}
	return client
}

// NewEngine returns the configured VLM engine or the standard Tesseract runner.
func NewEngine(ocrLang, ollamaBaseURL, ollamaModel string) extract.Engine {
	if ollamaModel != "" {
		return NewVLMRunner(ollamaBaseURL, ollamaModel, ocrLang, nil)
	}
	return DefaultRunner(ocrLang)
}

// defaultVlmPrompt is the OCR instruction sent to the model.
const defaultVlmPrompt = `Transcribe all text from this document image exactly as it appears.
Preserve line breaks. Do not translate, summarize, or add commentary.
Output only the transcribed text — no preamble, no markdown formatting.`

func (r *VLMRunner) prompt() string {
	if r.Prompt != "" {
		return r.Prompt
	}
	return defaultVlmPrompt
}

// Extract implements extract.Engine. Image kinds (PNG, JPEG, TIFF, WebP)
// are processed through the VLM engine; PDF and HEIC fall through to the
// Tesseract fallback. On any Ollama failure (unreachable host, missing
// model, stream error), the fallback is used.
func (r *VLMRunner) Extract(ctx context.Context, path string, kind extract.Kind) (*extract.Result, error) {
	switch kind {
	case extract.KindPNG, extract.KindJPEG, extract.KindTIFF, extract.KindWebP:
		return r.extractWithVLM(ctx, path, kind)
	case extract.KindPDF, extract.KindHEIC:
		// Keep the existing multi-page/conversion pipelines for these kinds.
		return r.Fallback.Extract(ctx, path, kind)
	default:
		return nil, fmt.Errorf("ocr: unsupported source kind %q", kind)
	}
}

func (r *VLMRunner) extractWithVLM(ctx context.Context, path string, kind extract.Kind) (*extract.Result, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // G304: path is the source selected by the extraction pipeline
	if err != nil {
		return nil, fmt.Errorf("vlm ocr: read image: %w", err)
	}

	b64 := base64.StdEncoding.EncodeToString(raw)

	var resultText string
	err = r.Ollama.Generate(ctx, r.OllamaModel, r.prompt(),
		func(resp llmkit.GenerateResponse) error {
			resultText += resp.Response
			return nil
		}, llmkit.WithGenerateImages([]string{b64}))

	if err != nil {
		// Fall back to Tesseract on any Ollama error.
		res, fallbackErr := r.Fallback.Extract(ctx, path, kind)
		if fallbackErr != nil {
			return nil, fmt.Errorf("vlm ocr failed (ollama: %v), and tesseract fallback also failed: %w", err, fallbackErr)
		}
		// Note the VLM failure in the engine name so downstream can see
		// that the result came from the fallback.
		return &extract.Result{
			Text:     res.Text,
			MIME:     res.MIME,
			Engine:   "tesseract(vlm-fallback)",
			Language: res.Language,
		}, nil
	}

	// Preserve the model name for downstream traceability.
	engine := r.OllamaModel
	if engine == "" {
		engine = "vlm"
	}
	return &extract.Result{
		Text:   strings.TrimSpace(resultText),
		MIME:   "image/ocr",
		Engine: engine,
	}, nil
}
