package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Real-process contract for MCP `desk_ask` with a configured provider (Hermes
// backend double, loopback Ollama /api/generate, loopback Anthropic Messages)
// and its notebook scope. The offline path is pinned by
// testdata/port/mcp/ask-offline.json; the Rust replay is
// crates/symdesk-cli/tests/mcp_ask_provider.rs.

const askProviderMCPFixturePath = "../../testdata/port/ai/ask-provider-mcp.json"

type askProviderMCPFixture struct {
	SchemaVersion int                  `json:"schema_version"`
	HermesScript  string               `json:"hermes_script"`
	Files         map[string]string    `json:"files"`
	FileSets      map[string][]string  `json:"file_sets"`
	FileTime      string               `json:"file_time"`
	Cases         []askProviderMCPCase `json:"cases"`
}

type askProviderMCPCase struct {
	Name          string               `json:"name"`
	FileSet       string               `json:"file_set"`
	Provider      string               `json:"provider"`
	Mode          string               `json:"mode,omitempty"`
	Env           map[string]string    `json:"env,omitempty"`
	Turns         []askProviderTurn    `json:"turns,omitempty"`
	ArgumentsJSON string               `json:"arguments_json"`
	ExitCode      int                  `json:"exit_code"`
	Stdout        string               `json:"stdout"`
	Stderr        string               `json:"stderr"`
	Requests      []askProviderRequest `json:"requests,omitempty"`
	Log           string               `json:"log,omitempty"`
}

func ollamaGenerateTurn(t *testing.T, chunks ...string) string {
	t.Helper()
	var lines []string
	for _, c := range chunks {
		lines = append(lines, askJSONLine(t, map[string]any{"model": "llama3.2", "response": c, "done": false}))
	}
	lines = append(lines, askJSONLine(t, map[string]any{"model": "llama3.2", "response": "", "done": true}))
	return strings.Join(lines, "\n") + "\n"
}

func askProviderMCPCases(t *testing.T) []askProviderMCPCase {
	t.Helper()
	generate := func(chunks ...string) []askProviderTurn {
		return []askProviderTurn{{Body: ollamaGenerateTurn(t, chunks...)}}
	}
	anthropic := func(blocks []anthropicBlock, stop string) []askProviderTurn {
		return []askProviderTurn{{Body: anthropicTurn(t, blocks, stop, 4, 6)}}
	}
	return []askProviderMCPCase{
		{Name: "hermes_ok", FileSet: "single", Provider: "hermes", Mode: "ok", ArgumentsJSON: `{"query":"quarterly"}`},
		{Name: "hermes_ok_notebook", FileSet: "core", Provider: "hermes", Mode: "ok", ArgumentsJSON: `{"query":"quarterly","notebook":"nb"}`},
		{Name: "hermes_fails", FileSet: "single", Provider: "hermes", Mode: "fail", ArgumentsJSON: `{"query":"quarterly"}`},
		{Name: "hermes_notebook_missing", FileSet: "core", Provider: "hermes", Mode: "ok", ArgumentsJSON: `{"query":"quarterly","notebook":"nope"}`},
		{Name: "ollama_generate", FileSet: "single", Provider: "ollama", ArgumentsJSON: `{"query":"quarterly"}`, Turns: generate("Budget ", "answer ", "[[alpha.md]].")},
		{Name: "ollama_generate_notebook_german", FileSet: "core", Provider: "ollama", ArgumentsJSON: `{"query":"quarterly","notebook":"nb"}`, Env: map[string]string{"SYMDESK_LANG": "German", "SYMDESK_OLLAMA_MODEL": "fake-model"}, Turns: generate("Antwort.")},
		{Name: "ollama_http_500", FileSet: "single", Provider: "ollama", ArgumentsJSON: `{"query":"quarterly"}`, Turns: []askProviderTurn{{Status: 500, Body: `{"error":"boom"}`}}},
		{Name: "anthropic_stream", FileSet: "single", Provider: "anthropic", ArgumentsJSON: `{"query":"quarterly"}`, Env: map[string]string{"SYMDESK_LLM_MODEL": "claude-haiku-fake", "SYMDESK_MAX_TOKENS": "321"}, Turns: anthropic([]anthropicBlock{{Text: []string{"Anthropic ", "says hi."}}}, "end_turn")},
		{Name: "anthropic_url_is_a_base_not_an_endpoint", FileSet: "single", Provider: "anthropic", ArgumentsJSON: `{"query":"quarterly"}`, Env: map[string]string{"SYMDESK_ANTHROPIC_URL": askProviderURLToken + "/v1/messages"}, Turns: anthropic([]anthropicBlock{{Text: []string{"unreachable"}}}, "end_turn")},
		{Name: "anthropic_max_tokens_marker", FileSet: "single", Provider: "anthropic", ArgumentsJSON: `{"query":"quarterly"}`, Turns: anthropic([]anthropicBlock{{Text: []string{"Cut off"}}}, "max_tokens")},
		{Name: "ollama_unconfigured_default", FileSet: "single", Provider: "none", ArgumentsJSON: `{"query":"quarterly"}`},
		{Name: "empty_query_still_rejected", FileSet: "single", Provider: "hermes", Mode: "ok", ArgumentsJSON: `{"query":""}`},
	}
}

func TestPortAskProviderMCPContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell double is Unix-only")
	}
	binary := processFixtureBinary(t)
	fixture := askProviderMCPFixture{SchemaVersion: 1, HermesScript: askAgentHermesScript, Files: askAgentFiles(), FileSets: askFileSets(), FileTime: askAgentFileTime}
	for _, c := range askProviderMCPCases(t) {
		fixture.Cases = append(fixture.Cases, observeAskProviderMCP(t, binary, fixture, c))
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
	path := filepath.Join(filepath.Dir(source), askProviderMCPFixturePath)
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
		portProcessFixtureDifference(t, "ask provider MCP fixture is stale; regenerate with make ask-provider-mcp-fixtures-generate", current, encoded)
	}
}

