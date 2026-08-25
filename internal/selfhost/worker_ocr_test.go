package selfhost

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/ingest"
)

// stubExtract points the OCR seam at a scripted extractor for one test and
// returns a pointer to the call counter, so a test can assert how often the
// guard retried.
func stubExtract(t *testing.T, fn func(call int, opts ingest.Options) (*ingest.Extraction, error)) *int {
	t.Helper()
	original := ingest.ExtractTextFunc
	t.Cleanup(func() { ingest.ExtractTextFunc = original })

	calls := 0
	ingest.ExtractTextFunc = func(_ context.Context, _ string, opts ingest.Options) (*ingest.Extraction, error) {
		calls++
		return fn(calls, opts)
	}
	return &calls
}

func TestWorkerExtractsInProcess(t *testing.T) {
	stubExtract(t, func(int, ingest.Options) (*ingest.Extraction, error) {
		return &ingest.Extraction{Text: "Invoice total: 42 EUR", Engine: "tesseract"}, nil
	})

	worker, err := NewWorker(WorkerConfig{ServerURL: "http://server:8787", Token: testToken, Engine: "tesseract"})
	if err != nil {
		t.Fatal(err)
	}
	text, engine, model, err := worker.process(context.Background(), filepath.Join(t.TempDir(), "invoice.png"))
	if err != nil {
		t.Fatal(err)
	}
	if text != "Invoice total: 42 EUR" || engine != "tesseract" || model != "" {
		t.Fatalf("unexpected result: text=%q engine=%q model=%q", text, engine, model)
	}
}

// An explicit tesseract choice must not inherit a VLM model from the user's
// symingest configuration.
func TestWorkerTesseractModeDisablesVLM(t *testing.T) {
	var seen ingest.Options
	stubExtract(t, func(_ int, opts ingest.Options) (*ingest.Extraction, error) {
		seen = opts
		return &ingest.Extraction{Text: "text", Engine: "tesseract"}, nil
	})

	worker, err := NewWorker(WorkerConfig{ServerURL: "http://server:8787", Token: testToken, Engine: "tesseract"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := worker.process(context.Background(), "invoice.png"); err != nil {
		t.Fatal(err)
	}
	if !seen.DisableVLM {
		t.Fatal("tesseract mode did not disable the VLM engine")
	}
	if seen.OllamaModel != "" {
		t.Fatalf("tesseract mode passed an Ollama model %q", seen.OllamaModel)
	}
}

func TestWorkerExtractionFailureIsExplicit(t *testing.T) {
	stubExtract(t, func(int, ingest.Options) (*ingest.Extraction, error) {
		return nil, errors.New("no tesseract binary")
	})

	worker, err := NewWorker(WorkerConfig{ServerURL: "http://server:8787", Token: testToken})
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = worker.process(context.Background(), "input.png")
	if err == nil || !strings.Contains(err.Error(), "OCR failed") {
		t.Fatalf("expected an explicit OCR failure, got %v", err)
	}
}

func TestWorkerPreservesModelProvenance(t *testing.T) {
	stubExtract(t, func(int, ingest.Options) (*ingest.Extraction, error) {
		return &ingest.Extraction{Text: "text", Engine: "paddleocr-vl"}, nil
	})

	worker, err := NewWorker(WorkerConfig{
		ServerURL: "http://server:8787", Token: testToken, Engine: "ollama", OllamaModel: "gemma3",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, engine, model, err := worker.process(context.Background(), filepath.Join(t.TempDir(), "invoice.png"))
	if err != nil {
		t.Fatal(err)
	}
	if engine != "paddleocr-vl" || model != "gemma3" {
		t.Fatalf("unexpected provenance: engine=%q model=%q", engine, model)
	}
}

func TestWorkerGuardRetriesOnce(t *testing.T) {
	goodText := "The retry returned healthy prose with enough distinct words for the plausibility guard to accept this result."
	badText := strings.TrimSpace(strings.Repeat("loading ", 250))

	calls := stubExtract(t, func(call int, _ ingest.Options) (*ingest.Extraction, error) {
		if call == 1 {
			return &ingest.Extraction{Text: badText, Engine: "tesseract"}, nil
		}
		return &ingest.Extraction{Text: goodText, Engine: "tesseract"}, nil
	})

	worker, err := NewWorker(WorkerConfig{ServerURL: "http://server:8787", Token: testToken})
	if err != nil {
		t.Fatal(err)
	}
	text, engine, _, err := worker.processJob(context.Background(), "job-guard", filepath.Join(t.TempDir(), "invoice.png"))
	if err != nil {
		t.Fatal(err)
	}
	if text != goodText || engine != "tesseract" {
		t.Fatalf("retry result = text %q engine %q", text, engine)
	}
	if *calls != 2 {
		t.Fatalf("extraction call count = %d, want 2", *calls)
	}
}

func TestWorkerGuardTruncatesAndMarksAfterSecondFailure(t *testing.T) {
	badText := strings.TrimSpace(strings.Repeat("loading ", 250))
	calls := stubExtract(t, func(int, ingest.Options) (*ingest.Extraction, error) {
		return &ingest.Extraction{Text: badText, Engine: "tesseract"}, nil
	})

	worker, err := NewWorker(WorkerConfig{ServerURL: "http://server:8787", Token: testToken})
	if err != nil {
		t.Fatal(err)
	}
	text, engine, _, err := worker.processJob(context.Background(), "job-guard", filepath.Join(t.TempDir(), "invoice.png"))
	if err != nil {
		t.Fatal(err)
	}
	if len(strings.Fields(text)) != 100 {
		t.Fatalf("truncated word count = %d, want 100", len(strings.Fields(text)))
	}
	if !strings.Contains(engine, "guard=truncated") {
		t.Fatalf("engine %q does not mark truncated output", engine)
	}
	if *calls != 2 {
		t.Fatalf("extraction call count = %d, want 2", *calls)
	}
}
