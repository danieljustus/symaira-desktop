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
	"time"
)

// Production NoteNew/NoteDaily/history have no injectable process clock.
// Only wall-clock timestamp/date/time fields are normalized; all remaining
// Markdown, history blobs/manifests, trash bytes and logical index rows compare.
func noteDocNormalize(s string) string {
	s = regexp.MustCompile(`[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?Z`).ReplaceAllString(s, "@NOW@")
	s = strings.ReplaceAll(s, time.Now().UTC().Format("2006-01-02"), "@TODAY@")
	s = regexp.MustCompile(`clock-time: [0-9]{2}:[0-9]{2}`).ReplaceAllString(s, "clock-time: @TIME@")
	return s
}

func TestPortNoteDocProcessContract(t *testing.T) {
	binary := buildIndexRetrievalBinary(t, t.TempDir())
	files := map[string]string{
		"a.md":               "---\ntitle: Alpha <&>\ntags: [finance]\n---\nOriginal body #inline\n",
		"b.md":               "---\ntitle: Beta\n---\nOther body\n",
		"plain.md":           "# Plain\nBody without frontmatter\n",
		"crlf.md":            "---\r\ntitle: CRLF\r\ntags: [old]\r\n---\r\nBody\r\n",
		"templates/basic.md": "# {{title}}\nclock-date: {{date}}\nclock-time: {{time}}\n",
		"templates/front.md": "---\ntags: [templated]\n---\n{{title}}\n",
		"templates/daily.md": "# Daily {{title}}\nclock-date: {{date}}\nclock-time: {{time}}\n",
	}
	var steps []duplicatesPortStep
	add := func(a ...string) { steps = append(steps, duplicatesPortStep{Args: a}) }
	for _, group := range []string{"doc", "docs"} {
		for _, pair := range [][2]string{{"status", "paid"}, {"due", "2028-02-29"}, {"type", "invoice <&>"}, {"correspondent", "Some Bank"}} {
			add("--json", group, pair[0], "a.md", pair[1])
			add(group, pair[0], "b.md", pair[1])
		}
		add(group, "tag", "add", " tax ", "a.md", "b.md", "--json")
		add(group, "tag", "add", "tax", "a.md", "--json")
		add(group, "tag", "remove", "finance", "a.md", "--json")
		add(group, "tag", "remove", "absent", "b.md", "--json")
		add(group, "status", "a.md", "bogus", "--json")
		add(group, "type", "b.md", " ", "--json")
		add(group, "correspondent", "a.md", "", "--json")
		add(group, "tag", "add", " ", "b.md", "--json")
		add(group, "status", "--json")
		add(group, "status", "a.md", "--json")
		add(group, "tag", "add", "x", "--json")
		add(group, "due", "plain.md", "", "--json")
		add(group, "tag", "add", "new", "crlf.md", "--json")
		add(group, "due", "crlf.md", "2030-01-01", "--json")
		add(group, "status", "../outside.md", "open", "--json")
		add(group, "status", "a.md", "paid", "--output=text", "--json")
		add(group, "due", "b.md", "2027-01-01", "--output=yaml")
		add(group, "asn", "a.md", "next", "--json")
		add(group, "asn", "b.md", "1", "--json")
		add(group, "asn", "b.md", "next", "--json")
		add(group, "asn", "a.md", "0", "--json")
		add(group, "asn", "a.md", "bad", "--json")
		add(group, "asn", "a.md", "9223372036854775807", "--json")
	}
	for _, group := range []string{"doc", "docs"} {
		add(group, "status", "a.md", "b.md", "done", "--json")
		steps = append(steps, duplicatesPortStep{Args: []string{group, "status", "submitted", "--stdin", "--json"}, Stdin: " a.md\n\nb.md\n"})
		steps = append(steps, duplicatesPortStep{Args: []string{group, "tag", "add", "from-stdin", "--stdin", "--json"}, Stdin: "a.md\nb.md\n"})
		steps = append(steps, duplicatesPortStep{Args: []string{group, "due", "2029-01-01", "--stdin", "--json"}, Stdin: "\n "})
		add(group, "status", "a.md", "paid", "--json=false", "--output=json", "--output=text")
		add(group, "status", "a.md", "paid", "--json", "--unknown")
		add(group, "status", "a.md", "paid", "--unknown", "--json")
		add(group, "status", "a.md", "paid", "--json", "--output")
		add(group, "status", "a.md", "paid", "--json", "--vault=$ROOT/vault")
	}
	add("note", "new", "--title", "Repeated", "--title", "Last", "body", "extra ignored", "--json")
	add("note", "daily", "--json")
	for _, date := range []string{"2025-02-29", "2024-13-01", "bad", "2024-1-02", "2024-01", "2024-01-02extra"} {
		add("note", "daily", "--date", date, "--json")
	}
	add("note", "new", "--title", "Hello World", "body <&>\n[[b]]", "--json")
	add("note", "new", "--title", "a", "replacement", "--json")
	add("note", "new", "--title", "From Template", "--template", "basic", "append", "--json")
	add("note", "new", "--title", "Front Template", "--template", "front", "append", "--json")
	add("note", "new", "--title", "Missing Template", "--template", "absent", "body", "--json")
	add("note", "new", "--title", "", "--json")
	add("note", "new", "--json")
	add("note", "daily", "--date", "2024-02-29", "--json")
	add("note", "daily", "--date", "2024-02-29", "--json")
	add("note", "move", "b.md", "moved.md", "--json")
	add("note", "delete", "moved.md", "--json")
	add("note", "move", "a.md", "--json")
	add("note", "delete", "--json")
	add("note", "move", "../escape.md", "safe.md", "--json")
	var states []map[string][][]string
	c := duplicatesPortCase{Name: "mutations", Files: files, SkipIndex: true, Steps: steps}
	observed := observeDuplicatesCase(t, binary, c, func(vault, home string) {
		state := linkProcessState(t, vault, home)
		// Unix leaves its advisory-lock file, Windows removes the exclusive-create lock.
		rows := state["vault"][:0]
		for _, row := range state["vault"] {
			if row[0] != ".symdesk/asn.lock" {
				rows = append(rows, row)
			}
		}
		state["vault"] = rows
		for _, rows := range state {
			for _, row := range rows {
				for i := range row {
					row[i] = noteDocNormalize(row[i])
				}
			}
		}
		states = append(states, state)
	})
	for i := range observed.Steps {
		observed.Steps[i].State = states[i]
		observed.Steps[i].Stdout = noteDocNormalize(observed.Steps[i].Stdout)
		observed.Steps[i].Stderr = noteDocNormalize(observed.Steps[i].Stderr)
	}
	fixture := duplicatesPortFixture{SchemaVersion: 1, Cases: []duplicatesPortCase{observed}}
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	_, source, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(source), "../../testdata/port/cli/note-doc-process.json")
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
		portProcessFixtureDifference(t, "note/doc process fixture stale", current, encoded)
	}
}
