package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/config"
)

// Records the AI composition surface (prompt building, honest degradation,
// the optional Hermes CLI backend) through the production Ask, Transform and
// PromptOne entry points. The Hermes binary is a loopback shell double on a
// private PATH; no network, keychain or operator credential is touched.

type portComposeDoc struct {
	Path    string `json:"path"`
	Title   string `json:"title"`
	Snippet string `json:"snippet"`
}

type portComposeCase struct {
	ID       string           `json:"id"`
	Op       string           `json:"op"` // ask | transform | prompt_one
	Provider string           `json:"provider"`
	Session  string           `json:"session,omitempty"`
	Language string           `json:"language,omitempty"`
	APIKey   string           `json:"api_key,omitempty"`
	Mode     string           `json:"mode,omitempty"` // fake hermes behaviour
	NoBinary bool             `json:"no_binary,omitempty"`
	NotExec  bool             `json:"not_executable,omitempty"`
	Query    string           `json:"query,omitempty"`
	Text     string           `json:"text,omitempty"`
	Intent   string           `json:"intent,omitempty"`
	Docs     []portComposeDoc `json:"docs,omitempty"`
	Chunks   []string         `json:"chunks"`
	Error    string           `json:"error,omitempty"`
	Result   string           `json:"result,omitempty"`
	Log      string           `json:"log,omitempty"`
}

const portComposeScript = `#!/bin/sh
log="$FAKE_HERMES_LOG"
{
  printf 'argc=%s\n' "$#"
  for a in "$@"; do printf 'arg<<%s>>\n' "$a"; done
  if IFS= read -r _line; then printf 'stdin=data\n'; else printf 'stdin=eof\n'; fi
} >> "$log"
case "$FAKE_HERMES_MODE" in
  ok) printf 'the answer' ;;
  padded) printf '  \n  first line\nsecond line  \n\n' ;;
  unicode) printf 'gr\303\274\303\237e <&> \342\200\250 done\n' ;;
  empty) : ;;
  blank) printf '   \n\t\n' ;;
  fail_stderr) printf 'partial\n'; printf '  boom: it broke  \n' >&2; exit 2 ;;
  fail_silent) exit 3 ;;
  fail_stdout) printf 'only stdout\n'; exit 1 ;;
  killed) kill -9 $$ ;;
  big)
    i=0
    while [ "$i" -lt 1500 ]; do printf 'xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx'; i=$((i+1)); done ;;
  *) printf 'unknown mode\n' >&2; exit 9 ;;
esac
`

func portComposeCases() []portComposeCase {
	docs := []portComposeDoc{
		{Path: "a.md", Title: "Alpha <A>", Snippet: "alpha snippet"},
		{Path: "b/c.md", Title: "Beta", Snippet: strings.Repeat("é", 800)},
		{Path: "d.md", Title: "Delta", Snippet: "delta"},
		{Path: "e.md", Title: "Eps", Snippet: "eps"},
		{Path: "f.md", Title: "Zeta", Snippet: "zeta"},
		{Path: "g.md", Title: "Eta", Snippet: "never in the prompt"},
	}
	var cases []portComposeCase
	add := func(c portComposeCase) { cases = append(cases, c) }

	for _, intent := range []string{"summarize", "rewrite", "continue", "shout"} {
		add(portComposeCase{ID: "transform-hermes-" + intent, Op: "transform", Provider: "hermes", Mode: "ok", Text: "  Some text\nwith lines  ", Intent: intent})
	}
	add(portComposeCase{ID: "transform-empty-text", Op: "transform", Provider: "hermes", Mode: "ok", Text: " \n\t ", Intent: "rewrite"})
	add(portComposeCase{ID: "transform-language", Op: "transform", Provider: "hermes", Mode: "ok", Language: "German", Text: "Hallo", Intent: "summarize"})
	add(portComposeCase{ID: "transform-session", Op: "transform", Provider: "hermes", Session: "weekly review", Mode: "padded", Text: "x", Intent: "continue"})
	add(portComposeCase{ID: "transform-unicode", Op: "transform", Provider: "hermes", Mode: "unicode", Text: "x", Intent: "rewrite"})
	add(portComposeCase{ID: "transform-big-output", Op: "transform", Provider: "hermes", Mode: "big", Text: "x", Intent: "rewrite"})
	for _, mode := range []string{"empty", "blank", "fail_stderr", "fail_silent", "fail_stdout", "killed", "unknown"} {
		add(portComposeCase{ID: "transform-hermes-" + mode, Op: "transform", Provider: "hermes", Mode: mode, Text: "x", Intent: "rewrite"})
	}
	add(portComposeCase{ID: "transform-hermes-missing", Op: "transform", Provider: "hermes", NoBinary: true, Text: "x", Intent: "rewrite"})
	add(portComposeCase{ID: "transform-hermes-not-executable", Op: "transform", Provider: "hermes", NotExec: true, Text: "x", Intent: "rewrite"})
	add(portComposeCase{ID: "transform-ollama-unconfigured", Op: "transform", Text: "x", Intent: "rewrite"})
	add(portComposeCase{ID: "transform-unknown-provider", Op: "transform", Provider: "mystery", Text: "x", Intent: "rewrite"})
	add(portComposeCase{ID: "transform-anthropic-unconfigured", Op: "transform", Provider: "anthropic", APIKey: "op://missing/key", Text: "x", Intent: "rewrite"})

	add(portComposeCase{ID: "ask-hermes-ok", Op: "ask", Provider: "hermes", Mode: "ok", Query: "What is <this>?", Docs: docs})
	add(portComposeCase{ID: "ask-hermes-language-session", Op: "ask", Provider: "hermes", Session: "s-1", Language: "French", Mode: "padded", Query: "Q", Docs: docs[:2]})
	add(portComposeCase{ID: "ask-hermes-no-docs", Op: "ask", Provider: "hermes", Mode: "ok", Query: "Q"})
	add(portComposeCase{ID: "ask-hermes-fail", Op: "ask", Provider: "hermes", Mode: "fail_stderr", Query: "Q", Docs: docs[:1]})
	add(portComposeCase{ID: "ask-hermes-empty", Op: "ask", Provider: "hermes", Mode: "empty", Query: "Q", Docs: docs[:1]})
	add(portComposeCase{ID: "ask-hermes-missing", Op: "ask", Provider: "hermes", NoBinary: true, Query: "Q", Docs: docs})
	add(portComposeCase{ID: "ask-ollama-unconfigured", Op: "ask", Query: "Q", Docs: docs})
	add(portComposeCase{ID: "ask-ollama-unconfigured-no-docs", Op: "ask", Query: "Q"})
	add(portComposeCase{ID: "ask-anthropic-unconfigured", Op: "ask", Provider: "anthropic", APIKey: "op://missing/key", Query: "Q", Docs: docs})

	for _, mode := range []string{"ok", "padded", "empty", "fail_stderr", "fail_silent", "killed"} {
		add(portComposeCase{ID: "prompt-one-hermes-" + mode, Op: "prompt_one", Provider: "hermes", Mode: mode, Text: "extract <this>"})
	}
	add(portComposeCase{ID: "prompt-one-hermes-session", Op: "prompt_one", Provider: "hermes", Session: "abc", Mode: "ok", Text: "p"})
	add(portComposeCase{ID: "prompt-one-hermes-missing", Op: "prompt_one", Provider: "hermes", NoBinary: true, Text: "p"})
	add(portComposeCase{ID: "prompt-one-ollama-unconfigured", Op: "prompt_one", Text: "p"})
	add(portComposeCase{ID: "prompt-one-anthropic-unconfigured", Op: "prompt_one", Provider: "anthropic", APIKey: "op://missing/key", Text: "p"})
	return cases
}

