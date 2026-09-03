package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/service"
)

func TestScopedSearchHandler_FiltersOutOfScopeResults(t *testing.T) {
	allowed := map[string]bool{"in-scope.md": true}
	inner := func(ctx context.Context, input json.RawMessage) (any, error) {
		return service.SearchResponse{
			Results: []service.SearchResult{
				{Path: "in-scope.md", Title: "In"},
				{Path: "out-of-scope.md", Title: "Out"},
			},
			Hint: "some hint",
		}, nil
	}

	wrapped := scopedSearchHandler(inner, allowed)
	res, err := wrapped(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	out, ok := res.(scopedSearchOutput)
	if !ok {
		t.Fatalf("expected scopedSearchOutput, got %T", res)
	}
	if len(out.Results) != 1 || out.Results[0].Path != "in-scope.md" {
		t.Errorf("Results = %+v, want exactly [in-scope.md]", out.Results)
	}
	if out.ScopedOut != 1 {
		t.Errorf("ScopedOut = %d, want 1", out.ScopedOut)
	}
	if out.Hint != "some hint" {
		t.Errorf("Hint = %q, want preserved", out.Hint)
	}
}

func TestScopedSearchHandler_PassesThroughUnexpectedType(t *testing.T) {
	inner := func(ctx context.Context, input json.RawMessage) (any, error) {
		return "not a SearchResponse", nil
	}
	res, err := scopedSearchHandler(inner, map[string]bool{})(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res != "not a SearchResponse" {
		t.Errorf("expected passthrough of unexpected type, got %v", res)
	}
}

func TestScopedLsHandler_FiltersOutOfScopeEntries(t *testing.T) {
	allowed := map[string]bool{"a.md": true}
	inner := func(ctx context.Context, input json.RawMessage) (any, error) {
		return []service.FileEntry{
			{Path: "a.md"},
			{Path: "b.md"},
			{Path: "c.md"},
		}, nil
	}
	res, err := scopedLsHandler(inner, allowed)(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	out := res.(scopedLsOutput)
	if len(out.Files) != 1 || out.Files[0].Path != "a.md" {
		t.Errorf("Files = %+v, want exactly [a.md]", out.Files)
	}
	if out.ScopedOut != 2 {
		t.Errorf("ScopedOut = %d, want 2", out.ScopedOut)
	}
}

func TestScopedBacklinksHandler_FiltersOutOfScopePaths(t *testing.T) {
	allowed := map[string]bool{"a.md": true, "b.md": true}
	inner := func(ctx context.Context, input json.RawMessage) (any, error) {
		return []string{"a.md", "b.md", "z.md"}, nil
	}
	res, err := scopedBacklinksHandler(inner, allowed)(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	out := res.(scopedBacklinksOutput)
	if len(out.Paths) != 2 {
		t.Errorf("Paths = %v, want 2 entries", out.Paths)
	}
	if out.ScopedOut != 1 {
		t.Errorf("ScopedOut = %d, want 1", out.ScopedOut)
	}
}

func TestScopedHandlers_PropagateInnerError(t *testing.T) {
	boom := context.DeadlineExceeded
	inner := func(ctx context.Context, input json.RawMessage) (any, error) {
		return nil, boom
	}
	if _, err := scopedSearchHandler(inner, nil)(context.Background(), nil); err != boom {
		t.Errorf("scopedSearchHandler err = %v, want %v", err, boom)
	}
	if _, err := scopedLsHandler(inner, nil)(context.Background(), nil); err != boom {
		t.Errorf("scopedLsHandler err = %v, want %v", err, boom)
	}
	if _, err := scopedBacklinksHandler(inner, nil)(context.Background(), nil); err != boom {
		t.Errorf("scopedBacklinksHandler err = %v, want %v", err, boom)
	}
}

func TestScopeAgentToolsToNotebook_OnlyScopesDiscoveryTools(t *testing.T) {
	vaultDir := withTestVault(t)
	cfg.Vault = vaultDir

	vRoot, db, err := initServiceDeps()
	if err != nil {
		t.Fatal(err)
	}
	defer closeWithWarning("sidecar database", db.Close)
	svc := service.New(vRoot, db)

	docPath, err := svc.NoteNew("Doc", "body", "")
	if err != nil {
		t.Fatal(err)
	}
	nb, err := svc.NotebookNew("Scope Test", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.NotebookAddSource(nb.ID, docPath); err != nil {
		t.Fatal(err)
	}

	tools := readOnlyAgentTools(vRoot, db, &config.Config{})
	scoped, err := scopeAgentToolsToNotebook(vRoot, nb.ID, tools)
	if err != nil {
		t.Fatalf("scopeAgentToolsToNotebook: %v", err)
	}
	if len(scoped) != len(tools) {
		t.Fatalf("scoped tool count = %d, want %d (no tool should be added or removed)", len(scoped), len(tools))
	}

	scopedCount := 0
	for i, orig := range tools {
		switch orig.Name {
		case "desk_search", "desk_ls", "desk_backlinks":
			scopedCount++
		default:
			// Non-scoped tools must keep their original name/schema — the
			// scoped-tool-specific tests above cover handler behavior.
			if scoped[i].Name != orig.Name || scoped[i].ReadOnly != orig.ReadOnly {
				t.Errorf("tool %q was unexpectedly modified", orig.Name)
			}
		}
	}
	if scopedCount == 0 {
		t.Fatal("expected at least one of desk_search/desk_ls/desk_backlinks to be present and scoped")
	}
}

func TestScopeAgentToolsToNotebook_UnknownNotebookErrors(t *testing.T) {
	vaultDir := withTestVault(t)
	cfg.Vault = vaultDir

	vRoot, db, err := initServiceDeps()
	if err != nil {
		t.Fatal(err)
	}
	defer closeWithWarning("sidecar database", db.Close)

	if _, err := scopeAgentToolsToNotebook(vRoot, "does-not-exist", nil); err == nil {
		t.Fatal("expected an error scoping to a nonexistent notebook")
	}
}
