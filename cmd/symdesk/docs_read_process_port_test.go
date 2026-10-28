package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

const docsReadFixturePath = "../../testdata/port/cli/docs-read-process.json"

type docsReadFixture struct {
	SchemaVersion int                  `json:"schema_version"`
	Cases         []duplicatesPortCase `json:"cases"`
}

func docsReadCases() []duplicatesPortCase {
	files := map[string]string{
		"alpha.md":       "---\ntitle: Alpha <&>\ntype: document\ndocument_type: invoice\ndocument_date: '2026-01-02'\nperson: Ada\nstatus: open\ndue_date: '2026-02-01'\nconfidence: 40\ncorrespondent: Bank\nasn: 7\ntags: [finance, tax]\n---\nInvoice body\n",
		"nested/beta.md": "---\ntitle: Beta\ntype: document\ndocument_type: contract\ndocument_date: '2025-02-03'\nconfidence: 99\nstatus: paid\n---\nContract\n",
		"note.md":        "---\ntitle: Note\n---\nNote body\n",
		"ignored.md":     "---\ntitle: Ignored\nreview_ignored: true\n---\nIgnored body\n",
	}
	steps := []duplicatesPortStep{}
	add := func(args ...string) { steps = append(steps, duplicatesPortStep{Args: args}) }
	for _, command := range []string{"list", "review"} {
		add("docs", command)
		add("--json", "docs", command)
		add("docs", "--json", command)
		add("docs", command, "--json")
		for _, output := range []string{"json", "yaml", "text"} {
			add("--output", output, "docs", command)
			add("docs", "--output="+output, command)
			add("docs", command, "--output", output)
		}
		add("docs", command, "extra")
		add("docs", command, "--output", "invalid")
		add("docs", command, "--unknown")
		add("--json", "docs", command, "--unknown")
		add("docs", command, "--unknown", "--json")
		add("docs", command, "-z")
		add("docs", command, "--output")
		add("docs", command, "--vault")
		add("docs", command, "--json", "--output")
		add("docs", command, "--output", "text", "--json")
		add("docs", command, "--output", "yaml", "--json")
		add("docs", command, "--json", "--output", "json")
		add("docs", command, "--json", "--json")
		add("docs", command, "--output", "text", "--output", "json")
		add("docs", command, "--", "--unknown")
	}
	for _, pair := range [][2]string{{"type", "invoice"}, {"file-type", "document"}, {"status", "open"}, {"person", "Ada"}, {"correspondent", "Bank"}, {"year", "2026"}, {"due-before", "2026-02-02"}, {"min-confidence", "50"}, {"max-confidence", "50"}, {"asn", "7"}, {"asn", "0"}, {"asn", "-1"}, {"asn", "2147483648"}, {"asn", "bad"}, {"min-confidence", "08"}, {"max-confidence", "0x32"}, {"year", "1900"}} {
		add("docs", "list", "--"+pair[0], pair[1], "--json")
	}
	add("docs", "list", "--type", "invoice", "--person", "Ada", "--min-confidence", "1", "--max-confidence", "50", "--json")
	add("docs", "list", "--status", "paid", "--status", "open", "--json")
	for _, threshold := range []string{"1", "100", "40", "0", "-1", "0x32", "bad"} {
		add("docs", "review", "--threshold", threshold, "--json")
	}
	add("docs", "list", "--vault", "$ROOT/vault", "--json")
	add("docs", "review", "--vault=$ROOT/vault", "--json")
	return []duplicatesPortCase{
		{Name: "indexed", Files: files, Steps: steps},
		{Name: "unindexed", Files: files, SkipIndex: true, Steps: []duplicatesPortStep{{Args: []string{"docs", "list", "--json"}}, {Args: []string{"docs", "review", "--json"}}}},
		{Name: "missing-vault", NoVault: true, Steps: []duplicatesPortStep{{Args: []string{"docs", "list"}}, {Args: []string{"docs", "review", "--json"}}}},
		{Name: "bad-vault", NoVault: true, UnixOnly: true, Steps: []duplicatesPortStep{{Args: []string{"docs", "list", "--vault", "$ROOT/nope", "--json"}}, {Args: []string{"docs", "review", "--vault", "$ROOT/nope"}}}},
	}
}

func TestPortDocsReadProcessContract(t *testing.T) {
	binary := buildIndexRetrievalBinary(t, t.TempDir())
	fixture := docsReadFixture{SchemaVersion: 1}
	for _, c := range docsReadCases() {
		if runtime.GOOS == "windows" && c.UnixOnly {
			continue
		}
		fixture.Cases = append(fixture.Cases, observeDuplicatesCase(t, binary, c, func(vault, _ string) {
			t.Helper()
			observed := map[string]string{}
			err := filepath.WalkDir(vault, func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.IsDir() {
					return nil
				}
				//nolint:gosec // path is derived from repository fixtures or the test-owned temporary root, not external input.
				body, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				relative, err := filepath.Rel(vault, path)
				if err != nil {
					return err
				}
				observed[filepath.ToSlash(relative)] = string(body)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(observed) != len(c.Files) {
				t.Fatalf("%s: read command changed vault file inventory", c.Name)
			}
			for path, expected := range c.Files {
				if body, ok := observed[path]; !ok || body != expected {
					t.Fatalf("%s: read command changed Markdown bytes in %s", c.Name, path)
				}
			}
		}))
	}
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("source path unavailable")
	}
	path := filepath.Join(filepath.Dir(source), docsReadFixturePath)
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
	if runtime.GOOS == "windows" {
		var saved docsReadFixture
		if err := json.Unmarshal(current, &saved); err != nil {
			t.Fatal(err)
		}
		filtered := saved
		filtered.Cases = nil
		for _, c := range saved.Cases {
			if !c.UnixOnly {
				filtered.Cases = append(filtered.Cases, c)
			}
		}
		current, err = json.MarshalIndent(filtered, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		current = append(current, '\n')
	}
	if !bytes.Equal(current, encoded) {
		portProcessFixtureDifference(t, "docs read fixture is stale; regenerate explicitly", current, encoded)
	}
}
