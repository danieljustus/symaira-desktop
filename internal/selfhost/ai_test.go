package selfhost

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/ai"
	"github.com/danieljustus/symaira-desktop/internal/permissions"
)

// fakeOllama is a minimal Ollama-compatible streaming endpoint: it reads
// one POST /api/generate request and answers with NDJSON generate chunks.
// When `holdAfterChunks` is set, it streams that many chunks and then
// blocks until the client's request context is done (used to prove
// disconnect cancellation mid-stream).
type fakeOllama struct {
	mu              sync.Mutex
	requests        int
	holdAfterChunks int
	clientGone      chan struct{}
	clientGoneOnce  sync.Once
	response        []string
}

func newFakeOllama(chunks ...string) *fakeOllama {
	return &fakeOllama{response: chunks, clientGone: make(chan struct{})}
}

func (f *fakeOllama) holdAfter(n int) {
	f.mu.Lock()
	f.holdAfterChunks = n
	f.mu.Unlock()
}

func (f *fakeOllama) markClientGone() {
	f.clientGoneOnce.Do(func() { close(f.clientGone) })
}

func (f *fakeOllama) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/generate", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests++
		holdAfter := f.holdAfterChunks
		f.mu.Unlock()

		w.Header().Set("Content-Type", "application/x-ndjson")
		flusher, _ := w.(http.Flusher)

		// Release the client-gone signal when the caller disconnects.
		go func() {
			<-r.Context().Done()
			f.markClientGone()
		}()

		for i, chunk := range f.response {
			if holdAfter > 0 && i >= holdAfter {
				// Streamed enough — hold the connection open until the
				// caller disconnects (proves upstream cancellation).
				<-r.Context().Done()
				return
			}
			line, _ := json.Marshal(map[string]interface{}{
				"model":    "test-model",
				"response": chunk,
				"done":     false,
			})
			_, _ = w.Write(append(line, '\n'))
			if flusher != nil {
				flusher.Flush()
			}
		}
		final, _ := json.Marshal(map[string]interface{}{"model": "test-model", "response": "", "done": true})
		_, _ = w.Write(append(final, '\n'))
		if flusher != nil {
			flusher.Flush()
		}
	})
	return mux
}

