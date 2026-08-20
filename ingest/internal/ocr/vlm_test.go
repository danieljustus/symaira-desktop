package ocr

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-corekit/ollamakit"
	"github.com/danieljustus/symaira-ingest/internal/extract"
)

// fakeOllamaClient is an in-memory stand-in for *ollamakit.Client. It
// satisfies the ollamaClient seam used by VLMRunner, so no live Ollama
// service is needed.
type fakeOllamaClient struct {
	// err, when non-nil, is returned by Generate.
	err error
	// response is streamed to the callback on success.
	response string

	calls     int
	gotModel  string
	gotPrompt string
}

func (f *fakeOllamaClient) Generate(_ context.Context, model, prompt string, _ *ollamakit.GenerateOptions, cb func(ollamakit.GenerateResponse) error) error {
	f.calls++
	f.gotModel = model
	f.gotPrompt = prompt
	if f.err != nil {
		return f.err
	}
	if f.response != "" {
		return cb(ollamakit.GenerateResponse{Response: f.response, Done: true})
	}
	return nil
}

// writeFakeImage writes a small file that only needs to exist; the VLM and
// tesseract paths never inspect the payload.
func writeFakeImage(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNewEngine_SelectsVLMOrTesseract(t *testing.T) {
	if e := NewEngine("eng", "http://localhost:11434", "paddleocr-vl:0.9b"); e == nil {
		t.Fatal("NewEngine with ollamaModel returned nil")
	} else if _, ok := e.(*VLMRunner); !ok {
		t.Fatalf("NewEngine with ollamaModel = %T, want *VLMRunner", e)
	}

	if e := NewEngine("eng", "", ""); e == nil {
		t.Fatal("NewEngine without ollamaModel returned nil")
	} else if _, ok := e.(*Runner); !ok {
		t.Fatalf("NewEngine without ollamaModel = %T, want *Runner", e)
	}
}

func TestVLMRunner_Extract_Success(t *testing.T) {
	const model = "paddleocr-vl:0.9b"
	dir := t.TempDir()
	img := writeFakeImage(t, dir, "scan.png")
	client := &fakeOllamaClient{response: "  hello vlm world \n"}

	// The fallback must never be reached on the success path; point it at a
	// nonexistent tesseract so a stray call would fail the test loudly.
	r := &VLMRunner{Ollama: client, OllamaModel: model, Fallback: &Runner{Tesseract: filepath.Join(dir, "missing")}}

	res, err := r.Extract(context.Background(), img, extract.KindPNG)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if res.Text != "hello vlm world" {
		t.Fatalf("text = %q, want %q", res.Text, "hello vlm world")
	}
	if res.MIME != "image/ocr" {
		t.Fatalf("MIME = %q, want %q", res.MIME, "image/ocr")
	}
	if res.Engine != model {
		t.Fatalf("engine = %q, want configured model %q", res.Engine, model)
	}
	if client.calls != 1 {
		t.Fatalf("Generate calls = %d, want 1", client.calls)
	}
	if client.gotModel != model {
		t.Fatalf("Generate model = %q, want %q", client.gotModel, model)
	}
	if !strings.Contains(client.gotPrompt, "Transcribe all text") {
		t.Fatalf("Generate prompt = %q, want default VLM prompt", client.gotPrompt)
	}
}

func TestVLMRunner_Extract_CustomPrompt(t *testing.T) {
	dir := t.TempDir()
	img := writeFakeImage(t, dir, "scan.png")
	client := &fakeOllamaClient{response: "text"}
	r := &VLMRunner{
		Ollama:      client,
		OllamaModel: "m",
		Prompt:      "custom instruction",
		Fallback:    &Runner{Tesseract: filepath.Join(dir, "missing")},
	}

	if _, err := r.Extract(context.Background(), img, extract.KindPNG); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if client.gotPrompt != "custom instruction" {
		t.Fatalf("Generate prompt = %q, want the configured custom prompt", client.gotPrompt)
	}
}

func TestVLMRunner_Extract_Success_EmptyModelLabel(t *testing.T) {
	dir := t.TempDir()
	img := writeFakeImage(t, dir, "scan.png")
	r := &VLMRunner{
		Ollama:   &fakeOllamaClient{response: "text"},
		Fallback: &Runner{Tesseract: filepath.Join(dir, "missing")},
	}

	res, err := r.Extract(context.Background(), img, extract.KindPNG)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if res.Engine != "vlm" {
		t.Fatalf("engine = %q, want %q", res.Engine, "vlm")
	}
}

func TestVLMRunner_Extract_OllamaErrorFallsBackToTesseract(t *testing.T) {
	dir := t.TempDir()
	img := writeFakeImage(t, dir, "scan.png")
	client := &fakeOllamaClient{err: errors.New("connection refused")}
	tess := writeFakeBin(t, dir, "tesseract", fakeTesseractScript("fallback text"))

	r := &VLMRunner{Ollama: client, OllamaModel: "paddleocr-vl:0.9b", Fallback: &Runner{Tesseract: tess, OCRLang: "eng"}}

	res, err := r.Extract(context.Background(), img, extract.KindPNG)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if res.Text != "fallback text" {
		t.Fatalf("text = %q, want %q", res.Text, "fallback text")
	}
	if res.Engine != "tesseract(vlm-fallback)" {
		t.Fatalf("engine = %q, want %q", res.Engine, "tesseract(vlm-fallback)")
	}
	if client.calls != 1 {
		t.Fatalf("Generate calls = %d, want 1", client.calls)
	}
}

func TestVLMRunner_Extract_DoubleFailureWrapsBoth(t *testing.T) {
	dir := t.TempDir()
	img := writeFakeImage(t, dir, "scan.png")
	client := &fakeOllamaClient{err: errors.New("ollama down")}
	r := &VLMRunner{
		Ollama:      client,
		OllamaModel: "paddleocr-vl:0.9b",
		Fallback:    &Runner{Tesseract: filepath.Join(dir, "no-such-tesseract"), OCRLang: "eng"},
	}

	_, err := r.Extract(context.Background(), img, extract.KindPNG)
	if err == nil {
		t.Fatal("expected error when Ollama and fallback both fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, "ollama down") {
		t.Fatalf("error %q does not mention the Ollama failure", msg)
	}
	if !strings.Contains(msg, "tesseract fallback also failed") {
		t.Fatalf("error %q does not mention the fallback failure", msg)
	}
}

func TestVLMRunner_Extract_ImageKindsUseVLM(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name string
		kind extract.Kind
		ext  string
	}{
		{"png", extract.KindPNG, ".png"},
		{"jpeg", extract.KindJPEG, ".jpg"},
		{"tiff", extract.KindTIFF, ".tiff"},
		{"webp", extract.KindWebP, ".webp"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			img := writeFakeImage(t, dir, "scan"+tc.ext)
			client := &fakeOllamaClient{response: "vlm text"}
			r := &VLMRunner{Ollama: client, OllamaModel: "m", Fallback: &Runner{Tesseract: filepath.Join(dir, "missing")}}

			res, err := r.Extract(context.Background(), img, tc.kind)
			if err != nil {
				t.Fatalf("Extract: %v", err)
			}
			if res.Text != "vlm text" {
				t.Fatalf("text = %q, want %q", res.Text, "vlm text")
			}
			if res.Engine != "m" {
				t.Fatalf("engine = %q, want %q", res.Engine, "m")
			}
			if client.calls != 1 {
				t.Fatalf("Generate calls = %d, want 1", client.calls)
			}
		})
	}
}

