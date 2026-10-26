package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/history"
)

// Fixed old timestamps make retention independent of the generating clock.
func TestPortHistoryReadProcessContract(t *testing.T) {
	binary := buildIndexRetrievalBinary(t, t.TempDir())
	t.Setenv("PATH", t.TempDir())
	files := map[string]string{"note.md": "---\ntitle: Current\n---\ncurrent\n"}
	var entries []history.Entry
	var ids []string
	for i, body := range []string{"old <&> snapshot\n", "new snapshot\n"} {
		hash := sha256.Sum256([]byte(body))
		id := hex.EncodeToString(hash[:])
		ids = append(ids, id)
		files[".symdesk/history/objects/"+id] = body
		entries = append(entries, history.Entry{ID: id, Timestamp: time.Date(2020+i, 1, 2, 3, 4, 5, 0, time.UTC), Size: int64(len(body))})
	}
	raw, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	files[".symdesk/history/manifest/note.md.json"] = string(raw)
	meta, err := json.Marshal(history.TrashEntry{Name: "old-note.md", OriginalPath: "old.md", DeletedAt: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), Size: 4})
	if err != nil {
		t.Fatal(err)
	}
	files[".symdesk/trash/old-note.md"] = "old\n"
	files[".symdesk/trash/old-note.md.trashinfo.json"] = string(meta)
	files[".symdesk/trash/broken.trashinfo.json"] = "not-json"
	makeSteps := func(argv ...[]string) []duplicatesPortStep {
		var out []duplicatesPortStep
		for _, a := range argv {
			out = append(out, duplicatesPortStep{Args: a})
		}
		return out
	}
	cases := []duplicatesPortCase{
		{Name: "read", Files: files, Steps: makeSteps(
			[]string{"history", "note.md", "--json"}, []string{"history", "missing.md"}, []string{"history", "missing.md", "--json"},
			[]string{"history", "./nested/../note.md", "--output=json"},
			[]string{"history", "show", ids[0]}, []string{"history", "show", ids[0], "--json"},
			[]string{"history", "show", ids[1], "--output=json"},
			[]string{"history", "show", "bad", "--json"}, []string{"history", "show", "bad"},
			[]string{"history", "../escape.md", "--json"},
			[]string{"trash", "list", "--json"},
			[]string{"history", "note.md", "--json=false", "--json=true"},
			[]string{"history", "note.md", "--output=text", "--output=json"},
		)},
		{Name: "prune", Files: files, Steps: makeSteps(
			[]string{"history", "prune", "--max-per-file=1", "--max-age-days=0", "--checkpoint-age-days=0", "--json"},
			[]string{"history", "note.md", "--json"},
			[]string{"history", "prune", "--max-per-file", "0x2", "--max-age-days", "0", "--checkpoint-age-days", "0"},
			[]string{"history", "prune", "--max-per-file=0", "--max-per-file=2", "--json"},
			[]string{"history", "prune", "--max-age-days=-1", "--json"},
		)},
		{Name: "purge", Files: files, Steps: makeSteps(
			[]string{"trash", "purge", "--older-than-days=1", "--json"},
			[]string{"trash", "list", "--json"}, []string{"trash", "list"},
			[]string{"trash", "purge", "--all"}, []string{"trash", "purge", "--older-than-days=-1", "--json"},
		)},
		{Name: "empty", Steps: makeSteps(
			[]string{"history", "note.md"}, []string{"history", "note.md", "--json"},
			[]string{"history", "prune", "--json"}, []string{"history", "prune"},
			[]string{"trash", "list"}, []string{"trash", "list", "--json"},
			[]string{"trash", "purge", "--json"}, []string{"trash", "purge", "--all"},
			[]string{"history"}, []string{"history", "a", "b", "--json"},
			[]string{"history", "show"}, []string{"history", "show", "a", "b", "--json"},
			[]string{"history", "prune", "--max-per-file=nope", "--json"},
			[]string{"trash", "purge", "--older-than-days=nope", "--json"},
			[]string{"trash", "list", "extra"}, []string{"history", "prune", "extra", "--json"},
			[]string{"history", "tasks", "extra", "--json"},
			[]string{"trash", "purge", "--all=false", "--all=TRUE", "--all=false", "--json"},
			[]string{"trash", "purge", "--all=wat"},
			[]string{"--json", "history", "prune", "--max-age-days=nope"},
			[]string{"history", "prune", "--max-age-days=9223372036854775807", "--json"},
			[]string{"history", "prune", "--max-per-file"},
			[]string{"history", "note.md", "--unknown"}, []string{"history", "note.md", "--json=wat"},
		)},
		{Name: "no-vault", NoVault: true, Steps: makeSteps([]string{"history", "note.md"}, []string{"history", "note.md", "--json"}, []string{"trash", "list", "--json"}, []string{"history", "prune", "--json"})},
	}
	fixture := duplicatesPortFixture{SchemaVersion: 1}
	for _, c := range cases {
		var states []map[string][][]string
		observed := observeDuplicatesCase(t, binary, c, func(vault, home string) { states = append(states, linkProcessState(t, vault, home)) })
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
	path := filepath.Join(filepath.Dir(source), "../../testdata/port/cli/history-read-process.json")
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
		t.Fatal("history read process fixture stale")
	}
}
