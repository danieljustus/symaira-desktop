package mcp

import (
	"context"
	"encoding/json"
	"fmt"
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
	if !strings.Contains(answer, "not configured") {
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

func TestStatusToolReturnsVersionAndVault(t *testing.T) {
	cfg := &config.Config{Vault: "/test/vault"}
	tool := newStatusTool(cfg, false)

	out, err := tool.Handler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	status, ok := out.(map[string]string)
	if !ok {
		t.Fatalf("expected map[string]string, got %T", out)
	}
	if status["vault"] != "/test/vault" {
		t.Errorf("expected vault '/test/vault', got %q", status["vault"])
	}
	if status["capabilities"] != "read_only" {
		t.Errorf("expected capabilities 'read_only', got %q", status["capabilities"])
	}
}

func errorFactory(t *testing.T) serviceFactory {
	t.Helper()
	return func() (*service.Service, *sidecar.DB, error) {
		return nil, nil, fmt.Errorf("service unavailable")
	}
}

func TestLsToolServiceError(t *testing.T) {
	tool := newLsTool(errorFactory(t))
	_, err := tool.Handler(context.Background(), json.RawMessage(`{"dir":"test"}`))
	if err == nil || !strings.Contains(err.Error(), "service unavailable") {
		t.Errorf("expected service unavailable error, got %v", err)
	}
}

func TestSearchToolServiceError(t *testing.T) {
	tool := newSearchTool(errorFactory(t))
	_, err := tool.Handler(context.Background(), json.RawMessage(`{"query":"test"}`))
	if err == nil || !strings.Contains(err.Error(), "service unavailable") {
		t.Errorf("expected service unavailable error, got %v", err)
	}
}

func TestPropsToolServiceError(t *testing.T) {
	tool := newPropsTool(errorFactory(t))
	_, err := tool.Handler(context.Background(), json.RawMessage(`{"file":"test.md"}`))
	if err == nil || !strings.Contains(err.Error(), "service unavailable") {
		t.Errorf("expected service unavailable error, got %v", err)
	}
}

func TestBacklinksToolServiceError(t *testing.T) {
	tool := newBacklinksTool(errorFactory(t))
	_, err := tool.Handler(context.Background(), json.RawMessage(`{"file":"test.md"}`))
	if err == nil || !strings.Contains(err.Error(), "service unavailable") {
		t.Errorf("expected service unavailable error, got %v", err)
	}
}

func TestNoteNewToolServiceError(t *testing.T) {
	tool := newNoteNewTool(errorFactory(t))
	_, err := tool.Handler(context.Background(), json.RawMessage(`{"title":"Test"}`))
	if err == nil || !strings.Contains(err.Error(), "service unavailable") {
		t.Errorf("expected service unavailable error, got %v", err)
	}
}

func TestAskToolServiceError(t *testing.T) {
	tool := newAskTool(errorFactory(t))
	_, err := tool.Handler(context.Background(), json.RawMessage(`{"query":"test"}`))
	if err == nil || !strings.Contains(err.Error(), "service unavailable") {
		t.Errorf("expected service unavailable error, got %v", err)
	}
}

func TestIngestToolServiceError(t *testing.T) {
	tool := newIngestTool(errorFactory(t))
	_, err := tool.Handler(context.Background(), json.RawMessage(`{"source_path":"/tmp/file.txt"}`))
	if err == nil || !strings.Contains(err.Error(), "service unavailable") {
		t.Errorf("expected service unavailable error, got %v", err)
	}
}

func TestDocsToolServiceError(t *testing.T) {
	tool := newDocsTool(errorFactory(t))
	_, err := tool.Handler(context.Background(), json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "service unavailable") {
		t.Errorf("expected service unavailable error, got %v", err)
	}
}

func TestDocSetStatusToolServiceError(t *testing.T) {
	tool := newDocSetStatusTool(errorFactory(t))
	_, err := tool.Handler(context.Background(), json.RawMessage(`{"file":"test.md","status":"done"}`))
	if err == nil || !strings.Contains(err.Error(), "service unavailable") {
		t.Errorf("expected service unavailable error, got %v", err)
	}
}

func TestDocsReviewToolServiceError(t *testing.T) {
	tool := newDocsReviewTool(errorFactory(t), &config.Config{ReviewThreshold: 85})
	_, err := tool.Handler(context.Background(), json.RawMessage(`{"threshold":85}`))
	if err == nil || !strings.Contains(err.Error(), "service unavailable") {
		t.Errorf("expected service unavailable error, got %v", err)
	}
}

func TestDocsSimilarToolServiceError(t *testing.T) {
	tool := newDocsSimilarTool(errorFactory(t))
	_, err := tool.Handler(context.Background(), json.RawMessage(`{"file":"test.md","threshold":50}`))
	if err == nil || !strings.Contains(err.Error(), "service unavailable") {
		t.Errorf("expected service unavailable error, got %v", err)
	}
}

func TestLsToolInvalidJSON(t *testing.T) {
	tool := newLsTool(testFactory(t))
	_, err := tool.Handler(context.Background(), json.RawMessage(`{invalid`))
	if err == nil {
		t.Error("expected json unmarshal error")
	}
}

func TestSearchToolInvalidJSON(t *testing.T) {
	tool := newSearchTool(testFactory(t))
	_, err := tool.Handler(context.Background(), json.RawMessage(`{invalid`))
	if err == nil {
		t.Error("expected json unmarshal error")
	}
}

func TestPropsToolInvalidJSON(t *testing.T) {
	tool := newPropsTool(testFactory(t))
	_, err := tool.Handler(context.Background(), json.RawMessage(`{invalid`))
	if err == nil {
		t.Error("expected json unmarshal error")
	}
}

func TestBacklinksToolInvalidJSON(t *testing.T) {
	tool := newBacklinksTool(testFactory(t))
	_, err := tool.Handler(context.Background(), json.RawMessage(`{invalid`))
	if err == nil {
		t.Error("expected json unmarshal error")
	}
}

func TestNoteNewToolInvalidJSON(t *testing.T) {
	tool := newNoteNewTool(testFactory(t))
	_, err := tool.Handler(context.Background(), json.RawMessage(`{invalid`))
	if err == nil {
		t.Error("expected json unmarshal error")
	}
}

func TestAskToolInvalidJSON(t *testing.T) {
	tool := newAskTool(testFactory(t))
	_, err := tool.Handler(context.Background(), json.RawMessage(`{invalid`))
	if err == nil {
		t.Error("expected json unmarshal error")
	}
}

func TestIngestToolInvalidJSON(t *testing.T) {
	tool := newIngestTool(testFactory(t))
	_, err := tool.Handler(context.Background(), json.RawMessage(`{invalid`))
	if err == nil {
		t.Error("expected json unmarshal error")
	}
}

func TestDocsToolInvalidJSON(t *testing.T) {
	tool := newDocsTool(testFactory(t))
	_, err := tool.Handler(context.Background(), json.RawMessage(`{invalid`))
	if err == nil {
		t.Error("expected json unmarshal error")
	}
}

func TestDocSetStatusToolInvalidJSON(t *testing.T) {
	tool := newDocSetStatusTool(testFactory(t))
	_, err := tool.Handler(context.Background(), json.RawMessage(`{invalid`))
	if err == nil {
		t.Error("expected json unmarshal error")
	}
}

func TestDocsReviewToolInvalidJSON(t *testing.T) {
	tool := newDocsReviewTool(testFactory(t), &config.Config{ReviewThreshold: 85})
	_, err := tool.Handler(context.Background(), json.RawMessage(`{invalid`))
	if err == nil {
		t.Error("expected json unmarshal error")
	}
}

func TestDocsSimilarToolInvalidJSON(t *testing.T) {
	tool := newDocsSimilarTool(testFactory(t))
	_, err := tool.Handler(context.Background(), json.RawMessage(`{invalid`))
	if err == nil {
		t.Error("expected json unmarshal error")
	}
}

func TestClipToolWithoutSymfetch(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	tool := newClipTool(testFactory(t))

	in, _ := json.Marshal(map[string]string{"url": "https://example.com"})
	_, err := tool.Handler(context.Background(), in)
	if err == nil {
		t.Error("expected an error when symfetch is not installed")
	}
}

func TestClipToolRequiresURL(t *testing.T) {
	tool := newClipTool(testFactory(t))
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Error("expected error for missing url")
	}
}

func TestClipToolServiceError(t *testing.T) {
	tool := newClipTool(errorFactory(t))
	_, err := tool.Handler(context.Background(), json.RawMessage(`{"url":"https://example.com"}`))
	if err == nil || !strings.Contains(err.Error(), "service unavailable") {
		t.Errorf("expected service unavailable error, got %v", err)
	}
}

func TestClipToolInvalidJSON(t *testing.T) {
	tool := newClipTool(testFactory(t))
	_, err := tool.Handler(context.Background(), json.RawMessage(`{invalid`))
	if err == nil {
		t.Error("expected json unmarshal error")
	}
}

func TestNoteNewToolCreatesNote(t *testing.T) {
	factory := testFactory(t)
	tool := newNoteNewTool(factory)

	in, _ := json.Marshal(map[string]string{"title": "Test Note", "content": "body text"})
	out, err := tool.Handler(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	result, ok := out.(map[string]string)
	if !ok {
		t.Fatalf("expected map[string]string, got %T", out)
	}
	if result["path"] == "" {
		t.Error("expected non-empty path")
	}
}
