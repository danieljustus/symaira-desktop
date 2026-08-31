package sidecar

import (
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/searchquery"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

func TestBackfillNormIndexRepairsRowsMissingFromNormalizedIndex(t *testing.T) {
	db := setupTestDB(t)
	doc := &vault.Document{
		Path:    "/tmp/backfill.md",
		Title:   "Rechnungen",
		Created: time.Now().UTC().Format(time.RFC3339),
		SHA256:  "backfill-hash",
		Body:    "Die Rechnungen liegen im Archiv.",
	}
	if err := db.IndexDocument(doc); err != nil {
		t.Fatalf("IndexDocument: %v", err)
	}

	var rowID int64
	if err := db.conn.QueryRow("SELECT id FROM files WHERE path = ?", doc.Path).Scan(&rowID); err != nil {
		t.Fatalf("lookup file row: %v", err)
	}
	if _, err := db.conn.Exec("DELETE FROM fts_norm WHERE rowid = ?", rowID); err != nil {
		t.Fatalf("remove normalized row: %v", err)
	}

	if err := backfillNormIndex(db.conn); err != nil {
		t.Fatalf("backfillNormIndex: %v", err)
	}

	var normalized string
	if err := db.conn.QueryRow("SELECT norm FROM fts_norm WHERE rowid = ?", rowID).Scan(&normalized); err != nil {
		t.Fatalf("lookup normalized row: %v", err)
	}
	want := searchquery.GermanNormText(doc.Title + " " + doc.Body)
	if normalized != want {
		t.Fatalf("normalized text = %q, want %q", normalized, want)
	}

	// A second pass is intentionally a no-op once the two indexes agree.
	if err := backfillNormIndex(db.conn); err != nil {
		t.Fatalf("second backfillNormIndex: %v", err)
	}
}

func TestBackfillNormIndexReportsClosedDatabase(t *testing.T) {
	db := setupTestDB(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := backfillNormIndex(db.conn); err == nil {
		t.Fatal("backfillNormIndex accepted a closed database")
	}
}

func TestBackfillNormIndexReportsInvalidFTSRow(t *testing.T) {
	db := setupTestDB(t)
	if _, err := db.conn.Exec("INSERT INTO fts_search(rowid, title, body) VALUES (?, ?, NULL)", 9999, "invalid"); err != nil {
		t.Fatalf("insert invalid FTS row: %v", err)
	}
	if err := backfillNormIndex(db.conn); err == nil {
		t.Fatal("backfillNormIndex accepted a NULL body")
	}
}
