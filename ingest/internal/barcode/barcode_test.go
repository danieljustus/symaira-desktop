package barcode

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

func TestSeparateDropsConfiguredSeparatorPage(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "batch.pdf")
	if err := os.WriteFile(input, []byte("%PDF-1.4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	options := Options{
		SeparatorPrefix: "PATCHT",
		PDFToPPM: writeExecutable(t, dir, "pdftoppm", `
dir=$(dirname "$5")
printf x > "$5-1.png"
printf x > "$5-2.png"
printf x > "$5-3.png"
`),
		ZBarImg: writeExecutable(t, dir, "zbarimg", `case "$3" in *-2.png) printf 'PATCHT-001\\n';; esac`),
		PDFSeparate: writeExecutable(t, dir, "pdfseparate", `out=$(printf "$6" "$2"); printf '%%PDF-1.4 page-%s\\n' "$2" > "$out"`),
		PDFUnite: writeExecutable(t, dir, "pdfunite", `last=""; for arg in "$@"; do last="$arg"; done; cp "$1" "$last"`),
	}
	result, err := options.Separate(context.Background(), input, filepath.Join(dir, "documents"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Paths) != 2 || len(result.SeparatorPages) != 1 || result.SeparatorPages[0] != 2 {
		t.Fatalf("unexpected result: %+v", result)
	}
	first, _ := os.ReadFile(result.Paths[0])
	second, _ := os.ReadFile(result.Paths[1])
	if !strings.Contains(string(first), "page-1") || !strings.Contains(string(second), "page-3") {
		t.Fatalf("separator page was not dropped: first=%q second=%q", first, second)
	}
}

func TestSeparateMissingDecoderFallsBackWithWarning(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "batch.pdf")
	if err := os.WriteFile(input, []byte("%PDF-1.4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := (Options{
		SeparatorPrefix: "PATCHT",
		PDFToPPM:        writeExecutable(t, dir, "pdftoppm", "exit 0\n"),
		ZBarImg:         filepath.Join(dir, "missing"),
	}).Separate(context.Background(), input, filepath.Join(dir, "documents"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Paths != nil || !strings.Contains(result.Warning, "fell back") {
		t.Fatalf("result = %+v, want fallback warning", result)
	}
}
