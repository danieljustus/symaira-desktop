package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// CLI-005 / CLI-006: output modes and the error taxonomy of the real Go
// `symdesk` process. Every step runs the oracle binary in an isolated
// HOME/XDG/cwd with a disposable vault and records exit code, stdout and
// stderr. The Rust replay (`crates/symdesk-cli/tests/cli_errors.rs`) must match
// each byte after the same normalization.

const cliErrorsFixturePath = "../../testdata/port/cli/cli-errors-process.json"

type cliErrorsStep struct {
	Args     []string `json:"args"`
	Stdin    string   `json:"stdin,omitempty"`
	ExitCode int      `json:"exit_code"`
	Stdout   string   `json:"stdout"`
	Stderr   string   `json:"stderr"`
}

type cliErrorsCase struct {
	Name string `json:"name"`
	// Files are written under the vault before `symdesk index` runs.
	Files map[string]string `json:"files,omitempty"`
	// PostIndexFiles are written after indexing.
	PostIndexFiles map[string]string `json:"post_index_files,omitempty"`
	// SkipIndex leaves the sidecar empty.
	SkipIndex bool `json:"skip_index,omitempty"`
	// NoVault runs without an injected --vault and without a configured vault.
	NoVault bool `json:"no_vault,omitempty"`
	// UnixOnly marks error text that uses the operating system's wording.
	UnixOnly bool `json:"unix_only,omitempty"`
	// HomeFiles are written under the isolated HOME before anything runs.
	HomeFiles map[string]string `json:"home_files,omitempty"`
	// Env is added to the isolated process environment after the SYMDESK_*
	// cleanup. Values in Canaries must never appear in any output.
	Env      map[string]string `json:"env,omitempty"`
	Canaries []string          `json:"canaries,omitempty"`
	// $CLOSED_PORT in env values and arguments is replaced by a loopback port
	// that nothing listens on.
	Steps []cliErrorsStep `json:"steps"`
}

type cliErrorsFixture struct {
	SchemaVersion int             `json:"schema_version"`
	Cases         []cliErrorsCase `json:"cases"`
}

func cliErrorsVault() map[string]string {
	return map[string]string{
		"a.md":         "---\ntitle: Alpha\ntags: [x, y]\n---\nhello alpha world about rust\n",
		"b.md":         "---\ntitle: Beta\n---\nhello beta links to [[a]] and more\n",
		"notes/c.md":   "---\ntitle: Gamma\n---\ngamma links to [[b]] and [[a]]\n",
		"plain.md":     "no frontmatter at all, just words about gamma\n",
		"notes/d.md":   "---\ntitle: Delta\ntags: [y]\n---\ndelta text\n",
		"unicode-é.md": "---\ntitle: Ünï <&> “quoted”\n---\nunicode body hello\n",
	}
}

// outputForms are the observable output switches. Prefix and suffix positions
// are both exercised because the flags are persistent.
func cliErrorsOutputForms() [][]string {
	return [][]string{
		nil,
		{"--json"},
		{"--output", "json"},
		{"--output=json"},
		{"--output", "yaml"},
		{"--output=yaml"},
		{"--output", "text"},
		{"--output", "text", "--json"},
		{"--json", "--output", "text"},
		{"--output", "JSON"},
		{"--output", ""},
		{"--output", "bogus"},
		{"--output=xml"},
	}
}

func cliErrorsEveryForm(args ...string) []cliErrorsStep {
	var steps []cliErrorsStep
	for _, form := range cliErrorsOutputForms() {
		steps = append(steps, cliErrorsStep{Args: append(append([]string(nil), form...), args...)})
		if len(form) > 0 {
			steps = append(steps, cliErrorsStep{Args: append(append([]string(nil), args...), form...)})
		}
	}
	return steps
}

func cliErrorsText(args ...string) cliErrorsStep { return cliErrorsStep{Args: args} }
func cliErrorsJSON(args ...string) cliErrorsStep {
	return cliErrorsStep{Args: append([]string{"--json"}, args...)}
}

