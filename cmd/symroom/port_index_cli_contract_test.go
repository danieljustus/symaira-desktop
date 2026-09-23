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

	"github.com/danieljustus/symaira-corekit/sqlitekit"
	"github.com/danieljustus/symaira-desktop/internal/room/event"
)

const indexCLIContractPath = "testdata/port/room/index-cli.json"

type indexCLIContract struct {
	SchemaVersion int               `json:"schema_version"`
	SourceHashes  map[string]string `json:"source_hashes"`
	JournalLine   string            `json:"journal_line"`
	Cases         []indexCLICase    `json:"cases"`
}

type indexCLICase struct {
	Name         string   `json:"name"`
	Args         []string `json:"args"`
	RoomOverride bool     `json:"room_override"`
	Corrupt      bool     `json:"corrupt,omitempty"`
	ExitCode     int      `json:"exit_code"`
	Stdout       string   `json:"stdout"`
	StderrPrefix string   `json:"stderr_prefix"`
	DBExists     bool     `json:"db_exists"`
	EventIDs     []string `json:"event_ids"`
}

func TestPortIndexCLIContract(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("source location unavailable")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "../.."))
	fixture := indexCLIContract{SchemaVersion: 1, SourceHashes: make(map[string]string)}
	for _, rel := range []string{"cmd/symroom/main.go", "cmd/symroom/cmd_index.go", "internal/room/index/index.go"} {
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(data)
		fixture.SourceHashes[rel] = hex.EncodeToString(hash[:])
	}
	e := &event.Event{V: 1, ID: "ev_cli_note", Room: "rm_cli", Author: "mem_cli", Seq: 1, Lamport: 1, TS: "2026-09-23T10:00:00.000Z", Kind: event.KindNotePosted, Body: json.RawMessage(`{"text":"from another room"}`)}
	line, err := e.MarshalJSONLine()
	if err != nil {
		t.Fatal(err)
	}
	fixture.JournalLine = string(line)
	for _, vector := range []indexCLICase{
		{Name: "usage", Args: []string{"symroom", "index"}},
		{Name: "unknown", Args: []string{"symroom", "index", "other"}},
		{Name: "rebuild-relative-db", Args: []string{"symroom", "index", "rebuild", "ignored"}, RoomOverride: true},
		{Name: "corrupt-journal", Args: []string{"symroom", "index", "rebuild"}, RoomOverride: true, Corrupt: true},
	} {
		caseRoot := filepath.Join(t.TempDir(), vector.Name)
		room := filepath.Join(caseRoot, "room")
		if err := os.MkdirAll(filepath.Join(room, "journal"), 0o700); err != nil {
			t.Fatal(err)
		}
		journalLine := fixture.JournalLine
		if vector.Corrupt {
			journalLine = "{\"v\":\n"
		}
		if err := os.WriteFile(filepath.Join(room, "journal", "mem_cli.jsonl"), []byte(journalLine), 0o600); err != nil {
			t.Fatal(err)
		}
		cwd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chdir(caseRoot); err != nil {
			t.Fatal(err)
		}
		if vector.RoomOverride {
			t.Setenv("SYMROOM_ROOM_DIR", room)
		} else {
			t.Setenv("SYMROOM_ROOM_DIR", "")
		}
		var stdout, stderr bytes.Buffer
		vector.ExitCode = runIndex(vector.Args, &stdout, &stderr)
		if err := os.Chdir(cwd); err != nil {
			t.Fatal(err)
		}
		vector.Stdout = stdout.String()
		vector.StderrPrefix = stderr.String()
		if vector.Corrupt {
			const prefix = "Error rebuilding index: merge all events: read segment mem_cli: unmarshal line:"
			if !strings.HasPrefix(vector.StderrPrefix, prefix) {
				t.Fatalf("unexpected corrupt index error: %s", vector.StderrPrefix)
			}
			vector.StderrPrefix = prefix
		}
		dbPath := filepath.Join(caseRoot, ".symroom", "index.sqlite")
		if _, err := os.Stat(dbPath); err == nil {
			vector.DBExists = true
			db, err := sqlitekit.Open(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			rows, err := db.Query("SELECT id FROM events ORDER BY id")
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					t.Fatal(err)
				}
				vector.EventIDs = append(vector.EventIDs, id)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			_ = rows.Close()
			_ = db.Close()
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		fixture.Cases = append(fixture.Cases, vector)
	}
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join(root, indexCLIContractPath)
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, want) {
		t.Fatal("Go Room index CLI fixture changed; regenerate explicitly with PORT_GENERATE=1")
	}
}
