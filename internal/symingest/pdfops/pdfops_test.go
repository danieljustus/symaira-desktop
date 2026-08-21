package pdfops

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeExecutable(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParsePages(t *testing.T) {
	pages, err := ParsePages("5,1-3")
	if err != nil {
		t.Fatal(err)
	}
	want := []int{1, 2, 3, 5}
	if len(pages) != len(want) {
		t.Fatalf("pages = %v, want %v", pages, want)
	}
	for i := range want {
		if pages[i] != want[i] {
			t.Fatalf("pages = %v, want %v", pages, want)
		}
	}
	for _, spec := range []string{"", "0", "3-1", "1,1", "1--2"} {
		if _, err := ParsePages(spec); err == nil {
			t.Fatalf("ParsePages(%q) unexpectedly succeeded", spec)
		}
	}
}

func TestSplitEndToEndWithExternalToolAdapter(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input.pdf")
	if err := os.WriteFile(input, []byte("%PDF-1.4\nminimal test PDF\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tools := Tools{
		PDFInfo: writeExecutable(t, dir, "pdfinfo", "printf 'Pages: 3\\n'"),
		PDFSeparate: writeExecutable(t, dir, "pdfseparate", `out=$(printf "$6" "$2")
printf '%%PDF-1.4 page-%s\\n' "$2" > "$out"
`),
		PDFUnite: writeExecutable(t, dir, "pdfunite", `
last=""
for arg in "$@"; do last="$arg"; done
cp "$1" "$last"
`),
	}
	outputDir := filepath.Join(dir, "parts")
	outputs, err := tools.Split(context.Background(), input, "2", outputDir)
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	if len(outputs) != 2 {
		t.Fatalf("outputs = %v, want two parts", outputs)
	}
	for _, output := range outputs {
		data, err := os.ReadFile(output)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(data), "%PDF-") {
			t.Fatalf("output %s is not PDF-like: %q", output, data)
		}
	}
	first, err := os.ReadFile(outputs[0])
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(outputs[1])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(first), "page-1") || !strings.Contains(string(second), "page-3") {
		t.Fatalf("split parts do not preserve page groups: first=%q second=%q", first, second)
	}
}

func TestRotateMissingQPDFIsActionable(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input.pdf")
	if err := os.WriteFile(input, []byte("%PDF-1.4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := (Tools{QPDF: filepath.Join(dir, "missing-qpdf")}).Rotate(context.Background(), input, filepath.Join(dir, "rotated.pdf"), 90, "1")
	if err == nil || !strings.Contains(err.Error(), "qpdf") {
		t.Fatalf("Rotate error = %v, want actionable qpdf error", err)
	}
}
