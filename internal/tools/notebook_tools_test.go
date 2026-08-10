package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/notebook"
)

func createTestNotebook(t *testing.T, factory ServiceFactory, title, description string) string {
	t.Helper()
	svc, db, err := factory()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	nb, err := svc.NotebookNew(title, description)
	if err != nil {
		t.Fatal(err)
	}
	return nb.ID
}

func TestNotebookListTool_ReturnsCreatedNotebooks(t *testing.T) {
	factory := testServiceFactory(t)
	createTestNotebook(t, factory, "Alpha", "")
	createTestNotebook(t, factory, "Beta", "")

	tool := newNotebookListTool(factory)
	out, err := tool.Handler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	list, ok := out.([]*notebook.Notebook)
	if !ok {
		t.Fatalf("expected []*notebook.Notebook, got %T", out)
	}
	if len(list) != 2 {
		t.Errorf("len(list) = %d, want 2", len(list))
	}
}

func TestNotebookGetTool_RequiresNotebook(t *testing.T) {
	tool := newNotebookGetTool(testServiceFactory(t))
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Error("expected an error for missing notebook")
	}
}

func TestNotebookGetTool_ReturnsResolvedSources(t *testing.T) {
	factory := testServiceFactory(t)
	docPath := createNote(t, factory, "Source Doc", "body")
	nbID := createTestNotebook(t, factory, "Get Test", "")

	svc, db, err := factory()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.NotebookAddSource(nbID, docPath); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	tool := newNotebookGetTool(factory)
	in, _ := json.Marshal(map[string]string{"notebook": nbID})
	out, err := tool.Handler(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	result, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any, got %T", out)
	}
	if result["id"] != nbID {
		t.Errorf("id = %v, want %v", result["id"], nbID)
	}
	sources, ok := result["sources"].([]notebook.SourceRef)
	if !ok || len(sources) != 1 {
		t.Errorf("sources = %v, want exactly one entry", result["sources"])
	}
}

func TestNotebookCreateTool_RequiresTitle(t *testing.T) {
	tool := newNotebookCreateTool(testServiceFactory(t))
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Error("expected an error for missing title")
	}
}

func TestNotebookCreateTool_CreatesNotebook(t *testing.T) {
	factory := testServiceFactory(t)
	tool := newNotebookCreateTool(factory)
	in, _ := json.Marshal(map[string]string{"title": "New Notebook", "description": "desc"})
	out, err := tool.Handler(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	nb, ok := out.(*notebook.Notebook)
	if !ok {
		t.Fatalf("expected *notebook.Notebook, got %T", out)
	}
	if nb.Title != "New Notebook" || nb.Description != "desc" {
		t.Errorf("nb = %+v, want title/description to match", nb)
	}
}

func TestNotebookAddSourceTool_RequiresBothArgs(t *testing.T) {
	tool := newNotebookAddSourceTool(testServiceFactory(t))
	for _, in := range []string{`{}`, `{"notebook":"x"}`, `{"path":"y"}`} {
		if _, err := tool.Handler(context.Background(), json.RawMessage(in)); err == nil {
			t.Errorf("input %s: expected an error", in)
		}
	}
}

func TestNotebookAddSourceTool_RejectsTraversal(t *testing.T) {
	factory := testServiceFactory(t)
	nbID := createTestNotebook(t, factory, "Scoped", "")

	tool := newNotebookAddSourceTool(factory)
	in, _ := json.Marshal(map[string]string{"notebook": nbID, "path": "../../etc/passwd"})
	if _, err := tool.Handler(context.Background(), in); err == nil {
		t.Error("expected an error adding a traversal path as a source")
	}
}

func TestNotebookRemoveSourceTool_RequiresBothArgs(t *testing.T) {
	tool := newNotebookRemoveSourceTool(testServiceFactory(t))
	for _, in := range []string{`{}`, `{"notebook":"x"}`, `{"path":"y"}`} {
		if _, err := tool.Handler(context.Background(), json.RawMessage(in)); err == nil {
			t.Errorf("input %s: expected an error", in)
		}
	}
}

func TestNotebookRemoveSourceTool_NeverDeletesReferencedFile(t *testing.T) {
	factory := testServiceFactory(t)
	docPath := createNote(t, factory, "Doc", "body")
	nbID := createTestNotebook(t, factory, "Remove Test", "")

	svc, db, err := factory()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.NotebookAddSource(nbID, docPath); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	tool := newNotebookRemoveSourceTool(factory)
	in, _ := json.Marshal(map[string]string{"notebook": nbID, "path": docPath})
	if _, err := tool.Handler(context.Background(), in); err != nil {
		t.Fatal(err)
	}

	svc2, db2, err := factory()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db2.Close() }()
	results, err := svc2.Search("body")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Error("referenced note must still exist after being removed from a notebook")
	}
}

func TestNotebookAskTool_RequiresNotebookAndQuery(t *testing.T) {
	tool := newNotebookAskTool(testServiceFactory(t))
	for _, in := range []string{`{}`, `{"notebook":"x"}`, `{"query":"y"}`} {
		if _, err := tool.Handler(context.Background(), json.RawMessage(in)); err == nil {
			t.Errorf("input %s: expected an error", in)
		}
	}
}

func TestNotebookAskTool_RestrictsCitationsToNotebookSources(t *testing.T) {
	factory := testServiceFactory(t)
	inScope := createNote(t, factory, "In Scope", "shared-tool-term content")
	createNote(t, factory, "Out Of Scope", "shared-tool-term content too")
	nbID := createTestNotebook(t, factory, "Ask Tool Test", "")

	svc, db, err := factory()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.NotebookAddSource(nbID, inScope); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	tool := newNotebookAskTool(factory)
	in, _ := json.Marshal(map[string]string{"notebook": nbID, "query": "shared-tool-term"})
	out, err := tool.Handler(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	result, ok := out.(map[string]string)
	if !ok {
		t.Fatalf("expected map[string]string, got %T", out)
	}
	if result["answer"] == "" {
		t.Error("expected a non-empty answer")
	}
}

func TestDeskAskTool_AcceptsOptionalNotebookField(t *testing.T) {
	factory := testServiceFactory(t)
	inScope := createNote(t, factory, "Scoped Doc", "scoped-desk-ask-term content")
	nbID := createTestNotebook(t, factory, "Desk Ask Scope", "")

	svc, db, err := factory()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.NotebookAddSource(nbID, inScope); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	tool := newAskTool(factory)
	in, _ := json.Marshal(map[string]string{"query": "scoped-desk-ask-term", "notebook": nbID})
	out, err := tool.Handler(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out.(map[string]string); !ok {
		t.Fatalf("expected map[string]string, got %T", out)
	}
}
