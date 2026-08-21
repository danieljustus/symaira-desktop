package index

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"

	"github.com/danieljustus/symaira-corekit/sqlitekit"
	"github.com/danieljustus/symaira-room/internal/event"
	"github.com/danieljustus/symaira-room/internal/journal"
	"github.com/danieljustus/symaira-room/internal/members"
)

type Indexer struct {
	DBPath string
}

func New(dbPath string) *Indexer {
	return &Indexer{DBPath: dbPath}
}

func (idx *Indexer) Rebuild(j *journal.Journal) error {
	_ = os.Remove(idx.DBPath)

	if err := os.MkdirAll(filepath.Dir(idx.DBPath), 0755); err != nil {
		return fmt.Errorf("mkdir db dir: %w", err)
	}

	db, err := sqlitekit.Open(idx.DBPath)
	if err != nil {
		return fmt.Errorf("open sqlite db: %w", err)
	}
	defer func() { _ = db.Close() }()

	schema := `
CREATE TABLE IF NOT EXISTS events (
    id TEXT PRIMARY KEY,
    room TEXT,
    author TEXT,
    seq INTEGER,
    lamport INTEGER,
    ts TEXT,
    kind TEXT,
    body TEXT
);
CREATE TABLE IF NOT EXISTS members (
    id TEXT PRIMARY KEY,
    name TEXT,
    public_key TEXT,
    role TEXT,
    kind TEXT
);
CREATE TABLE IF NOT EXISTS notes (
    event_id TEXT PRIMARY KEY,
    author TEXT,
    ts TEXT,
    text TEXT
);
CREATE TABLE IF NOT EXISTS decisions (
    event_id TEXT PRIMARY KEY,
    author TEXT,
    ts TEXT,
    text TEXT,
    refs TEXT
);
`
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("init schema: %w", err)
	}

	merged, err := j.MergeAll()
	if err != nil {
		return fmt.Errorf("merge all events: %w", err)
	}

	state := members.NewState()

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	stmtEv, err := tx.Prepare("INSERT OR REPLACE INTO events (id, room, author, seq, lamport, ts, kind, body) VALUES (?, ?, ?, ?, ?, ?, ?, ?)")
	if err != nil {
		return err
	}
	defer func() { _ = stmtEv.Close() }()

	stmtNote, err := tx.Prepare("INSERT OR REPLACE INTO notes (event_id, author, ts, text) VALUES (?, ?, ?, ?)")
	if err != nil {
		return err
	}
	defer func() { _ = stmtNote.Close() }()

	stmtDec, err := tx.Prepare("INSERT OR REPLACE INTO decisions (event_id, author, ts, text, refs) VALUES (?, ?, ?, ?, ?)")
	if err != nil {
		return err
	}
	defer func() { _ = stmtDec.Close() }()

	for _, ev := range merged {
		_ = state.ApplyEvent(ev)

		if _, err := stmtEv.Exec(ev.ID, ev.Room, ev.Author, ev.Seq, ev.Lamport, ev.TS, ev.Kind, string(ev.Body)); err != nil {
			return fmt.Errorf("insert event %s: %w", ev.ID, err)
		}

		if ev.Kind == event.KindNotePosted {
			var bodyMap map[string]string
			_ = json.Unmarshal(ev.Body, &bodyMap)
			if _, err := stmtNote.Exec(ev.ID, ev.Author, ev.TS, bodyMap["text"]); err != nil {
				return err
			}
		}

		if ev.Kind == event.KindDecisionRecorded {
			var bodyMap struct {
				Text string   `json:"text"`
				Refs []string `json:"refs"`
			}
			_ = json.Unmarshal(ev.Body, &bodyMap)
			refsJSON, _ := json.Marshal(bodyMap.Refs)
			if _, err := stmtDec.Exec(ev.ID, ev.Author, ev.TS, bodyMap.Text, string(refsJSON)); err != nil {
				return err
			}
		}
	}

	stmtMem, err := tx.Prepare("INSERT OR REPLACE INTO members (id, name, public_key, role, kind) VALUES (?, ?, ?, ?, ?)")
	if err != nil {
		return err
	}
	defer func() { _ = stmtMem.Close() }()

	for _, m := range state.Members {
		if _, err := stmtMem.Exec(m.ID, m.Name, string(m.PublicKey), string(m.Role), string(m.Kind)); err != nil {
			return err
		}
	}

	return tx.Commit()
}