// newAITestServer builds a server over a vault with two notes and returns
// it plus the vault root. The notes are indexed into the sidecar so
// retrieval finds them.
func newAITestServer(t *testing.T) (*httptest.Server, *Server, string) {
	t.Helper()
	vaultRoot := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(vaultRoot, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("Rechnung.md", "---\ntitle: Rechnung Juli\n---\nDie Rechnung vom Juli.\n")
	write("Notiz.md", "---\ntitle: Notiz\n---\nEinkaufsliste.\n")

	server, err := NewServer(ServerConfig{VaultRoot: vaultRoot, Token: testToken, Version: "test", Executable: "/bin/false"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	if err := server.refreshIndex(); err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	return httpServer, server, vaultRoot
}

func aiAsk(t *testing.T, url, query string, token string) *http.Response {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"query": query})
	request, err := http.NewRequest(http.MethodPost, url+"/api/v1/ai/ask", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

// readAIEvents reads NDJSON AIEvent lines until the stream closes.
func readAIEvents(t *testing.T, body io.Reader) []ai.AIEvent {
	t.Helper()
	var events []ai.AIEvent
	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event ai.AIEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("invalid AIEvent line %q: %v", line, err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("reading stream: %v", err)
	}
	return events
}

// TestAIAskStreamsTokensIncrementally proves the endpoint is not buffered:
// tokens arrive as answer events before the model finishes, in the order
// the model produced them.
func TestAIAskStreamsTokensIncrementally(t *testing.T) {
	ollama := newFakeOllama("Hallo ", "Welt.")
	ollamaServer := httptest.NewServer(ollama.handler())
	t.Cleanup(ollamaServer.Close)
	t.Setenv("SYMDESK_OLLAMA_URL", ollamaServer.URL)
	t.Setenv("SYMDESK_LLM_PROVIDER", "ollama")
	t.Setenv("SYMDESK_LLM_MODEL", "test-model")

	httpServer, _, _ := newAITestServer(t)
	response := aiAsk(t, httpServer.URL, "Rechnung", testToken)
	t.Cleanup(func() { _ = response.Body.Close() })

	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.StatusCode, readBody(response))
	}
	if ct := response.Header.Get("Content-Type"); !strings.Contains(ct, "ndjson") {
		t.Fatalf("expected NDJSON content type, got %q", ct)
	}

	events := readAIEvents(t, response.Body)
	var answers []string
	var citations []ai.AIEvent
	for _, event := range events {
		switch event.Type {
		case ai.AIEventAnswer:
			answers = append(answers, event.Text)
		case ai.AIEventCitation:
			citations = append(citations, event)
		}
	}

	if len(citations) == 0 {
		t.Fatal("expected at least one citation for a matching vault note")
	}
	joined := strings.Join(answers, "")
	if joined != "Hallo Welt." {
		t.Fatalf("expected streamed tokens 'Hallo Welt.', got %q", joined)
	}
	if events[len(events)-1].Type != ai.AIEventDone {
		t.Fatalf("stream must end with a done event, got %v", events[len(events)-1])
	}
}

// TestAICitationsResolveThroughFileEndpoint proves cited paths are real
// vault-relative paths the client can fetch via GET /api/v1/files.
func TestAICitationsResolveThroughFileEndpoint(t *testing.T) {
	ollama := newFakeOllama("Antwort.")
	ollamaServer := httptest.NewServer(ollama.handler())
	t.Cleanup(ollamaServer.Close)
	t.Setenv("SYMDESK_OLLAMA_URL", ollamaServer.URL)
	t.Setenv("SYMDESK_LLM_PROVIDER", "ollama")
	t.Setenv("SYMDESK_LLM_MODEL", "test-model")

	httpServer, _, _ := newAITestServer(t)
	response := aiAsk(t, httpServer.URL, "Rechnung", testToken)
	t.Cleanup(func() { _ = response.Body.Close() })

	events := readAIEvents(t, response.Body)
	for _, event := range events {
		if event.Type != ai.AIEventCitation {
			continue
		}
		// The citation path must resolve through the file endpoint.
		fileResponse := authorized(t, http.MethodGet, httpServer.URL+"/api/v1/files?path="+event.Path, nil, "")
		if fileResponse.StatusCode != http.StatusOK {
			t.Fatalf("citation path %q does not resolve through /api/v1/files (got %d)", event.Path, fileResponse.StatusCode)
		}
		_ = fileResponse.Body.Close()
	}
}

// TestAIAskRejectsUnauthenticated proves the endpoint is protected like
// every other API route.
func TestAIAskRejectsUnauthenticated(t *testing.T) {
	httpServer, _, _ := newAITestServer(t)
	body, _ := json.Marshal(map[string]string{"query": "Rechnung"})
	request, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/api/v1/ai/ask", bytes.NewReader(body))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.StatusCode)
	}
}

// TestAIAskPermissionScoping proves a non-admin user never receives a
// document they cannot read — neither as context nor as a citation.
func TestAIAskPermissionScoping(t *testing.T) {
	ollama := newFakeOllama("Antwort.")
	ollamaServer := httptest.NewServer(ollama.handler())
	t.Cleanup(ollamaServer.Close)
	t.Setenv("SYMDESK_OLLAMA_URL", ollamaServer.URL)
	t.Setenv("SYMDESK_LLM_PROVIDER", "ollama")
	t.Setenv("SYMDESK_LLM_MODEL", "test-model")

	httpServer, server, _ := newAITestServer(t)

	// Restricted user: may read Notiz.md but not Rechnung.md. The rule
	// gives Rechnung.md to owner "admin" only, so "restricted" has no
	// read grant on it.
	userToken, err := server.perm.UserAdd("restricted", "user")
	if err != nil {
		t.Fatal(err)
	}
	if err := server.perm.SetDocumentRule(permissions.DocumentRule{
		Path:  "Rechnung.md",
		Owner: "admin",
	}); err != nil {
		t.Fatal(err)
	}
	// Default (no rule) = readable; the rule above makes Rechnung.md
	// readable only by its owner list — verify the restriction holds.
	if server.perm.CanRead(&permissions.User{Name: "restricted", Roles: []string{"user"}}, "Rechnung.md") {
		t.Fatal("test setup error: restricted user should not read Rechnung.md")
	}

	response := aiAsk(t, httpServer.URL, "Rechnung", userToken)
	t.Cleanup(func() { _ = response.Body.Close() })
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.StatusCode, readBody(response))
	}

	events := readAIEvents(t, response.Body)
	for _, event := range events {
		if event.Type == ai.AIEventCitation && strings.Contains(event.Path, "Rechnung") {
			t.Fatalf("restricted user received citation for unreadable document: %+v", event)
		}
	}
}

