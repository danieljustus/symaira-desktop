package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-ingest/internal/pdfops"
	"github.com/danieljustus/symaira-ingest/internal/store"
)

func TestRegisterPDFTools_MergePDF(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	input1 := filepath.Join(dir, "a.pdf")
	input2 := filepath.Join(dir, "b.pdf")
	output := filepath.Join(dir, "merged.pdf")
	os.WriteFile(input1, []byte("%PDF-1.4\na\n"), 0o600)
	os.WriteFile(input2, []byte("%PDF-1.4\nb\n"), 0o600)

	pdfunite := writeMCPExecutable(t, dir, "pdfunite", `last=""; for arg in "$@"; do last="$arg"; done; cat "$1" "$2" > "$last"`)
	tools := pdfops.Tools{PDFUnite: pdfunite}
	server := newMCPServerForPDFTest(st, filepath.Join(dir, "vault"), filepath.Join(dir, "archive"), tools)

	result, isError := callTool(t, server, "merge_pdf", map[string]any{
		"input_paths": []any{input1, input2},
		"output_path": output,
	})
	if isError {
		t.Fatalf("merge_pdf returned error: %v", result)
	}
	if result["status"] != "success" {
		t.Fatalf("status = %v, want success", result["status"])
	}
	paths, ok := result["output_paths"].([]any)
	if !ok || len(paths) != 1 {
		t.Fatalf("output_paths = %v, want single path", result["output_paths"])
	}
}

func TestRegisterPDFTools_MergePDF_TooFewInputs(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(filepath.Join(dir, "mcp.db"))
	defer st.Close()

	server := newMCPServerForPDFTest(st, filepath.Join(dir, "vault"), filepath.Join(dir, "archive"), pdfops.Tools{})

	_, isError := callToolRaw(t, server, "merge_pdf", map[string]any{
		"input_paths": []any{"/tmp/a.pdf"},
		"output_path": "/tmp/out.pdf",
	})
	if !isError {
		t.Fatal("expected error for too few inputs")
	}
}

func TestRegisterPDFTools_MergePDF_MissingOutputPath(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(filepath.Join(dir, "mcp.db"))
	defer st.Close()

	server := newMCPServerForPDFTest(st, filepath.Join(dir, "vault"), filepath.Join(dir, "archive"), pdfops.Tools{})

	_, isError := callToolRaw(t, server, "merge_pdf", map[string]any{
		"input_paths": []any{"/tmp/a.pdf", "/tmp/b.pdf"},
		"output_path": "",
	})
	if !isError {
		t.Fatal("expected error for empty output_path")
	}
}

func TestRegisterPDFTools_RotatePDF(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	input := filepath.Join(dir, "input.pdf")
	output := filepath.Join(dir, "rotated.pdf")
	os.WriteFile(input, []byte("%PDF-1.4\nvalid\n"), 0o600)

	qpdf := writeMCPExecutable(t, dir, "qpdf", `cp "$2" "$3"`)
	tools := pdfops.Tools{QPDF: qpdf}
	server := newMCPServerForPDFTest(st, filepath.Join(dir, "vault"), filepath.Join(dir, "archive"), tools)

	result, isError := callTool(t, server, "rotate_pdf", map[string]any{
		"input_path":  input,
		"output_path": output,
		"degrees":     90,
	})
	if isError {
		t.Fatalf("rotate_pdf returned error: %v", result)
	}
	if result["status"] != "success" {
		t.Fatalf("status = %v, want success", result["status"])
	}
}

func TestRegisterPDFTools_RotatePDF_MissingInputPath(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(filepath.Join(dir, "mcp.db"))
	defer st.Close()

	server := newMCPServerForPDFTest(st, filepath.Join(dir, "vault"), filepath.Join(dir, "archive"), pdfops.Tools{})

	_, isError := callToolRaw(t, server, "rotate_pdf", map[string]any{
		"input_path":  "",
		"output_path": "/tmp/out.pdf",
		"degrees":     90,
	})
	if !isError {
		t.Fatal("expected error for empty input_path")
	}
}

func TestRegisterPDFTools_RotatePDF_MissingOutputPath(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(filepath.Join(dir, "mcp.db"))
	defer st.Close()

	server := newMCPServerForPDFTest(st, filepath.Join(dir, "vault"), filepath.Join(dir, "archive"), pdfops.Tools{})

	_, isError := callToolRaw(t, server, "rotate_pdf", map[string]any{
		"input_path":  "/tmp/in.pdf",
		"output_path": "",
		"degrees":     90,
	})
	if !isError {
		t.Fatal("expected error for empty output_path")
	}
}

