package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-corekit/mcpserver"

	"github.com/danieljustus/symaira-ingest/internal/store"
)

func newMCPServerForTest(st *store.Store, vault, archive string) *mcpserver.Server {
	server := mcpserver.New("symingest", "0.1.0")
	Register(server, st, fakeEngine{}, vault, archive)
	return server
}

func TestRegister_ReocrByDocumentIDPreservesFrontmatter(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	vault := filepath.Join(dir, "vault")
	archive := filepath.Join(dir, "archive")
	source := filepath.Join(dir, "scan.png")
	if err := os.WriteFile(source, []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}, 0o600); err != nil {
		t.Fatal(err)
	}
	server := newMCPServerForTest(st, vault, archive)
	initial, isError := callTool(t, server, "ingest_file", map[string]any{"path": source})
	if isError {
		t.Fatalf("initial ingest returned an error: %v", initial)
	}
	archivePath := initial["archive_path"].(string)
	vaultPath := initial["vault_path"].(string)
	doc, err := st.ByArchivePath(context.Background(), archivePath)
	if err != nil {
		t.Fatalf("lookup ingested document: %v", err)
	}
	original, err := os.ReadFile(vaultPath)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(original), "category: \"\"\n", "category: \"\"\ncustom_property: keep-me\n", 1)
	edited = strings.Replace(edited, "ocr-result", "stale manually edited body", 1)
	if err := os.WriteFile(vaultPath, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}

	result, isError := callTool(t, server, "reocr", map[string]any{"document_id": doc.ID})
	if isError {
		t.Fatalf("reocr returned an error: %v", result)
	}
	if result["status"] != "completed" || result["document_id"] != float64(doc.ID) {
		t.Fatalf("unexpected reocr result: %+v", result)
	}
	if result["job_id"].(float64) <= 0 {
		t.Fatalf("job_id = %v, want positive ID", result["job_id"])
	}

	updated, err := os.ReadFile(vaultPath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(updated)
	if !strings.Contains(content, "custom_property: keep-me") {
		t.Fatalf("reocr dropped user frontmatter: %s", content)
	}
	if !strings.Contains(content, "ocr-result") || strings.Contains(content, "stale manually edited body") {
		t.Fatalf("reocr did not replace note body: %s", content)
	}
}

func TestRegister_ReocrBySourcePath(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	vault := filepath.Join(dir, "vault")
	archive := filepath.Join(dir, "archive")
	source := filepath.Join(dir, "scan.png")
	if err := os.WriteFile(source, []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}, 0o600); err != nil {
		t.Fatal(err)
	}
	server := newMCPServerForTest(st, vault, archive)
	initial, isError := callTool(t, server, "ingest_file", map[string]any{"path": source})
	if isError {
		t.Fatalf("initial ingest returned an error: %v", initial)
	}
	result, isError := callTool(t, server, "reocr", map[string]any{"source": initial["archive_path"]})
	if isError {
		t.Fatalf("reocr returned an error: %v", result)
	}
	if result["status"] != "completed" {
		t.Fatalf("status = %v, want completed", result["status"])
	}
}

func TestRegister_ReocrMissingSourceAndInvalidSelector(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	vault := filepath.Join(dir, "vault")
	archive := filepath.Join(dir, "archive")
	source := filepath.Join(dir, "scan.png")
	if err := os.WriteFile(source, []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}, 0o600); err != nil {
		t.Fatal(err)
	}
	server := newMCPServerForTest(st, vault, archive)
	initial, isError := callTool(t, server, "ingest_file", map[string]any{"path": source})
	if isError {
		t.Fatalf("initial ingest returned an error: %v", initial)
	}
	doc, err := st.ByArchivePath(context.Background(), initial["archive_path"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(initial["archive_path"].(string)); err != nil {
		t.Fatal(err)
	}
	result, isError := callTool(t, server, "reocr", map[string]any{"document_id": doc.ID})
	if !isError || !strings.Contains(result["error"].(string), "stat archived source") {
		t.Fatalf("missing-source result = %+v, isError=%v", result, isError)
	}

	result, isError = callTool(t, server, "reocr", map[string]any{})
	if !isError || !strings.Contains(result["error"].(string), "exactly one") {
		t.Fatalf("invalid-selector result = %+v, isError=%v", result, isError)
	}
}
