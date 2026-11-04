package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

// RUST-013 slice r013-admin: the administrative command bodies that the Rust
// CLI gained after the Cobra front half and that are not part of the ingest,
// mail, meeting, rules, clip, paperless or views families (`perm`, `vault
// health|adopt`, `relations contact`, `demo init`, `doctor`, `dataset list`,
// `ai autofill`). Every step runs the real Go `symdesk` process in an isolated
// HOME/XDG/cwd with a disposable vault and records exit code, stdout and
// stderr. Steps of a case with SnapshotVault additionally record the whole
// vault tree after the step and steps of a case with SnapshotCwd the tree
// below the process working directory, so the files a command changed are
// compared as well. The Rust replay is
// `crates/symdesk-cli/tests/cli_admin_bodies.rs`.

const adminBodiesFixturePath = "../../testdata/port/cli/cli-admin-bodies-process.json"

type adminBodiesStep struct {
	Args     []string `json:"args"`
	Stdin    string   `json:"stdin,omitempty"`
	ExitCode int      `json:"exit_code"`
	Stdout   string   `json:"stdout"`
	Stderr   string   `json:"stderr"`
	// Vault is the vault tree after the step (SnapshotVault cases only).
	Vault *map[string]string `json:"vault,omitempty"`
	// Cwd is the working-directory tree after the step (SnapshotCwd cases);
	// CwdDigest replaces it for cases that set DigestCwd.
	Cwd       *map[string]string `json:"cwd,omitempty"`
	CwdDigest string             `json:"cwd_digest,omitempty"`
}

