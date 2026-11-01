package main

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"
)

// RUST-013 slice r013-ingestcmds: the native bodies of the ingest, mail,
// meeting, rules and clip command families. Every step runs the real Go
// `symdesk` process in an isolated HOME/XDG/cwd with a disposable vault and
// records exit code, stdout and stderr; cases with SnapshotVault /
// SnapshotHome additionally record the vault tree / the text files below the
// case HOME after the step, so the files a command changed are compared as
// well. Loopback fakes (embedding service, IMAP) replace every network
// peer; no real service or operator credential is involved. The Rust replay is
// `crates/symdesk-cli/tests/cli_ingest_bodies.rs`. This fixture is separate
// from cli-bodies-process.json.

const cliIngestFixturePath = "../../testdata/port/cli/cli-ingest-bodies-process.json"

type cliIngestStep struct {
	Args     []string `json:"args"`
	Stdin    string   `json:"stdin,omitempty"`
	ExitCode int      `json:"exit_code"`
	Stdout   string   `json:"stdout"`
	Stderr   string   `json:"stderr"`
	// Vault is the vault tree after the step (SnapshotVault cases only).
	Vault map[string]string `json:"vault,omitempty"`
	// Home holds the text files below the case HOME after the step
	// (SnapshotHome cases only).
	Home map[string]string `json:"home,omitempty"`
}

