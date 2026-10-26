package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// This corpus pins the local graph with optional external binaries absent.
func TestPortGraphProcessContract(t *testing.T) {
	binary := buildIndexRetrievalBinary(t, t.TempDir())
	t.Setenv("PATH", t.TempDir())
	files := map[string]string{
		"alpha.md":        "---\ntitle: Alpha <&>\naliases: [First, One]\n---\n[[beta]] [[First]] [[missing]] [[beta]]\n",
		"beta.md":         "---\ntitle: Beta\n---\n[[alpha]] [[One]] [[contact:relate:person:123]]\n",
		"nested/third.md": "---\ntitle: Third\n---\n[[Alpha <&>]] [[alpha.md]] [[nested/third]]\n",
	}
	var steps []duplicatesPortStep
	for _, args := range [][]string{
		{"graph"}, {"graph", "--json"}, {"--json", "graph"}, {"graph", "--output=json"},
		{"graph", "--output", "yaml"}, {"graph", "--output=text"}, {"graph", "extra", "more"},
		{"graph", "--", "--json"}, {"graph", "--json", "--output=text"},
		{"graph", "--output=text", "--output=json"}, {"--json", "graph", "--json=false"},
		{"graph", "--json=false", "--json=true"}, {"graph", "--json=wat"},
		{"graph", "--unknown"}, {"--json", "graph", "--unknown"}, {"graph", "--output"},
		{"graph", "--output=invalid"}, {"graph", "--vault=$ROOT/nope", "--vault=$ROOT/vault", "--json"},
	} {
		steps = append(steps, duplicatesPortStep{Args: args})
	}
	cases := []duplicatesPortCase{
		{Name: "indexed", Files: files, Steps: steps},
		{Name: "unindexed", Files: files, SkipIndex: true, Steps: []duplicatesPortStep{{Args: []string{"graph"}}, {Args: []string{"graph", "--json"}}}},
		{Name: "empty", Steps: []duplicatesPortStep{{Args: []string{"graph"}}, {Args: []string{"graph", "--json"}}}},
		{Name: "disk-drift", Files: files, PostIndexFiles: map[string]string{"alpha.md": "changed\n", "late.md": "[[beta]]\n"}, Steps: []duplicatesPortStep{{Args: []string{"graph", "--json"}}}},
		{Name: "no-vault", NoVault: true, Steps: []duplicatesPortStep{{Args: []string{"graph"}}, {Args: []string{"graph", "--json"}}}},
	}
	fixture := duplicatesPortFixture{SchemaVersion: 1}
	for _, c := range cases {
		var states []map[string][][]string
		observed := observeDuplicatesCase(t, binary, c, func(vault, home string) {
			states = append(states, linkProcessState(t, vault, home))
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
	path := filepath.Join(filepath.Dir(source), "../../testdata/port/cli/graph-process.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.WriteFile(path, encoded, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, encoded) {
		t.Fatal("graph process fixture stale")
	}
}
