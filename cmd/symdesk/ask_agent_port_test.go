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

// Real-process contract for `symdesk ask --agent` (bounded tool loop,
// externalizer, citation validation), `ask --notebook` and notebook-scoped
// agent tools. Providers are loopback fakes served from this process (Ollama
// /api/chat NDJSON and Anthropic Messages SSE); the Hermes backend is a shell
// double on a private PATH. The Rust replay is
// crates/symdesk-cli/tests/ask_agent.rs.

const askAgentFixturePath = "../../testdata/port/ai/ask-agent-process.json"

type askAgentFixture struct {
	SchemaVersion int               `json:"schema_version"`
	HermesScript  string            `json:"hermes_script"`
	Files         map[string]string `json:"files"`
	FileTime      string            `json:"file_time"`
	Cases         []askAgentCase    `json:"cases"`
}

type askAgentCase struct {
	Name     string               `json:"name"`
	Args     []string             `json:"args"`
	Provider string               `json:"provider"`
	Mode     string               `json:"mode,omitempty"`
	Env      map[string]string    `json:"env,omitempty"`
	Turns    []askProviderTurn    `json:"turns,omitempty"`
	ExitCode int                  `json:"exit_code"`
	Stdout   string               `json:"stdout"`
	Stderr   string               `json:"stderr"`
	Requests []askProviderRequest `json:"requests,omitempty"`
	Results  []askResultFile      `json:"results,omitempty"`
	Log      string               `json:"log,omitempty"`
}

const askAgentHermesScript = `#!/bin/sh
log="$FAKE_HERMES_LOG"
{
  printf 'argc=%s\n' "$#"
  for a in "$@"; do printf 'arg<<%s>>\n' "$a"; done
} >> "$log"
case "$FAKE_HERMES_MODE" in
  ok) printf 'Answer from <hermes> [[alpha.md]]\n' ;;
  warn) printf 'Findings.\n\n## Sources\n- [[notes/outside.md]]\n- [[alpha.md]]\n' ;;
  fail) printf 'hermes exploded\n' >&2; exit 4 ;;
  *) exit 9 ;;
esac
`

const askAgentFileTime = "2026-01-02T03:04:05Z"

func askAgentFiles() map[string]string {
	files := map[string]string{
		"alpha.md":          "---\ntitle: Alpha Project\n---\n# Alpha\n\nThe quarterly budget review covers marketing spend and hiring.\n",
		"notes/beta.md":     "---\ntitle: Beta Notes\n---\n# Beta\n\nSee [[alpha]] for the plan.\n",
		"notes/gamma.md":    "---\ntitle: Gamma Notes\n---\n# Gamma\n\nUnrelated gardening remarks.\n",
		"notes/outside.md":  "---\ntitle: Outside\n---\n# Outside\n\nA note outside the notebook.\n",
		"notebooks/nb.md":   "---\ntype: notebook\ntitle: Budget notebook\nnotebook_id: nb\ncreated: 2026-01-02T03:04:05Z\nsources:\n  - alpha.md\n  - missing.md\n---\n\n# Budget notebook\n",
		"notebooks/none.md": "---\ntype: notebook\ntitle: Empty notebook\nnotebook_id: none\ncreated: 2026-01-02T03:04:05Z\nsources: []\n---\n\n# Empty notebook\n",
	}
	for i := 1; i <= 70; i++ {
		files[fmt.Sprintf("bulk/note-%03d.md", i)] = fmt.Sprintf("---\ntitle: Bulk archive note number %03d with padding words\n---\n# Bulk %03d\n\nFiller text.\n", i, i)
	}
	return files
}