// cliErrorsBoth runs the same argv as text and as JSON.
func cliErrorsBoth(args ...string) []cliErrorsStep {
	return []cliErrorsStep{cliErrorsText(args...), cliErrorsJSON(args...)}
}

func cliErrorsConcat(groups ...[]cliErrorsStep) []cliErrorsStep {
	var out []cliErrorsStep
	for _, group := range groups {
		out = append(out, group...)
	}
	return out
}

// cliErrorsReadCommands are read-only commands with deterministic output on
// the fixed vault (timestamps and sidecar hashes are normalized).
func cliErrorsReadCommands() [][]string {
	return [][]string{
		{"search", "hello"},
		{"search", "zzzznomatch"},
		{"search", "rust"},
		{"backlinks", "a.md"},
		{"backlinks", "plain.md"},
		{"graph"},
		{"docs", "list"},
		{"docs", "review"},
		{"views", "list"},
		{"views", "base", "list"},
		{"notebook", "list"},
		{"trash", "list"},
		{"history", "a.md"},
		{"history", "tasks"},
		{"duplicates"},
		{"sources", "list"},
		{"retention", "list"},
		{"relations", "inverse", "a.md"},
		{"ls"},
	}
}

// cliErrorsVaultCommands name a valid invocation of every vault-reading or
// vault-writing command that Rust implements. The output switch, the vault
// selection and the argument count are exercised on all of them.
func cliErrorsVaultCommands() [][]string {
	return [][]string{
		{"ls"},
		{"search", "hello"},
		{"backlinks", "a.md"},
		{"graph"},
		{"duplicates"},
		{"similar", "a.md"},
		{"duplicates", "similar", "a.md"},
		{"docs", "list"},
		{"docs", "review"},
		{"docs", "status", "a.md", "reviewed"},
		{"history", "a.md"},
		{"history", "tasks"},
		{"history", "show", "a.md"},
		{"history", "prune"},
		{"restore", "a.md"},
		{"trash", "list"},
		{"trash", "restore", "a.md"},
		{"trash", "purge"},
		{"note", "new", "n.md"},
		{"note", "move", "a.md", "z.md"},
		{"note", "delete", "a.md"},
		{"doc", "status", "a.md", "reviewed"},
		{"notebook", "list"},
		{"notebook", "show", "nb"},
		{"notebook", "new", "nb"},
		{"views", "list"},
		{"views", "get", "v"},
		{"views", "siblings", "a.md"},
		{"views", "base", "list"},
		{"views", "base", "get", "b"},
		{"retention", "list"},
		{"retention", "eval"},
		{"sources", "list"},
		{"sources", "add", "$ROOT/home"},
		{"sources", "remove", "x"},
		{"index"},
		{"index", "retry"},
		{"relations", "inverse", "a.md"},
		{"recipe", "validate", "nope.yaml"},
		{"conflict", "resolve", "a.md"},
	}
}