// askScratch builds one isolated vault/home/bin tree for a process case.
type askScratch struct {
	root, home, bin, vault, logPath string
}

func newAskScratch(t *testing.T, fixtureFiles map[string]string, fileTime, hermes string, installHermes bool) askScratch {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := askScratch{root: root, home: filepath.Join(root, "home"), bin: filepath.Join(root, "bin"), vault: filepath.Join(root, "vault"), logPath: filepath.Join(root, "hermes.log")}
	for _, dir := range []string{s.home, s.bin, s.vault} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// Retrieval embeddings point at a closed port; keep the retry loop of the
	// failing embedder instant so setup does not depend on how long the host
	// takes to refuse a connection.
	seekConfig := filepath.Join(s.home, ".config", "symseek")
	if err := os.MkdirAll(seekConfig, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seekConfig, "config.toml"), []byte("retry_count = 1\nretry_backoff_ms = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stamp, err := time.Parse(time.RFC3339, fileTime)
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range fixtureFiles {
		dest := filepath.Join(s.vault, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dest, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(dest, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if installHermes {
		if err := os.WriteFile(filepath.Join(s.bin, "hermes"), []byte(hermes), 0o700); err != nil { //nolint:gosec // executable test double in a test-owned directory
			t.Fatal(err)
		}
	}
	return s
}

func (s askScratch) environment(mode string, extra map[string]string) []string {
	env := []string{
		"PATH=" + s.bin + ":/usr/bin:/bin", "HOME=" + s.home, "TZ=UTC",
		"XDG_CONFIG_HOME=" + filepath.Join(s.home, "config"),
		"XDG_CACHE_HOME=" + filepath.Join(s.home, "cache"),
		"XDG_DATA_HOME=" + filepath.Join(s.home, "data"),
		"FAKE_HERMES_LOG=" + s.logPath, "FAKE_HERMES_MODE=" + mode,
	}
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

// index builds the sidecar with embeddings pointed at a closed port.
func (s askScratch) index(t *testing.T, binary, mode string) {
	t.Helper()
	index := exec.Command(binary, "--vault", s.vault, "index") //nolint:gosec // test-built CLI with controlled arguments
	index.Dir, index.Env = s.root, s.environment(mode, map[string]string{"SYMDESK_OLLAMA_URL": askClosedOllamaURL})
	if out, err := index.CombinedOutput(); err != nil {
		t.Fatalf("index: %v\n%s", err, out)
	}
	_ = os.Remove(s.logPath)
}

// providerEnv is the provider selection a case runs under.
func providerEnv(provider string, server *askProviderServer, extra map[string]string) map[string]string {
	env := map[string]string{}
	if provider != "none" {
		env["SYMDESK_OLLAMA_URL"] = askClosedOllamaURL
	}
	switch provider {
	case "ollama":
		env["SYMDESK_LLM_PROVIDER"] = "ollama"
		env["SYMDESK_OLLAMA_URL"] = server.URL()
	case "anthropic":
		env["SYMDESK_LLM_PROVIDER"] = "anthropic"
		// llmkit's Anthropic client treats the URL as a base and appends
		// /messages; the agentic loop treats it as the full endpoint.
		env["SYMDESK_ANTHROPIC_URL"] = server.URL() + "/v1"
		env["SYMDESK_LLM_API_KEY"] = askProviderKey
	case "hermes":
		env["SYMDESK_LLM_PROVIDER"] = "hermes"
	}
	for k, v := range extra {
		if server != nil {
			v = strings.ReplaceAll(v, askProviderURLToken, server.URL())
		}
		env[k] = v
	}
	return env
}

func (s askScratch) hermesLog() string {
	if b, err := os.ReadFile(s.logPath); err == nil { //nolint:gosec // test-owned log
		return strings.ReplaceAll(string(b), s.root, "<ROOT>")
	}
	return ""
}

func filterSlogInfo(stderr string) string {
	var kept []string
	for _, line := range strings.SplitAfter(stderr, "\n") {
		if strings.HasPrefix(line, "time=") && strings.Contains(line, "level=INFO") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "")
}

func mcpToolCallLine(arguments string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"desk_ask","arguments":%s}}`, arguments) + "\n"
}

func observeAskProviderMCP(t *testing.T, binary string, fixture askProviderMCPFixture, c askProviderMCPCase) askProviderMCPCase {
	t.Helper()
	s := newAskScratch(t, askSelectFiles(t, fixture.Files, fixture.FileSets, c.FileSet), fixture.FileTime, fixture.HermesScript, c.Provider == "hermes")
	var server *askProviderServer
	if c.Provider == "ollama" || c.Provider == "anthropic" {
		server = newAskProviderServer(t, s.root, c.Turns)
		defer server.Close()
	}
	s.index(t, binary, c.Mode)
	cmd := exec.Command(binary, "--vault", s.vault, "mcp") //nolint:gosec // test-built CLI with controlled arguments
	cmd.Dir, cmd.Env = s.root, s.environment(c.Mode, providerEnv(c.Provider, server, c.Env))
	cmd.Stdin = strings.NewReader(mcpToolCallLine(c.ArgumentsJSON))
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("%s: %v", c.Name, err)
		}
		c.ExitCode = exit.ExitCode()
	}
	url := ""
	if server != nil {
		url = server.URL()
		c.Requests = server.Requests()
	}
	c.Stdout = normalizeProviderText(stdout.String(), s.root, url)
	c.Stderr = normalizeProviderText(filterSlogInfo(stderr.String()), s.root, url)
	c.Log = s.hermesLog()
	return c
}
