package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"testing"
)

const viewsFixturePath = "../../testdata/port/cli/views-process.json"

// Go's text printer exposes heap addresses for []*Base and nested pointers.
// Only those process-specific addresses are normalized, never their contents.
var viewsPointer = regexp.MustCompile(`0x[0-9a-f]+`)

func viewsCases(t *testing.T) []duplicatesPortCase {
	step := func(args ...string) duplicatesPortStep { return duplicatesPortStep{Args: args} }
	rich := "---\ntype: base\nbase_id: work\ntitle: Work\ncreated: 2026-01-02T03:04:05Z\ntags: [base, work]\ndescription: Tasks & notes\nproperties:\n  status:\n    type: select\n    options: [open, done]\nviews:\n  - id: tasks\n    name: Tasks\n    type: table\n    source: 'tag:task'\n    columns: [title, status]\n    filters:\n      - key: status\n        operator: equals\n        value: open\n    sorts:\n      - key: title\n        ascending: true\n    computed:\n      label:\n        formula: '{status}'\n  - id: board\n    name: Board\n    source: 'tag:task'\n  - id: solo\n    name: Solo\ncustom: retained\n---\n# Work\n"
	files := map[string]string{"bases/work.md": rich, "bases/alpha.md": "---\ntype: base\nbase_id: alpha\ntitle: Alpha\ncreated: fixed\nviews: []\n---\n", "bases/not-a-base.md": "# Ignore\n", "bases/nested/no.md": rich, "bases/skip.MD": rich}
	var steps []duplicatesPortStep
	for _, command := range [][]string{{"views", "list"}, {"views", "get", "tasks"}, {"views", "get", "board"}, {"views", "siblings", "tasks"}, {"views", "siblings", "solo"}, {"views", "base", "list"}, {"views", "base", "get", "work"}, {"views", "base", "get", "bases/work.md"}} {
		steps = append(steps, step(command...))
		steps = append(steps, step(append([]string{"--json"}, command...)...))
		for _, format := range []string{"json", "yaml", "text"} {
			steps = append(steps, step(append(command, "--output", format)...))
		}
	}
	for _, command := range [][]string{{"views", "get"}, {"views", "get", "a", "b"}, {"views", "get", "missing"}, {"views", "siblings"}, {"views", "siblings", "missing"}, {"views", "base", "get"}, {"views", "base", "get", "missing"}, {"views", "base", "get", ""}, {"views", "list", "extra"}, {"views", "base", "list", "extra"}} {
		steps = append(steps, step(command...), step(append([]string{"--json"}, command...)...))
	}
	steps = append(steps,
		step("--output=json", "views", "get", "tasks"),
		step("views", "--output", "json", "get", "tasks"),
		step("views", "get", "--json", "tasks"),
		step("views", "get", "--", "tasks"),
		step("views", "base", "--json", "get", "work"),
		step("views", "base", "get", "work", "--vault", "$ROOT/vault"),
	)
	// An upper-case base ID resolves through the file system: a folding file
	// system echoes the requested spelling, a case-sensitive one falls back
	// to the indexed file. Go and Rust both delegate, so each class is pinned.
	command := []string{"views", "base", "get", "WORK"}
	casefold := []duplicatesPortStep{step(command...), step(append([]string{"--json"}, command...)...)}
	for _, format := range []string{"json", "yaml", "text"} {
		casefold = append(casefold, step(append(command, "--output", format)...))
	}
	class := viewsFSCaseClass(t)
	return []duplicatesPortCase{
		{Name: "reads", Files: files, SkipIndex: true, Steps: steps},
		{Name: "reads-casefold-" + class, Files: files, SkipIndex: true, FSCase: class, Steps: casefold},
		{Name: "empty", SkipIndex: true, Steps: []duplicatesPortStep{step("views", "list"), step("--json", "views", "list"), step("views", "base", "list"), step("--json", "views", "base", "list")}},
		{Name: "no-vault", NoVault: true, SkipIndex: true, Steps: []duplicatesPortStep{step("views", "list"), step("--json", "views", "get", "missing"), step("views", "base", "list"), step("--json", "views", "base", "get", "missing")}},
	}
}

func TestPortViewsProcessContract(t *testing.T) {
	binary := buildIndexRetrievalBinary(t, t.TempDir())
	observed := viewsPortFixture{SchemaVersion: 1}
	for _, c := range viewsCases(t) {
		result := observeViewsCase(t, binary, c)
		for i := range result.Steps {
			result.Steps[i].Stdout = viewsPointer.ReplaceAllString(result.Steps[i].Stdout, "$$PTR")
		}
		observed.Cases = append(observed.Cases, result)
	}
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("source path unavailable")
	}
	path := filepath.Join(filepath.Dir(source), viewsFixturePath)
	// The other file-system class cannot be observed here; carry its native
	// recording over unchanged so generation on one class never drops the other.
	//nolint:gosec // path is derived from repository fixtures or the test-owned temporary root, not external input.
	if previous, err := os.ReadFile(path); err == nil {
		var recorded viewsPortFixture
		if err := json.Unmarshal(previous, &recorded); err != nil {
			t.Fatal(err)
		}
		class := viewsFSCaseClass(t)
		for _, c := range recorded.Cases {
			if c.FSCase != "" && c.FSCase != class {
				observed.Cases = append(observed.Cases, c)
			}
		}
	}
	sort.SliceStable(observed.Cases, func(i, j int) bool {
		return viewsCaseRank(observed.Cases[i].Name) < viewsCaseRank(observed.Cases[j].Name)
	})
	encoded, err := json.MarshalIndent(observed, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	//nolint:gosec // path is derived from repository fixtures or the test-owned temporary root, not external input.
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, encoded) {
		t.Fatal("views process fixture drift; regenerate deliberately")
	}
}

// viewsFSCaseClass reports whether the test's temporary file system folds
// case, the property that decides how `views base get WORK` resolves.
func viewsFSCaseClass(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "CaseProbe"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "caseprobe")); err == nil {
		return "insensitive"
	}
	return "sensitive"
}

func viewsCaseRank(name string) int {
	for i, n := range []string{"reads", "reads-casefold-insensitive", "reads-casefold-sensitive", "empty", "no-vault"} {
		if n == name {
			return i
		}
	}
	return 99
}