func observePortComposeCase(t *testing.T, c portComposeCase) portComposeCase {
	t.Helper()
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	switch {
	case c.NoBinary:
	case c.NotExec:
		if err := os.WriteFile(filepath.Join(binDir, "hermes"), []byte(portComposeScript), 0o600); err != nil {
			t.Fatal(err)
		}
	default:
		if err := os.WriteFile(filepath.Join(binDir, "hermes"), []byte(portComposeScript), 0o700); err != nil { //nolint:gosec // executable test double in a test-owned directory
			t.Fatal(err)
		}
	}
	logPath := filepath.Join(root, "hermes.log")
	system := "/usr/bin:/bin"
	t.Setenv("PATH", binDir+":"+system)
	t.Setenv("FAKE_HERMES_LOG", logPath)
	t.Setenv("FAKE_HERMES_MODE", c.Mode)
	t.Setenv("SYMDESK_LLM_API_KEY", "")
	t.Setenv("SYMDESK_OLLAMA_URL", "")
	t.Setenv("SYMDESK_OLLAMA_MODEL", "")
	t.Setenv("SYMAIRA_BIN", filepath.Join(root, "no-such-symaira-bin"))
	t.Setenv("HOME", filepath.Join(root, "home"))
	cfg := &config.Config{LLMProvider: c.Provider, HermesSession: c.Session, Language: c.Language, LLMAPIKey: c.APIKey}

	var chunks []AskChunk
	collect := func(run func(out chan<- AskChunk)) {
		out := make(chan AskChunk)
		go run(out)
		for chunk := range out {
			chunks = append(chunks, chunk)
		}
	}
	switch c.Op {
	case "ask":
		contextDocs := make([]map[string]interface{}, 0, len(c.Docs))
		for _, d := range c.Docs {
			contextDocs = append(contextDocs, map[string]interface{}{"path": d.Path, "title": d.Title, "snippet": d.Snippet})
		}
		collect(func(out chan<- AskChunk) { Ask(context.Background(), cfg, c.Query, contextDocs, out) })
	case "transform":
		collect(func(out chan<- AskChunk) { Transform(context.Background(), cfg, c.Text, c.Intent, out) })
	case "prompt_one":
		result, err := PromptOneReal(cfg, c.Text)
		c.Result = result
		if err != nil {
			c.Error = err.Error()
		}
	default:
		t.Fatalf("unknown op %q", c.Op)
	}
	c.Chunks = make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		c.Chunks = append(c.Chunks, chunk.Chunk)
	}
	if b, err := os.ReadFile(logPath); err == nil { //nolint:gosec // test-owned log
		c.Log = string(b)
	}
	return c
}

func TestPortAIComposeContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell double is Unix-only")
	}
	var cases []portComposeCase
	for _, c := range portComposeCases() {
		cases = append(cases, observePortComposeCase(t, c))
	}
	// Go's fmt of the killed double depends on the signal text, not the pid.
	document := struct {
		SchemaVersion int               `json:"schema_version"`
		Script        string            `json:"hermes_script"`
		Cases         []portComposeCase `json:"cases"`
	}{1, portComposeScript, cases}
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repo, "testdata/port/ai/ai-compose.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	} else {
		expected, err := os.ReadFile(path) //nolint:gosec // fixed repository fixture path
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, expected) {
			t.Fatal("AI composition fixture drift; regenerate deliberately with make ai-compose-fixtures-generate")
		}
	}
	t.Logf("executed %d real Go composition cases", len(cases))
	_ = fmt.Sprint
}
