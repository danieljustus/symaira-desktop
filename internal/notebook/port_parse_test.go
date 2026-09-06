package notebook

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type portNotebookCase struct {
	Name   string      `json:"name"`
	Path   string      `json:"path"`
	Input  string      `json:"input"`
	OK     bool        `json:"ok"`
	Output interface{} `json:"output,omitempty"`
	Error  string      `json:"error,omitempty"`
}

type portNotebookFixture struct {
	SchemaVersion int                `json:"schema_version"`
	Cases         []portNotebookCase `json:"cases"`
}

func TestNotebookParseInventory(t *testing.T) {
	full, err := render(&Notebook{
		ID: "research", Title: "Forschung ✓", Description: "Überblick", Created: "2026-09-06T12:00:00Z",
		Sources: []string{"zeta.md", "äpfel.md", "zeta.md"}, Query: "tag:研究",
	})
	if err != nil {
		t.Fatal(err)
	}
	minimal, err := render(&Notebook{ID: "empty", Title: "Empty", Created: "2026-09-06", Sources: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	inputs := []struct{ name, path, input string }{
		{"full-sorted-duplicates-preserved", "notebooks/research.md", string(full)},
		{"minimal", "notebooks/empty.md", string(minimal)},
		{"legacy-id-fallback", "notebooks/legacy.md", "---\ntype: notebook\ntitle: Legacy\ncreated: 2026-01-01\nsources: []\n---\n"},
		{"mixed-sources", "notebooks/mixed.md", "---\ntype: notebook\ntitle: Mixed\ncreated: 2026-01-01\nsources: [b.md, 7, \"\", a.md]\n---\n"},
		{"not-a-notebook", "notebooks/note.md", "---\ntype: note\ntitle: No\n---\n"},
		{"malformed", "notebooks/bad.md", "---\ntype: notebook\nsources: [\n---\n"},
	}
	fixture := portNotebookFixture{SchemaVersion: 1, Cases: make([]portNotebookCase, 0, len(inputs))}
	for _, item := range inputs {
		parsed, parseErr := parse(item.path, []byte(item.input))
		entry := portNotebookCase{Name: item.name, Path: item.path, Input: item.input, OK: parseErr == nil}
		if parseErr != nil {
			entry.Error = parseErr.Error()
		} else {
			entry.Output = parsed
		}
		fixture.Cases = append(fixture.Cases, entry)
	}
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	fixturePath := filepath.Join("..", "..", "testdata", "port", "vault", "notebook.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.WriteFile(fixturePath, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	//nolint:gosec // fixturePath is a fixed repo-relative path
	current, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, encoded) {
		t.Fatal("notebook fixture is stale; run make port-fixtures-generate")
	}
}