func TestVLMRunner_Extract_PDFAndHEICDelegateToFallback(t *testing.T) {
	dir := t.TempDir()
	tess := writeFakeBin(t, dir, "tesseract", fakeTesseractScript("fallback text"))

	t.Run("pdf", func(t *testing.T) {
		pdfppm := writeFakeBin(t, dir, "pdftoppm", `echo "page-1.png" > "$5-page-1.png"`)
		pdf := filepath.Join(dir, "doc.pdf")
		if err := os.WriteFile(pdf, []byte("%PDF-1.4"), 0o644); err != nil {
			t.Fatal(err)
		}
		client := &fakeOllamaClient{response: "must not be used"}
		r := &VLMRunner{
			Ollama:      client,
			OllamaModel: "paddleocr-vl:0.9b",
			Fallback:    &Runner{Tesseract: tess, PDFToPPM: pdfppm, OCRLang: "eng"},
		}

		res, err := r.Extract(context.Background(), pdf, extract.KindPDF)
		if err != nil {
			t.Fatalf("Extract: %v", err)
		}
		if res.Text != "fallback text" {
			t.Fatalf("text = %q, want %q", res.Text, "fallback text")
		}
		// The fallback result passes through unchanged: VLM was never
		// attempted for PDF, so there is no "tesseract(vlm-fallback)" remap.
		if res.Engine != "pdftoppm+tesseract" {
			t.Fatalf("engine = %q, want %q", res.Engine, "pdftoppm+tesseract")
		}
		if client.calls != 0 {
			t.Fatalf("Generate calls = %d, want 0 (PDF must skip the VLM)", client.calls)
		}
	})

	t.Run("heic", func(t *testing.T) {
		heic := filepath.Join(dir, "scan.heic")
		if err := os.WriteFile(heic, []byte("fake heic"), 0o644); err != nil {
			t.Fatal(err)
		}
		sips := writeFakeBin(t, dir, "sips", `echo "converted" > "$6"`)
		client := &fakeOllamaClient{response: "must not be used"}
		r := &VLMRunner{
			Ollama:      client,
			OllamaModel: "paddleocr-vl:0.9b",
			Fallback:    &Runner{Tesseract: tess, SIPS: sips, OCRLang: "eng"},
		}

		res, err := r.Extract(context.Background(), heic, extract.KindHEIC)
		if err != nil {
			t.Fatalf("Extract: %v", err)
		}
		if res.Text != "fallback text" {
			t.Fatalf("text = %q, want %q", res.Text, "fallback text")
		}
		// The fallback result passes through unchanged: VLM was never
		// attempted for HEIC, so there is no "tesseract(vlm-fallback)" remap.
		if res.Engine != "sips+tesseract" {
			t.Fatalf("engine = %q, want %q", res.Engine, "sips+tesseract")
		}
		if client.calls != 0 {
			t.Fatalf("Generate calls = %d, want 0 (HEIC must skip the VLM)", client.calls)
		}
	})
}

func TestVLMRunner_Extract_UnsupportedKind(t *testing.T) {
	r := &VLMRunner{Ollama: &fakeOllamaClient{}, Fallback: &Runner{Tesseract: "x"}}
	_, err := r.Extract(context.Background(), "some-file", extract.KindUnknown)
	if err == nil {
		t.Fatal("expected error for unsupported kind")
	}
	if !strings.Contains(err.Error(), "unsupported source kind") {
		t.Fatalf("unexpected error: %v", err)
	}
}
