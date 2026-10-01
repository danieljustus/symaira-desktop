package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/ai"
)

func TestScopedSearchResultsReturnsOnlyNotebookSources(t *testing.T) {
	svc := newTestService(t)

	matchedPath, err := svc.NoteNew("Allowed Match", "vault-scope-needle appears in this allowed note", "")
	if err != nil {
		t.Fatal(err)
	}
	fallbackPath, err := svc.NoteNew("Allowed Context", "This allowed note has no literal terms from the query.", "")
	if err != nil {
		t.Fatal(err)
	}
	outsidePath, err := svc.NoteNew("Outside Match", "vault-scope-needle must remain outside the notebook", "")
	if err != nil {
		t.Fatal(err)
	}
	nb, err := svc.NotebookNew("Search Scope", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{matchedPath, fallbackPath} {
		if _, err := svc.NotebookAddSource(nb.ID, path); err != nil {
			t.Fatal(err)
		}
	}

	results, scopedPaths, err := svc.ScopedSearchResults(nb.ID, "vault-scope-needle")
	if err != nil {
		t.Fatalf("ScopedSearchResults: %v", err)
	}
	scopedPathSet := make(map[string]bool, len(scopedPaths))
	for _, path := range scopedPaths {
		scopedPathSet[path] = true
	}
	if len(scopedPathSet) != 2 || !scopedPathSet[matchedPath] || !scopedPathSet[fallbackPath] {
		t.Fatalf("scoped paths = %v, want [%s %s]", scopedPaths, matchedPath, fallbackPath)
	}
	byPath := make(map[string]SearchResult, len(results))
	for _, result := range results {
		byPath[result.Path] = result
		if result.Path == outsidePath {
			t.Fatalf("out-of-scope note appeared in search results: %+v", result)
		}
	}
	if len(byPath) != 2 {
		t.Fatalf("results = %+v, want exactly the two notebook sources", results)
	}
	if matched, ok := byPath[matchedPath]; !ok || matched.Score != 1 || !strings.Contains(matched.Snippet, "vault-scope-needle") {
		t.Fatalf("matching source result = %+v, want FTS hit with the query excerpt", matched)
	}
	if fallback, ok := byPath[fallbackPath]; !ok || fallback.Score != 0 || !strings.Contains(fallback.Snippet, "no literal terms") {
		t.Fatalf("nonmatching source fallback = %+v, want its parsed note excerpt", fallback)
	}
}

func TestScopedSearchResultsBlankQueryUsesPresentSourcesAndBoundsExcerpt(t *testing.T) {
	svc := newTestService(t)

	longPath, err := svc.NoteNew("Long Source", strings.Repeat("long scoped body ", 140), "")
	if err != nil {
		t.Fatal(err)
	}
	missingPath, err := svc.NoteNew("Deleted Source", "this file will be removed", "")
	if err != nil {
		t.Fatal(err)
	}
	outsidePath, err := svc.NoteNew("Outside Source", "never include this note", "")
	if err != nil {
		t.Fatal(err)
	}
	nb, err := svc.NotebookNew("Blank Scope", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{longPath, missingPath} {
		if _, err := svc.NotebookAddSource(nb.ID, path); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(filepath.Join(svc.VaultRoot, filepath.FromSlash(missingPath))); err != nil {
		t.Fatal(err)
	}

	results, scopedPaths, err := svc.ScopedSearchResults(nb.ID, "  \t ")
	if err != nil {
		t.Fatalf("ScopedSearchResults: %v", err)
	}
	if len(scopedPaths) != 1 || scopedPaths[0] != longPath {
		t.Fatalf("scoped paths = %v, want only present source %s", scopedPaths, longPath)
	}
	if len(results) != 1 || results[0].Path != longPath || results[0].Score != 0 {
		t.Fatalf("blank-query results = %+v, want one fallback result for %s", results, longPath)
	}
	if len(results[0].Snippet) != 1500 {
		t.Fatalf("fallback excerpt length = %d, want the 1500-character bound", len(results[0].Snippet))
	}
	if strings.Contains(results[0].Snippet, "Deleted Source") || strings.Contains(results[0].Snippet, outsidePath) {
		t.Fatalf("fallback excerpt leaked a missing or out-of-scope source: %q", results[0].Snippet)
	}
}

func TestScopedSearchResultsEmptyNotebookDoesNotWidenScope(t *testing.T) {
	svc := newTestService(t)
	if _, err := svc.NoteNew("Unscoped Match", "empty-scope-needle exists elsewhere in the vault", ""); err != nil {
		t.Fatal(err)
	}
	nb, err := svc.NotebookNew("Empty Scope", "")
	if err != nil {
		t.Fatal(err)
	}

	results, scopedPaths, err := svc.ScopedSearchResults(nb.ID, "empty-scope-needle")
	if err != nil {
		t.Fatalf("ScopedSearchResults: %v", err)
	}
	if len(results) != 0 || len(scopedPaths) != 0 {
		t.Fatalf("empty notebook scope returned results=%+v paths=%v; want both empty", results, scopedPaths)
	}
}

func TestAskTextScopedPromptContainsOnlyNotebookSources(t *testing.T) {
	svc := newTestService(t)
	allowedPath, err := svc.NoteNew("Allowed Prompt Note", "private-allowed-evidence is the only permitted grounding text", "")
	if err != nil {
		t.Fatal(err)
	}
	outsidePath, err := svc.NoteNew("Excluded Prompt Note", "private-excluded-evidence must never reach the model", "")
	if err != nil {
		t.Fatal(err)
	}
	nb, err := svc.NotebookNew("Prompt Scope", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.NotebookAddSource(nb.ID, allowedPath); err != nil {
		t.Fatal(err)
	}

	var prompt string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			t.Errorf("generation path = %q, want /api/generate", r.URL.Path)
		}
		var request struct {
			Prompt string `json:"prompt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode model request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		prompt = request.Prompt
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = fmt.Fprintln(w, `{"response":"scoped answer","done":true}`)
	}))
	defer server.Close()
	t.Setenv("SYMDESK_OLLAMA_URL", server.URL)

	answer, err := svc.AskTextScoped(context.Background(), nb.ID, "private-allowed-evidence")
	if err != nil {
		t.Fatalf("AskTextScoped: %v", err)
	}
	if answer != "scoped answer" {
		t.Fatalf("answer = %q, want mocked model response", answer)
	}
	if !strings.Contains(prompt, allowedPath) || !strings.Contains(prompt, "private-allowed-evidence") {
		t.Fatalf("model prompt omitted the allowed note: %q", prompt)
	}
	if strings.Contains(prompt, outsidePath) || strings.Contains(prompt, "private-excluded-evidence") {
		t.Fatalf("model prompt included an out-of-scope note: %q", prompt)
	}
}

func TestAskTextScopedReturnsResolutionError(t *testing.T) {
	svc := newTestService(t)
	if _, err := svc.AskTextScoped(context.Background(), "missing-notebook", "question"); err == nil {
		t.Fatal("AskTextScoped succeeded for a missing notebook")
	}
}

func TestAskTextScopedEmptyReferenceUsesUnscopedAsk(t *testing.T) {
	t.Setenv("SYMDESK_OLLAMA_URL", "")
	svc := newTestService(t)
	path, err := svc.NoteNew("Unscoped Fallback", "unscoped-scoped-compatibility-query body", "")
	if err != nil {
		t.Fatal(err)
	}

	answer, err := svc.AskTextScoped(context.Background(), "", "unscoped-scoped-compatibility-query")
	if err != nil {
		t.Fatalf("AskTextScoped with empty notebook ref: %v", err)
	}
	if !strings.Contains(answer, "[["+path+"]]") {
		t.Fatalf("unscoped fallback answer = %q, want relevant note citation %q", answer, path)
	}
}

func TestScopedSearchResultsPropagatesClosedIndexError(t *testing.T) {
	svc := newTestService(t)
	path, err := svc.NoteNew("Index Error Source", "index-error-scope-query source body", "")
	if err != nil {
		t.Fatal(err)
	}
	nb, err := svc.NotebookNew("Index Error Scope", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.NotebookAddSource(nb.ID, path); err != nil {
		t.Fatal(err)
	}
	if err := svc.DB.Close(); err != nil {
		t.Fatal(err)
	}

	results, scopedPaths, err := svc.ScopedSearchResults(nb.ID, "index-error-scope-query")
	if err == nil {
		t.Fatal("ScopedSearchResults succeeded after its index was closed")
	}
	if results != nil {
		t.Fatalf("failed scoped search results = %+v, want nil", results)
	}
	if len(scopedPaths) != 1 || scopedPaths[0] != path {
		t.Fatalf("paths retained on search error = %v, want [%s]", scopedPaths, path)
	}
}

func TestAskScopedMissingNotebookEmitsTerminalError(t *testing.T) {
	svc := newTestService(t)
	out := make(chan interface{})
	go svc.AskScoped(context.Background(), "missing-notebook", "question", out)
	events := collectAskEvents(t, out)
	if len(events) != 4 {
		t.Fatalf("events = %+v, want search running, error, answer, and done", events)
	}
	if events[0].Type != ai.AIEventTool || events[0].ToolName != "search" || events[0].Status != "running" {
		t.Fatalf("first event = %+v, want search running", events[0])
	}
	if events[1].Type != ai.AIEventTool || events[1].ToolName != "search" || events[1].Status != "error" {
		t.Fatalf("second event = %+v, want search error", events[1])
	}
	if events[2].Type != ai.AIEventAnswer || !strings.HasPrefix(events[2].Text, "Notebook not found:") {
		t.Fatalf("error answer = %+v, want notebook resolution message", events[2])
	}
	if events[3].Type != ai.AIEventDone {
		t.Fatalf("terminal event = %+v, want done", events[3])
	}
}

func TestAskScopedIndexFailureEmitsTerminalError(t *testing.T) {
	svc := newTestService(t)
	path, err := svc.NoteNew("Search Failure Note", "search-failure-scope-marker", "")
	if err != nil {
		t.Fatal(err)
	}
	nb, err := svc.NotebookNew("Search Failure Scope", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.NotebookAddSource(nb.ID, path); err != nil {
		t.Fatal(err)
	}
	if err := svc.DB.Close(); err != nil {
		t.Fatal(err)
	}

	out := make(chan interface{})
	go svc.AskScoped(context.Background(), nb.ID, "search-failure-scope-marker", out)
	events := collectAskEvents(t, out)
	if len(events) != 4 {
		t.Fatalf("events = %+v, want search running, error, answer, and done", events)
	}
	if events[0].Type != ai.AIEventTool || events[0].ToolName != "search" || events[0].Status != "running" {
		t.Fatalf("first event = %+v, want search running", events[0])
	}
	if events[1].Type != ai.AIEventTool || events[1].ToolName != "search" || events[1].Status != "error" {
		t.Fatalf("second event = %+v, want search error", events[1])
	}
	if events[2].Type != ai.AIEventAnswer || !strings.HasPrefix(events[2].Text, "Search failed:") {
		t.Fatalf("error answer = %+v, want search failure message", events[2])
	}
	if events[3].Type != ai.AIEventDone {
		t.Fatalf("terminal event = %+v, want done", events[3])
	}
}
