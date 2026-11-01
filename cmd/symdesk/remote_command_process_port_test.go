package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
)

// CLI-008: the remote compatibility command allowlist of `symdesk serve`
// (`POST /api/v1/command`). The real Go binary serves a disposable vault on
// a loopback port, every accepted and rejected command vector is posted over
// HTTP and the status, content type, body and resulting vault files are
// recorded. `crates/symdesk-cli/tests/remote_command.rs` replays the same
// requests against the Rust server.

const remoteCommandFixturePath = "../../testdata/port/cli/remote-command-process.json"

// Synthetic credentials: the canary check requires that neither appears in a
// response body, and the server strips them from the spawned command.
const (
	remoteCommandAdminToken  = "remote-command-admin-token-0123456789abcdef"
	remoteCommandWorkerToken = "remote-command-worker-token-fedcba9876543210"
)

type remoteCommandStep struct {
	Name string `json:"name"`
	// Method defaults to POST; Auth is "admin" (default), "worker" or "none".
	Method string `json:"method,omitempty"`
	Auth   string `json:"auth,omitempty"`
	// Args and Stdin build the JSON request; RawBody replaces it entirely.
	Args    []string `json:"args,omitempty"`
	Stdin   string   `json:"stdin,omitempty"`
	RawBody *string  `json:"raw_body,omitempty"`
	// Compare is "exact" (default) or "decision": only whether the server
	// refused the command (HTTP 403) must match. Reason documents why.
	Compare string `json:"compare,omitempty"`
	Reason  string `json:"reason,omitempty"`
	// Observed result.
	Status      int      `json:"status"`
	ContentType string   `json:"content_type"`
	Body        string   `json:"body"`
	VaultFiles  []string `json:"vault_files"`
	// OutsideFiles lists files the command left beside the vault; a command
	// forced into the server vault never creates any.
	OutsideFiles []string `json:"outside_files"`
}

type remoteCommandCase struct {
	Name  string            `json:"name"`
	Files map[string]string `json:"files,omitempty"`
	// HomeFiles are written under the isolated HOME before the server starts
	// ($CLOSED_PORT is a loopback port nothing listens on).
	HomeFiles map[string]string   `json:"home_files,omitempty"`
	Steps     []remoteCommandStep `json:"steps"`
}

type remoteCommandFixture struct {
	SchemaVersion int                 `json:"schema_version"`
	Cases         []remoteCommandCase `json:"cases"`
}

const (
	remoteParserWords = "clap-generated flag/argument wording (CLI-003/CLI-004)"
	remoteHostState   = "output depends on host tools and environment"
)

func remoteArgs(name string, args ...string) remoteCommandStep {
	return remoteCommandStep{Name: name, Args: args}
}

func remotePending(name, reason string, args ...string) remoteCommandStep {
	return remoteCommandStep{Name: name, Args: args, Compare: "decision", Reason: reason}
}

func remoteRaw(name, auth, method, raw string) remoteCommandStep {
	return remoteCommandStep{Name: name, Auth: auth, Method: method, RawBody: &raw}
}

func remoteCommandVault() map[string]string {
	return map[string]string{
		"a.md":       "---\ntitle: Alpha\ntags: [x, y]\n---\nhello alpha world about rust\n",
		"b.md":       "---\ntitle: Beta\n---\nhello beta links to [[a]] and more\n",
		"notes/c.md": "---\ntitle: Gamma\n---\ngamma links to [[b]] and [[a]]\n",
	}
}

