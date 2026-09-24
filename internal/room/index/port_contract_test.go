package index

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
	"github.com/danieljustus/symaira-desktop/internal/room/journal"
)

type indexColumn struct {
	Table      string `json:"table"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	NotNull    int    `json:"not_null"`
	PrimaryKey int    `json:"primary_key"`
}
type indexEvent struct {
	ID      string `json:"id"`
	Room    string `json:"room"`
	Author  string `json:"author"`
	Seq     int64  `json:"seq"`
	Lamport int64  `json:"lamport"`
	TS      string `json:"ts"`
	Kind    string `json:"kind"`
	Body    string `json:"body"`
}
type indexMember struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	PublicKey string `json:"public_key"`
	Role      string `json:"role"`
	Kind      string `json:"kind"`
}
type indexNote struct {
	EventID string `json:"event_id"`
	Author  string `json:"author"`
	TS      string `json:"ts"`
	Text    string `json:"text"`
}
type indexDecision struct {
	EventID string `json:"event_id"`
	Author  string `json:"author"`
	TS      string `json:"ts"`
	Text    string `json:"text"`
	Refs    string `json:"refs"`
}
type indexSnapshot struct {
	Tables    []string        `json:"tables"`
	Columns   []indexColumn   `json:"columns"`
	Events    []indexEvent    `json:"events"`
	Members   []indexMember   `json:"members"`
	Notes     []indexNote     `json:"notes"`
	Decisions []indexDecision `json:"decisions"`
}
type indexCorruption struct {
	ErrorClass  string        `json:"error_class"`
	ErrorPrefix string        `json:"error_prefix"`
	Snapshot    indexSnapshot `json:"snapshot"`
}
type indexFixture struct {
	SchemaVersion int             `json:"schema_version"`
	SourceHash    string          `json:"source_hash"`
	Rebuild       indexSnapshot   `json:"rebuild"`
	Corruption    indexCorruption `json:"corruption"`
}

func TestPortSymRoomIndexOracle(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("source location unavailable")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "../../.."))
	indexSource, err := os.ReadFile(filepath.Join(root, "internal/room/index/index.go"))
	if err != nil {
		t.Fatal(err)
	}
	membersSource, err := os.ReadFile(filepath.Join(root, "internal/room/members/members.go"))
	if err != nil {
		t.Fatal(err)
	}
	sourceBytes := append(append([]byte(nil), indexSource...), membersSource...)
	sourceDigest := sha256.Sum256(sourceBytes)

	room := t.TempDir()
	journalDir := filepath.Join(room, "journal")
	if err := os.MkdirAll(journalDir, 0o700); err != nil {
		t.Fatal(err)
	}
	const owner = "mem_owner"
	const rootKey = "4141414141414141414141414141414141414141414141414141414141414141"
	const guestKey = "4242424242424242424242424242424242424242424242424242424242424242"
	events := []*event.Event{
		{V: 1, ID: "ev_root", Room: "rm_index", Author: owner, Seq: 1, Lamport: 1, TS: "2026-09-23T10:00:00.000Z", Kind: event.KindRoomCreated, Body: json.RawMessage(`{"name":"Oracle room","public_key":"` + rootKey + `"}`)},
		{V: 1, ID: "ev_member", Room: "rm_index", Author: owner, Seq: 2, Lamport: 2, TS: "2026-09-23T10:00:01.000Z", Kind: event.KindMemberAdded, Body: json.RawMessage(`{"id":"mem_guest","name":"Guest","public_key":"` + guestKey + `","role":"agent","kind":"agent"}`)},
		{V: 1, ID: "ev_note_1", Room: "rm_index", Author: owner, Seq: 3, Lamport: 3, TS: "2026-09-23T10:00:02.000Z", Kind: event.KindNotePosted, Body: json.RawMessage(`{"text":"café note"}`)},
		{V: 1, ID: "ev_decision", Room: "rm_index", Author: owner, Seq: 4, Lamport: 4, TS: "2026-09-23T10:00:03.000Z", Kind: event.KindDecisionRecorded, Body: json.RawMessage(`{"text":"recorded choice","refs":["ev_note_1","doc_2"]}`)},
		{V: 1, ID: "ev_note_2", Room: "rm_index", Author: owner, Seq: 5, Lamport: 5, TS: "2026-09-23T10:00:04.000Z", Kind: event.KindNotePosted, Body: json.RawMessage(`{"text":"second note"}`)},
	}
	segment, err := os.OpenFile(filepath.Join(journalDir, owner+".jsonl"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // journalDir is the test's temporary room directory
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		line, err := ev.MarshalJSONLine()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := segment.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	if err := segment.Close(); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(room, ".symroom", "index.sqlite")
	seedStaleIndex(t, dbPath)
	if err := New(dbPath).Rebuild(journal.New(journalDir)); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	insertStaleEvent(t, dbPath)
	if err := New(dbPath).Rebuild(journal.New(journalDir)); err != nil {
		t.Fatalf("second Rebuild: %v", err)
	}
	rebuilt := snapshotIndex(t, dbPath)
	if containsString(rebuilt.Tables, "stale_table") || len(rebuilt.Events) != len(events) {
		t.Fatalf("rebuild did not replace stale derived data: %+v", rebuilt)
	}

	corruptRoom := t.TempDir()
	corruptJournal := filepath.Join(corruptRoom, "journal")
	if err := os.MkdirAll(corruptJournal, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(corruptJournal, "broken.jsonl"), []byte("{\"v\":\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	corruptDB := filepath.Join(corruptRoom, ".symroom", "index.sqlite")
	seedStaleIndex(t, corruptDB)
	corruptErr := New(corruptDB).Rebuild(journal.New(corruptJournal))
	if corruptErr == nil || !strings.HasPrefix(corruptErr.Error(), "merge all events: read segment broken: unmarshal line:") {
		t.Fatalf("unexpected corrupt journal result: %v", corruptErr)
	}
	corruptSnapshot := snapshotIndex(t, corruptDB)
	if len(corruptSnapshot.Events) != 0 || containsString(corruptSnapshot.Tables, "stale_table") {
		t.Fatalf("corrupt rebuild left stale rows: %+v", corruptSnapshot)
	}

	doc := indexFixture{SchemaVersion: 1, SourceHash: hex.EncodeToString(sourceDigest[:]), Rebuild: rebuilt, Corruption: indexCorruption{ErrorClass: "malformed_event", ErrorPrefix: "merge all events: read segment broken: unmarshal line:", Snapshot: corruptSnapshot}}
	encoded, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	path := filepath.Join(root, "testdata/port/room/index.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.WriteFile(path, encoded, 0o644); err != nil { //nolint:gosec // generated contract fixture is intentionally world-readable
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, want) {
		t.Fatal("Go Room index fixture changed; regenerate explicitly with PORT_GENERATE=1")
	}
}

func seedStaleIndex(t *testing.T, dbPath string) {
	t.Helper()
	db, err := sqlitekit.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE stale_table (value TEXT); INSERT INTO stale_table VALUES ('stale'); CREATE TABLE events (id TEXT PRIMARY KEY); INSERT INTO events VALUES ('stale');"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func insertStaleEvent(t *testing.T, dbPath string) {
	t.Helper()
	db, err := sqlitekit.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO events (id) VALUES ('stale')"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func snapshotIndex(t *testing.T, dbPath string) indexSnapshot {
	t.Helper()
	db, err := sqlitekit.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close index database: %v", err)
		}
	})
	out := indexSnapshot{Tables: []string{}, Columns: []indexColumn{}, Events: []indexEvent{}, Members: []indexMember{}, Notes: []indexNote{}, Decisions: []indexDecision{}}
	tables, err := db.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	for tables.Next() {
		var name string
		if err := tables.Scan(&name); err != nil {
			t.Fatal(err)
		}
		out.Tables = append(out.Tables, name)
	}
	if err := tables.Close(); err != nil {
		t.Fatal(err)
	}
	for _, table := range out.Tables {
		rows, err := db.Query("PRAGMA table_info(" + table + ")")
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var cid, notNull, primary int
			var name, kind string
			var defaultValue any
			if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primary); err != nil {
				t.Fatal(err)
			}
			out.Columns = append(out.Columns, indexColumn{Table: table, Name: name, Type: kind, NotNull: notNull, PrimaryKey: primary})
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	queryEvents := "SELECT id,room,author,seq,lamport,ts,kind,body FROM events ORDER BY lamport,ts,author,seq,id"
	rows, err := db.Query(queryEvents)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var row indexEvent
		if err := rows.Scan(&row.ID, &row.Room, &row.Author, &row.Seq, &row.Lamport, &row.TS, &row.Kind, &row.Body); err != nil {
			t.Fatal(err)
		}
		out.Events = append(out.Events, row)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	rows, err = db.Query("SELECT id,name,public_key,role,kind FROM members ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var row indexMember
		if err := rows.Scan(&row.ID, &row.Name, &row.PublicKey, &row.Role, &row.Kind); err != nil {
			t.Fatal(err)
		}
		out.Members = append(out.Members, row)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	rows, err = db.Query("SELECT event_id,author,ts,text FROM notes ORDER BY ts,event_id")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var row indexNote
		if err := rows.Scan(&row.EventID, &row.Author, &row.TS, &row.Text); err != nil {
			t.Fatal(err)
		}
		out.Notes = append(out.Notes, row)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	rows, err = db.Query("SELECT event_id,author,ts,text,refs FROM decisions ORDER BY ts,event_id")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var row indexDecision
		if err := rows.Scan(&row.EventID, &row.Author, &row.TS, &row.Text, &row.Refs); err != nil {
			t.Fatal(err)
		}
		out.Decisions = append(out.Decisions, row)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	return out
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
