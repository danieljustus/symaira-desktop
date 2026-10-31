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

// Real-process contract for `symdesk transform` with the optional Hermes CLI
// backend (llm_provider = hermes). The backend is a loopback shell double on a
// private PATH; the Rust replay (crates/symdesk-cli/tests/transform_hermes.rs)
// runs the identical script.

const transformHermesFixturePath = "../../testdata/port/ai/transform-hermes-process.json"

type transformHermesFixture struct {
	SchemaVersion int                   `json:"schema_version"`
	HermesScript  string                `json:"hermes_script"`
	Cases         []transformHermesCase `json:"cases"`
}

type transformHermesCase struct {
	Name     string            `json:"name"`
	Args     []string          `json:"args"`
	Stdin    string            `json:"stdin,omitempty"`
	Mode     string            `json:"mode,omitempty"`
	NoBinary bool              `json:"no_binary,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	ExitCode int               `json:"exit_code"`
	Stdout   string            `json:"stdout"`
	Stderr   string            `json:"stderr"`
	Log      string            `json:"log,omitempty"`
}

const transformHermesScript = `#!/bin/sh
log="$FAKE_HERMES_LOG"
{
  printf 'argc=%s\n' "$#"
  for a in "$@"; do printf 'arg<<%s>>\n' "$a"; done
  if IFS= read -r _line; then printf 'stdin=data\n'; else printf 'stdin=eof\n'; fi
} >> "$log"
case "$FAKE_HERMES_MODE" in
  ok) printf 'rewritten <text> & more\n\n' ;;
  fail) printf 'bad\n' >&2; exit 4 ;;
  empty) : ;;
  *) exit 9 ;;
esac
`

func transformHermesCases() []transformHermesCase {
	hermes := map[string]string{"SYMDESK_LLM_PROVIDER": "hermes"}
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
	return []transformHermesCase{
		{Name: "text_ok", Args: []string{"transform", "rewrite", "--text", "Hello <world>"}, Mode: "ok", Env: hermes},
		{Name: "json_ok", Args: []string{"--json", "transform", "summarize", "--text", "Hello <world>"}, Mode: "ok", Env: hermes},
		{Name: "stdin_text", Args: []string{"transform", "continue"}, Stdin: "  from stdin\nsecond line \n", Mode: "ok", Env: hermes},
		{Name: "session_and_language", Args: []string{"--json", "transform", "rewrite", "--text", "x"}, Mode: "ok", Env: with(map[string]string{"SYMDESK_HERMES_SESSION": "weekly review", "SYMDESK_LANG": "German"})},
		{Name: "unknown_intent", Args: []string{"--json", "transform", "shout", "--text", "x"}, Mode: "ok", Env: hermes},
		{Name: "failing_backend", Args: []string{"transform", "rewrite", "--text", "x"}, Mode: "fail", Env: hermes},
		{Name: "failing_backend_json", Args: []string{"--json", "transform", "rewrite", "--text", "x"}, Mode: "fail", Env: hermes},
		{Name: "empty_answer", Args: []string{"--json", "transform", "rewrite", "--text", "x"}, Mode: "empty", Env: hermes},
		{Name: "silent_failure", Args: []string{"--json", "transform", "rewrite", "--text", "x"}, Mode: "unknown", Env: hermes},
		{Name: "backend_missing", Args: []string{"--json", "transform", "rewrite", "--text", "x"}, NoBinary: true, Env: hermes},
		{Name: "backend_missing_text", Args: []string{"transform", "rewrite", "--text", "x"}, NoBinary: true, Env: hermes},
		{Name: "empty_text_json", Args: []string{"--json", "transform", "rewrite", "--text", "   "}, Mode: "ok", Env: hermes},
		{Name: "empty_stdin", Args: []string{"transform", "rewrite"}, Stdin: "\n", Mode: "ok", Env: hermes},
	}
}

func TestPortTransformHermesProcessContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell double is Unix-only")
	}
	binary := processFixtureBinary(t)
	fixture := transformHermesFixture{SchemaVersion: 1, HermesScript: transformHermesScript}
	for _, c := range transformHermesCases() {
		fixture.Cases = append(fixture.Cases, observeTransformHermes(t, binary, c))
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
	path := filepath.Join(filepath.Dir(source), transformHermesFixturePath)
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
		portProcessFixtureDifference(t, "transform hermes fixture is stale; regenerate with make transform-hermes-fixtures-generate", current, encoded)
	}
}

func observeTransformHermes(t *testing.T, binary string, c transformHermesCase) transformHermesCase {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	bin := filepath.Join(root, "bin")
	for _, dir := range []string{home, bin} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if !c.NoBinary {
		if err := os.WriteFile(filepath.Join(bin, "hermes"), []byte(transformHermesScript), 0o700); err != nil { //nolint:gosec // executable test double in a test-owned directory
			t.Fatal(err)
		}
	}
	logPath := filepath.Join(root, "hermes.log")
	cmd := exec.Command(binary, c.Args...) //nolint:gosec // test-built CLI with controlled arguments
	cmd.Dir = root
	cmd.Env = []string{
		"PATH=" + bin + ":/usr/bin:/bin", "HOME=" + home, "TZ=UTC",
		"XDG_CONFIG_HOME=" + filepath.Join(home, "config"),
		"XDG_CACHE_HOME=" + filepath.Join(home, "cache"),
		"XDG_DATA_HOME=" + filepath.Join(home, "data"),
		"FAKE_HERMES_LOG=" + logPath, "FAKE_HERMES_MODE=" + c.Mode,
	}
	for k, v := range c.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdin = strings.NewReader(c.Stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	exitCode := 0
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("%s: %v", c.Name, err)
		}
		exitCode = exit.ExitCode()
	}
	c.ExitCode = exitCode
	c.Stdout = strings.ReplaceAll(stdout.String(), root, "<ROOT>")
	c.Stderr = strings.ReplaceAll(stderr.String(), root, "<ROOT>")
	if b, err := os.ReadFile(logPath); err == nil { //nolint:gosec // test-owned log
		c.Log = string(b)
	}
	return c
}
