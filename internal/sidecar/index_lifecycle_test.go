package sidecar

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/searchquery"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

func TestIndexLifecycleStatusAndSearchFilter(t *testing.T) {
	db := setupTestDB(t)
	doc := &vault.Document{Path: "/vault/failed.md", Title: "Failed note", SHA256: "hash", Body: "searchable body"}
	if err := db.IndexDocument(doc); err != nil {
		t.Fatalf("IndexDocument: %v", err)
	}
	if err := db.SetIndexStatus(doc.Path, IndexStateFailed, "embedding backend unavailable"); err != nil {
		t.Fatalf("SetIndexStatus: %v", err)
	}

	status, ok, err := db.GetIndexStatus(doc.Path)
	if err != nil || !ok {
		t.Fatalf("GetIndexStatus = %#v, %v, want a row", status, err)
	}
	if status.State != IndexStateFailed || status.Reason != "embedding backend unavailable" {
		t.Fatalf("status = %#v, want failed with reason", status)
	}

	plan, err := searchquery.Parse("index:failed searchable")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	matches, err := db.SearchPlan(plan)
	if err != nil {
		t.Fatalf("SearchPlan: %v", err)
	}
	if len(matches) != 1 || matches[0].Path != doc.Path || matches[0].IndexState != string(IndexStateFailed) {
		t.Fatalf("matches = %#v, want failed document", matches)
	}

	rows, err := db.ListIndexStatuses(IndexStateFailed)
	if err != nil {
		t.Fatalf("ListIndexStatuses: %v", err)
	}
	if len(rows) != 1 || rows[0].Path != doc.Path {
		t.Fatalf("rows = %#v, want one failed document", rows)
	}
}

func TestIndexLifecycleRejectsUnknownState(t *testing.T) {
	db := setupTestDB(t)
	if err := db.SetIndexStatus("/vault/note.md", IndexState("bogus"), ""); err == nil {
		t.Fatal("SetIndexStatus accepted an unknown state")
	}
	if _, err := db.ListIndexStatuses(IndexState("bogus")); err == nil {
		t.Fatal("ListIndexStatuses accepted an unknown state")
	}
}

func TestPruneIndexStatusesOnlyRemovesMissingDerivedRows(t *testing.T) {
	db := setupTestDB(t)
	vaultRoot := t.TempDir()
	kept := filepath.Join(vaultRoot, "kept.md")
	if err := os.WriteFile(kept, []byte("# kept"), 0o644); err != nil { //nolint:gosec // G306: test fixture, permissions are irrelevant here
		t.Fatal(err)
	}
	if err := db.SetIndexStatus(kept, IndexStateIndexed, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.SetIndexStatus(filepath.Join(vaultRoot, "missing.md"), IndexStateFailed, "parse failed"); err != nil {
		t.Fatal(err)
	}
	removed, err := db.PruneIndexStatuses(vaultRoot)
	if err != nil {
		t.Fatalf("PruneIndexStatuses: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if _, ok, err := db.GetIndexStatus(kept); err != nil || !ok {
		t.Fatalf("kept status missing: %v %v", ok, err)
	}
	if _, ok, err := db.GetIndexStatus(filepath.Join(vaultRoot, "missing.md")); err != nil || ok {
		t.Fatalf("missing status remains: %v %v", ok, err)
	}
	if _, err := os.Stat(kept); err != nil {
		t.Fatalf("prune touched Markdown source: %v", err)
	}
}
