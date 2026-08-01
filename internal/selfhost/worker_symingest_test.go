package selfhost

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSymingestMock(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "symingest")
	script := `#!/bin/sh
set -eu
vault=""
input=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --vault) vault="$2"; shift 2 ;;
    --archive|--db|--ocr-lang) shift 2 ;;
    *) input="$1"; shift ;;
  esac
done
mkdir -p "$vault"
count=0
if [ -n "${MOCK_COUNTER:-}" ] && [ -f "$MOCK_COUNTER" ]; then count=$(cat "$MOCK_COUNTER"); fi
count=$((count + 1))
if [ -n "${MOCK_COUNTER:-}" ]; then printf '%s' "$count" > "$MOCK_COUNTER"; fi
text="${MOCK_OCR_TEXT:-Invoice total: 42 EUR}"
if [ "${MOCK_ALWAYS_BAD:-}" = "1" ]; then text="$(printf 'loading %.0s' $(seq 1 250))"; fi
if [ "$count" -eq 1 ] && [ "${MOCK_FIRST_BAD:-}" = "1" ]; then text="$(printf 'loading %.0s' $(seq 1 250))"; fi
engine="${MOCK_ENGINE:-tesseract}"
printf '%s\n' '---' "title: Result" "ocr_engine: \"$engine\"" "archive_path: \"$input\"" '---' '' "$text" '' '---' "[Archived Original](file://$input)" > "$vault/result.md"
printf 'ingested: %s\nengine: %s\ntext length: %s\n' "$input" "$engine" "${#text}"
`
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestWorkerUsesSymingestFromPATH(t *testing.T) {
	dir := writeSymingestMock(t)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	worker, err := NewWorker(WorkerConfig{ServerURL: "http://server:8787", Token: testToken, Engine: "tesseract"})
	if err != nil {
		t.Fatal(err)
	}
	text, engine, model, err := worker.process(context.Background(), filepath.Join(t.TempDir(), "invoice.png"))
	if err != nil {
		t.Fatal(err)
	}
	if text != "Invoice total: 42 EUR" || engine != "tesseract" || model != "" {
		t.Fatalf("unexpected symingest result: text=%q engine=%q model=%q", text, engine, model)
	}
}

func TestWorkerMissingSymingestIsExplicit(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	worker, err := NewWorker(WorkerConfig{ServerURL: "http://server:8787", Token: testToken})
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = worker.process(context.Background(), "input.png")
	if err == nil || !strings.Contains(err.Error(), "symingest is required for OCR") {
		t.Fatalf("expected explicit missing-symingest error, got %v", err)
	}
}

func TestWorkerPreservesSymingestModelProvenance(t *testing.T) {
	dir := writeSymingestMock(t)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MOCK_ENGINE", "paddleocr-vl")
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
	dir := writeSymingestMock(t)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	counter := filepath.Join(t.TempDir(), "counter")
	t.Setenv("MOCK_COUNTER", counter)
	t.Setenv("MOCK_FIRST_BAD", "1")
	goodText := "The retry returned healthy prose with enough distinct words for the plausibility guard to accept this result."
	t.Setenv("MOCK_OCR_TEXT", goodText)

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
	if got, err := os.ReadFile(counter); err != nil || string(got) != "2" {
		t.Fatalf("symingest invocation count = %q, err=%v; want 2", got, err)
	}
}

func TestWorkerGuardTruncatesAndMarksAfterSecondFailure(t *testing.T) {
	dir := writeSymingestMock(t)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	counter := filepath.Join(t.TempDir(), "counter")
	t.Setenv("MOCK_COUNTER", counter)
	t.Setenv("MOCK_ALWAYS_BAD", "1")

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
	if got, err := os.ReadFile(counter); err != nil || string(got) != "2" {
		t.Fatalf("symingest invocation count = %q, err=%v; want 2", got, err)
	}
}
