package index

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danieljustus/symaira-corekit/sqlitekit"
	"github.com/danieljustus/symaira-room/internal/event"
	"github.com/danieljustus/symaira-room/internal/identity"
	"github.com/danieljustus/symaira-room/internal/journal"
)

func TestRebuildIndex(t *testing.T) {
	tempDir := t.TempDir()
	ownerID, _ := identity.Generate("founder")

	j := journal.New(filepath.Join(tempDir, "journal"))

	bodyCreated, _ := json.Marshal(map[string]string{
		"name":       "Test Room",
		"public_key": hex.EncodeToString(ownerID.PublicKey),
	})
	evCreated := &event.Event{
		V:       1,
		ID:      "ev_root",
		Room:    "rm_test",
		Author:  ownerID.MemberID,
		Seq:     1,
		Prev:    "sha256:0000000000000000000000000000000000000000000000000000000000000000",
		Lamport: 1,
		TS:      event.FormatTimestamp(time.Now()),
		Kind:    event.KindRoomCreated,
		Body:    json.RawMessage(bodyCreated),
	}
	if err := evCreated.Sign(ownerID); err != nil {
		t.Fatalf("sign room.created: %v", err)
	}
	if err := j.Append(evCreated); err != nil {
		t.Fatalf("append room.created: %v", err)
	}

	// Append note
	evNote := &event.Event{
		V:       1,
		ID:      "ev_n1",
		Room:    "rm_test",
		Author:  ownerID.MemberID,
		Seq:     2,
		Prev:    journal.ComputeLineHash([]byte("dummy")),
		Lamport: 2,
		TS:      event.FormatTimestamp(time.Now()),
		Kind:    event.KindNotePosted,
		Body:    json.RawMessage(`{"text":"hello SQLite"}`),
	}
	if err := evNote.Sign(ownerID); err != nil {
		t.Fatalf("sign note.posted: %v", err)
	}
	if err := j.Append(evNote); err != nil {
		t.Fatalf("append note.posted: %v", err)
	}

	dbPath := filepath.Join(tempDir, ".symroom", "index.sqlite")
	indexer := New(dbPath)

	if err := indexer.Rebuild(j); err != nil {
		t.Fatalf("Rebuild failed: %v", err)
	}

	// Verify database content
	db, err := sqlitekit.Open(dbPath)
	if err != nil {
		t.Fatalf("Open sqlite failed: %v", err)
	}
	defer func() { _ = db.Close() }()

	var eventCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM events").Scan(&eventCount); err != nil {
		t.Fatalf("query event count: %v", err)
	}
	if eventCount != 2 {
		t.Errorf("expected 2 events in SQLite index, got %d", eventCount)
	}

	var noteText string
	if err := db.QueryRow("SELECT text FROM notes WHERE event_id = 'ev_n1'").Scan(&noteText); err != nil {
		t.Fatalf("query note text: %v", err)
	}
	if noteText != "hello SQLite" {
		t.Errorf("expected 'hello SQLite', got %s", noteText)
	}

	// Delete index and rebuild again
	_ = db.Close()
	_ = os.Remove(dbPath)

	if err := indexer.Rebuild(j); err != nil {
		t.Fatalf("Rebuild after deletion failed: %v", err)
	}

	db2, err := sqlitekit.Open(dbPath)
	if err != nil {
		t.Fatalf("Open rebuilt sqlite failed: %v", err)
	}
	defer func() { _ = db2.Close() }()

	var rebuiltCount int
	_ = db2.QueryRow("SELECT COUNT(*) FROM events").Scan(&rebuiltCount)
	if rebuiltCount != 2 {
		t.Errorf("expected 2 events after rebuild, got %d", rebuiltCount)
	}
}
