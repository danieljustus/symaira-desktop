package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// RUST-013 closure: the command bodies that the Rust CLI gained after the
// Cobra front half (props, tags, history checkpoint/undo-task, search
// export/notebook, trash, views, relations, ...). Every step runs the real Go
// `symdesk` process in an isolated HOME/XDG/cwd with a disposable vault and
// records exit code, stdout and stderr; steps of a case with SnapshotVault
// additionally record the whole vault tree after the step, so the files a
// command changed are compared as well. The Rust replay is
// `crates/symdesk-cli/tests/cli_bodies.rs`.

const cliBodiesFixturePath = "../../testdata/port/cli/cli-bodies-process.json"

type cliBodiesStep struct {
	Args     []string `json:"args"`
	Stdin    string   `json:"stdin,omitempty"`
	ExitCode int      `json:"exit_code"`
	Stdout   string   `json:"stdout"`
	Stderr   string   `json:"stderr"`
	// Vault is the vault tree after the step (SnapshotVault cases only).
	Vault map[string]string `json:"vault,omitempty"`
}

type cliBodiesCase struct {
	Name           string            `json:"name"`
	Files          map[string]string `json:"files,omitempty"`
	PostIndexFiles map[string]string `json:"post_index_files,omitempty"`
	SkipIndex      bool              `json:"skip_index,omitempty"`
	NoVault        bool              `json:"no_vault,omitempty"`
	UnixOnly       bool              `json:"unix_only,omitempty"`
	HomeFiles      map[string]string `json:"home_files,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	Canaries       []string          `json:"canaries,omitempty"`
	SnapshotVault  bool              `json:"snapshot_vault,omitempty"`
	// VolatileHistory hides the 64-digit content ids of the history store: the
	// case writes files that embed the wall clock (for example `created:`).
	VolatileHistory bool            `json:"volatile_history,omitempty"`
	Steps           []cliBodiesStep `json:"steps"`
}

type cliBodiesFixture struct {
	SchemaVersion int             `json:"schema_version"`
	Cases         []cliBodiesCase `json:"cases"`
}

// bodiesJSON runs the argv as JSON only (text output that prints Go heap
// addresses cannot be compared).
func bodiesJSON(args ...string) []cliBodiesStep {
	return []cliBodiesStep{{Args: append([]string{"--json"}, args...)}}
}

func bodiesStepOf(args ...string) cliBodiesStep { return cliBodiesStep{Args: args} }
func bodiesStdin(stdin string, args ...string) cliBodiesStep {
	return cliBodiesStep{Args: args, Stdin: stdin}
}

// bodiesBoth runs the same argv as text and as JSON.
func bodiesBoth(args ...string) []cliBodiesStep {
	return []cliBodiesStep{
		{Args: args},
		{Args: append([]string{"--json"}, args...)},
	}
}

func bodiesConcat(groups ...[]cliBodiesStep) []cliBodiesStep {
	var out []cliBodiesStep
	for _, group := range groups {
		out = append(out, group...)
	}
	return out
}

func bodiesVault() map[string]string {
	vault := cliErrorsVault()
	vault["tagged.md"] = "---\ntitle: Tagged\ntags: [x, Y, z]\n---\nbody with #x and #y inline, `#x` code and #other\n\n```\n#x fenced\n```\n"
	vault["inline-only.md"] = "no frontmatter but #x inline and #x again\n"
	vault["crlf.md"] = "---\r\ntitle: Crlf\r\ntags: [x]\r\n---\r\nbody #x\r\n"
	return vault
}

var cliBodiesCaseBuilders []func() []cliBodiesCase

func cliBodiesCases() []cliBodiesCase {
	var cases []cliBodiesCase
	for _, build := range cliBodiesCaseBuilders {
		cases = append(cases, build()...)
	}
	return cases
}

func TestPortCLIBodiesProcessContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("CLI bodies fixture is recorded and replayed on Unix hosts")
	}
	fixture := observeCLIBodiesProcess(t)
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source path unavailable")
	}
	path := filepath.Join(filepath.Dir(source), cliBodiesFixturePath)
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
		t.Fatalf("CLI bodies fixture is stale; regenerate through portgen\nfirst difference at byte %d\ncommitted: %q\nobserved:  %q", offset, string(current[from:min(offset+160, len(current))]), string(encoded[from:min(offset+160, len(encoded))]))
	}
}

func observeCLIBodiesProcess(t *testing.T) cliBodiesFixture {
	t.Helper()
	binary := processFixtureBinary(t)
	fixture := cliBodiesFixture{SchemaVersion: 1}
	for _, c := range cliBodiesCases() {
		fixture.Cases = append(fixture.Cases, observeCLIBodiesCase(t, binary, c))
	}
	return fixture
}

