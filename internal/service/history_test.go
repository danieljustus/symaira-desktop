package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOverwriteLeavesRestorableVersion(t *testing.T) {
	svc := newTestService(t)

	path, err := svc.NoteNew("Safety Net", "original body", "")
	if err != nil {
		t.Fatal(err)
	}

	// Overwrite the note through the same service write path (as MCP does).
	if _, err := svc.NoteNew("Safety Net", "clobbered body", ""); err != nil {
		t.Fatal(err)
	}

	entries, err := svc.HistoryList(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 snapshot of the pre-overwrite content, got %d", len(entries))
	}

	if _, err := svc.HistoryRestore(path, ""); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(svc.VaultRoot, path))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "original body") {
		t.Fatalf("restored note lost original body:\n%s", data)
	}

	// Sidecar must reflect the restored content.
	results, err := svc.Search("original")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("restored content not searchable; sidecar out of sync")
	}
}

func TestPropsEditIsSnapshotted(t *testing.T) {
	svc := newTestService(t)
	path, err := svc.NoteNew("Props", "body", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.PropsEdit(path, "status", "open"); err != nil {
		t.Fatal(err)
	}
	entries, err := svc.HistoryList(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 snapshot before props edit, got %d", len(entries))
	}
}

func TestNoteDeleteAndTrashRestoreKeepSidecarConsistent(t *testing.T) {
	svc := newTestService(t)
	path, err := svc.NoteNew("Deleted Note", "unique-trash-body", "")
	if err != nil {
		t.Fatal(err)
	}

	entry, err := svc.NoteDelete(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(svc.VaultRoot, path)); !os.IsNotExist(err) {
		t.Fatal("note should be gone from the vault")
	}
	results, err := svc.Search("unique-trash-body")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatal("deleted note must not remain in the sidecar index")
	}

	if _, err := svc.TrashRestore(entry.Name); err != nil {
		t.Fatal(err)
	}
	results, err = svc.Search("unique-trash-body")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("restored note must be indexed again, got %d results", len(results))
	}
}