func remoteCommandCases() []remoteCommandCase {
	accepted := []remoteCommandStep{
		remoteArgs("ls", "ls"),
		remoteArgs("ls-dir", "ls", "--dir", "notes"),
		remoteArgs("search", "search", "hello"),
		remoteArgs("backlinks", "backlinks", "a.md"),
		remoteArgs("graph", "graph"),
		remoteArgs("similar", "similar", "a.md"),
		remoteArgs("duplicates", "duplicates"),
		remoteArgs("duplicates-similar", "duplicates", "similar", "a.md"),
		remoteArgs("docs-list", "docs", "list"),
		remoteArgs("docs-review", "docs", "review"),
		remoteArgs("history-list", "history", "a.md"),
		remoteArgs("history-prune", "history", "prune"),
		remoteArgs("history-show-missing", "history", "show", "deadbeef"),
		remoteArgs("restore-missing", "restore", "a.md"),
		remoteArgs("relations-inverse", "relations", "inverse", "a.md"),
		remoteArgs("note-new", "note", "new", "--title", "remote-note"),
		remoteArgs("note-move", "note", "move", "remote-note.md", "remote-moved.md"),
		remoteArgs("note-daily", "note", "daily", "--date", "2026-01-02"),
		remoteArgs("note-delete", "note", "delete", "remote-moved.md"),
		remoteArgs("trash-list", "trash", "list"),
		remoteArgs("trash-restore", "trash", "restore", "remote-moved.md"),
		remoteArgs("doc-status", "doc", "status", "a.md", "done"),
		remoteArgs("doc-due", "doc", "due", "a.md", "2026-12-31"),
		remoteArgs("doc-type", "doc", "type", "a.md", "invoice"),
		remoteArgs("doc-correspondent", "doc", "correspondent", "a.md", "ACME"),
		remoteArgs("doc-tag-add", "doc", "tag", "add", "remote", "a.md"),
		remoteArgs("doc-tag-remove", "doc", "tag", "remove", "remote", "a.md"),
		remoteArgs("doc-asn", "doc", "asn", "a.md", "next"),
		remoteArgs("conflict-resolve-missing", "conflict", "resolve", "missing.md"),
		remoteArgs("views-list", "views", "list"),
		remoteArgs("views-get-missing", "views", "get", "missing"),
		remoteArgs("views-siblings", "views", "siblings", "a.md"),
		remoteArgs("props-get", "props", "get", "a.md"),
		remoteArgs("props-edit", "props", "edit", "a.md", "k", "v"),
		remoteArgs("tags-rename", "tags", "rename", "x", "y"),
		remoteArgs("tags-merge", "tags", "merge", "x", "y"),
		remoteArgs("tags-delete", "tags", "delete", "x"),
		remoteArgs("paperless-import", "paperless", "import", "/nonexistent-paperless-export"),
		remoteArgs("trash-delete", "trash", "delete", "missing"),
		remoteArgs("views-save", "views", "save", "{}"),
		remoteArgs("views-delete", "views", "delete", "missing"),
		remoteArgs("views-new-entry", "views", "new-entry", "missing", "T"),
		remoteArgs("views-exec", "views", "exec", "missing"),
		remotePending("doctor", remoteHostState, "doctor"),
		{Name: "transform-stream", Args: []string{"transform", "summarize", "--text", "short input"}},
		{Name: "transform-stdin", Args: []string{"transform", "summarize"}, Stdin: "short stdin input"},
		remoteArgs("ask-stream", "ask", "hello"),
		remoteArgs("explicit-json-flag", "ls", "--json"),
		remoteArgs("json-flag-twice", "ls", "--json", "--json"),
		remoteArgs("unknown-flag-accepted-by-validation", "ls", "--bogus"),
		remoteArgs("history-flag-position-accepted", "history", "--bogus"),
		remoteArgs("missing-arguments-accepted-by-validation", "backlinks"),
	}

	var rejected []remoteCommandStep
	for _, command := range [][]string{
		{"not-real"}, {"config", "get"}, {"config", "set", "vault", "/tmp"}, {"mcp"}, {"serve"}, {"worker"},
		{"index"}, {"index", "status"}, {"index", "maintenance", "relocate", "/tmp/x"}, {"sources", "list"},
		{"sources", "add", "/etc"}, {"notebook", "list"}, {"dataset", "list"}, {"export"}, {"ingest"},
		{"ingest", "jobs"}, {"perm", "user", "list"}, {"retention", "list"}, {"recipe", "validate", "x"},
		{"events"}, {"version"}, {"vault", "health"}, {"ai-config", "show"}, {"mail", "status"},
		{"meeting", "list"}, {"asset", "store", "x"}, {"completion", "bash"}, {"help"}, {"demo", "init"},
		{"clip"}, {"consume", "status"}, {"sidecar", "list"}, {"ai", "autofill"}, {"rules", "list"},
		{"transform-x"}, {"Note", "new", "x"}, {"LS"}, {" ls"}, {"ls "}, {""}, {"-h"}, {"--help"}, {"--json", "ls"},
		{"--vault"}, {"-"}, {"--"},
	} {
		rejected = append(rejected, remoteArgs("reject-"+strings.Join(command, "_"), command...))
	}
	for _, command := range [][]string{
		{"note"}, {"note", "bogus"}, {"note", "--json"}, {"note", "", "new"}, {"note", "NEW", "x"},
		{"doc"}, {"doc", "bogus"}, {"doc", "-x"},
		{"views"}, {"views", "base", "list"}, {"views", "embed", "x"}, {"views", "export-csv", "x"}, {"views", "import-csv", "x"},
		{"relations"}, {"relations", "related", "x"}, {"relations", "contact", "link"},
		{"docs"}, {"docs", "status", "a.md", "x"}, {"docs", "tag", "add", "a.md", "x"}, {"docs", "asn", "a.md", "next"},
		{"tags"}, {"tags", "x"}, {"conflict"}, {"conflict", "x"}, {"history", "nope"},
		{"trash"}, {"trash", "purge"}, {"trash", "nope"}, {"paperless"}, {"paperless", "migrate"},
		{"props"}, {"props", "x"}, {"props", "--json"},
	} {
		rejected = append(rejected, remoteArgs("reject-sub-"+strings.Join(command, "_"), command...))
	}
	// The "any argument" entries accept every subcommand-looking word.
	accepted = append(accepted,
		remoteArgs("history-tasks", "history", "tasks"),
		remoteArgs("doc-tag-group-help", "doc", "tag"),
		remoteArgs("history-checkpoint", "history", "checkpoint", "t"),
		remoteArgs("history-undo-task", "history", "undo-task", "t"),
		remoteArgs("search-export", "search", "export", "x"),
		remoteArgs("search-notebook", "search", "notebook", "x"),
		remoteArgs("ls-with-subcommand-like-word", "ls", "export"),
		remoteArgs("similar-with-subcommand-like-word", "similar", "a.md", "extra"),
	)

	forced := []remoteCommandStep{
		remoteArgs("ls-forced-vault", "ls"),
		remoteArgs("vault-flag", "ls", "--vault", "/tmp"),
		remoteArgs("vault-flag-equals", "ls", "--vault=/tmp/other-vault"),
		remoteArgs("vault-flag-upper", "ls", "--VAULT", "/tmp"),
		remoteArgs("vault-flag-mixed", "ls", "--Vault=/tmp"),
		remoteArgs("vault-flag-first", "--vault", "/tmp", "ls"),
		remoteArgs("vault-flag-after-terminator", "ls", "--", "--vault", "/tmp"),
		remoteArgs("vault-flag-inside-ask", "ask", "--vault=/tmp/other-vault"),
		remoteArgs("vault-flag-note-new", "note", "new", "--title", "x", "--vault", "/tmp"),
		remoteArgs("output-flag", "ls", "--output", "yaml"),
		remoteArgs("output-flag-equals", "ls", "--output=json"),
		remoteArgs("output-flag-upper", "ls", "--OUTPUT=json"),
		remoteArgs("output-flag-search", "search", "hello", "--output", "text"),
		remoteArgs("vault-and-unknown-command", "nope", "--vault", "/tmp"),
		remoteArgs("note-new-traversal", "note", "new", "--title", "../escape"),
		remoteArgs("note-new-absolute", "note", "new", "--title", "$ROOT/outside/absolute-escape"),
		remoteArgs("note-move-traversal", "note", "move", "a.md", "../escape-move.md"),
		remoteArgs("note-delete-traversal", "note", "delete", "../escape.md"),
		remoteArgs("similar-traversal", "similar", "../escape.md"),
		remoteArgs("note-new-inside", "note", "new", "--title", "inside"),
		remoteArgs("ls-after-write", "ls"),
	}

	envelope := []remoteCommandStep{
		remoteRaw("missing-auth", "none", "POST", `{"arguments":["ls"]}`),
		remoteRaw("worker-token", "worker", "POST", `{"arguments":["ls"]}`),
		remoteRaw("wrong-method-get", "admin", "GET", ""),
		remoteRaw("wrong-method-put", "admin", "PUT", `{"arguments":["ls"]}`),
		remoteRaw("empty-body", "admin", "POST", ``),
		remoteRaw("malformed-json", "admin", "POST", `{`),
		remoteRaw("not-an-object", "admin", "POST", `[]`),
		remoteRaw("null-body", "admin", "POST", `null`),
		remoteRaw("arguments-missing", "admin", "POST", `{}`),
		remoteRaw("arguments-null", "admin", "POST", `{"arguments":null}`),
		remoteRaw("arguments-empty", "admin", "POST", `{"arguments":[]}`),
		remoteRaw("arguments-string", "admin", "POST", `{"arguments":"ls"}`),
		remoteRaw("arguments-number-element", "admin", "POST", `{"arguments":["ls",1]}`),
		remoteRaw("arguments-null-element", "admin", "POST", `{"arguments":[null]}`),
		remoteRaw("unknown-field", "admin", "POST", `{"arguments":["ls"],"extra":true}`),
		remoteRaw("stdin-number", "admin", "POST", `{"arguments":["ls"],"stdin":1}`),
		remoteRaw("trailing-data", "admin", "POST", `{"arguments":["ls"]}{"arguments":["ls"]}`),
		remoteRaw("ok-with-whitespace", "admin", "POST", " \n{\"arguments\":[\"ls\"]}\n"),
		remoteRaw("case-insensitive-keys", "admin", "POST", `{"ARGUMENTS":["ls"],"Stdin":""}`),
		remoteRaw("duplicate-keys", "admin", "POST", `{"arguments":["nope"],"arguments":["ls"]}`),
		remoteRaw("body-too-large", "admin", "POST", `{"arguments":["ls"],"stdin":"`+strings.Repeat("x", 2<<20)+`"}`),
		remoteArgs("failing-command-envelope", "restore", "missing.md"),
		remoteArgs("failing-command-note", "note", "delete", "missing.md"),
		remoteArgs("empty-output-command", "history", "prune"),
	}
	// Commands the Rust CLI does not implement yet cannot reproduce Go's
	// output; they run on their own server so they cannot disturb the vault
	// state compared by the exact cases.
	var exact, pending []remoteCommandStep
	for _, step := range accepted {
		if step.Compare == "decision" {
			pending = append(pending, step)
		} else {
			exact = append(exact, step)
		}
	}
	return []remoteCommandCase{
		{Name: "allowlist-accepted", Files: remoteCommandVault(), Steps: exact},
		{Name: "allowlist-accepted-decision-only", Files: remoteCommandVault(), Steps: pending},
		{Name: "allowlist-rejected", Files: remoteCommandVault(), Steps: rejected},
		{Name: "forced-vault-and-paths", Files: remoteCommandVault(), Steps: forced},
		{Name: "request-envelope", Files: remoteCommandVault(), Steps: envelope},
	}
}

func TestPortRemoteCommandProcessContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("remote command fixture is recorded and replayed on Unix hosts")
	}
	fixture := observeRemoteCommandProcess(t)
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source path unavailable")
	}
	path := filepath.Join(filepath.Dir(source), remoteCommandFixturePath)
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
		t.Fatalf("remote command fixture is stale; regenerate through portgen\nfirst difference at byte %d\ncommitted: %q\nobserved:  %q", offset, string(current[from:min(offset+160, len(current))]), string(encoded[from:min(offset+160, len(encoded))]))
	}
}

func observeRemoteCommandProcess(t *testing.T) remoteCommandFixture {
	t.Helper()
	binary := processFixtureBinary(t)
	fixture := remoteCommandFixture{SchemaVersion: 1}
	for _, c := range remoteCommandCases() {
		fixture.Cases = append(fixture.Cases, observeRemoteCommandCase(t, binary, c))
	}
	return fixture
}

func observeRemoteCommandCase(t *testing.T, binary string, c remoteCommandCase) remoteCommandCase {
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
	for name, content := range c.Files {
		target := filepath.Join(vault, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if c.HomeFiles == nil {
		c.HomeFiles = cliErrorsHomeFiles()
	}
	writeCLIErrorsHomeFiles(t, home, c.HomeFiles, closedLoopbackPort(t), startCLIErrorsEmbeddingServer(t).URL)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()

	environment := cleanDuplicatesEnv(os.Environ())
	environment = append(environment, "TZ=UTC", "HOME="+home, "USERPROFILE="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, "config"),
		"XDG_CACHE_HOME="+filepath.Join(home, "cache"),
		"XDG_DATA_HOME="+filepath.Join(home, "data"),
		"SYMDESK_SERVER_TOKEN="+remoteCommandAdminToken,
		"SYMDESK_WORKER_TOKEN="+remoteCommandWorkerToken)
	server := exec.Command(binary, "--vault", vault, "serve", "--listen", address) //nolint:gosec // test-only fixed helper binary
	server.Dir = cwd
	server.Env = environment
	var serverLog bytes.Buffer
	server.Stdout, server.Stderr = &serverLog, &serverLog
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = server.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = server.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = server.Process.Kill()
			<-done
		}
	}()
	waitForRemoteServer(t, address, &serverLog)

	client := &http.Client{Timeout: 2 * time.Minute}
	observed := c
	observed.Steps = nil
	for _, step := range c.Steps {
		materialized := step
		materialized.Args = nil
		for _, argument := range step.Args {
			materialized.Args = append(materialized.Args, materializePortProcessArg(argument, root, string(filepath.Separator)))
		}
		status, contentType, body := postRemoteCommand(t, client, address, materialized)
		for _, canary := range []string{remoteCommandAdminToken, remoteCommandWorkerToken} {
			if strings.Contains(body, canary) {
				t.Fatalf("response of %s/%s contains a credential canary", c.Name, step.Name)
			}
		}
		step.Status = status
		step.ContentType = contentType
		step.Body = remoteDateRE.ReplaceAllString(normalizeCLIErrors(body, root), "$$DATE.md")
		if step.Compare == "decision" && status != http.StatusForbidden {
			// Decision-only steps record that the allowlist accepted the
			// command; the body of a command Rust does not implement yet may
			// depend on the recording host (tools on PATH, OS wording).
			step.Status, step.ContentType, step.Body = http.StatusOK, "", ""
		}
		step.VaultFiles = remoteVaultFiles(t, vault)
		step.OutsideFiles = remoteOutsideFiles(t, root)
		for i := range step.VaultFiles {
			step.VaultFiles[i] = remoteDateRE.ReplaceAllString(step.VaultFiles[i], "$$DATE.md")
		}
		observed.Steps = append(observed.Steps, step)
	}
	return observed
}