// portJournalToday hides the clock-dependent name of the daily journal of the
// run's own UTC day (the cases run with TZ=UTC) so recordings stay valid on any
// day; journals of other days keep their names.
func portJournalToday(rel string) string {
	if rel == ".symdesk/journal/"+time.Now().UTC().Format("2006-01-02")+".ndjson" {
		return ".symdesk/journal/$DATE.ndjson"
	}
	return rel
}

// snapshotBodiesVault returns every regular file below the vault: text files
// as normalized text, anything else as its SHA-256.
func snapshotBodiesVault(t *testing.T, vault, root string, volatile bool) map[string]string {
	t.Helper()
	snapshot := map[string]string{}
	err := filepath.WalkDir(vault, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(vault, path)
		if err != nil {
			return err
		}
		rel = portJournalToday(filepath.ToSlash(rel))
		data, err := os.ReadFile(path) //nolint:gosec // test-only walk below a temporary vault
		if err != nil {
			return err
		}
		key := normalizeBodiesText(volatile, filepath.ToSlash(rel))
		if volatile && strings.HasPrefix(filepath.ToSlash(rel), ".symdesk/history/objects/") {
			// Content ids embed the wall clock and identical contents of one
			// second share an object, so neither names nor counts are stable;
			// the manifests below prove that a snapshot was taken.
			return nil
		}
		if volatile && strings.HasPrefix(filepath.ToSlash(rel), ".symdesk/history/manifest/") {
			// Manifests list content ids and sizes of volatile files.
			snapshot[key] = "(present)"
			return nil
		}
		if utf8.Valid(data) {
			snapshot[key] = normalizeBodiesText(volatile, normalizeCLIErrors(string(data), root))
		} else {
			sum := sha256.Sum256(data)
			snapshot[key] = "sha256:" + hex.EncodeToString(sum[:])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func observeCLIBodiesCase(t *testing.T, binary string, c cliBodiesCase) cliBodiesCase {
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
	baseEnv := func() []string {
		environment := cleanDuplicatesEnv(os.Environ())
		return append(environment, "TZ=UTC", "HOME="+home, "USERPROFILE="+home,
			"XDG_CONFIG_HOME="+filepath.Join(home, "config"),
			"XDG_CACHE_HOME="+filepath.Join(home, "cache"),
			"XDG_DATA_HOME="+filepath.Join(home, "data"))
	}
	environment := baseEnv()
	for name, value := range c.Env {
		environment = append(environment, name+"="+materialize(value))
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
	if !c.SkipIndex {
		if code, _, stderr := run(baseEnv(), []string{"--vault", vault, "index"}, ""); code != 0 {
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
		code, stdout, stderr := run(environment, args, materialize(step.Stdin))
		for _, canary := range c.Canaries {
			if strings.Contains(stdout, canary) || strings.Contains(stderr, canary) {
				t.Fatalf("oracle leaked canary %q for %s %v", canary, c.Name, step.Args)
			}
		}
		recorded := cliBodiesStep{
			Args:     step.Args,
			Stdin:    step.Stdin,
			ExitCode: code,
			Stdout:   normalizeBodiesText(c.VolatileHistory, normalizeCLIErrors(stdout, root)),
			Stderr:   normalizeBodiesText(c.VolatileHistory, normalizeCLIErrors(stderr, root)),
		}
		if c.SnapshotVault {
			recorded.Vault = snapshotBodiesVault(t, vault, root, c.VolatileHistory)
		}
		observed.Steps = append(observed.Steps, recorded)
	}
	return observed
}

// normalizeBodiesRandom hides the random identifiers Go draws for trash names
// (24 lowercase hex digits) and the wall-clock timestamps of text listings.
func normalizeBodiesText(volatile bool, text string) string {
	if volatile {
		text = bodiesSHARE.ReplaceAllString(text, "$$SHA256")
	}
	text = bodiesRandomRE.ReplaceAllString(text, "$$RAND24")
	text = bodiesLocalTimeRE.ReplaceAllString(text, "$$LOCALTIME")
	return bodiesRFC3339RE.ReplaceAllString(text, "$$RFC3339")
}

// bodiesRFC3339RE matches the UTC `created:` timestamps the commands write
// (the cases run with TZ=UTC).
var bodiesRFC3339RE = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z`)

var bodiesRandomRE = regexp.MustCompile(`[0-9a-f]{24}`)

var bodiesSHARE = regexp.MustCompile(`[0-9a-f]{64}`)

// bodiesLocalTimeRE matches the local timestamps (`2006-01-02 15:04:05`) that
// `history tasks` and `trash list` print.
var bodiesLocalTimeRE = regexp.MustCompile(`\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}`)
