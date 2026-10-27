package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/history"
)

// Fixed old timestamps make retention independent of the generating clock.
func TestPortRestoreProcessContract(t *testing.T) {
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

	second, err := json.Marshal(history.TrashEntry{Name: "text-item", OriginalPath: "sub/plain.txt", DeletedAt: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), Size: 4})
	if err != nil {
		t.Fatal(err)
	}
	files[".symdesk/trash/text-item"] = "text"
	files[".symdesk/trash/text-item.trashinfo.json"] = string(second)
	occupied, err := json.Marshal(history.TrashEntry{Name: "occupied.md", OriginalPath: "note.md", DeletedAt: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), Size: 4})
	if err != nil {
		t.Fatal(err)
	}
	files[".symdesk/trash/occupied.md"] = "copy"
	files[".symdesk/trash/occupied.md.trashinfo.json"] = string(occupied)
	cases := []duplicatesPortCase{
		{Name: "snapshot", Files: files, Steps: makeSteps(
			[]string{"restore", "note.md", "--json"},
			[]string{"restore", "note.md", "--at", ids[0][:12], "--json"},
			[]string{"restore", "note.md", "--at=" + ids[1]},
			[]string{"restore", "note.md", "--at=nope", "--json"},
			[]string{"restore", "missing.md", "--json"},
			[]string{"restore", "../outside.md", "--json"},
			[]string{"graph", "--json"},
		)},

		{Name: "latest-empty", Files: files, Steps: makeSteps(
			[]string{"restore", "note.md", "--at=", "--output=json"},
			[]string{"restore", "note.md", "--at=nope", "--at=" + ids[1], "--json"},
		)},
		{Name: "trash-text", Files: files, Steps: makeSteps(
			[]string{"trash", "restore", "text-item"},
			[]string{"trash", "restore", "occupied.md", "--json"},
			[]string{"trash", "list", "--json"},
		)},
		{Name: "trash", Files: files, Steps: makeSteps(
			[]string{"trash", "restore", "old-note.md", "--json"},
			[]string{"trash", "list", "--json"},
			[]string{"graph", "--json"},
			[]string{"trash", "restore", "../bad", "--json"},
		)},
		{Name: "errors", Files: files, Steps: makeSteps(
			[]string{"restore"}, []string{"restore", "a", "b", "--json"},
			[]string{"restore", "note.md", "--at"}, []string{"restore", "note.md", "--unknown"},
			[]string{"restore", "note.md", "--json=wat"},
			[]string{"trash", "restore"}, []string{"trash", "restore", "a", "b", "--json"},
		)},
		{Name: "no-vault", NoVault: true, Steps: makeSteps(
			[]string{"restore", "note.md", "--json"}, []string{"trash", "restore", "old-note.md", "--json"},
		)},
	}

	fixture := duplicatesPortFixture{SchemaVersion: 1}
	for _, c := range cases {
		var states []map[string][][]string
		observed := observeDuplicatesCase(t, binary, c, func(vault, home string) {
			state := linkProcessState(t, vault, home)
			for _, row := range state["vault"] {
				row[0], row[1] = normalizeRestoreFile(row[0], row[1])
			}
			states = append(states, state)
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
	path := filepath.Join(filepath.Dir(source), "../../testdata/port/cli/restore-process.json")
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
		t.Fatal("restore process fixture stale")
	}
}

// Only newly written clock fields and daily journal filenames are volatile.
// Seeded 2020/2021 snapshot timestamps remain exact-byte assertions.
func normalizeRestoreFile(path, content string) (string, string) {
	if strings.HasPrefix(path, ".symdesk/journal/") {
		path = ".symdesk/journal/$DATE.ndjson"
	}
	if strings.HasPrefix(path, ".symdesk/journal/") || strings.HasPrefix(path, ".symdesk/history/manifest/") {
		parts := strings.Split(content, `"`)
		for i, p := range parts {
			if at, err := time.Parse(time.RFC3339Nano, p); err == nil && at.Year() >= 2026 {
				parts[i] = "$NOW"
			}
		}
		content = strings.Join(parts, `"`)
	}
	return path, content
}
