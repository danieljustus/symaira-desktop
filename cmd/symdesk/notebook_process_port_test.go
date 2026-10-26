package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func notebookReadCases() []duplicatesPortCase {
	step := func(args ...string) duplicatesPortStep { return duplicatesPortStep{Args: args} }
	files := map[string]string{
		"notebooks/research.md": "---\ntype: notebook\nnotebook_id: research\ntitle: Research & notes\ncreated: fixed\ndescription: Context <here>\nsources: [notes/source.md, missing.md, notes/source.md]\nquery: old query\n---\n# Research\n",
		"notebooks/empty.md":    "---\ntype: notebook\nnotebook_id: empty\ntitle: Alpha\ncreated: fixed\nsources: []\n---\n",
		"notebooks/ordinary.md": "# Not a notebook\n",
		"notebooks/broken.md":   "---\ntype: notebook\nsources: {bad: shape}\n---\n",
		"notes/source.md":       "---\ntitle: Source title\n---\nBody\n",
	}
	var steps []duplicatesPortStep
	for _, args := range [][]string{{"notebook", "list"}, {"notebook", "list", "ignored"}, {"notebook", "show", "research"}, {"notebook", "show", "notebooks/research"}, {"notebook", "show", "notebooks/research.md"}, {"notebook", "show", "other/research"}, {"notebook", "show", "empty"}, {"notebook", "show", "missing"}, {"notebook", "show", ""}, {"notebook", "show"}, {"notebook", "show", "a", "b"}, {"notebook", "show", "ordinary"}} {
		steps = append(steps, step(args...), step(append([]string{"--json"}, args...)...))
	}
	steps = append(steps, step("notebook", "--output", "json", "show", "research"), step("notebook", "show", "empty", "--output=yaml"), step("notebook", "show", "--json", "empty"), step("notebook", "show", "--", "research"))
	return []duplicatesPortCase{
		{Name: "reads", Files: files, SkipIndex: true, Steps: steps},
		{Name: "empty", SkipIndex: true, Steps: []duplicatesPortStep{step("notebook", "list"), step("--json", "notebook", "list")}},
		{Name: "no-vault", NoVault: true, SkipIndex: true, Steps: []duplicatesPortStep{step("notebook", "list"), step("--json", "notebook", "show", "missing")}},
	}
}

func TestPortNotebookReadProcessContract(t *testing.T) {
	binary := buildIndexRetrievalBinary(t, t.TempDir())
	observed := viewsPortFixture{SchemaVersion: 1}
	for _, c := range notebookReadCases() {
		result := observeViewsCase(t, binary, c)
		for i := range result.Steps {
			result.Steps[i].Stdout = normalizeNotebookPaths(viewsPointer.ReplaceAllString(result.Steps[i].Stdout, "$$PTR"))
		}
		observed.Cases = append(observed.Cases, result)
	}
	encoded, err := json.MarshalIndent(observed, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("source path unavailable")
	}
	path := filepath.Join(filepath.Dir(source), "../../testdata/port/cli/notebook-read-process.json")
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
		t.Fatal("notebook read process fixture drift; regenerate deliberately")
	}
}

// Notebook references use slash-normalized paths in the portable process corpus.
func normalizeNotebookPaths(text string) string {
	text = strings.ReplaceAll(text, `notebooks\\`, `notebooks/`)
	return strings.ReplaceAll(text, `notebooks\`, `notebooks/`)
}