// remoteDateRE hides the current date in `note daily` file names.
var remoteDateRE = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}\.md`)

func waitForRemoteServer(t *testing.T, address string, log *bytes.Buffer) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		connection, err := net.DialTimeout("tcp", address, 200*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("server did not listen on %s: %s", address, log.String())
}

func postRemoteCommand(t *testing.T, client *http.Client, address string, step remoteCommandStep) (int, string, string) {
	t.Helper()
	method := step.Method
	if method == "" {
		method = http.MethodPost
	}
	var body []byte
	if step.RawBody != nil {
		body = []byte(*step.RawBody)
	} else {
		payload := map[string]any{"arguments": step.Args}
		if step.Stdin != "" {
			payload["stdin"] = step.Stdin
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		body = encoded
	}
	request, err := http.NewRequestWithContext(context.Background(), method, "http://"+address+"/api/v1/command", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	switch step.Auth {
	case "none":
	case "worker":
		request.Header.Set("Authorization", "Bearer "+remoteCommandWorkerToken)
	default:
		request.Header.Set("Authorization", "Bearer "+remoteCommandAdminToken)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, step.Name, err)
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read %s: %v", step.Name, err)
	}
	return response.StatusCode, response.Header.Get("Content-Type"), string(data)
}

// remoteOutsideFiles lists everything below the case root except the vault,
// the isolated home and the working directory.
func remoteOutsideFiles(t *testing.T, root string) []string {
	t.Helper()
	files := []string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		switch relative {
		case ".":
			return nil
		case "vault", "home", "cwd":
			return filepath.SkipDir
		}
		if !entry.IsDir() {
			files = append(files, filepath.ToSlash(relative))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	return files
}

// remoteVaultFiles lists the vault's files, skipping the server's hidden
// state, so the effect of a forced-vault command is part of the contract.
func remoteVaultFiles(t *testing.T, vault string) []string {
	t.Helper()
	files := []string{}
	err := filepath.WalkDir(vault, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, relErr := filepath.Rel(vault, path)
		if relErr != nil {
			return relErr
		}
		if entry.IsDir() {
			if strings.HasPrefix(entry.Name(), ".") && relative != "." {
				return filepath.SkipDir
			}
			return nil
		}
		files = append(files, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	return files
}
