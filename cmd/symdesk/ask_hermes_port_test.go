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
	"testing"
)

// Real-process contract for `symdesk ask` with a configured provider. The
// Hermes CLI backend is a loopback shell double on a private PATH, so the
// whole path (retrieval, event stream, prompt construction, degradation) is
// recorded without any network. The Rust replay is
// crates/symdesk-cli/tests/ask_hermes.rs.

const askHermesFixturePath = "../../testdata/port/ai/ask-hermes-process.json"

type askHermesFixture struct {
	SchemaVersion int               `json:"schema_version"`
	HermesScript  string            `json:"hermes_script"`
	Files         map[string]string `json:"files"`
	Cases         []askHermesCase   `json:"cases"`
}

type askHermesCase struct {
	Name     string            `json:"name"`
	Args     []string          `json:"args"`
	Mode     string            `json:"mode,omitempty"`
	NoBinary bool              `json:"no_binary,omitempty"`
	NoIndex  bool              `json:"no_index,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	ExitCode int               `json:"exit_code"`
	Stdout   string            `json:"stdout"`
	Stderr   string            `json:"stderr"`
	Log      string            `json:"log,omitempty"`
}

const askHermesScript = `#!/bin/sh
log="$FAKE_HERMES_LOG"
{
  printf 'argc=%s\n' "$#"
  for a in "$@"; do printf 'arg<<%s>>\n' "$a"; done
  if IFS= read -r _line; then printf 'stdin=data\n'; else printf 'stdin=eof\n'; fi
} >> "$log"
case "$FAKE_HERMES_MODE" in
  ok) printf 'Answer from <hermes> [[alpha.md]]\n' ;;
  fail) printf 'hermes exploded\n' >&2; exit 4 ;;
  empty) : ;;
  *) exit 9 ;;
esac
`

// One document: with several, the order of score ties in the hybrid lists
// follows the walk order of the host filesystem, so Darwin and Linux would
// record different (equally valid) rankings.
var askHermesFiles = map[string]string{
	"alpha.md": "---\ntitle: Alpha Project\n---\n# Alpha\n\nThe quarterly budget review covers marketing spend and hiring.\n",
}

// askClosedOllama is a closed loopback port. Embeddings must never reach a
// real Ollama on the machine that records or replays the fixture: with one
// running, hybrid ranking differs per host. Retrieval stays lexical.
const askClosedOllama = "http://127.0.0.1:9"

func askHermesCases() []askHermesCase {
	hermes := map[string]string{"SYMDESK_LLM_PROVIDER": "hermes", "SYMDESK_OLLAMA_URL": askClosedOllama}
	with := func(extra map[string]string) map[string]string {
		out := map[string]string{}
		for k, v := range hermes {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	return []askHermesCase{
		{Name: "ok_json", Args: []string{"--json", "ask", "budget hiring"}, Mode: "ok", Env: hermes},
		{Name: "ok_text", Args: []string{"ask", "budget hiring"}, Mode: "ok", Env: hermes},
		{Name: "session_language", Args: []string{"--json", "ask", "budget"}, Mode: "ok", Env: with(map[string]string{"SYMDESK_HERMES_SESSION": "weekly", "SYMDESK_LANG": "German"})},
		{Name: "no_hits", Args: []string{"--json", "ask", "zzzznomatch"}, Mode: "ok", Env: hermes},
		{Name: "empty_query_hits", Args: []string{"--json", "ask", "tomatoes"}, Mode: "ok", Env: hermes},
		{Name: "backend_fails", Args: []string{"--json", "ask", "budget"}, Mode: "fail", Env: hermes},
		{Name: "backend_fails_text", Args: []string{"ask", "budget"}, Mode: "fail", Env: hermes},
		{Name: "backend_empty", Args: []string{"--json", "ask", "budget"}, Mode: "empty", Env: hermes},
		{Name: "backend_missing", Args: []string{"--json", "ask", "budget hiring"}, NoBinary: true, Env: hermes},
		{Name: "unconfigured_default", Args: []string{"--json", "ask", "budget hiring"}},
		{Name: "unconfigured_text", Args: []string{"ask", "budget hiring"}},
		{Name: "unindexed_vault", Args: []string{"--json", "ask", "budget"}, Mode: "ok", NoIndex: true, Env: hermes},
	}
}

func TestPortAskHermesProcessContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell double is Unix-only")
	}
	binary := processFixtureBinary(t)
	fixture := askHermesFixture{SchemaVersion: 1, HermesScript: askHermesScript, Files: askHermesFiles}
	for _, c := range askHermesCases() {
		fixture.Cases = append(fixture.Cases, observeAskHermes(t, binary, c))
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
	path := filepath.Join(filepath.Dir(source), askHermesFixturePath)
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
		portProcessFixtureDifference(t, "ask hermes fixture is stale; regenerate with make ask-hermes-fixtures-generate", current, encoded)
	}
}

func observeAskHermes(t *testing.T, binary string, c askHermesCase) askHermesCase {
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
	for name, content := range askHermesFiles {
		dest := filepath.Join(vault, name)
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dest, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if !c.NoBinary {
		if err := os.WriteFile(filepath.Join(bin, "hermes"), []byte(askHermesScript), 0o700); err != nil { //nolint:gosec // executable test double in a test-owned directory
			t.Fatal(err)
		}
	}
	logPath := filepath.Join(root, "hermes.log")
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
	if !c.NoIndex {
		index := exec.Command(binary, "--vault", vault, "index") //nolint:gosec // test-built CLI with controlled arguments
		index.Dir, index.Env = root, environment(map[string]string{"SYMDESK_OLLAMA_URL": askClosedOllama})
		if out, err := index.CombinedOutput(); err != nil {
			t.Fatalf("%s: index: %v\n%s", c.Name, err, out)
		}
	}
	_ = os.Remove(logPath)
	cmd := exec.Command(binary, append([]string{"--vault", vault}, c.Args...)...) //nolint:gosec // test-built CLI with controlled arguments
	cmd.Dir, cmd.Env = root, environment(c.Env)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("%s: %v", c.Name, err)
		}
		c.ExitCode = exit.ExitCode()
	}
	c.Stdout = strings.ReplaceAll(stdout.String(), root, "<ROOT>")
	// Go's slog lines carry wall-clock timestamps; the contract compared is
	// everything else on stderr.
	var kept []string
	for _, line := range strings.SplitAfter(stderr.String(), "\n") {
		if strings.HasPrefix(line, "time=") && strings.Contains(line, "level=INFO") {
			continue
		}
		kept = append(kept, line)
	}
	c.Stderr = strings.ReplaceAll(strings.Join(kept, ""), root, "<ROOT>")
	if b, err := os.ReadFile(logPath); err == nil { //nolint:gosec // test-owned log
		c.Log = strings.ReplaceAll(string(b), root, "<ROOT>")
	}
	return c
}