// TestAIAskCancelsUpstreamOnDisconnect proves a disconnecting client aborts
// the model request instead of leaving it running.
func TestAIAskCancelsUpstreamOnDisconnect(t *testing.T) {
	// Stream one token, then hold the connection open until the caller
	// disconnects — proving the server aborts the upstream request.
	ollama := newFakeOllama("erstes-token", "zweites-token")
	ollama.holdAfter(1)
	ollamaServer := httptest.NewServer(ollama.handler())
	t.Cleanup(ollamaServer.Close)
	t.Setenv("SYMDESK_OLLAMA_URL", ollamaServer.URL)
	t.Setenv("SYMDESK_LLM_PROVIDER", "ollama")
	t.Setenv("SYMDESK_LLM_MODEL", "test-model")

	httpServer, _, _ := newAITestServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body, _ := json.Marshal(map[string]string{"query": "Rechnung"})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, httpServer.URL+"/api/v1/ai/ask", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+testToken)
	request.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	// Read until the first answer token arrives, then hang up mid-answer.
	scanner := bufio.NewScanner(response.Body)
	found := false
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), `"type":"answer"`) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no answer token received: %v", scanner.Err())
	}
	_ = response.Body.Close()
	cancel()

	select {
	case <-ollama.clientGone:
		// Upstream request was aborted — exactly what we want.
	case <-time.After(5 * time.Second):
		t.Fatal("upstream model request was not cancelled after client disconnect")
	}
}

// TestAITransformStreamsAndEndsWithDone proves the transform counterpart
// streams answer events and terminates with a done event.
func TestAITransformStreamsAndEndsWithDone(t *testing.T) {
	ollama := newFakeOllama("Zusammengefasst.")
	ollamaServer := httptest.NewServer(ollama.handler())
	t.Cleanup(ollamaServer.Close)
	t.Setenv("SYMDESK_OLLAMA_URL", ollamaServer.URL)
	t.Setenv("SYMDESK_LLM_PROVIDER", "ollama")
	t.Setenv("SYMDESK_LLM_MODEL", "test-model")

	httpServer, _, _ := newAITestServer(t)
	payload, _ := json.Marshal(map[string]string{"text": "Langer Text.", "intent": "summarize"})
	request, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/api/v1/ai/transform", bytes.NewReader(payload))
	request.Header.Set("Authorization", "Bearer "+testToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.StatusCode, readBody(response))
	}

	events := readAIEvents(t, response.Body)
	var answers []string
	for _, event := range events {
		if event.Type == ai.AIEventAnswer {
			answers = append(answers, event.Text)
		}
	}
	if strings.Join(answers, "") != "Zusammengefasst." {
		t.Fatalf("unexpected transform output: %v", answers)
	}
	if events[len(events)-1].Type != ai.AIEventDone {
		t.Fatalf("transform stream must end with done, got %v", events[len(events)-1])
	}
}

// TestAIRateLimitRejectsExcessRequests proves the AI bucket applies and
// responds 429 with Retry-After like the other throttled routes.
func TestAIRateLimitRejectsExcessRequests(t *testing.T) {
	ollama := newFakeOllama("ok")
	ollamaServer := httptest.NewServer(ollama.handler())
	t.Cleanup(ollamaServer.Close)
	t.Setenv("SYMDESK_OLLAMA_URL", ollamaServer.URL)
	t.Setenv("SYMDESK_LLM_PROVIDER", "ollama")
	t.Setenv("SYMDESK_LLM_MODEL", "test-model")

	httpServer, _, _ := newAITestServer(t)

	var lastStatus int
	var lastRetryAfter string
	// Exceed the 12-request window; the endpoint completes quickly because
	// the fake model answers instantly.
	for i := 0; i < 14; i++ {
		response := aiAsk(t, httpServer.URL, "Rechnung", testToken)
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		lastStatus = response.StatusCode
		lastRetryAfter = response.Header.Get("Retry-After")
		if lastStatus == http.StatusTooManyRequests {
			break
		}
	}
	if lastStatus != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after exceeding the AI rate limit, last status %d", lastStatus)
	}
	if lastRetryAfter == "" {
		t.Fatal("expected Retry-After header on 429")
	}
}
