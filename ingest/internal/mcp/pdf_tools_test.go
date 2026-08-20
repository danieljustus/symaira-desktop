package mcp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-corekit/mcpserver"

	"github.com/danieljustus/symaira-ingest/internal/pdfops"
	"github.com/danieljustus/symaira-ingest/internal/store"
)

func writeMCPExecutable(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRegisterPDFTools_SplitPDF(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	input := filepath.Join(dir, "input.pdf")
	if err := os.WriteFile(input, []byte("%PDF-1.4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tools := pdfops.Tools{
		PDFInfo:     writeMCPExecutable(t, dir, "pdfinfo", "printf 'Pages: 2\\n'"),
		PDFSeparate: writeMCPExecutable(t, dir, "pdfseparate", "out=$(printf \"$6\" \"$2\"); cp \"$5\" \"$out\""),
		PDFUnite:    writeMCPExecutable(t, dir, "pdfunite", "last=\"\"; for arg in \"$@\"; do last=\"$arg\"; done; cp \"$1\" \"$last\""),
	}
	server := newMCPServerForPDFTest(st, filepath.Join(dir, "vault"), filepath.Join(dir, "archive"), tools)
	result, isError := callTool(t, server, "split_pdf", map[string]any{
		"input_path": input,
		"split_at":   "1",
		"output_dir": filepath.Join(dir, "parts"),
	})
	if isError {
		t.Fatalf("split_pdf returned an error: %v", result)
	}
	paths, ok := result["output_paths"].([]any)
	if !ok || len(paths) != 2 {
		t.Fatalf("output_paths = %v, want two paths", result["output_paths"])
	}
	if result["status"] != "success" {
		t.Fatalf("status = %v, want success", result["status"])
	}
}

func newMCPServerForPDFTest(st *store.Store, vault, archive string, tools pdfops.Tools) *mcpserver.Server {
	server := mcpserver.New("symingest", "0.1.0")
	registerPDFTools(server, st, fakeEngine{}, vault, archive, tools)
	return server
}
