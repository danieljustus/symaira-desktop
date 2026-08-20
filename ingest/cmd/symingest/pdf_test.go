package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeMockTool(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func setupMockPDFTools(t *testing.T) string {
	t.Helper()
	binDir := t.TempDir()

	// Mock pdfinfo
	writeMockTool(t, binDir, "pdfinfo", `
for arg in "$@"; do
    if [ "$arg" = "invalid" ]; then
        exit 1
    fi
done
echo "Pages: 3"
`)

	// Mock pdfseparate
	writeMockTool(t, binDir, "pdfseparate", `
# args: -f <page> -l <page> <input> <pattern>
page="$2"
pattern="$6"
dest=$(printf "$pattern" "$page")
echo "%PDF-1.4 mock page $page" > "$dest"
`)

	// Mock pdfunite
	writeMockTool(t, binDir, "pdfunite", `
# last argument is output. Concatenate all other input files into it.
output=""
for arg; do
  output="$arg"
done
> "$output"
for arg; do
  if [ "$arg" != "$output" ]; then
    cat "$arg" >> "$output"
  fi
done
`)

	// Mock qpdf
	writeMockTool(t, binDir, "qpdf", `
# qpdf --warning-exit-0 <input> <output> --rotate=<selector>
# Copy input to output, adding a marker so it is modified but preserves uniqueness.
echo "%PDF-1.4 rotated" > "$3"
cat "$2" >> "$3"
`)

	// Mock pdftoppm
	writeMockTool(t, binDir, "pdftoppm", `
prefix="$5"
echo "fake image content" > "${prefix}-1.png"
`)

	// Mock tesseract
	writeMockTool(t, binDir, "tesseract", `
if [ "$1" = "--list-langs" ]; then
  echo "eng"
  exit 0
fi
echo "mock OCR text"
`)

	// Prepend to PATH
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+oldPath)

	return binDir
}

