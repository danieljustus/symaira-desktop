package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// Shared helpers for the RUST-012 configured-provider process contracts
// (ask --agent, ask --notebook, MCP desk_ask, cancellation). Providers are
// loopback fakes served from the test process; nothing contacts a real or paid
// API and no operator credential is read: the API key is a generated canary.

// askProviderKey is the generated fake API key handed to the CLI through
// SYMDESK_LLM_API_KEY. It never leaves the loopback fake.
const askProviderKey = "fake-key-0123456789abcdef"

const (
	askProviderURLToken = "{{URL}}"
	askHandleToken      = "{{HANDLE}}"
)

// askProviderTurn is one scripted provider response.
type askProviderTurn struct {
	Status int    `json:"status"`
	Body   string `json:"body"`
	// Stall keeps the connection open after Body (for cancellation cases).
	Stall bool `json:"stall,omitempty"`
	// DelayMS holds the response back before anything is written.
	DelayMS int `json:"delay_ms,omitempty"`
}

// askProviderRequest is one request the fake provider observed. Chat bodies
// are kept as normalized JSON; any other path (for example an embedding probe)
// records only method and path.
type askProviderRequest struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    json.RawMessage   `json:"body,omitempty"`
}

type askProviderServer struct {
	t        *testing.T
	server   *httptest.Server
	turns    []askProviderTurn
	mu       sync.Mutex
	requests []askProviderRequest
	rawLast  string
	served   int
	// disconnected is closed when a stalled connection observed the client leave.
	disconnected chan struct{}
	started      chan struct{}
	startOnce    sync.Once
	closeOnce    sync.Once
	root         string
}

var askHandleRE = regexp.MustCompile(`externalized to (task-[0-9]+/[A-Za-z_]+-[0-9]{3}\.txt)`)

func newAskProviderServer(t *testing.T, root string, turns []askProviderTurn) *askProviderServer {
	t.Helper()
	p := &askProviderServer{t: t, turns: turns, root: root, disconnected: make(chan struct{}), started: make(chan struct{})}
	p.server = httptest.NewServer(http.HandlerFunc(p.handle))
	return p
}

func (p *askProviderServer) URL() string { return p.server.URL }

func (p *askProviderServer) Close() { p.server.Close() }

// Disconnected reports whether a stalled connection saw the client leave.
func (p *askProviderServer) Disconnected(wait time.Duration) bool {
	select {
	case <-p.disconnected:
		return true
	case <-time.After(wait):
		return false
	}
}

// WaitStarted blocks until a turn began streaming to the client.
func (p *askProviderServer) WaitStarted(wait time.Duration) bool {
	select {
	case <-p.started:
		return true
	case <-time.After(wait):
		return false
	}
}

func (p *askProviderServer) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	record := askProviderRequest{Method: r.Method, Path: r.URL.Path}
	chat := r.URL.Path == "/api/chat" || r.URL.Path == "/api/generate" || r.URL.Path == "/v1/messages"
	if chat {
		record.Headers = map[string]string{}
		if v := r.Header.Get("X-Api-Key"); v != "" {
			if v == askProviderKey {
				v = "<KEY>"
			}
			record.Headers["x-api-key"] = v
		}
		if v := r.Header.Get("Anthropic-Version"); v != "" {
			record.Headers["anthropic-version"] = v
		}
		if v := r.Header.Get("Content-Type"); v != "" {
			record.Headers["content-type"] = v
		}
		record.Body = json.RawMessage(normalizeProviderText(string(body), p.root, p.server.URL))
	}
	p.mu.Lock()
	index := p.served
	if chat {
		p.served++
	}
	p.requests = append(p.requests, record)
	if chat {
		p.rawLast = string(body)
	}
	last := p.rawLast
	p.mu.Unlock()
	if !chat {
		http.NotFound(w, r)
		return
	}
	if index >= len(p.turns) {
		http.Error(w, `{"error":"no scripted turn"}`, http.StatusInternalServerError)
		return
	}
	turn := p.turns[index]
	reply := turn.Body
	if strings.Contains(reply, askHandleToken) {
		if m := askHandleRE.FindStringSubmatch(last); m != nil {
			reply = strings.ReplaceAll(reply, askHandleToken, m[1])
		}
	}
	if turn.DelayMS > 0 {
		time.Sleep(time.Duration(turn.DelayMS) * time.Millisecond)
	}
	status := turn.Status
	if status == 0 {
		status = http.StatusOK
	}
	if strings.HasPrefix(r.URL.Path, "/v1/") && status == http.StatusOK {
		w.Header().Set("Content-Type", "text/event-stream")
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, reply)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	p.startOnce.Do(func() { close(p.started) })
	if turn.Stall {
		select {
		case <-r.Context().Done():
			p.closeOnce.Do(func() { close(p.disconnected) })
		case <-time.After(30 * time.Second):
		}
	}
}

func (p *askProviderServer) Requests() []askProviderRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]askProviderRequest, len(p.requests))
	copy(out, p.requests)
	return out
}

var (
	askTaskRE     = regexp.MustCompile(`task-[0-9]+`)
	askScoreRawRE = regexp.MustCompile(`(\\?"score\\?":)-?[0-9][0-9.eE+-]*`)
)

