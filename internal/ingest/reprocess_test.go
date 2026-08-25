package ingest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/ingest/internal/store"
)

// setupReprocessFixture ingests one document and returns the options that
// address its store plus the ingest Result, so tests can look the document
// back up by archive path or, once discovered, by ID.
func setupReprocessFixture(t *testing.T) (Options, *Result) {
	t.Helper()
	tempDir := t.TempDir()
	vaultDir := filepath.Join(tempDir, "vault")
	if err := os.MkdirAll(vaultDir, 0750); err != nil {
		t.Fatal(err)
	}

	srcFile := filepath.Join(tempDir, "document.txt")
	if err := os.WriteFile(srcFile, []byte("Invoice from Acme Corp for services rendered."), 0600); err != nil {
		t.Fatal(err)
	}

	opts := Options{Vault: vaultDir, Archive: filepath.Join(tempDir, "archive"), DBPath: filepath.Join(tempDir, "docs.db")}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res, err := Ingest(ctx, srcFile, opts)
	if err != nil {
		t.Fatalf("Ingest failed: %v", err)
	}
	return opts, res
}

func TestReprocessByArchivePathAndByID(t *testing.T) {
	opts, res := setupReprocessFixture(t)
	ctx := context.Background()

	out, err := ReprocessByArchivePath(ctx, opts, res.ArchivePath)
	if err != nil {
		t.Fatalf("ReprocessByArchivePath failed: %v", err)
	}
	if out.Status != "completed" {
		t.Errorf("Status = %q, want completed", out.Status)
	}
	if out.DocumentID == 0 {
		t.Error("expected non-zero DocumentID")
	}
	if out.OutputPath == "" {
		t.Error("expected non-empty OutputPath")
	}

	// Reprocess by ID with the document ID ReprocessByArchivePath discovered.
	out2, err := Reprocess(ctx, opts, out.DocumentID)
	if err != nil {
		t.Fatalf("Reprocess by ID failed: %v", err)
	}
	if out2.Status != "completed" || out2.DocumentID != out.DocumentID {
		t.Errorf("unexpected second reprocess result: %+v", out2)
	}
}

func TestReprocessAlreadyRunning(t *testing.T) {
	opts, res := setupReprocessFixture(t)
	ctx := context.Background()

	r, err := opts.resolve()
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(r.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := st.ByArchivePath(ctx, res.ArchivePath)
	if err != nil {
		t.Fatal(err)
	}
	// Pre-create a pending reprocess job, the same state a concurrent reocr
	// request would leave, without letting it complete.
	if _, _, err := st.EnqueueReprocessJob(ctx, doc.ID); err != nil {
		t.Fatalf("EnqueueReprocessJob failed: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	out, err := Reprocess(ctx, opts, doc.ID)
	if err != nil {
		t.Fatalf("Reprocess failed: %v", err)
	}
	if out.Status != "already_running" {
		t.Errorf("Status = %q, want already_running", out.Status)
	}
	if out.JobID == 0 {
		t.Error("expected the pre-existing job's ID to be reported")
	}
}

func TestReprocessDocumentNotFound(t *testing.T) {
	tempDir := t.TempDir()
	opts := Options{DBPath: filepath.Join(tempDir, "docs.db")}
	ctx := context.Background()

	if _, err := Reprocess(ctx, opts, 9999); !errors.Is(err, ErrDocumentNotFound) {
		t.Errorf("expected ErrDocumentNotFound, got %v", err)
	}
	if _, err := ReprocessByArchivePath(ctx, opts, filepath.Join(tempDir, "nope.pdf")); !errors.Is(err, ErrDocumentNotFound) {
		t.Errorf("expected ErrDocumentNotFound for an unknown archive path, got %v", err)
	}
}

func TestReprocessNoArchivedOriginal(t *testing.T) {
	opts, res := setupReprocessFixture(t)
	ctx := context.Background()

	r, err := opts.resolve()
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(r.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := st.ByArchivePath(ctx, res.ArchivePath)
	if err != nil {
		t.Fatal(err)
	}
	docID := doc.ID
	// Blank the recorded archive path — the exact condition
	// pipeline.Reprocess reports as ErrNoArchivedOriginal.
	if err := st.SetVaultAndArchivePath(ctx, docID, res.VaultPath, "", res.Category, res.Tags, res.Correspondent, res.DocumentType); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := Reprocess(ctx, opts, docID); !errors.Is(err, ErrNoArchivedOriginal) {
		t.Errorf("expected ErrNoArchivedOriginal, got %v", err)
	}
}