type adminBodiesCase struct {
	Name           string            `json:"name"`
	Files          map[string]string `json:"files,omitempty"`
	PostIndexFiles map[string]string `json:"post_index_files,omitempty"`
	// Mtimes sets the modification time (Unix seconds) of vault files after
	// they are written; birth-time derived values follow it.
	Mtimes    map[string]int64  `json:"mtimes,omitempty"`
	SkipIndex bool              `json:"skip_index,omitempty"`
	NoVault   bool              `json:"no_vault,omitempty"`
	HomeFiles map[string]string `json:"home_files,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Canaries  []string          `json:"canaries,omitempty"`
	// SnapshotVault records the vault tree after every step; SnapshotCwd
	// records the working-directory tree. HashFiles records every file as its
	// SHA-256 instead of its text.
	SnapshotVault bool `json:"snapshot_vault,omitempty"`
	SnapshotCwd   bool `json:"snapshot_cwd,omitempty"`
	HashFiles     bool `json:"hash_files,omitempty"`
	// DigestCwd records one SHA-256 over the sorted (path, content) pairs of
	// the working-directory tree instead of the tree (very large outputs).
	DigestCwd bool `json:"digest_cwd,omitempty"`
	// SortLines compares the sorted lines of stdout (commands that print a
	// Go map in its random order).
	SortLines bool `json:"sort_lines,omitempty"`
	// ContactSQL seeds the contact store below the isolated HOME: the real Go
	// process first creates and migrates the store, then each statement runs
	// against it (the Rust replay applies the same statements to its own store).
	ContactSQL []string `json:"contact_sql,omitempty"`
	// CwdFiles and CwdDirs prepare the working directory before the steps.
	CwdFiles map[string]string `json:"cwd_files,omitempty"`
	CwdDirs  []string          `json:"cwd_dirs,omitempty"`
	// Volatile hides 64-digit hex values (random tokens, content ids) and the
	// history objects that embed the wall clock.
	Volatile bool              `json:"volatile,omitempty"`
	Steps    []adminBodiesStep `json:"steps"`
}

type adminBodiesFixture struct {
	SchemaVersion int               `json:"schema_version"`
	Cases         []adminBodiesCase `json:"cases"`
}

func adminStep(args ...string) adminBodiesStep { return adminBodiesStep{Args: args} }

// adminBoth runs the same argv as text and as JSON.
func adminBoth(args ...string) []adminBodiesStep {
	return []adminBodiesStep{
		{Args: args},
		{Args: append([]string{"--json"}, args...)},
	}
}

func adminConcat(groups ...[]adminBodiesStep) []adminBodiesStep {
	var out []adminBodiesStep
	for _, group := range groups {
		out = append(out, group...)
	}
	return out
}

func sortedKeys(files map[string]string) []string {
	keys := make([]string, 0, len(files))
	for name := range files {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	return keys
}

func adminVault() map[string]string {
	vault := cliErrorsVault()
	vault["tagged.md"] = "---\ntitle: Tagged\ntags: [x, Y, z]\n---\nbody with #x and #y inline\n"
	return vault
}

var adminBodiesCaseBuilders []func() []adminBodiesCase

func adminBodiesCases() []adminBodiesCase {
	var cases []adminBodiesCase
	for _, build := range adminBodiesCaseBuilders {
		cases = append(cases, build()...)
	}
	return cases
}

func TestPortCLIAdminBodiesProcessContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("CLI admin bodies fixture is recorded and replayed on Unix hosts")
	}
	fixture := observeCLIAdminBodiesProcess(t)
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source path unavailable")
	}
	path := filepath.Join(filepath.Dir(source), adminBodiesFixturePath)
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
		t.Fatalf("CLI admin bodies fixture is stale; regenerate through portgen\nfirst difference at byte %d\ncommitted: %q\nobserved:  %q", offset, string(current[from:min(offset+160, len(current))]), string(encoded[from:min(offset+160, len(encoded))]))
	}
}

func observeCLIAdminBodiesProcess(t *testing.T) adminBodiesFixture {
	t.Helper()
	binary := processFixtureBinary(t)
	fixture := adminBodiesFixture{SchemaVersion: 1}
	for _, c := range adminBodiesCases() {
		fixture.Cases = append(fixture.Cases, observeCLIAdminBodiesCase(t, binary, c))
	}
	return fixture
}

// snapshotAdminTree returns every regular file below dir: text files as
// normalized text, anything else (or everything with hashFiles) as its
// SHA-256.
func snapshotAdminTree(t *testing.T, dir, root string, volatile, hashFiles bool) map[string]string {
	t.Helper()
	snapshot := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		if entry.Name() == ".DS_Store" {
			// macOS Finder/Spotlight metadata dropped into the scratch tree is
			// host state, not CLI output (it made the recorded demo-init-large
			// digests count 10028 files instead of 10026).
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path) //nolint:gosec // test-only walk below a temporary directory
		if err != nil {
			return err
		}
		slash := filepath.ToSlash(rel)
		key := normalizeAdminText(volatile, maskJournalDay(slash))
		if volatile && strings.HasPrefix(slash, ".symdesk/history/objects/") {
			// Content ids embed the wall clock and identical contents of one
			// second share an object, so neither names nor counts are stable;
			// the manifests below prove that a snapshot was taken.
			return nil
		}
		if volatile && strings.HasPrefix(slash, ".symdesk/history/manifest/") {
			snapshot[key] = "(present)"
			return nil
		}
		if utf8.Valid(data) && !hashFiles {
			snapshot[key] = normalizeAdminText(volatile, normalizeCLIErrors(string(data), root))
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

func observeCLIAdminBodiesCase(t *testing.T, binary string, c adminBodiesCase) adminBodiesCase {
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
	applyMtimes := func() {
		for name, seconds := range c.Mtimes {
			target := filepath.Join(vault, filepath.FromSlash(name))
			when := time.Unix(seconds, 0)
			if err := os.Chtimes(target, when, when); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
		}
	}
	writeFiles(c.Files)
	applyMtimes()
	for _, name := range c.CwdDirs {
		if err := os.MkdirAll(filepath.Join(cwd, filepath.FromSlash(name)), 0o755); err != nil { //nolint:gosec // test-only working directory
			t.Fatal(err)
		}
	}
	for name, content := range c.CwdFiles {
		target := filepath.Join(cwd, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	closedPort := closedLoopbackPort(t)
	llmURL := startAdminLLMServer(t).URL
	if c.HomeFiles == nil {
		c.HomeFiles = cliErrorsHomeFiles()
	}
	writeCLIErrorsHomeFiles(t, home, c.HomeFiles, closedPort, startCLIErrorsEmbeddingServer(t).URL)
	for name := range c.HomeFiles {
		// Fake tools below `.symaira/bin/` must be executable as well.
		if strings.Contains(name, "/bin/") {
			if err := os.Chmod(filepath.Join(home, filepath.FromSlash(name)), 0o700); err != nil {
				t.Fatal(err)
			}
		}
	}
	materialize := func(value string) string {
		value = strings.ReplaceAll(value, "$CLOSED_PORT", closedPort)
		value = strings.ReplaceAll(value, "$LLM_URL", llmURL)
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
	applyMtimes()
	if len(c.ContactSQL) > 0 {
		run(baseEnv(), []string{"--vault", vault, "relations", "contact", "--id", "store-init"}, "")
		store, err := sql.Open("sqlite", filepath.Join(home, "data", "symdesk", "symrelate.db"))
		if err != nil {
			t.Fatal(err)
		}
		for _, statement := range c.ContactSQL {
			if _, err := store.Exec(statement); err != nil {
				t.Fatalf("seed contact store for %s: %v", c.Name, err)
			}
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
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
			args[i] = materialize(args[i])
		}
		code, stdout, stderr := run(environment, args, materialize(step.Stdin))
		for _, canary := range c.Canaries {
			if strings.Contains(stdout, canary) || strings.Contains(stderr, canary) {
				t.Fatalf("oracle leaked canary %q for %s %v", canary, c.Name, step.Args)
			}
		}
		recorded := adminBodiesStep{
			Args:     step.Args,
			Stdin:    step.Stdin,
			ExitCode: code,
			Stdout:   sortAdminLines(c.SortLines, normalizeAdminText(c.Volatile, normalizeCLIErrors(stdout, root))),
			Stderr:   normalizeAdminText(c.Volatile, normalizeCLIErrors(stderr, root)),
		}
		if c.SnapshotVault {
			tree := snapshotAdminTree(t, vault, root, c.Volatile, c.HashFiles)
			recorded.Vault = &tree
		}
		if c.SnapshotCwd {
			tree := snapshotAdminTree(t, cwd, root, c.Volatile, c.HashFiles)
			if c.DigestCwd {
				recorded.CwdDigest = adminTreeDigest(tree)
			} else {
				recorded.Cwd = &tree
			}
		}
		observed.Steps = append(observed.Steps, recorded)
	}
	return observed
}

// sortAdminLines sorts the lines of a report whose section order is random.
func sortAdminLines(enabled bool, text string) string {
	if !enabled {
		return text
	}
	lines := strings.SplitAfter(text, "\n")
	sort.Strings(lines)
	return strings.Join(lines, "")
}

// normalizeAdminText hides the random identifiers Go draws (24 lowercase hex
// digits), the wall-clock timestamps of text listings and the measured
// duration of a report. A volatile case also hides 64-digit hex values and the
// random order of the migrated dataset policy fields.
func normalizeAdminText(volatile bool, text string) string {
	if volatile {
		text = adminSHARE.ReplaceAllString(text, "$$SHA256")
		// The dataset policy migration ranges over a two-entry map, so Go
		// writes the two fields in either order from run to run.
		text = strings.ReplaceAll(text, "retention_rule: \"default\"\nsensitivity: \"restricted\"", "sensitivity: \"restricted\"\nretention_rule: \"default\"")
	}
	// Reading a regular file as a directory fails in fdopendir on macOS and in
	// readdirent on Linux; the replay reports the macOS spelling.
	text = strings.ReplaceAll(text, "readdirent ", "fdopendir ")
	text = adminRandomRE.ReplaceAllString(text, "$$RAND24")
	text = adminLocalTimeRE.ReplaceAllString(text, "$$LOCALTIME")
	text = adminDurationRE.ReplaceAllString(text, `"duration_seconds":$$DURATION`)
	return adminRFC3339RE.ReplaceAllString(text, "$$RFC3339")
}

// adminRFC3339RE matches the UTC timestamps the commands write (the cases run
// with TZ=UTC).
var adminRFC3339RE = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z`)

var adminRandomRE = regexp.MustCompile(`[0-9a-f]{24}`)

var adminSHARE = regexp.MustCompile(`[0-9a-f]{64}`)

// adminLocalTimeRE matches `2006-01-02 15:04:05`.
var adminLocalTimeRE = regexp.MustCompile(`\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}`)

// adminDurationRE matches the measured `"duration_seconds":<number>` member.
var adminDurationRE = regexp.MustCompile(`"duration_seconds":[0-9eE.+-]+`)

// adminTreeDigest folds a snapshot into `files=<n> sha256=<hex>` over the
// sorted (path NUL value LF) lines.
func adminTreeDigest(tree map[string]string) string {
	keys := make([]string, 0, len(tree))
	for key := range tree {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	hasher := sha256.New()
	for _, key := range keys {
		_, _ = hasher.Write([]byte(key + "\x00" + tree[key] + "\n"))
	}
	return "files=" + strconv.Itoa(len(keys)) + " sha256=" + hex.EncodeToString(hasher.Sum(nil))
}