// normalizeProviderText removes everything that varies per run or per host:
// scratch root, loopback port, task ids and retrieval scores (hybrid ranking
// arithmetic differs between Darwin and Linux and is pinned by the retrieval
// differentials, not here).
func normalizeProviderText(text, root, url string) string {
	text = strings.ReplaceAll(text, root, "<ROOT>")
	if url != "" {
		text = strings.ReplaceAll(text, url, "<URL>")
	}
	text = askTaskRE.ReplaceAllString(text, "task-<ID>")
	text = askScoreRawRE.ReplaceAllString(text, "${1}<SCORE>")
	return maskAskScores(text)
}

// askResultFile is one externalized tool result left in the results area.
type askResultFile struct {
	DigestMatchesVault bool   `json:"digest_matches_vault"`
	Name               string `json:"name"`
	Size               int    `json:"size"`
	SHA256             string `json:"sha256"`
}

// observeAskResults lists the files below <data>/symdesk/agent-results.
func observeAskResults(t *testing.T, dataHome, vault string) []askResultFile {
	t.Helper()
	canonical, err := filepath.EvalSymlinks(vault)
	if err != nil {
		canonical = vault
	}
	want := fmt.Sprintf("%x", sha256.Sum256([]byte(filepath.Clean(canonical))))[:16]
	base := filepath.Join(dataHome, "symdesk", "agent-results")
	var out []askResultFile
	digests, _ := os.ReadDir(base)
	for _, digest := range digests {
		tasks, _ := os.ReadDir(filepath.Join(base, digest.Name()))
		for _, task := range tasks {
			files, _ := os.ReadDir(filepath.Join(base, digest.Name(), task.Name()))
			for _, file := range files {
				data, err := os.ReadFile(filepath.Join(base, digest.Name(), task.Name(), file.Name())) //nolint:gosec // test-owned scratch path
				if err != nil {
					t.Fatal(err)
				}
				out = append(out, askResultFile{
					DigestMatchesVault: digest.Name() == want,
					Name:               file.Name(),
					Size:               len(data),
					SHA256:             fmt.Sprintf("%x", sha256.Sum256(data)),
				})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// askJSONLine compacts one JSON document the way a fixture stores it.
func askJSONLine(t *testing.T, v any) string {
	t.Helper()
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// ollamaChatTurn renders one Ollama /api/chat NDJSON turn: optional streamed
// content chunks, optional tool calls (raw argument JSON, preserved verbatim),
// and the closing done line with usage counters.
func ollamaChatTurn(t *testing.T, content []string, calls [][2]string, promptEval, evalCount int) string {
	t.Helper()
	var lines []string
	for _, c := range content {
		lines = append(lines, askJSONLine(t, map[string]any{"message": map[string]any{"role": "assistant", "content": c}, "done": false}))
	}
	if len(calls) > 0 {
		var parts []string
		for _, call := range calls {
			args := call[1]
			if args == "" {
				parts = append(parts, fmt.Sprintf(`{"function":{"name":%q}}`, call[0]))
			} else {
				parts = append(parts, fmt.Sprintf(`{"function":{"name":%q,"arguments":%s}}`, call[0], args))
			}
		}
		lines = append(lines, `{"message":{"role":"assistant","content":"","tool_calls":[`+strings.Join(parts, ",")+`]},"done":false}`)
	}
	done := map[string]any{"message": map[string]any{"role": "assistant", "content": ""}, "done": true}
	if promptEval > 0 {
		done["prompt_eval_count"] = promptEval
	}
	if evalCount > 0 {
		done["eval_count"] = evalCount
	}
	lines = append(lines, askJSONLine(t, done))
	return strings.Join(lines, "\n") + "\n"
}

// anthropicTurn renders one Anthropic Messages SSE turn. Each block is either
// text (a list of text deltas) or a tool_use with its input_json_delta
// fragments. usage is reported where the Go parser reads it (event level).
type anthropicBlock struct {
	Text      []string
	ToolID    string
	ToolName  string
	Fragments []string
	Tool      bool
}

func anthropicTurn(t *testing.T, blocks []anthropicBlock, stop string, inputTokens, outputTokens int) string {
	t.Helper()
	var lines []string
	add := func(v any) { lines = append(lines, "data: "+askJSONLine(t, v), "") }
	start := map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_fake", "role": "assistant"}}
	if inputTokens > 0 {
		start["usage"] = map[string]any{"input_tokens": inputTokens}
	}
	add(start)
	for i, b := range blocks {
		if b.Tool {
			add(map[string]any{"type": "content_block_start", "index": i, "content_block": map[string]any{"type": "tool_use", "id": b.ToolID, "name": b.ToolName}})
			for _, f := range b.Fragments {
				add(map[string]any{"type": "content_block_delta", "index": i, "delta": map[string]any{"type": "input_json_delta", "partial_json": f}})
			}
		} else {
			add(map[string]any{"type": "content_block_start", "index": i, "content_block": map[string]any{"type": "text", "text": ""}})
			for _, text := range b.Text {
				add(map[string]any{"type": "content_block_delta", "index": i, "delta": map[string]any{"type": "text_delta", "text": text}})
			}
		}
		add(map[string]any{"type": "content_block_stop", "index": i})
	}
	delta := map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stop}}
	if outputTokens > 0 {
		delta["usage"] = map[string]any{"output_tokens": outputTokens}
	}
	add(delta)
	add(map[string]any{"type": "message_stop"})
	return strings.Join(lines, "\n") + "\n"
}
