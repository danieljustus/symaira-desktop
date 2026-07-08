package ai

import (
	"encoding/json"
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

func collectTransform(text, intent string) string {
	out := make(chan AskChunk)
	go Transform(text, intent, out)
	var b strings.Builder
	for c := range out {
		b.WriteString(c.Chunk)
	}
	return b.String()
}

func TestTransformWithoutOllamaDegradesHonestly(t *testing.T) {
	t.Setenv("SYMDESK_OLLAMA_URL", "")
	got := collectTransform("etwas Text", IntentRewrite)
	if !strings.Contains(got, "nicht konfiguriert") {
		t.Errorf("expected honest degradation, got: %q", got)
	}
}

func TestTransformEmptyTextIsRejected(t *testing.T) {
	t.Setenv("SYMDESK_OLLAMA_URL", "http://127.0.0.1:0") // must never be reached
	got := collectTransform("   ", IntentSummarize)
	if !strings.Contains(got, "Kein Text") {
		t.Errorf("expected empty-text notice, got: %q", got)
	}
}

func TestTransformStreamsFromOllama(t *testing.T) {
	var gotPrompt string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Prompt string `json:"prompt"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotPrompt = body.Prompt
		fmt.Fprintln(w, `{"response":"Kurz","done":false}`)
		fmt.Fprintln(w, `{"response":"fassung","done":true}`)
	}))
	defer srv.Close()

	t.Setenv("SYMDESK_OLLAMA_URL", srv.URL)
	got := collectTransform("ein langer Absatz", IntentSummarize)
	if got != "Kurzfassung" {
		t.Errorf("expected streamed transform, got: %q", got)
	}
	if !strings.Contains(gotPrompt, "ein langer Absatz") {
		t.Errorf("prompt should carry the source text, got: %q", gotPrompt)
	}
	if !strings.Contains(gotPrompt, "zusammen") {
		t.Errorf("summarize prompt should carry summarize instruction, got: %q", gotPrompt)
	}
}

func TestBuildTransformPromptVariesByIntent(t *testing.T) {
	text := "quelle"
	sum := buildTransformPrompt(text, IntentSummarize)
	cont := buildTransformPrompt(text, IntentContinue)
	rew := buildTransformPrompt(text, "unknown-defaults-to-rewrite")
	if !strings.Contains(sum, "zusammen") {
		t.Errorf("summarize prompt missing instruction: %s", sum)
	}
	if !strings.Contains(cont, "weiter") {
		t.Errorf("continue prompt missing instruction: %s", cont)
	}
	if !strings.Contains(rew, "um") {
		t.Errorf("rewrite fallback prompt missing instruction: %s", rew)
	}
	for _, p := range []string{sum, cont, rew} {
		if !strings.Contains(p, text) {
			t.Errorf("prompt missing source text: %s", p)
		}
	}
}
