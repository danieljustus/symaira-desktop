package ai

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func collect(query string, docs []map[string]interface{}) string {
	out := make(chan AskChunk)
	go Ask(query, docs, out)
	var b strings.Builder
	for c := range out {
		b.WriteString(c.Chunk)
	}
	return b.String()
}

func TestAskWithoutOllamaFallsBack(t *testing.T) {
	t.Setenv("SYMDESK_OLLAMA_URL", "")

	docs := []map[string]interface{}{
		{"path": "notes/a.md", "title": "A", "snippet": "alpha"},
		{"path": "notes/b.md", "title": "B", "snippet": "beta"},
	}
	got := collect("what is alpha?", docs)

	if !strings.Contains(got, "nicht konfiguriert") {
		t.Errorf("expected honest fallback, got: %s", got)
	}
	if !strings.Contains(got, "[[notes/a.md]]") {
		t.Errorf("expected search results in fallback, got: %s", got)
	}
}

func TestAskStreamsFromOllama(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		fmt.Fprintln(w, `{"response":"Hallo ","done":false}`)
		fmt.Fprintln(w, `{"response":"Welt","done":true}`)
	}))
	defer srv.Close()

	t.Setenv("SYMDESK_OLLAMA_URL", srv.URL)
	got := collect("gruß?", nil)
	if got != "Hallo Welt" {
		t.Errorf("expected streamed answer, got: %q", got)
	}
}

func TestAskReportsOllamaError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintln(w, `{"error":"model not found"}`)
	}))
	defer srv.Close()

	t.Setenv("SYMDESK_OLLAMA_URL", srv.URL)
	got := collect("q", nil)
	if !strings.Contains(got, "model not found") {
		t.Errorf("expected surfaced ollama error, got: %q", got)
	}
}

func TestBuildPromptGroundsInDocs(t *testing.T) {
	docs := []map[string]interface{}{{"path": "x.md", "title": "X", "snippet": "inhalt"}}
	p := buildPrompt("frage?", docs)
	if !strings.Contains(p, "[[x.md]]") || !strings.Contains(p, "inhalt") || !strings.Contains(p, "frage?") {
		t.Errorf("prompt missing pieces: %s", p)
	}
}