type cliIngestCase struct {
	Name          string            `json:"name"`
	Files         map[string]string `json:"files,omitempty"`
	CwdFiles      map[string]string `json:"cwd_files,omitempty"`
	HomeFiles     map[string]string `json:"home_files,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
	Servers       []string          `json:"servers,omitempty"`
	SkipIndex     bool              `json:"skip_index,omitempty"`
	NoVault       bool              `json:"no_vault,omitempty"`
	SnapshotVault bool              `json:"snapshot_vault,omitempty"`
	SnapshotHome  bool              `json:"snapshot_home,omitempty"`
	Canaries      []string          `json:"canaries,omitempty"`
	Steps         []cliIngestStep   `json:"steps"`
}

type cliIngestFixture struct {
	SchemaVersion int             `json:"schema_version"`
	Cases         []cliIngestCase `json:"cases"`
}

var cliIngestCaseBuilders []func() []cliIngestCase

func cliIngestCases() []cliIngestCase {
	var cases []cliIngestCase
	for _, build := range cliIngestCaseBuilders {
		cases = append(cases, build()...)
	}
	return cases
}

func ingestStep(args ...string) cliIngestStep { return cliIngestStep{Args: args} }
func ingestStdin(stdin string, args ...string) cliIngestStep {
	return cliIngestStep{Args: args, Stdin: stdin}
}

// ingestBoth runs the same argv as text and as JSON.
func ingestBoth(args ...string) []cliIngestStep {
	return []cliIngestStep{{Args: args}, {Args: append([]string{"--json"}, args...)}}
}

func ingestConcat(groups ...[]cliIngestStep) []cliIngestStep {
	var out []cliIngestStep
	for _, group := range groups {
		out = append(out, group...)
	}
	return out
}

func TestPortCLIIngestBodiesProcessContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("CLI ingest bodies fixture is recorded and replayed on Unix hosts")
	}
	fixture := observeCLIIngestProcess(t)
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source path unavailable")
	}
	path := filepath.Join(filepath.Dir(source), cliIngestFixturePath)
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
		t.Fatalf("CLI ingest bodies fixture is stale; regenerate through portgen\nfirst difference at byte %d\ncommitted: %q\nobserved:  %q", offset, string(current[from:min(offset+160, len(current))]), string(encoded[from:min(offset+160, len(encoded))]))
	}
}

func observeCLIIngestProcess(t *testing.T) cliIngestFixture {
	t.Helper()
	binary := processFixtureBinary(t)
	fixture := cliIngestFixture{SchemaVersion: 1}
	for _, c := range cliIngestCases() {
		fixture.Cases = append(fixture.Cases, observeCLIIngestCase(t, binary, c))
	}
	return fixture
}

// snapshotIngestTree returns every regular UTF-8 file below dir (key: the
// slash-separated relative path), normalized; non-text files and SQLite
// stores are skipped because their bytes are not a portable contract.
func snapshotIngestTree(t *testing.T, dir, root, closedPort string, skip func(rel string) bool) map[string]string {
	t.Helper()
	snapshot := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if skip != nil && skip(rel) {
			return nil
		}
		data, err := os.ReadFile(path) //nolint:gosec // test-only walk below a temporary directory
		if err != nil {
			return err
		}
		if !utf8.Valid(data) {
			return nil
		}
		// The case's closed loopback port is random per run.
		snapshot[normalizeIngestText(rel, root)] = strings.ReplaceAll(normalizeIngestText(string(data), root), closedPort, "$CLOSED_PORT")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func ingestHomeSkip(rel string) bool {
	return strings.HasPrefix(rel, "cache/") || strings.HasPrefix(rel, "bin/") ||
		strings.Contains(rel, ".db") || strings.HasPrefix(rel, ".config/symseek/")
}

func observeCLIIngestCase(t *testing.T, binary string, c cliIngestCase) cliIngestCase {
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
	closedPort := closedLoopbackPort(t)
	embedURL := startCLIErrorsEmbeddingServer(t).URL
	placeholders := map[string]string{"$CLOSED_PORT": closedPort, "$EMBED_URL": embedURL}
	for _, name := range c.Servers {
		startIngestServer(t, name, placeholders)
	}
	replace := func(value string) string {
		keys := make([]string, 0, len(placeholders))
		for key := range placeholders {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
		for _, key := range keys {
			value = strings.ReplaceAll(value, key, placeholders[key])
		}
		return materializePortProcessArg(value, root, string(filepath.Separator))
	}
	writeTree := func(base string, files map[string]string) {
		for name, content := range files {
			target := filepath.Join(base, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				t.Fatal(err)
			}
			mode := os.FileMode(0o600)
			if strings.HasPrefix(name, "bin/") {
				mode = 0o700
			}
			if err := os.WriteFile(target, []byte(replace(content)), mode); err != nil {
				t.Fatal(err)
			}
		}
	}
	writeTree(vault, c.Files)
	writeTree(cwd, c.CwdFiles)
	homeFiles := c.HomeFiles
	if homeFiles == nil {
		homeFiles = cliErrorsHomeFiles()
	}
	writeTree(home, homeFiles)
	baseEnv := func() []string {
		environment := cleanDuplicatesEnv(os.Environ())
		return append(environment, "TZ=UTC", "HOME="+home, "USERPROFILE="+home,
			"XDG_CONFIG_HOME="+filepath.Join(home, "config"),
			"XDG_CACHE_HOME="+filepath.Join(home, "cache"),
			"XDG_DATA_HOME="+filepath.Join(home, "data"))
	}
	environment := baseEnv()
	for name, value := range c.Env {
		environment = append(environment, name+"="+replace(value))
	}
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
	if !c.SkipIndex && !c.NoVault {
		if code, _, stderr := run(baseEnv(), []string{"--vault", vault, "index"}, ""); code != 0 {
			t.Fatalf("setup index for %s exited %d: %s", c.Name, code, stderr)
		}
	}
	observed := c
	observed.Steps = nil
	for _, step := range c.Steps {
		args := append([]string(nil), step.Args...)
		if !c.NoVault {
			args = append([]string{"--vault", vault}, args...)
		}
		for i := range args {
			args[i] = replace(args[i])
		}
		code, stdout, stderr := run(environment, args, replace(step.Stdin))
		for _, canary := range c.Canaries {
			if strings.Contains(stdout, canary) || strings.Contains(stderr, canary) {
				t.Fatalf("oracle leaked canary %q for %s %v", canary, c.Name, step.Args)
			}
		}
		recorded := cliIngestStep{
			Args:     step.Args,
			Stdin:    step.Stdin,
			ExitCode: code,
			Stdout:   normalizeIngestText(stdout, root),
			Stderr:   normalizeIngestText(stderr, root),
		}
		if c.SnapshotVault {
			recorded.Vault = snapshotIngestTree(t, vault, root, closedPort, nil)
		}
		if c.SnapshotHome {
			recorded.Home = snapshotIngestTree(t, home, root, closedPort, ingestHomeSkip)
		}
		observed.Steps = append(observed.Steps, recorded)
	}
	return observed
}

// startIngestServer starts the named loopback fake and registers the
// placeholders (replaced in argv, stdin, env and file contents) it provides.
func startIngestServer(t *testing.T, name string, placeholders map[string]string) {
	t.Helper()
	for _, start := range ingestServerStarters {
		if start.name == name {
			start.run(t, placeholders)
			return
		}
	}
	t.Fatalf("unknown ingest fake server %q", name)
}

type ingestServerStarter struct {
	name string
	run  func(t *testing.T, placeholders map[string]string)
}

var ingestServerStarters []ingestServerStarter

// ingestTomlRE hides the parser-specific tail of a TOML syntax error: the Go
// (BurntSushi) and Rust parsers word them differently, so only the failing
// line is part of the contract (declared limit).
var ingestTomlRE = regexp.MustCompile(`toml: line \d+[^\n]*`)

func normalizeIngestText(text, root string) string {
	text = normalizeBodiesText(false, normalizeCLIErrors(text, root))
	return ingestTomlRE.ReplaceAllString(text, "toml: line $$TOML")
}