func TestRunPDFSplit(t *testing.T) {
	setupMockPDFTools(t)

	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")
	vaultDir := filepath.Join(tempDir, "vault")
	archiveDir := filepath.Join(tempDir, "archive")
	inputPath := filepath.Join(tempDir, "input.pdf")
	if err := os.WriteFile(inputPath, []byte("%PDF-1.4\nminimal PDF\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 1. Success case without ingest
	outDir := filepath.Join(tempDir, "parts")
	sb := withCapturedStdout(t)
	err := run([]string{"split", "-at", "2", "-output-dir", outDir, inputPath})
	if err != nil {
		t.Fatalf("run split failed: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "created: ") {
		t.Errorf("expected mention of created file in output: %q", out)
	}

	// 2. Success case with ingest
	sb.Reset()
	err = run([]string{"split", "-at", "2", "-output-dir", filepath.Join(tempDir, "parts-ingest"), "-ingest", "-db", dbPath, "-vault", vaultDir, "-archive", archiveDir, inputPath})
	if err != nil {
		t.Fatalf("run split with ingest failed: %v", err)
	}
	out = sb.String()
	if !strings.Contains(out, "ingested note: ") {
		t.Errorf("expected mention of ingested note in output: %q", out)
	}

	// 3. Validation errors
	// Missing --at
	err = run([]string{"split", inputPath})
	if err == nil {
		t.Error("expected error for missing --at")
	}

	// Invalid input path
	err = run([]string{"split", "-at", "2", filepath.Join(tempDir, "nonexistent.pdf")})
	if err == nil {
		t.Error("expected error for nonexistent input PDF")
	}
}

func TestRunPDFMerge(t *testing.T) {
	setupMockPDFTools(t)

	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")
	vaultDir := filepath.Join(tempDir, "vault")
	archiveDir := filepath.Join(tempDir, "archive")
	input1 := filepath.Join(tempDir, "input1.pdf")
	input2 := filepath.Join(tempDir, "input2.pdf")
	os.WriteFile(input1, []byte("%PDF-1.4\nPDF1\n"), 0o644)
	os.WriteFile(input2, []byte("%PDF-1.4\nPDF2\n"), 0o644)

	output := filepath.Join(tempDir, "merged.pdf")

	// 1. Success case without ingest
	sb := withCapturedStdout(t)
	err := run([]string{"merge", "-output", output, input1, input2})
	if err != nil {
		t.Fatalf("run merge failed: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "created: "+output) {
		t.Errorf("expected mention of created file in output: %q", out)
	}

	// 2. Success case with ingest
	output2 := filepath.Join(tempDir, "merged2.pdf")
	sb.Reset()
	err = run([]string{"merge", "-output", output2, "-ingest", "-db", dbPath, "-vault", vaultDir, "-archive", archiveDir, input1, input2})
	if err != nil {
		t.Fatalf("run merge with ingest failed: %v", err)
	}
	out = sb.String()
	if !strings.Contains(out, "ingested note: ") {
		t.Errorf("expected mention of ingested note in output: %q", out)
	}

	// 3. Validation errors
	// Missing output
	err = run([]string{"merge", input1, input2})
	if err == nil {
		t.Error("expected error for missing --output")
	}

	// Less than two inputs
	err = run([]string{"merge", "-output", output, input1})
	if err == nil {
		t.Error("expected error for less than two inputs")
	}
}

func TestRunPDFRotate(t *testing.T) {
	setupMockPDFTools(t)

	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")
	vaultDir := filepath.Join(tempDir, "vault")
	archiveDir := filepath.Join(tempDir, "archive")
	input := filepath.Join(tempDir, "input.pdf")
	os.WriteFile(input, []byte("%PDF-1.4\nPDF\n"), 0o644)

	output := filepath.Join(tempDir, "rotated.pdf")

	// 1. Success case without ingest
	sb := withCapturedStdout(t)
	err := run([]string{"rotate", "-degrees", "90", "-output", output, input})
	if err != nil {
		t.Fatalf("run rotate failed: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "created: "+output) {
		t.Errorf("expected mention of created file in output: %q", out)
	}

	// 2. Success case with ingest
	output2 := filepath.Join(tempDir, "rotated2.pdf")
	sb.Reset()
	err = run([]string{"rotate", "-degrees", "-180", "-output", output2, "-ingest", "-db", dbPath, "-vault", vaultDir, "-archive", archiveDir, input})
	if err != nil {
		t.Fatalf("run rotate with ingest failed: %v", err)
	}
	out = sb.String()
	if !strings.Contains(out, "ingested note: ") {
		t.Errorf("expected mention of ingested note in output: %q", out)
	}

	// 3. Validation errors
	// Missing output
	err = run([]string{"rotate", "-degrees", "90", input})
	if err == nil {
		t.Error("expected error for missing --output")
	}

	// Invalid degrees
	err = run([]string{"rotate", "-degrees", "45", "-output", output, input})
	if err == nil {
		t.Error("expected error for invalid degrees")
	}
}

func TestIngestDerivedPDFs_Errors(t *testing.T) {
	tempDir := t.TempDir()

	// 1. Vault not configured
	cfg := &resolvedConfig{}
	_, err := ingestDerivedPDFs(cfg, []string{"dummy.pdf"})
	if err == nil || !strings.Contains(err.Error(), "no vault configured") {
		t.Errorf("expected error for unconfigured vault, got: %v", err)
	}

	// 2. Invalid database path
	dbParent := filepath.Join(tempDir, "some-file")
	if err := os.WriteFile(dbParent, []byte("regular file"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg = &resolvedConfig{
		vault: tempDir,
		db:    filepath.Join(dbParent, "test.db"),
	}
	_, err = ingestDerivedPDFs(cfg, []string{"dummy.pdf"})
	if err == nil || !strings.Contains(err.Error(), "open document store") {
		t.Errorf("expected error for invalid DB path, got: %v", err)
	}
}