func TestRegisterPDFTools_SplitPDF_MissingInputPath(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(filepath.Join(dir, "mcp.db"))
	defer st.Close()

	server := newMCPServerForPDFTest(st, filepath.Join(dir, "vault"), filepath.Join(dir, "archive"), pdfops.Tools{})

	_, isError := callToolRaw(t, server, "split_pdf", map[string]any{
		"input_path": "",
		"split_at":   "1",
	})
	if !isError {
		t.Fatal("expected error for empty input_path")
	}
}

func TestRegisterPDFTools_SplitPDF_MissingSplitAt(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(filepath.Join(dir, "mcp.db"))
	defer st.Close()

	server := newMCPServerForPDFTest(st, filepath.Join(dir, "vault"), filepath.Join(dir, "archive"), pdfops.Tools{})

	_, isError := callToolRaw(t, server, "split_pdf", map[string]any{
		"input_path": "/tmp/in.pdf",
		"split_at":   "",
	})
	if !isError {
		t.Fatal("expected error for empty split_at")
	}
}

func TestCompletePDFToolResult_WithIngest(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	vault := filepath.Join(dir, "vault")
	archive := filepath.Join(dir, "archive")
	os.MkdirAll(vault, 0o700)
	os.MkdirAll(archive, 0o700)

	output := filepath.Join(dir, "output.pdf")
	os.WriteFile(output, []byte("%PDF-1.4\ntest\n"), 0o600)

	resultJSON, err := completePDFToolResult(context.Background(), st, fakeEngine{}, vault, archive, []string{output}, true)
	if err != nil {
		t.Fatalf("completePDFToolResult: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result["status"] != "success" {
		t.Errorf("status = %v", result["status"])
	}
	ingested, ok := result["ingested_paths"].([]any)
	if !ok || len(ingested) != 1 {
		t.Errorf("ingested_paths = %v, want 1 path", result["ingested_paths"])
	}
}

func TestCompletePDFToolResult_WithoutIngest(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	resultJSON, err := completePDFToolResult(context.Background(), st, fakeEngine{}, "", "", []string{"/tmp/a.pdf"}, false)
	if err != nil {
		t.Fatalf("completePDFToolResult: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	ingested, ok := result["ingested_paths"].([]any)
	if !ok || len(ingested) != 0 {
		t.Errorf("ingested_paths = %v, want empty", result["ingested_paths"])
	}
}

func TestResolveVaultArchive_Defaults(t *testing.T) {
	vault, archive, err := resolveVaultArchive("/explicit/vault", "/explicit/archive", "/default/vault", "/default/archive")
	if err != nil {
		t.Fatalf("resolveVaultArchive: %v", err)
	}
	if vault != "/explicit/vault" {
		t.Errorf("vault = %q, want /explicit/vault", vault)
	}
	if archive != "/explicit/archive" {
		t.Errorf("archive = %q, want /explicit/archive", archive)
	}
}

func TestResolveVaultArchive_FallbackToDefaults(t *testing.T) {
	vault, archive, err := resolveVaultArchive("", "", "/default/vault", "/default/archive")
	if err != nil {
		t.Fatalf("resolveVaultArchive: %v", err)
	}
	if vault != "/default/vault" {
		t.Errorf("vault = %q, want /default/vault", vault)
	}
	if archive != "/default/archive" {
		t.Errorf("archive = %q, want /default/archive", archive)
	}
}

func TestResolveVaultArchive_NoDefaults(t *testing.T) {
	_, _, err := resolveVaultArchive("", "", "", "")
	if err == nil {
		t.Fatal("expected error when no vault/archive configured")
	}
}

func TestParseMCPDocumentIDs_Additional(t *testing.T) {
	cases := []struct {
		name    string
		input   any
		want    []int
		wantErr bool
	}{
		{"empty array", []any{}, nil, false},
		{"single id", []any{float64(42)}, []int{42}, false},
		{"multiple ids", []any{float64(1), float64(2), float64(3)}, []int{1, 2, 3}, false},
		{"string comma-separated", "5,10", []int{5, 10}, false},
		{"string with spaces", "  7  ", []int{7}, false},
		{"empty string", "", nil, false},
		{"invalid string", []any{"abc"}, nil, true},
		{"negative id", []any{float64(-1)}, nil, true},
		{"zero id", []any{float64(0)}, nil, true},
		{"bool type", []any{true}, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, _ := json.Marshal(tc.input)
			got, err := parseMCPDocumentIDs(data)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got %v", got)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if len(got) != len(tc.want) {
					t.Errorf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}
