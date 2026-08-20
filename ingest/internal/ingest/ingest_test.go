package ingest

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-ingest/internal/extract"
)

type fakeEngine struct {
	result *extract.Result
}

func (f *fakeEngine) Extract(ctx context.Context, path string, kind extract.Kind) (*extract.Result, error) {
	return f.result, nil
}

func TestExtractText_TextFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	kind, err := extract.Detect(path)
	if err != nil {
		t.Fatal(err)
	}
	res, err := extractText(context.Background(), path, kind, nil)
	if err != nil {
		t.Fatalf("extractText: %v", err)
	}
	if res.Text != "hello" {
		t.Fatalf("text = %q, want hello", res.Text)
	}
}

func TestExtractText_Engine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "scan.png")
	if err := os.WriteFile(path, []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, 0o644); err != nil {
		t.Fatal(err)
	}
	kind, err := extract.Detect(path)
	if err != nil {
		t.Fatal(err)
	}
	res, err := extractText(context.Background(), path, kind, &fakeEngine{result: &extract.Result{Text: "ocr"}})
	if err != nil {
		t.Fatalf("extractText: %v", err)
	}
	if res.Text != "ocr" {
		t.Fatalf("text = %q, want ocr", res.Text)
	}
}

func TestExtractText_StructuredEMLDoesNotUseOCREngine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mail.eml")
	if err := os.WriteFile(path, []byte("Subject: hi\n\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}
	kind, err := extract.Detect(path)
	if err != nil {
		t.Fatal(err)
	}
	res, err := extractText(context.Background(), path, kind, &fakeEngine{result: &extract.Result{Text: "should not be used"}})
	if err != nil {
		t.Fatalf("extractText: %v", err)
	}
	if res.Text != "Subject: hi\n\nbody" {
		t.Fatalf("text = %q, want mail subject and body", res.Text)
	}
	if res.Engine != "eml-native" {
		t.Fatalf("engine = %q, want eml-native", res.Engine)
	}
}