func cliErrorsCases() []cliErrorsCase {
	vault := cliErrorsVault()
	var cases []cliErrorsCase

	// Output modes of read commands: every switch spelling and position.
	var outputSteps []cliErrorsStep
	for _, command := range cliErrorsReadCommands() {
		outputSteps = append(outputSteps, cliErrorsEveryForm(command...)...)
	}
	cases = append(cases, cliErrorsCase{Name: "output-modes-read", Files: vault, Steps: outputSteps})

	// Output modes of write commands, each form against a fresh state.
	var writeSteps []cliErrorsStep
	for i, form := range cliErrorsOutputForms() {
		name := fmt.Sprintf("w%d.md", i)
		moved := fmt.Sprintf("moved%d.md", i)
		writeSteps = append(writeSteps,
			cliErrorsStep{Args: append(append([]string(nil), form...), "note", "new", name)},
			cliErrorsStep{Args: append(append([]string(nil), form...), "note", "move", name, moved)},
			cliErrorsStep{Args: append(append([]string(nil), form...), "note", "delete", moved)},
			cliErrorsStep{Args: append(append([]string(nil), form...), "trash", "list")},
			cliErrorsStep{Args: append(append([]string(nil), form...), "doc", "status", "a.md", "reviewed")},
			cliErrorsStep{Args: append(append([]string(nil), form...), "docs", "due", "b.md", "2026-12-31")},
		)
	}
	cases = append(cases, cliErrorsCase{Name: "output-modes-write", Files: vault, Steps: writeSteps})

	// An invalid --output wins over everything the command would have done.
	var invalid []cliErrorsStep
	for _, command := range cliErrorsVaultCommands() {
		invalid = append(invalid,
			cliErrorsStep{Args: append([]string{"--output", "bogus"}, command...)},
			cliErrorsStep{Args: append(append([]string(nil), command...), "--output=bogus")},
			cliErrorsStep{Args: append([]string{"--json", "--output", "bogus"}, command...)},
		)
	}
	cases = append(cases, cliErrorsCase{Name: "invalid-output-everywhere", Files: vault, Steps: invalid})

	// No vault configured anywhere.
	var noVault []cliErrorsStep
	for _, command := range cliErrorsVaultCommands() {
		noVault = append(noVault, cliErrorsBoth(command...)...)
	}
	cases = append(cases, cliErrorsCase{Name: "no-vault-configured", NoVault: true, SkipIndex: true, Steps: noVault})

	// Vault named by --vault that is missing or not a directory.
	var badVault []cliErrorsStep
	for _, command := range cliErrorsVaultCommands() {
		for _, vaultArg := range []string{"$ROOT/nope", "$ROOT/vault/a.md"} {
			badVault = append(badVault, cliErrorsBoth(append([]string{"--vault", vaultArg}, command...)...)...)
		}
	}
	cases = append(cases, cliErrorsCase{Name: "bad-vault-path", NoVault: true, UnixOnly: true, SkipIndex: true,
		Files: map[string]string{"a.md": "---\ntitle: A\n---\nbody\n"}, Steps: badVault})

	// Vault selection from SYMDESK_VAULT (valid and invalid) and from the flag.
	var envVault []cliErrorsStep
	for _, command := range [][]string{{"ls"}, {"search", "hello"}, {"duplicates"}, {"docs", "list"}, {"graph"}, {"backlinks", "a.md"}} {
		envVault = append(envVault, cliErrorsBoth(command...)...)
	}
	cases = append(cases, cliErrorsCase{Name: "vault-from-environment", NoVault: true, Files: vault,
		Env: map[string]string{"SYMDESK_VAULT": "$ROOT/vault"}, Steps: envVault})
	cases = append(cases, cliErrorsCase{Name: "vault-from-environment-missing", NoVault: true, UnixOnly: true, SkipIndex: true,
		Env: map[string]string{"SYMDESK_VAULT": "$ROOT/missing-vault"}, Steps: envVault})
	cases = append(cases, cliErrorsCase{Name: "vault-from-environment-overridden", NoVault: true, Files: vault,
		Env: map[string]string{"SYMDESK_VAULT": "$ROOT/missing-vault"},
		Steps: cliErrorsConcat(
			cliErrorsBoth("--vault", "$ROOT/vault", "ls"),
			cliErrorsBoth("--vault", "$ROOT/vault", "search", "hello"),
			cliErrorsBoth("--vault=$ROOT/vault", "duplicates"),
		)})

	// Not-found targets (implemented commands only; unimplemented Rust
	// commands are inventoried in docs/rust-port/cli-output-errors.md).
	cases = append(cases, cliErrorsCase{Name: "not-found", Files: vault, Steps: cliErrorsConcat(
		cliErrorsBoth("backlinks", "missing.md"),
		cliErrorsBoth("history", "show", "missing.md"),
		cliErrorsBoth("restore", "missing.md"),
		cliErrorsBoth("trash", "restore", "missing.md"),
		cliErrorsBoth("note", "move", "missing.md", "z.md"),
		cliErrorsBoth("note", "delete", "missing.md"),
		cliErrorsBoth("doc", "status", "missing.md", "reviewed"),
		cliErrorsBoth("docs", "status", "missing.md", "reviewed"),
		cliErrorsBoth("docs", "due", "missing.md", "2026-12-31"),
		cliErrorsBoth("notebook", "show", "missing"),
		cliErrorsBoth("notebook", "add-source", "missing", "a.md"),
		cliErrorsBoth("notebook", "remove-source", "missing", "a.md"),
		cliErrorsBoth("views", "get", "missing"),
		cliErrorsBoth("views", "base", "get", "missing"),
		cliErrorsBoth("views", "siblings", "missing.md"),
		cliErrorsBoth("conflict", "resolve", "missing.md"),
		cliErrorsBoth("recipe", "validate", "missing.yaml"),
		cliErrorsBoth("recipe", "run", "missing.yaml"),
		cliErrorsBoth("retention", "diff", "missing-run"),
		cliErrorsBoth("retention", "accept", "missing-run"),
		cliErrorsBoth("retention", "reject", "missing-run"),
		cliErrorsBoth("sources", "remove", "missing"),
		cliErrorsBoth("config", "get", "no.such.key"),
		cliErrorsBoth("similar", "missing.md"),
		cliErrorsBoth("duplicates", "similar", "missing.md"),
	)})

	// Conflicts: the target already exists or the state forbids the change.
	cases = append(cases, cliErrorsCase{Name: "conflict-and-state", Files: vault, Steps: cliErrorsConcat(
		cliErrorsBoth("note", "new", "a.md"),
		cliErrorsBoth("note", "move", "a.md", "b.md"),
		cliErrorsBoth("note", "move", "a.md", "a.md"),
		cliErrorsBoth("note", "new", "x/../../outside.md"),
		cliErrorsBoth("note", "new", "/abs.md"),
		cliErrorsBoth("note", "move", "a.md", "../outside.md"),
		cliErrorsBoth("note", "delete", "../outside.md"),
		cliErrorsBoth("note", "new", "n1.md"),
		cliErrorsBoth("note", "new", "n1.md"),
		cliErrorsBoth("note", "delete", "n1.md"),
		cliErrorsBoth("trash", "restore", "n1.md"),
		cliErrorsBoth("trash", "restore", "n1.md"),
		cliErrorsBoth("notebook", "new", "nb"),
		cliErrorsBoth("notebook", "new", "nb"),
		cliErrorsBoth("notebook", "show", "nb"),
		cliErrorsBoth("notebook", "add-source", "nb", "a.md"),
		cliErrorsBoth("notebook", "add-source", "nb", "a.md"),
		cliErrorsBoth("notebook", "add-source", "nb", "../outside.md"),
		cliErrorsBoth("notebook", "remove-source", "nb", "b.md"),
		cliErrorsBoth("doc", "status", "a.md", "not-a-status"),
		cliErrorsBoth("docs", "due", "a.md", "not-a-date"),
		cliErrorsBoth("docs", "due", "a.md", "2026-13-45"),
		cliErrorsBoth("sources", "add", "$ROOT/nope"),
		cliErrorsBoth("sources", "add", "$ROOT/vault/a.md"),
	)})

	// Missing provider, closed provider port and unknown provider are the
	// auth/external-tool part of the taxonomy.
	cases = append(cases, cliErrorsCase{Name: "ask-transform-without-provider", Files: vault, Steps: cliErrorsConcat(
		cliErrorsBoth("transform", "summarize", "--text", "short input"),
		cliErrorsBoth("transform", "summarize"),
		cliErrorsBoth("ask", "hello"),
		cliErrorsBoth("ask", "--vault", "$ROOT/nope", "hello"),
	)})
	cases = append(cases, cliErrorsCase{Name: "provider-unreachable", Files: vault,
		Env:      map[string]string{"SYMDESK_LLM_PROVIDER": "ollama", "SYMDESK_OLLAMA_URL": "http://127.0.0.1:$CLOSED_PORT", "SYMDESK_LLM_API_KEY": "cli-errors-canary-key-91c4"},
		Canaries: []string{"cli-errors-canary-key-91c4"},
		Steps: cliErrorsConcat(
			cliErrorsBoth("transform", "summarize", "--text", "short input"),
			cliErrorsBoth("ask", "hello"),
		)})
	cases = append(cases, cliErrorsCase{Name: "provider-unknown-and-missing-key", Files: vault,
		Env:      map[string]string{"SYMDESK_LLM_PROVIDER": "nonexistent-provider", "SYMDESK_LLM_API_KEY": "cli-errors-canary-key-77aa"},
		Canaries: []string{"cli-errors-canary-key-77aa"},
		Steps: cliErrorsConcat(
			cliErrorsBoth("transform", "summarize", "--text", "short input"),
			cliErrorsBoth("ask", "hello"),
		)})
	cases = append(cases, cliErrorsCase{Name: "provider-anthropic-without-key", Files: vault,
		Env: map[string]string{"SYMDESK_LLM_PROVIDER": "anthropic"},
		Steps: cliErrorsConcat(
			cliErrorsBoth("transform", "summarize", "--text", "short input"),
			cliErrorsBoth("ask", "hello"),
		)})

	// Credentials held in the environment must never reach stdout/stderr on
	// failing commands of any family.
	secretEnv := map[string]string{
		"SYMDESK_LLM_API_KEY":      "cli-errors-canary-llm-3f90",
		"SYMDESK_SERVER_TOKEN":     "cli-errors-canary-server-3f90",
		"SYMDESK_WORKER_TOKEN":     "cli-errors-canary-worker-3f90",
		"SYMDESK_ADMIN_TOKEN":      "cli-errors-canary-admin-3f90",
		"SYMDESK_ANTHROPIC_APIKEY": "cli-errors-canary-anthropic-3f90",
	}
	var canarySteps []cliErrorsStep
	for _, command := range cliErrorsVaultCommands() {
		canarySteps = append(canarySteps, cliErrorsBoth(command...)...)
	}
	canarySteps = append(canarySteps, cliErrorsBoth("config", "paths")...)
	canarySteps = append(canarySteps, cliErrorsBoth("config", "get", "llm_api_key")...)
	canarySteps = append(canarySteps, cliErrorsBoth("ai-config", "show")...)
	cases = append(cases, cliErrorsCase{Name: "secret-canary-failing-commands", Files: vault, Env: secretEnv,
		Canaries: []string{"cli-errors-canary-llm-3f90", "cli-errors-canary-server-3f90", "cli-errors-canary-worker-3f90", "cli-errors-canary-admin-3f90", "cli-errors-canary-anthropic-3f90"},
		Steps:    canarySteps})
	cases = append(cases, cliErrorsCase{Name: "secret-canary-no-vault", NoVault: true, SkipIndex: true, Env: secretEnv,
		Canaries: []string{"cli-errors-canary-llm-3f90", "cli-errors-canary-server-3f90", "cli-errors-canary-worker-3f90", "cli-errors-canary-admin-3f90", "cli-errors-canary-anthropic-3f90"},
		Steps:    cliErrorsConcat(cliErrorsBoth("ls"), cliErrorsBoth("search", "hello"), cliErrorsBoth("ask", "hello"), cliErrorsBoth("transform", "summarize", "--text", "x"))})

	// Unindexed vault: the same read commands without a sidecar.
	var unindexed []cliErrorsStep
	for _, command := range cliErrorsReadCommands() {
		unindexed = append(unindexed, cliErrorsBoth(command...)...)
	}
	cases = append(cases, cliErrorsCase{Name: "unindexed-vault", Files: vault, SkipIndex: true, Steps: unindexed})

	// Empty vault.
	var empty []cliErrorsStep
	for _, command := range cliErrorsReadCommands() {
		empty = append(empty, cliErrorsBoth(command...)...)
	}
	cases = append(cases, cliErrorsCase{Name: "empty-vault", Files: map[string]string{}, Steps: empty})

	return cases
}

