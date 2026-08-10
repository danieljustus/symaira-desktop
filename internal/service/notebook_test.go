package service

import (
	"testing"
)

func TestNotebookNew_IsSearchableAndBacklinked(t *testing.T) {
	svc := newTestService(t)

	invoicePath, err := svc.NoteNew("Invoice 2026 03", "unique-invoice-body", "")
	if err != nil {
		t.Fatal(err)
	}

	nb, err := svc.NotebookNew("Research X", "notes on X")
	if err != nil {
		t.Fatalf("NotebookNew: %v", err)
	}

	// The notebook note itself must be searchable immediately (issue #424
	// acceptance: "sidecar indexing: notebooks are indexed as notes").
	results, err := svc.Search("Research X")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range results {
		if r.Path == nb.Path {
			found = true
		}
	}
	if !found {
		t.Fatalf("notebook note not searchable right after creation; results=%v", results)
	}

	if _, err := svc.NotebookAddSource(nb.ID, invoicePath); err != nil {
		t.Fatalf("NotebookAddSource: %v", err)
	}

	// Adding a source must produce a backlink edge (VAULT.md section 10:
	// "sources are recorded as link edges").
	backlinks, err := svc.Backlinks(invoicePath)
	if err != nil {
		t.Fatal(err)
	}
	linked := false
	for _, b := range backlinks {
		if b == nb.Path {
			linked = true
		}
	}
	if !linked {
		t.Fatalf("expected %s in backlinks of %s, got %v", nb.Path, invoicePath, backlinks)
	}
}

func TestNotebookAddSource_RejectsTraversal(t *testing.T) {
	svc := newTestService(t)
	nb, err := svc.NotebookNew("Scoped", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.NotebookAddSource(nb.ID, "../outside.md"); err == nil {
		t.Fatal("expected error adding a traversal path as a source")
	}
}

func TestNotebookRemoveSource_KeepsReferencedNote(t *testing.T) {
	svc := newTestService(t)
	docPath, err := svc.NoteNew("Doc To Keep", "body", "")
	if err != nil {
		t.Fatal(err)
	}
	nb, err := svc.NotebookNew("Keeper Test", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.NotebookAddSource(nb.ID, docPath); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.NotebookRemoveSource(nb.ID, docPath); err != nil {
		t.Fatalf("NotebookRemoveSource: %v", err)
	}

	results, err := svc.Search("body")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("referenced note must still exist and be searchable after being removed from a notebook")
	}
}

func TestNotebookDelete_MovesToTrash(t *testing.T) {
	svc := newTestService(t)
	nb, err := svc.NotebookNew("Disposable", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.NotebookDelete(nb.ID); err != nil {
		t.Fatalf("NotebookDelete: %v", err)
	}

	trashed, err := svc.TrashList()
	if err != nil {
		t.Fatal(err)
	}
	if len(trashed) != 1 {
		t.Fatalf("expected 1 trashed entry, got %d", len(trashed))
	}

	if _, err := svc.NotebookGet(nb.ID); err == nil {
		t.Fatal("expected NotebookGet to fail after delete")
	}
}

func TestNotebookList_ReturnsCreatedNotebooks(t *testing.T) {
	svc := newTestService(t)
	if _, err := svc.NotebookNew("Bravo", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.NotebookNew("Alpha", ""); err != nil {
		t.Fatal(err)
	}

	list, err := svc.NotebookList()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("len(list) = %d, want 2", len(list))
	}
	if list[0].Title != "Alpha" {
		t.Errorf("list[0].Title = %q, want Alpha (sorted)", list[0].Title)
	}
}