func askAgentCases(t *testing.T) []askAgentCase {
	t.Helper()
	ollama := func(content []string, calls [][2]string, in, out int) askProviderTurn {
		return askProviderTurn{Body: ollamaChatTurn(t, content, calls, in, out)}
	}
	anthropic := func(blocks []anthropicBlock, stop string, in, out int) askProviderTurn {
		return askProviderTurn{Body: anthropicTurn(t, blocks, stop, in, out)}
	}
	lsNotes := [][2]string{{"desk_ls", `{"dir":"notes"}`}}
	text := func(parts ...string) anthropicBlock { return anthropicBlock{Text: parts} }
	tool := func(id, name string, fragments ...string) anthropicBlock {
		return anthropicBlock{Tool: true, ToolID: id, ToolName: name, Fragments: fragments}
	}
	ask := func(extra ...string) []string { return append([]string{"--json", "ask", "--agent"}, extra...) }
	askText := func(extra ...string) []string { return append([]string{"ask", "--agent"}, extra...) }
	cases := []askAgentCase{
		{Name: "ollama_ls_json", Provider: "ollama", Args: ask("what is in notes"), Turns: []askProviderTurn{
			ollama([]string{"Looking. "}, lsNotes, 10, 5),
			ollama([]string{"The folder holds ", "[[notes/beta.md]] and [[notes/gamma.md]]."}, nil, 30, 12),
		}},
		{Name: "ollama_ls_text", Provider: "ollama", Args: askText("what is in notes"), Env: map[string]string{"SYMDESK_OLLAMA_MODEL": "fake-model"}, Turns: []askProviderTurn{
			ollama([]string{"Looking. "}, lsNotes, 10, 5),
			ollama([]string{"The folder holds [[notes/beta.md]]."}, nil, 30, 12),
		}},
		{Name: "ollama_tool_errors", Provider: "ollama", Args: ask("props please"), Turns: []askProviderTurn{
			ollama(nil, [][2]string{{"desk_props", `{"file":"alpha.md"}`}, {"desk_props", `{"file":"missing.md"}`}, {"desk_note_new", `{"title":"x"}`}, {"desk_ls", ""}}, 0, 0),
			ollama([]string{"Only [[alpha.md]] had properties."}, nil, 0, 0),
		}},
		{Name: "ollama_externalize", Provider: "ollama", Args: ask("list the bulk archive"), Turns: []askProviderTurn{
			ollama([]string{"Listing. "}, [][2]string{{"desk_ls", `{"dir":"bulk"}`}}, 7, 3),
			ollama(nil, [][2]string{{"desk_read_result", `{"handle":"` + askHandleToken + `"}`}}, 8, 4),
			ollama([]string{"Read it back."}, nil, 9, 5),
		}},
		{Name: "ollama_search_citation_warning", Provider: "ollama", Args: ask("budget"), Turns: []askProviderTurn{
			ollama(nil, [][2]string{{"desk_search", `{"query":"budget"}`}}, 0, 0),
			ollama([]string{"The budget is in [[alpha.md]].\n\n## Sources\n- [[notes/gamma.md]]\n- [[alpha.md]]\n"}, nil, 0, 0),
		}},
		{Name: "ollama_iteration_cap", Provider: "ollama", Args: ask("loop forever"), Env: map[string]string{"SYMDESK_AGENT_MAX_ITERATIONS": "2"}, Turns: []askProviderTurn{
			ollama(nil, lsNotes, 1, 1), ollama(nil, lsNotes, 2, 2),
		}},
		{Name: "ollama_http_500", Provider: "ollama", Args: ask("anything"), Turns: []askProviderTurn{
			{Status: 500, Body: `{"error":"boom"}`},
		}},
		{Name: "ollama_unconfigured", Provider: "none", Args: ask("anything"), Env: map[string]string{"SYMDESK_LLM_PROVIDER": "ollama"}},
		{Name: "ollama_arguments_not_object", Provider: "ollama", Args: ask("odd arguments"), Turns: []askProviderTurn{
			ollama(nil, [][2]string{{"desk_ls", `"{\"dir\":\"notes\"}"`}}, 0, 0),
			ollama([]string{"Odd."}, nil, 0, 0),
		}},
		{Name: "anthropic_tool_use_json", Provider: "anthropic", Args: ask("what is in notes"),
			Env: map[string]string{"SYMDESK_LLM_MODEL": "claude-haiku-fake", "SYMDESK_MAX_TOKENS": "123", "SYMDESK_LANG": "German"},
			Turns: []askProviderTurn{
				anthropic([]anthropicBlock{text("Checking. "), tool("toolu_1", "desk_ls", `{"dir":`, `"notes"}`)}, "tool_use", 12, 7),
				anthropic([]anthropicBlock{text("Es gibt ", "[[notes/beta.md]].")}, "end_turn", 40, 20),
			}},
		{Name: "anthropic_tool_use_text", Provider: "anthropic", Args: askText("what is in notes"), Turns: []askProviderTurn{
			anthropic([]anthropicBlock{tool("toolu_1", "desk_ls", `{"dir":"notes"}`)}, "tool_use", 5, 6),
			anthropic([]anthropicBlock{text("Done [[notes/gamma.md]].")}, "end_turn", 9, 10),
		}},
		{Name: "anthropic_tool_without_input_is_dropped", Provider: "anthropic", Args: ask("empty tool"), Turns: []askProviderTurn{
			anthropic([]anthropicBlock{text("Nothing to call."), {Tool: true, ToolID: "toolu_x", ToolName: "desk_ls"}}, "tool_use", 3, 4),
		}},
		{Name: "anthropic_http_429", Provider: "anthropic", Args: ask("limited"), Turns: []askProviderTurn{
			{Status: 429, Body: `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`},
		}},
		{Name: "anthropic_truncated_tool_json_text", Provider: "anthropic", Args: askText("broken tool json"), Turns: []askProviderTurn{
			anthropic([]anthropicBlock{tool("toolu_9", "desk_ls", `{"dir":`)}, "tool_use", 1, 1),
			anthropic([]anthropicBlock{text("Recovered.")}, "end_turn", 2, 2),
		}},
		{Name: "anthropic_truncated_tool_json_json", Provider: "anthropic", Args: ask("broken tool json"), Turns: []askProviderTurn{
			anthropic([]anthropicBlock{tool("toolu_9", "desk_ls", `{"dir":`)}, "tool_use", 1, 1),
		}},
		{Name: "agent_notebook_scoped_tools", Provider: "ollama", Args: ask("--notebook", "nb", "budget"), Turns: []askProviderTurn{
			ollama(nil, [][2]string{{"desk_search", `{"query":"budget"}`}, {"desk_ls", `{"dir":""}`}, {"desk_backlinks", `{"file":"alpha.md"}`}, {"desk_backlinks", `{"file":"notes/gamma.md"}`}}, 0, 0),
			ollama([]string{"Scoped answer [[alpha.md]]."}, nil, 0, 0),
		}},
		{Name: "agent_notebook_missing", Provider: "ollama", Args: ask("--notebook", "nope", "budget")},
		{Name: "ask_notebook_hermes_json", Provider: "hermes", Mode: "ok", Args: []string{"--json", "ask", "--notebook", "nb", "budget"}},
		{Name: "ask_notebook_hermes_text", Provider: "hermes", Mode: "ok", Args: []string{"ask", "--notebook", "nb", "budget"}},
		{Name: "ask_notebook_citation_warning", Provider: "hermes", Mode: "warn", Args: []string{"--json", "ask", "--notebook", "nb", "budget"}},
		{Name: "ask_notebook_backend_fails", Provider: "hermes", Mode: "fail", Args: []string{"--json", "ask", "--notebook", "nb", "budget"}},
		{Name: "ask_notebook_empty_sources", Provider: "hermes", Mode: "ok", Args: []string{"--json", "ask", "--notebook", "none", "budget"}},
		{Name: "ask_notebook_missing", Provider: "hermes", Mode: "ok", Args: []string{"--json", "ask", "--notebook", "nope", "budget"}},
		{Name: "ask_notebook_unconfigured", Provider: "none", Args: []string{"--json", "ask", "--notebook", "nb", "budget"}},
		{Name: "ask_notebook_unconfigured_text", Provider: "none", Args: []string{"ask", "--notebook", "nb", "budget"}},
	}
	return cases
}

func TestPortAskAgentProcessContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell double is Unix-only")
	}
	binary := processFixtureBinary(t)
	fixture := askAgentFixture{SchemaVersion: 1, HermesScript: askAgentHermesScript, Files: askAgentFiles(), FileTime: askAgentFileTime}
	for _, c := range askAgentCases(t) {
		fixture.Cases = append(fixture.Cases, observeAskAgent(t, binary, fixture, c))
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
	path := filepath.Join(filepath.Dir(source), askAgentFixturePath)
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
		portProcessFixtureDifference(t, "ask agent fixture is stale; regenerate with make ask-agent-fixtures-generate", current, encoded)
	}
}

// askClosedOllamaURL is a closed loopback port: embeddings never reach a real
// Ollama on the recording machine.
const askClosedOllamaURL = "http://127.0.0.1:9"

func observeAskAgent(t *testing.T, binary string, fixture askAgentFixture, c askAgentCase) askAgentCase {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home, bin, vault := filepath.Join(root, "home"), filepath.Join(root, "bin"), filepath.Join(root, "vault")
	for _, dir := range []string{home, bin, vault} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	stamp, err := time.Parse(time.RFC3339, fixture.FileTime)
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range fixture.Files {
		dest := filepath.Join(vault, filepath.FromSlash(name))
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
	if c.Provider == "hermes" {
		if err := os.WriteFile(filepath.Join(bin, "hermes"), []byte(fixture.HermesScript), 0o700); err != nil { //nolint:gosec // executable test double in a test-owned directory
			t.Fatal(err)
		}
	}
	logPath := filepath.Join(root, "hermes.log")
	var provider *askProviderServer
	if c.Provider == "ollama" || c.Provider == "anthropic" {
		provider = newAskProviderServer(t, root, c.Turns)
		defer provider.Close()
	}
	environment := func(extra map[string]string) []string {
		env := []string{
			"PATH=" + bin + ":/usr/bin:/bin", "HOME=" + home, "TZ=UTC",
			"XDG_CONFIG_HOME=" + filepath.Join(home, "config"),
			"XDG_CACHE_HOME=" + filepath.Join(home, "cache"),
			"XDG_DATA_HOME=" + filepath.Join(home, "data"),
			"FAKE_HERMES_LOG=" + logPath, "FAKE_HERMES_MODE=" + c.Mode,
		}
		for k, v := range extra {
			env = append(env, k+"="+v)
		}
		return env
	}
	index := exec.Command(binary, "--vault", vault, "index") //nolint:gosec // test-built CLI with controlled arguments
	index.Dir, index.Env = root, environment(map[string]string{"SYMDESK_OLLAMA_URL": askClosedOllamaURL})
	if out, err := index.CombinedOutput(); err != nil {
		t.Fatalf("%s: index: %v\n%s", c.Name, err, out)
	}
	_ = os.Remove(logPath)

	runEnv := map[string]string{}
	if c.Provider != "none" {
		runEnv["SYMDESK_OLLAMA_URL"] = askClosedOllamaURL
	}
	switch c.Provider {
	case "ollama":
		runEnv["SYMDESK_LLM_PROVIDER"] = "ollama"
		runEnv["SYMDESK_OLLAMA_URL"] = provider.URL()
	case "anthropic":
		runEnv["SYMDESK_LLM_PROVIDER"] = "anthropic"
		runEnv["SYMDESK_ANTHROPIC_URL"] = provider.URL() + "/v1/messages"
		runEnv["SYMDESK_LLM_API_KEY"] = askProviderKey
	case "hermes":
		runEnv["SYMDESK_LLM_PROVIDER"] = "hermes"
	}
	for k, v := range c.Env {
		runEnv[k] = v
	}
	cmd := exec.Command(binary, append([]string{"--vault", vault}, c.Args...)...) //nolint:gosec // test-built CLI with controlled arguments
	cmd.Dir, cmd.Env = root, environment(runEnv)
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
	if provider != nil {
		url = provider.URL()
	}
	c.Stdout = normalizeProviderText(stdout.String(), root, url)
	var kept []string
	for _, line := range strings.SplitAfter(stderr.String(), "\n") {
		if strings.HasPrefix(line, "time=") && strings.Contains(line, "level=INFO") {
			continue
		}
		kept = append(kept, line)
	}
	c.Stderr = normalizeProviderText(strings.Join(kept, ""), root, url)
	if provider != nil {
		c.Requests = provider.Requests()
	}
	c.Results = observeAskResults(t, filepath.Join(home, "data"), vault)
	if b, err := os.ReadFile(logPath); err == nil { //nolint:gosec // test-owned log
		c.Log = strings.ReplaceAll(string(b), root, "<ROOT>")
	}
	return c
}