var (
	cliErrorsTimeRE  = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})`)
	cliErrorsVaultRE = regexp.MustCompile(`(/vaults/)[0-9a-f]{16}`)
	cliErrorsPortRE  = regexp.MustCompile(`127\.0\.0\.1:\d+`)
	cliErrorsHashRE  = regexp.MustCompile(`folder-[0-9a-f]{64}`)
)

// cliErrorsSeedLogRE drops Go's one-time slog line for the legacy standalone
// retrieval-store migration; it depends on index-time state, not on the
// command under test (declared limit in the contract matrix).
var cliErrorsSeedLogRE = regexp.MustCompile(`(?m)^time=\S+ level=INFO msg="seeded per-vault retrieval index from standalone store"[^\n]*\n`)

func normalizeCLIErrors(text, root string) string {
	text = cliErrorsSeedLogRE.ReplaceAllString(text, "")
	text = normalizeRetryText(normalizeConflictLog(text), root)
	text = cliErrorsTimeRE.ReplaceAllString(text, "$$TIME")
	text = cliErrorsVaultRE.ReplaceAllString(text, "${1}$$VAULT_ID")
	text = cliErrorsHashRE.ReplaceAllString(text, "folder-$$HASH")
	return cliErrorsPortRE.ReplaceAllString(text, "127.0.0.1:$$PORT")
}

// cliErrorsHomeFiles points the retrieval engine's embeddings at a loopback
// fake so no ambient Ollama on the recording host can change search ranking
// or index output. $EMBED_URL (the fake) and $CLOSED_PORT (nothing listens)
// are replaced at run time.
func cliErrorsHomeFiles() map[string]string {
	return map[string]string{
		".config/symseek/config.toml": "ollama_url = \"$EMBED_URL/api/embeddings\"\nembedding_dim = 3\ntimeout_seconds = 2\nretry_count = 0\nmodel = \"fixture\"\n",
	}
}

// startCLIErrorsEmbeddingServer serves the deterministic fixture embedding
// vectors of the other retrieval process fixtures.
func startCLIErrorsEmbeddingServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		data := make([]map[string]any, 0, len(body.Input))
		for _, input := range body.Input {
			data = append(data, map[string]any{"embedding": indexRetrievalVector(input)})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(server.Close)
	return server
}

func writeCLIErrorsHomeFiles(t *testing.T, home string, files map[string]string, closedPort, embedURL string) {
	t.Helper()
	for name, content := range files {
		target := filepath.Join(home, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		content = strings.ReplaceAll(content, "$CLOSED_PORT", closedPort)
		content = strings.ReplaceAll(content, "$EMBED_URL", embedURL)
		if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPortCLIErrorsProcessContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("CLI error fixture is recorded and replayed on Unix hosts")
	}
	fixture := observeCLIErrorsProcess(t)
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source path unavailable")
	}
	path := filepath.Join(filepath.Dir(source), cliErrorsFixturePath)
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded, 0o644); err != nil { //nolint:gosec // generated contract fixture is intentionally world-readable
			t.Fatal(err)
		}
		return
	}
	current, err := os.ReadFile(path) //nolint:gosec // test-only path is constrained by fixed fixture inputs
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, encoded) {
		offset := 0
		for offset < len(current) && offset < len(encoded) && current[offset] == encoded[offset] {
			offset++
		}
		from := max(offset-160, 0)
		t.Fatalf("CLI errors fixture is stale; regenerate through portgen\nfirst difference at byte %d\ncommitted: %q\nobserved:  %q", offset, string(current[from:min(offset+160, len(current))]), string(encoded[from:min(offset+160, len(encoded))]))
	}
}

func observeCLIErrorsProcess(t *testing.T) cliErrorsFixture {
	t.Helper()
	binary := processFixtureBinary(t)
	fixture := cliErrorsFixture{SchemaVersion: 1}
	for _, c := range cliErrorsCases() {
		fixture.Cases = append(fixture.Cases, observeCLIErrorsCase(t, binary, c))
	}
	return fixture
}

func observeCLIErrorsCase(t *testing.T, binary string, c cliErrorsCase) cliErrorsCase {
	t.Helper()
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	home := filepath.Join(root, "home")
	vault := filepath.Join(root, "vault")
	cwd := filepath.Join(root, "cwd")
	for _, directory := range []string{home, vault, cwd} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeFiles := func(files map[string]string) {
		for name, content := range files {
			target := filepath.Join(vault, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	writeFiles(c.Files)
	closedPort := closedLoopbackPort(t)
	if c.HomeFiles == nil {
		c.HomeFiles = cliErrorsHomeFiles()
	}
	writeCLIErrorsHomeFiles(t, home, c.HomeFiles, closedPort, startCLIErrorsEmbeddingServer(t).URL)
	materialize := func(value string) string {
		value = strings.ReplaceAll(value, "$CLOSED_PORT", closedPort)
		return materializePortProcessArg(value, root, string(filepath.Separator))
	}
	environment := cleanDuplicatesEnv(os.Environ())
	environment = append(environment, "TZ=UTC", "HOME="+home, "USERPROFILE="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, "config"),
		"XDG_CACHE_HOME="+filepath.Join(home, "cache"),
		"XDG_DATA_HOME="+filepath.Join(home, "data"))
	for name, value := range c.Env {
		environment = append(environment, name+"="+materialize(value))
	}
	setupEnvironment := cleanDuplicatesEnv(os.Environ())
	setupEnvironment = append(setupEnvironment, "TZ=UTC", "HOME="+home, "USERPROFILE="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, "config"),
		"XDG_CACHE_HOME="+filepath.Join(home, "cache"),
		"XDG_DATA_HOME="+filepath.Join(home, "data"))
	run := func(env []string, args []string, input string) (int, string, string) {
		cmd := exec.Command(binary, args...) //nolint:gosec // test-only command uses a fixed helper and controlled arguments
		cmd.Dir = cwd
		cmd.Stdin = strings.NewReader(input)
		cmd.Env = env
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		code := 0
		if err := cmd.Run(); err != nil {
			exit, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatalf("run Go CLI %v: %v", args, err)
			}
			code = exit.ExitCode()
		}
		return code, stdout.String(), stderr.String()
	}
	if !c.SkipIndex {
		if code, _, stderr := run(setupEnvironment, []string{"--vault", vault, "index"}, ""); code != 0 {
			t.Fatalf("setup index for %s exited %d: %s", c.Name, code, stderr)
		}
	}
	writeFiles(c.PostIndexFiles)
	observed := c
	observed.Steps = nil
	for _, step := range c.Steps {
		args := append([]string(nil), step.Args...)
		if !c.NoVault {
			args = append([]string{"--vault", vault}, args...)
		}
		for i := range args {
			args[i] = materialize(args[i])
		}
		code, stdout, stderr := run(environment, args, step.Stdin)
		for _, canary := range c.Canaries {
			if strings.Contains(stdout, canary) || strings.Contains(stderr, canary) {
				t.Fatalf("oracle leaked canary %q for %s %v", canary, c.Name, step.Args)
			}
		}
		observed.Steps = append(observed.Steps, cliErrorsStep{
			Args:     step.Args,
			Stdin:    step.Stdin,
			ExitCode: code,
			Stdout:   normalizeCLIErrors(stdout, root),
			Stderr:   normalizeCLIErrors(stderr, root),
		})
	}
	return observed
}

// closedLoopbackPort returns a port nothing listens on at call time.
func closedLoopbackPort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	_, port, found := strings.Cut(address, ":")
	if !found {
		t.Fatalf("unexpected listener address %q", address)
	}
	return port
}
