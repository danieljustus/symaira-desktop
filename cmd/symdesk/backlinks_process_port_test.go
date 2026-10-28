package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPortBacklinksProcessContract(t *testing.T) {
	binary := buildIndexRetrievalBinary(t, t.TempDir())
	files := map[string]string{
		"alpha.md":        "---\ntitle: Alpha Title\naliases: [First, One]\n---\n[[alpha]] [[beta]]\n",
		"beta.md":         "---\ntitle: Beta\n---\n[[Alpha Title]] [[First]] [[alpha.md]]\n",
		"nested/third.md": "---\ntitle: Third\n---\n[[alpha]] [[One]] [[contact:relate:person:123]]\n",
	}
	var steps []duplicatesPortStep
	for _, target := range []string{"alpha.md", "beta.md", "missing.md", "nested/third.md", "contact:relate:person:123", "contact:bad", "../escape.md"} {
		for _, output := range [][]string{nil, {"--json"}, {"--output", "json"}, {"--output", "yaml"}, {"--output", "text"}} {
			args := append([]string{"backlinks", target}, output...)
			steps = append(steps, duplicatesPortStep{Args: args})
		}
	}
	steps = append(steps,
		duplicatesPortStep{Args: []string{"backlinks"}},
		duplicatesPortStep{Args: []string{"--json", "backlinks"}},
		duplicatesPortStep{Args: []string{"backlinks", "a", "b"}},
		duplicatesPortStep{Args: []string{"--json", "backlinks", "a", "b"}},
		duplicatesPortStep{Args: []string{"--output=json", "backlinks", "--", "alpha.md"}},
		duplicatesPortStep{Args: []string{"backlinks", "--json", "alpha.md"}},
		duplicatesPortStep{Args: []string{"backlinks", "alpha.md", "--output=invalid"}},
		duplicatesPortStep{Args: []string{"backlinks", "alpha.md", "--json", "--output=text"}},
		duplicatesPortStep{Args: []string{"--json", "backlinks", "alpha.md", "--output=json"}},
	)
	for _, args := range [][]string{
		{"backlinks", "alpha.md", "--output=text", "--output=json"},
		{"--json", "backlinks", "alpha.md", "--json=false"},
		{"backlinks", "alpha.md", "--json=false", "--json=true"},
		{"backlinks", "alpha.md", "--json=wat"},
		{"--json", "backlinks", "alpha.md", "--unknown"},
		{"backlinks", "alpha.md", "--unknown"},
		{"backlinks", "alpha.md", "--output"},
		{"backlinks", "alpha.md", "--vault=$ROOT/nope", "--vault=$ROOT/vault"},
	} {
		steps = append(steps, duplicatesPortStep{Args: args})
	}
	cases := []duplicatesPortCase{
		{Name: "indexed", Files: files, Steps: steps},
		{Name: "unindexed", Files: files, SkipIndex: true, Steps: []duplicatesPortStep{{Args: []string{"backlinks", "alpha.md", "--json"}}}},
		{Name: "no-vault", NoVault: true, Steps: []duplicatesPortStep{{Args: []string{"backlinks", "alpha.md"}}, {Args: []string{"backlinks", "alpha.md", "--json"}}}},
		{Name: "override", NoVault: true, Files: files, Steps: []duplicatesPortStep{{Args: []string{"backlinks", "alpha.md", "--vault=$ROOT/vault", "--json"}}}},
	}
	fixture := duplicatesPortFixture{SchemaVersion: 1}
	for _, c := range cases {
		var states []map[string][][]string
		observed := observeDuplicatesCase(t, binary, c, func(vault, home string) {
			states = append(states, linkProcessState(t, vault, home))
			for name, expected := range c.Files {
				//nolint:gosec // path is derived from repository fixtures or the test-owned temporary root, not external input.
				actual, err := os.ReadFile(filepath.Join(vault, filepath.FromSlash(name)))
				if err != nil || string(actual) != expected {
					t.Fatalf("backlinks changed vault file %s: %v", name, err)
				}
			}
		})
		for i := range observed.Steps {
			observed.Steps[i].State = states[i]
		}
		fixture.Cases = append(fixture.Cases, observed)
	}
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	_, source, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(source), "../../testdata/port/cli/backlinks-process.json")
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
		portProcessFixtureDifference(t, "backlinks process fixture stale", current, encoded)
	}
}
