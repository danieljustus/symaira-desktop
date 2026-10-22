package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

// repoPath resolves a repository-relative path from the package test directory.
func repoPath(t *testing.T, rel string) string {
	t.Helper()
	root, err := fixtureoracle.FindRepositoryRoot(".")
	if err != nil {
		t.Fatalf("find repository root: %v", err)
	}
	return filepath.Join(root, filepath.FromSlash(rel))
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- test reads committed fixtures or its own temp files
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func defaultOptions(t *testing.T, check bool) options {
	t.Helper()
	return options{
		output:  repoPath(t, defaultOutput),
		tables:  repoPath(t, defaultTables),
		check:   check,
		commit:  fixtureoracle.Defaults().Commit,
		release: fixtureoracle.Defaults().Release,
	}
}

// TestGenerateCheckModeMatchesCommittedFixture is the staleness guard: the
// in-process generator must reproduce the committed fixture and Unicode tables.
func TestGenerateCheckModeMatchesCommittedFixture(t *testing.T) {
	message, err := generate(defaultOptions(t, true))
	if err != nil {
		t.Fatalf("committed DSL fixture is stale: %v", err)
	}
	if !strings.HasPrefix(message, "PASS render DSL parse fixture (") {
		t.Fatalf("unexpected check message %q", message)
	}
}

// TestGenerateWritesByteIdenticalOutputs regenerates into a scratch directory
// and compares byte-for-byte with the committed files, then proves check mode
// rejects a corrupted fixture, a corrupted table and a missing fixture.
func TestGenerateWritesByteIdenticalOutputs(t *testing.T) {
	dir := t.TempDir()
	opts := defaultOptions(t, false)
	committedFixture := readFile(t, opts.output)
	committedTables := readFile(t, opts.tables)
	opts.output = filepath.Join(dir, "nested", "dsl-parse.json")
	opts.tables = filepath.Join(dir, "other", "go_unicode.rs")

	message, err := generate(opts)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !strings.HasPrefix(message, "PASS render DSL parse fixture generated (") {
		t.Fatalf("unexpected generate message %q", message)
	}
	if got := readFile(t, opts.output); !bytes.Equal(got, committedFixture) {
		t.Fatal("regenerated DSL fixture differs from the committed fixture")
	}
	if got := readFile(t, opts.tables); !bytes.Equal(got, committedTables) {
		t.Fatal("regenerated Go Unicode tables differ from the committed file")
	}

	var parsed fixture
	if err := json.Unmarshal(readFile(t, opts.output), &parsed); err != nil {
		t.Fatalf("fixture is not valid JSON: %v", err)
	}
	if parsed.SchemaVersion != 1 || len(parsed.Cases) == 0 || strings.Join(parsed.Entries, ",") != "source,mermaid,json" {
		t.Fatalf("unexpected fixture header: schema=%d cases=%d entries=%v", parsed.SchemaVersion, len(parsed.Cases), parsed.Entries)
	}
	accepted, rejected := 0, 0
	for _, c := range parsed.Cases {
		if c.Accepted {
			accepted++
			if c.DiagramJSON == "" || c.Error != nil {
				t.Fatalf("%s: accepted case must carry a diagram and no error", c.ID)
			}
		} else {
			rejected++
			if c.Error == nil || c.Error.Text == "" || c.DiagramJSON != "" {
				t.Fatalf("%s: rejected case must carry a typed error and no diagram", c.ID)
			}
		}
	}
	if accepted == 0 || rejected == 0 {
		t.Fatalf("corpus must exercise both outcomes: accepted=%d rejected=%d", accepted, rejected)
	}

	check := opts
	check.check = true
	if _, err := generate(check); err != nil {
		t.Fatalf("check on freshly generated files: %v", err)
	}

	corrupt := func(path string) func() {
		original := readFile(t, path)
		mutated := append([]byte(nil), original...)
		mutated[len(mutated)/2] ^= 0x01
		if err := os.WriteFile(path, mutated, 0o600); err != nil { // #nosec G703 -- path is inside t.TempDir()
			t.Fatal(err)
		}
		return func() {
			if err := os.WriteFile(path, original, 0o600); err != nil { // #nosec G703 -- path is inside t.TempDir()
				t.Fatal(err)
			}
		}
	}
	restore := corrupt(check.output)
	if _, err := generate(check); err == nil || !strings.Contains(err.Error(), "fixture is stale") {
		t.Fatalf("corrupted fixture accepted by check mode: %v", err)
	}
	restore()
	restore = corrupt(check.tables)
	if _, err := generate(check); err == nil || !strings.Contains(err.Error(), "table is stale") {
		t.Fatalf("corrupted tables accepted by check mode: %v", err)
	}
	restore()
	missing := check
	missing.output = filepath.Join(dir, "absent.json")
	if _, err := generate(missing); err == nil || !strings.Contains(err.Error(), "fixture is stale") {
		t.Fatalf("missing fixture accepted by check mode: %v", err)
	}
}

func TestGenerateRejectsUnknownOracle(t *testing.T) {
	opts := defaultOptions(t, true)
	opts.commit = strings.Repeat("0", 40)
	if _, err := generate(opts); err == nil || !strings.Contains(err.Error(), "verify selected fixture source") {
		t.Fatalf("unknown oracle commit accepted: %v", err)
	}
}

func TestGenerateReportsUnwritableOutput(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := defaultOptions(t, false)
	opts.output = filepath.Join(blocker, "dsl-parse.json")
	opts.tables = filepath.Join(dir, "go_unicode.rs")
	if _, err := generate(opts); err == nil {
		t.Fatal("writing below a regular file must fail")
	}
}

func TestRunRecordsAcceptedAndTypedErrorCases(t *testing.T) {
	ok := run("ok", "mermaid", "graph LR\nA-->B\n")
	if !ok.Accepted || ok.DiagramJSON == "" || ok.Error != nil {
		t.Fatalf("valid mermaid graph rejected: %+v", ok)
	}
	for entry, input := range map[string]string{"source": "", "mermaid": "not a diagram", "json": "{"} {
		bad := run("bad-"+entry, entry, input)
		if bad.Accepted || bad.Error == nil || bad.Error.Text == "" || bad.Error.Stage == "" || bad.DiagramJSON != "" {
			t.Fatalf("%s: expected typed ParseError, got %+v", entry, bad)
		}
	}
	if j := run("json-ok", "json", `{"kind":"graph","nodes":[{"id":"a","label":"A"}]}`); !j.Accepted {
		t.Fatalf("minimal JSON graph rejected: %+v", j)
	}
	if s := run("source-ok", "source", "graph TD\nA-->B\n"); !s.Accepted {
		t.Fatalf("source entry rejected valid mermaid: %+v", s)
	}
}

func TestCorporaAreDeterministicAndWellFormed(t *testing.T) {
	hand := handCorpus()
	if len(hand) == 0 {
		t.Fatal("hand corpus is empty")
	}
	again := handCorpus()
	if len(again) != len(hand) {
		t.Fatalf("hand corpus is not deterministic: %d != %d", len(again), len(hand))
	}
	for i := range hand {
		if hand[i] != again[i] {
			t.Fatalf("hand corpus order differs at %d: %+v vs %+v", i, hand[i], again[i])
		}
	}
	root, err := fixtureoracle.FindRepositoryRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	mined := minedCorpus(root)
	if len(mined) == 0 {
		t.Fatal("mined corpus is empty")
	}
	seen := map[string]bool{}
	for _, c := range mined {
		if c.entry != "source" || !strings.HasPrefix(c.id, "mined-") || seen[c.input] {
			t.Fatalf("malformed mined case %+v", c)
		}
		seen[c.input] = true
	}
	cases := make([]testCase, 0, len(hand))
	for _, c := range hand {
		cases = append(cases, run(c.id, c.entry, c.input))
	}
	generated := generatedCorpus(cases)
	if len(generated) == 0 {
		t.Fatal("generated corpus is empty")
	}
	ids := map[string]bool{}
	for _, c := range generated {
		if ids[c.id] {
			t.Fatalf("duplicate generated id %s", c.id)
		}
		ids[c.id] = true
	}
}

func TestStressHelpersAndSlug(t *testing.T) {
	if seqMany(3) == "" || seqActors(2) == "" {
		t.Fatal("sequence helpers produced no output")
	}
	for name, got := range map[string]string{"chain": chain(4), "lines": lines(4), "fan": fan(4), "fan2": fan2(4)} {
		if got == "" {
			t.Fatalf("%s helper produced empty output", name)
		}
	}
	if slug("Hello, World!") == "" {
		t.Fatal("slug of printable text must not be empty")
	}
}

func TestRenderTablesShape(t *testing.T) {
	src := renderTables()
	for _, want := range []string{"Code generated by scripts/rust-port/cmd/renderdslgen; DO NOT EDIT.", "GO_SPACE", "GO_PRINT", "GO_TO_LOWER", "(0x20, 0x20)", "(0x41, 0x61)"} {
		if !strings.Contains(src, want) {
			t.Fatalf("tables missing %q", want)
		}
	}
	if src != renderTables() {
		t.Fatal("renderTables is not deterministic")
	}
}
