package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestPortConflictProcessContract(t *testing.T) {
	binary := buildIndexRetrievalBinary(t, t.TempDir())
	t.Setenv("PATH", t.TempDir())
	t.Setenv("SYMAIRA_BIN", t.TempDir())
	files := map[string]string{
		"note.md":                 "---\ntitle: Mine\n---\n[[target]]\n",
		"note conflicted copy.md": "---\ntitle: Theirs\n---\n[[other]]\n",
		"nested/second.md":        "---\ntitle: Second Mine\n---\n",
		"nested/second 2.md":      "---\ntitle: Second Theirs\n---\n",
		"plain.md":                "---\ntitle: Plain\n---\n",
	}
	makeSteps := func(argv ...[]string) []duplicatesPortStep {
		var out []duplicatesPortStep
		for _, a := range argv {
			out = append(out, duplicatesPortStep{Args: a})
		}
		return out
	}
	// Embeddings must never reach a real Ollama: a closed loopback port makes
	// every platform record the same hermetic, embedding-less retrieval state.
	homeFiles := map[string]string{".config/symseek/config.toml": "ollama_url = \"http://127.0.0.1:1/api/embeddings\"\nretry_count = 0\ntimeout_seconds = 1\n"}
	cases := []duplicatesPortCase{
		{Name: "mine", Files: files, HomeFiles: homeFiles, Steps: makeSteps(
			[]string{"conflict", "resolve", "note conflicted copy.md", "--action=keep-mine", "--json"},
			[]string{"conflict", "resolve", "nested/second 2.md", "--action", "keep-mine"},
			[]string{"graph", "--json"},
		)},
		{Name: "theirs", Files: files, HomeFiles: homeFiles, Steps: makeSteps(
			[]string{"conflict", "resolve", "note conflicted copy.md", "--action=keep-theirs", "--json"},
			[]string{"conflict", "resolve", "nested/second 2.md", "--action", "keep-theirs"},
			[]string{"graph", "--json"},
		)},
		{Name: "absolute-and-plain", Files: files, Steps: makeSteps(
			[]string{"conflict", "resolve", "$ROOT/vault/note conflicted copy.md", "--action=bad", "--action=keep-theirs", "--output=json"},
			[]string{"conflict", "resolve", "plain.md", "--action=keep-theirs", "--json"},
			[]string{"graph", "--json"},
		)},
		{Name: "arguments", Files: files, Steps: makeSteps(
			[]string{"conflict", "resolve"}, []string{"conflict", "resolve", "--json"},
			[]string{"conflict", "resolve", "note.md"}, []string{"conflict", "resolve", "note.md", "--json"},
			[]string{"conflict", "resolve", "note.md", "--action=invalid"},
			[]string{"conflict", "resolve", "note.md", "--action=invalid", "--json"},
			[]string{"conflict", "resolve", "a", "b", "--action=keep-mine", "--json"},
			[]string{"conflict", "resolve", "note.md", "--action"},
			[]string{"conflict", "resolve", "note.md", "--unknown"},
			[]string{"conflict", "resolve", "note.md", "--json=wat"},
		)},
		{Name: "no-vault", NoVault: true, Steps: makeSteps(
			[]string{"conflict", "resolve", "note.md", "--action=keep-mine"},
			[]string{"conflict", "resolve", "note.md", "--action=keep-mine", "--json"},
			[]string{"conflict", "resolve", "note.md", "--action=invalid", "--json"},
		)},
	}
	fixture := duplicatesPortFixture{SchemaVersion: 1}
	for _, c := range cases {
		var states []map[string][][]string
		observed := observeDuplicatesCase(t, binary, c, func(vault, home string) { states = append(states, conflictProcessState(t, vault, home)) })
		for i := range observed.Steps {
			observed.Steps[i].State = states[i]
			observed.Steps[i].Stderr = normalizeConflictLog(observed.Steps[i].Stderr)
		}
		fixture.Cases = append(fixture.Cases, observed)
	}
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	_, source, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(source), "../../testdata/port/cli/conflict-process.json")
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
		t.Fatal("conflict process fixture stale")
	}
}

// Only the migration diagnostic clock and vault-root-derived cache id vary.
func normalizeConflictLog(text string) string {
	if !strings.Contains(text, "seeded per-vault retrieval index from standalone store") {
		return text
	}
	text = regexp.MustCompile(`time=[^ ]+`).ReplaceAllString(text, `time=$$NOW`)
	return regexp.MustCompile(`(/vaults/)[0-9a-f]{16}(/)`).ReplaceAllString(text, `${1}$$VAULT_ID${2}`)
}
