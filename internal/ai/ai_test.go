package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

func TestAskStreamsFromAnthropicProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, `data: {"type": "content_block_delta", "delta": {"text": "Antwort"}}`)
		fmt.Fprintln(w, `data: [DONE]`)
	}))
	defer srv.Close()

	t.Setenv("SYMDESK_LLM_PROVIDER", "anthropic")
	t.Setenv("SYMDESK_LLM_API_KEY", "test-key")
	t.Setenv("SYMDESK_ANTHROPIC_URL", srv.URL)

	got := collect("frage?", nil)
	if got != "Antwort" {
		t.Errorf("expected the anthropic provider to be used, got: %q", got)
	}
}

func TestAskAnthropicProviderWithoutKeyDegradesHonestly(t *testing.T) {
	t.Setenv("SYMDESK_LLM_PROVIDER", "anthropic")
	t.Setenv("SYMDESK_LLM_API_KEY", "")

	got := collect("frage?", nil)
	if !strings.Contains(got, "nicht konfiguriert") {
		t.Errorf("expected honest degradation without an API key, got: %q", got)
	}
	if !strings.Contains(got, "Anthropic") {
		t.Errorf("expected the message to name the anthropic provider, got: %q", got)
	}
}

func TestAskAnthropicProviderSurfacesRequestError(t *testing.T) {
	t.Setenv("SYMDESK_LLM_PROVIDER", "anthropic")
	t.Setenv("SYMDESK_LLM_API_KEY", "test-key")
	t.Setenv("SYMDESK_ANTHROPIC_URL", "http://127.0.0.1:0")

	got := collect("frage?", nil)
	if !strings.Contains(got, "fehlgeschlagen") {
		t.Errorf("expected a surfaced anthropic request error, got: %q", got)
	}
}

func TestTransformStreamsFromAnthropicProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, `data: {"type": "content_block_delta", "delta": {"text": "Kurz"}}`)
		fmt.Fprintln(w, `data: [DONE]`)
	}))
	defer srv.Close()

	t.Setenv("SYMDESK_LLM_PROVIDER", "anthropic")
	t.Setenv("SYMDESK_LLM_API_KEY", "test-key")
	t.Setenv("SYMDESK_ANTHROPIC_URL", srv.URL)

	got := collectTransform("ein langer Absatz", IntentSummarize)
	if got != "Kurz" {
		t.Errorf("expected the anthropic provider to be used, got: %q", got)
	}
}

func TestTransformAnthropicProviderWithoutKeyDegradesHonestly(t *testing.T) {
	t.Setenv("SYMDESK_LLM_PROVIDER", "anthropic")
	t.Setenv("SYMDESK_LLM_API_KEY", "")

	got := collectTransform("etwas Text", IntentRewrite)
	if !strings.Contains(got, "nicht konfiguriert") {
		t.Errorf("expected honest degradation without an API key, got: %q", got)
	}
}

func TestStreamAnthropicSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("expected API key 'test-key', got %q", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("expected version header '2023-06-01', got %q", r.Header.Get("anthropic-version"))
		}
		fmt.Fprintln(w, `data: {"type": "message_start"}`)
		fmt.Fprintln(w, `data: {"type": "content_block_delta", "delta": {"text": "Hello"}}`)
		fmt.Fprintln(w, `data: {"type": "content_block_delta", "delta": {"invalid_delta_field": true}}`)
		fmt.Fprintln(w, `data: {"type": "content_block_delta", "delta": {"text": "!"}}`)
		fmt.Fprintln(w, `not data line`)
		fmt.Fprintln(w, `data: [DONE]`)
	}))
	defer srv.Close()

	t.Setenv("SYMDESK_ANTHROPIC_URL", srv.URL)

	out := make(chan AskChunk, 10)
	err := streamAnthropic(context.Background(), "test-key", "model", "prompt", out)
	close(out)

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	var chunks []string
	for c := range out {
		chunks = append(chunks, c.Chunk)
	}

	got := strings.Join(chunks, "")
	if got != "Hello!" {
		t.Errorf("expected 'Hello!', got %q", got)
	}
}

func TestStreamAnthropicHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error": {"type": "invalid_request_error", "message": "bad api key"}}`))
	}))
	defer srv.Close()

	t.Setenv("SYMDESK_ANTHROPIC_URL", srv.URL)

	out := make(chan AskChunk, 10)
	err := streamAnthropic(context.Background(), "test-key", "model", "prompt", out)
	close(out)

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP 400") || !strings.Contains(err.Error(), "bad api key") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestStreamAnthropicMalformedEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `data: {invalid json`)
		fmt.Fprintln(w, `data: {"type": "content_block_delta", "delta": {"text": "recovered"}}`)
		fmt.Fprintln(w, `data: [DONE]`)
	}))
	defer srv.Close()

	t.Setenv("SYMDESK_ANTHROPIC_URL", srv.URL)

	out := make(chan AskChunk, 10)
	err := streamAnthropic(context.Background(), "test-key", "model", "prompt", out)
	close(out)

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	var chunks []string
	for c := range out {
		chunks = append(chunks, c.Chunk)
	}

	got := strings.Join(chunks, "")
	if got != "recovered" {
		t.Errorf("expected 'recovered', got %q", got)
	}
}

func TestStreamAnthropicCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, `data: {"type": "content_block_delta", "delta": {"text": "first"}}`)
		w.(http.Flusher).Flush()

		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer srv.Close()

	t.Setenv("SYMDESK_ANTHROPIC_URL", srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan AskChunk, 10)

	go func() {
		<-out
		cancel()
	}()

	err := streamAnthropic(ctx, "test-key", "model", "prompt", out)
	close(out)

	if err == nil {
		t.Fatal("expected cancellation error, got nil")
	}
	if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("expected context canceled error, got: %v", err)
	}
}

func TestStreamAnthropicNetworkError(t *testing.T) {
	t.Setenv("SYMDESK_ANTHROPIC_URL", "http://127.0.0.1:0")

	out := make(chan AskChunk, 10)
	err := streamAnthropic(context.Background(), "test-key", "model", "prompt", out)
	close(out)

	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
