package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Real-process contract for cancelling an in-flight provider call. The Go CLI
// installs no signal handler for `ask`/`mcp`: SIGTERM ends the process, which
// closes the provider connection while the model is still streaming. The same
// scripted provider also pins MCP's EOF rule: closing stdin does not cancel an
// in-flight tool call. The Rust replay is crates/symdesk-cli/tests/ask_cancel.rs.

const askCancelFixturePath = "../../testdata/port/ai/ask-cancel-process.json"

type askCancelFixture struct {
	SchemaVersion int               `json:"schema_version"`
	Files         map[string]string   `json:"files"`
	FileSets      map[string][]string `json:"file_sets"`
	FileTime      string            `json:"file_time"`
	Cases         []askCancelCase   `json:"cases"`
}

type askCancelCase struct {
	Name     string            `json:"name"`
	Kind     string            `json:"kind"` // sigterm | mcp_sigterm | mcp_eof
	Provider string            `json:"provider"`
	Args     []string          `json:"args,omitempty"`
	MCPArgs  string            `json:"mcp_arguments_json,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Turns    []askProviderTurn `json:"turns"`
	// WaitFor is text the CLI must have printed before it is signalled.
	WaitFor string `json:"wait_for,omitempty"`

	Signal                string               `json:"signal,omitempty"`
	ExitCode              int                  `json:"exit_code"`
	Stdout                string               `json:"stdout"`
	Stderr                string               `json:"stderr"`
	ProviderSawClientGone bool                 `json:"provider_saw_client_gone"`
	Requests              []askProviderRequest `json:"requests,omitempty"`
}

func ollamaGenerateStall(t *testing.T, first string) []askProviderTurn {
	t.Helper()
	return []askProviderTurn{{
		Body:  askJSONLine(t, map[string]any{"model": "llama3.2", "response": first, "done": false}) + "\n",
		Stall: true,
	}}
}

func askCancelCases(t *testing.T) []askCancelCase {
	t.Helper()
	return []askCancelCase{
		{Name: "ask_ollama_sigterm", Kind: "sigterm", Provider: "ollama", Args: []string{"--json", "ask", "quarterly"}, WaitFor: "first chunk", Turns: ollamaGenerateStall(t, "first chunk")},
		{Name: "ask_agent_ollama_sigterm", Kind: "sigterm", Provider: "ollama", Args: []string{"--json", "ask", "--agent", "quarterly"}, WaitFor: "thinking", Turns: []askProviderTurn{{
			Body:  askJSONLine(t, map[string]any{"message": map[string]any{"role": "assistant", "content": "thinking"}, "done": false}) + "\n",
			Stall: true,
		}}},
		{Name: "ask_agent_anthropic_sigterm", Kind: "sigterm", Provider: "anthropic", Args: []string{"--json", "ask", "--agent", "quarterly"}, Env: map[string]string{"SYMDESK_ANTHROPIC_URL": askProviderURLToken + "/v1/messages"}, WaitFor: "pondering", Turns: []askProviderTurn{{
			Body:  anthropicPartial(t, "pondering"),
			Stall: true,
		}}},
		{Name: "mcp_desk_ask_sigterm", Kind: "mcp_sigterm", Provider: "ollama", MCPArgs: `{"query":"quarterly"}`, Turns: ollamaGenerateStall(t, "first chunk")},
		{Name: "mcp_stdin_eof_lets_in_flight_ask_finish", Kind: "mcp_eof", Provider: "ollama", MCPArgs: `{"query":"quarterly"}`, Turns: []askProviderTurn{{
			Body:    ollamaGenerateTurn(t, "late ", "answer"),
			DelayMS: 400,
		}}},
	}
}

func anthropicPartial(t *testing.T, text string) string {
	t.Helper()
	lines := []string{
		"data: " + askJSONLine(t, map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_fake", "role": "assistant"}}),
		"",
		"data: " + askJSONLine(t, map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}}),
		"",
		"data: " + askJSONLine(t, map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": text}}),
		"",
	}
	return strings.Join(lines, "\n") + "\n"
}

// lockedBuffer is a bytes.Buffer safe for the copier goroutine plus polling.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func waitForText(buf *lockedBuffer, text string, limit time.Duration) bool {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), text) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return strings.Contains(buf.String(), text)
}

func TestPortAskCancelProcessContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM contract is Unix-only")
	}
	binary := processFixtureBinary(t)
	fixture := askCancelFixture{SchemaVersion: 1, Files: askAgentFiles(), FileSets: askFileSets(), FileTime: askAgentFileTime}
	for _, c := range askCancelCases(t) {
		fixture.Cases = append(fixture.Cases, observeAskCancel(t, binary, fixture, c))
	}
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source path unavailable")
	}
	path := filepath.Join(filepath.Dir(source), askCancelFixturePath)
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	current, err := os.ReadFile(path) //nolint:gosec // fixed repository fixture path
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, encoded) {
		portProcessFixtureDifference(t, "ask cancel fixture is stale; regenerate with make ask-cancel-fixtures-generate", current, encoded)
	}
}

func observeAskCancel(t *testing.T, binary string, fixture askCancelFixture, c askCancelCase) askCancelCase {
	t.Helper()
	s := newAskScratch(t, askSelectFiles(t, fixture.Files, fixture.FileSets, "single"), fixture.FileTime, "", false)
	server := newAskProviderServer(t, s.root, c.Turns)
	defer server.Close()
	s.index(t, binary, "")
	args := append([]string{"--vault", s.vault}, c.Args...)
	if c.Kind != "sigterm" {
		args = []string{"--vault", s.vault, "mcp"}
	}
	cmd := exec.Command(binary, args...) //nolint:gosec // test-built CLI with controlled arguments
	cmd.Dir, cmd.Env = s.root, s.environment("", providerEnv(c.Provider, server, c.Env))
	stdout, stderr := &lockedBuffer{}, &lockedBuffer{}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if c.Kind != "sigterm" {
		if _, err := stdin.Write([]byte(mcpToolCallLine(c.MCPArgs))); err != nil {
			t.Fatal(err)
		}
	}
	if c.Kind != "mcp_eof" {
		if !server.WaitStarted(20 * time.Second) {
			_ = cmd.Process.Kill()
			t.Fatalf("%s: the provider never received a request", c.Name)
		}
		if c.WaitFor != "" && !waitForText(stdout, c.WaitFor, 20*time.Second) {
			_ = cmd.Process.Kill()
			t.Fatalf("%s: CLI never printed %q; stdout=%q", c.Name, c.WaitFor, stdout.String())
		}
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
	} else if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("%s: %v", c.Name, err)
		}
		c.ExitCode = exit.ExitCode()
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			c.Signal = status.Signal().String()
		}
	}
	if c.Kind != "mcp_eof" {
		c.ProviderSawClientGone = server.Disconnected(10 * time.Second)
		if !c.ProviderSawClientGone {
			t.Fatalf("%s: the provider did not observe the client disconnect", c.Name)
		}
	}
	c.Requests = server.Requests()
	url := server.URL()
	c.Stdout = normalizeProviderText(stdout.String(), s.root, url)
	c.Stderr = normalizeProviderText(filterSlogInfo(stderr.String()), s.root, url)
	return c
}
