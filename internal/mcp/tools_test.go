package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/service"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
)

func testFactory(t *testing.T) serviceFactory {
	t.Helper()
	vaultRoot := t.TempDir()
	return func() (*service.Service, *sidecar.DB, error) {
		db, err := sidecar.Open(filepath.Join(vaultRoot, "sidecar.db"))
		if err != nil {
			return nil, nil, err
		}
		return service.New(vaultRoot, db), db, nil
	}
}

func TestAskToolFallbackWithoutOllama(t *testing.T) {
	t.Setenv("SYMDESK_OLLAMA_URL", "")
	tool := newAskTool(testFactory(t))

	out, err := tool.Handler(context.Background(), json.RawMessage(`{"query":"anything"}`))
	if err != nil {
		t.Fatal(err)
	}
	answer := out.(map[string]string)["answer"]
	if !strings.Contains(answer, "nicht konfiguriert") {
		t.Errorf("expected honest unconfigured note, got: %s", answer)
	}
}

func TestAskToolRequiresQuery(t *testing.T) {
	tool := newAskTool(testFactory(t))
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Error("expected error for missing query")
	}
}

func TestIngestToolCreatesInboxNote(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	factory := testFactory(t)
	tool := newIngestTool(factory)

	src := filepath.Join(t.TempDir(), "doc.txt")
	if err := os.WriteFile(src, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	in, _ := json.Marshal(map[string]string{"source_path": src})
	out, err := tool.Handler(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	path := out.(map[string]string)["path"]
	if !strings.HasPrefix(path, "inbox/") || !strings.HasSuffix(path, ".md") {
		t.Errorf("unexpected note path: %s", path)
	}

	// The note must be indexed and findable via the same service.
	svc, db, err := factory()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	files, err := svc.Ls("")
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, f := range files {
		if f["path"] == path {
			found = true
		}
	}
	if !found {
		t.Errorf("ingested note %s not in index", path)
	}
}

func TestIngestToolRequiresSourcePath(t *testing.T) {
	tool := newIngestTool(testFactory(t))
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Error("expected error for missing source_path")
	}
}

func TestDocsToolReturnsResults(t *testing.T) {
	factory := testFactory(t)
	tool := newDocsTool(factory)

	in, _ := json.Marshal(map[string]string{})
	out, err := tool.Handler(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	results, ok := out.([]service.DocsListResult)
	if !ok {
		t.Fatalf("expected []service.DocsListResult, got %T", out)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 docs in empty vault, got %d", len(results))
	}
}

func TestDocSetStatusToolRequiresArgs(t *testing.T) {
	tool := newDocSetStatusTool(testFactory(t))
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Error("expected error for missing args")
	}
}

func TestDocSetStatusToolUpdatesFile(t *testing.T) {
	factory := testFactory(t)
	svc, db, err := factory()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	content := "---\ntitle: \"Test\"\nstatus: \"open\"\n---\n\nBody.\n"
	absPath := filepath.Join(svc.VaultRoot, "test.md")
	if err := os.WriteFile(absPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	tool := newDocSetStatusTool(factory)
	in, _ := json.Marshal(map[string]string{"file": "test.md", "status": "done"})
	out, err := tool.Handler(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]string)
	if result["new_status"] != "done" {
		t.Errorf("expected new_status 'done', got '%s'", result["new_status"])
	}
}

func TestDocsReviewToolReturnsResults(t *testing.T) {
	factory := testFactory(t)
	tool := newDocsReviewTool(factory, &config.Config{ReviewThreshold: 85})

	in, _ := json.Marshal(map[string]int{"threshold": 85})
	out, err := tool.Handler(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	results, ok := out.([]sidecar.ReviewResult)
	if !ok {
		t.Fatalf("expected []sidecar.ReviewResult, got %T", out)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 review items in empty vault, got %d", len(results))
	}
}

func TestDocsSimilarToolRequiresFile(t *testing.T) {
	tool := newDocsSimilarTool(testFactory(t))
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Error("expected error for missing file")
	}
}

func TestDocsSimilarToolReturnsResults(t *testing.T) {
	factory := testFactory(t)
	tool := newDocsSimilarTool(factory)

	in, _ := json.Marshal(map[string]interface{}{"file": "nonexistent.md", "threshold": 50})
	_, err := tool.Handler(context.Background(), in)
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}
