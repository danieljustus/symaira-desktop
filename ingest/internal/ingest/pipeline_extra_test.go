package ingest

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-ingest/internal/extract"
	"github.com/danieljustus/symaira-ingest/internal/store"
	"github.com/danieljustus/symaira-ingest/internal/writer"
)

func TestCloneIngestOptions_CopiesSlice(t *testing.T) {
	orig := &IngestOptions{
		PresetCategory:      "cat",
		PresetTags:          []string{"a", "b"},
		PresetCorrespondent: "corr",
		SourcePathOverride:  "/orig",
	}
	clone := cloneIngestOptions(orig)

	if clone.PresetCategory != orig.PresetCategory {
		t.Errorf("PresetCategory = %q", clone.PresetCategory)
	}
	if clone.SourcePathOverride != orig.SourcePathOverride {
		t.Errorf("SourcePathOverride = %q", clone.SourcePathOverride)
	}
	if len(clone.PresetTags) != len(orig.PresetTags) {
		t.Errorf("PresetTags len = %d, want %d", len(clone.PresetTags), len(orig.PresetTags))
	}
	clone.PresetTags[0] = "modified"
	if orig.PresetTags[0] == "modified" {
		t.Error("modifying clone affected original")
	}
}

func TestCloneIngestOptions_NilTags(t *testing.T) {
	orig := &IngestOptions{PresetCategory: "cat"}
	clone := cloneIngestOptions(orig)
	if clone.PresetTags != nil {
		t.Errorf("PresetTags = %v, want nil", clone.PresetTags)
	}
}

func TestReprocess_MissingArchivePath(t *testing.T) {
	dir := t.TempDir()
	s, _ := store.Open(filepath.Join(dir, "db.db"))
	defer s.Close()
	ctx := context.Background()

	doc, _, _ := s.CreateOrGet(ctx, "/tmp/doc.txt", "hash1", "text/plain")
	s.SetVaultAndArchivePath(ctx, doc.ID, "/vault/doc.txt.md", "", "", nil, "", "")

	vault := filepath.Join(dir, "vault")
	p := &Pipeline{
		Store:  s,
		Writer: &writer.NoteWriter{Vault: vault},
	}

	_, err := p.Reprocess(ctx, doc.ID, "", nil)
	if err == nil {
		t.Fatal("expected error for missing archive path")
	}
}

func TestReprocess_MissingVaultPath(t *testing.T) {
	dir := t.TempDir()
	s, _ := store.Open(filepath.Join(dir, "db.db"))
	defer s.Close()
	ctx := context.Background()

	doc, _, _ := s.CreateOrGet(ctx, "/tmp/doc.txt", "hash2", "text/plain")
	s.SetVaultAndArchivePath(ctx, doc.ID, "", "/archive/doc.txt", "", nil, "", "")

	p := &Pipeline{
		Store:  s,
		Writer: &writer.NoteWriter{Vault: filepath.Join(dir, "vault")},
	}

	_, err := p.Reprocess(ctx, doc.ID, "", nil)
	if err == nil {
		t.Fatal("expected error for missing vault path")
	}
}

func TestReprocess_NonexistentDocument(t *testing.T) {
	dir := t.TempDir()
	s, _ := store.Open(filepath.Join(dir, "db.db"))
	defer s.Close()

	p := &Pipeline{
		Store:  s,
		Writer: &writer.NoteWriter{Vault: filepath.Join(dir, "vault")},
	}

	_, err := p.Reprocess(context.Background(), 9999, "", nil)
	if err == nil {
		t.Fatal("expected error for nonexistent document")
	}
}

func TestReprocess_SourceIsDirectory(t *testing.T) {
	dir := t.TempDir()
	s, _ := store.Open(filepath.Join(dir, "db.db"))
	defer s.Close()
	ctx := context.Background()

	archiveDir := filepath.Join(dir, "archive")
	os.MkdirAll(archiveDir, 0o700)
	srcPath := filepath.Join(archiveDir, "doc.pdf")
	os.WriteFile(srcPath, []byte("%PDF-1.4\n"), 0o600)

	doc, _, _ := s.CreateOrGet(ctx, "/tmp/doc.pdf", "hash-dir", "application/pdf")
	s.SetVaultAndArchivePath(ctx, doc.ID, "/vault/doc.pdf.md", archiveDir, "", nil, "", "")

	p := &Pipeline{
		Store:  s,
		Writer: &writer.NoteWriter{Vault: filepath.Join(dir, "vault")},
	}

	_, err := p.Reprocess(ctx, doc.ID, archiveDir, nil)
	if err == nil {
		t.Fatal("expected error when source is a directory")
	}
}

func TestReprocess_HashMismatch(t *testing.T) {
	dir := t.TempDir()
	s, _ := store.Open(filepath.Join(dir, "db.db"))
	defer s.Close()
	ctx := context.Background()

	archiveDir := filepath.Join(dir, "archive")
	os.MkdirAll(archiveDir, 0o700)
	srcPath := filepath.Join(archiveDir, "doc.pdf")
	os.WriteFile(srcPath, []byte("%PDF-1.4\ncontent\n"), 0o600)

	doc, _, _ := s.CreateOrGet(ctx, "/tmp/doc.pdf", "wrong-hash", "application/pdf")
	s.SetVaultAndArchivePath(ctx, doc.ID, "/vault/doc.pdf.md", srcPath, "", nil, "", "")

	p := &Pipeline{
		Store:  s,
		Writer: &writer.NoteWriter{Vault: filepath.Join(dir, "vault")},
	}

	_, err := p.Reprocess(ctx, doc.ID, "", nil)
	if err == nil {
		t.Fatal("expected error for hash mismatch")
	}
}

func TestReprocess_AlreadyRunning(t *testing.T) {
	dir := t.TempDir()
	s, _ := store.Open(filepath.Join(dir, "db.db"))
	defer s.Close()
	ctx := context.Background()

	archiveDir := filepath.Join(dir, "archive")
	os.MkdirAll(archiveDir, 0o700)
	srcPath := filepath.Join(archiveDir, "doc.txt")
	content := []byte("reprocess idempotency test content for hash match")
	os.WriteFile(srcPath, content, 0o600)

	hash, _ := hashFile(srcPath)
	doc, _, _ := s.CreateOrGet(ctx, "/tmp/doc.txt", hash, "text/plain")

	vaultDir := filepath.Join(dir, "vault")
	os.MkdirAll(vaultDir, 0o700)
	notePath := filepath.Join(vaultDir, "doc.txt.md")
	os.WriteFile(notePath, []byte("---\nsource_path: /tmp/doc.txt\nsha256: "+hash+"\nmime: text/plain\ntags: []\ncategory: \"\"\narchive_path: "+srcPath+"\n---\n\nhello\n"), 0o600)

	s.SetVaultAndArchivePath(ctx, doc.ID, notePath, srcPath, "", nil, "", "")

	p := &Pipeline{
		Store:  s,
		Writer: &writer.NoteWriter{Vault: vaultDir},
	}

	job, created, _ := s.EnqueueReprocessJob(ctx, doc.ID)
	if !created {
		t.Fatal("expected first EnqueueReprocessJob to create")
	}
	if _, err := s.ClaimJobByID(ctx, job.ID); err != nil {
		t.Fatal(err)
	}

	result2, err := p.Reprocess(ctx, doc.ID, "", nil)
	if err != nil {
		t.Fatalf("second reprocess: %v", err)
	}
	if !result2.AlreadyRunning {
		t.Error("expected AlreadyRunning=true when job is already claimed")
	}
}

func TestReprocess_SuccessfulReprocess(t *testing.T) {
	dir := t.TempDir()
	s, _ := store.Open(filepath.Join(dir, "db.db"))
	defer s.Close()
	ctx := context.Background()

	archiveDir := filepath.Join(dir, "archive")
	os.MkdirAll(archiveDir, 0o700)
	srcPath := filepath.Join(archiveDir, "doc.txt")
	content := []byte("successful reprocess test content for hash match")
	os.WriteFile(srcPath, content, 0o600)

	hash, _ := hashFile(srcPath)
	doc, _, _ := s.CreateOrGet(ctx, "/tmp/doc.txt", hash, "text/plain")

	vaultDir := filepath.Join(dir, "vault")
	os.MkdirAll(vaultDir, 0o700)
	notePath := filepath.Join(vaultDir, "doc.txt.md")
	os.WriteFile(notePath, []byte("---\nsource_path: /tmp/doc.txt\nsha256: "+hash+"\nmime: text/plain\ntags: []\ncategory: \"\"\narchive_path: "+srcPath+"\n---\n\nhello\n"), 0o600)

	s.SetVaultAndArchivePath(ctx, doc.ID, notePath, srcPath, "", nil, "", "")

	p := &Pipeline{
		Store:  s,
		Writer: &writer.NoteWriter{Vault: vaultDir},
	}

	result, err := p.Reprocess(ctx, doc.ID, "", nil)
	if err != nil {
		t.Fatalf("Reprocess: %v", err)
	}
	if result.Result == nil {
		t.Fatal("expected non-nil Result")
	}
	if result.Job == nil {
		t.Fatal("expected non-nil Job")
	}
	if result.AlreadyRunning {
		t.Error("should not be AlreadyRunning on first call")
	}
}

func TestProcessJob_ReprocessJobKind(t *testing.T) {
	dir := t.TempDir()
	s, _ := store.Open(filepath.Join(dir, "db.db"))
	defer s.Close()
	ctx := context.Background()

	archiveDir := filepath.Join(dir, "archive")
	os.MkdirAll(archiveDir, 0o700)
	srcPath := filepath.Join(archiveDir, "doc.txt")
	content := []byte("processJob reprocess test content for hash match")
	os.WriteFile(srcPath, content, 0o600)

	hash, _ := hashFile(srcPath)
	doc, _, _ := s.CreateOrGet(ctx, "/tmp/doc.txt", hash, "text/plain")

	vaultDir := filepath.Join(dir, "vault")
	os.MkdirAll(vaultDir, 0o700)
	notePath := filepath.Join(vaultDir, "doc.txt.md")
	os.WriteFile(notePath, []byte("---\nsource_path: /tmp/doc.txt\nsha256: "+hash+"\nmime: text/plain\ntags: []\ncategory: \"\"\narchive_path: "+srcPath+"\n---\n\nhello\n"), 0o600)

	s.SetVaultAndArchivePath(ctx, doc.ID, notePath, srcPath, "", nil, "", "")

	p := &Pipeline{
		Store:  s,
		Writer: &writer.NoteWriter{Vault: vaultDir},
	}

	job, _ := s.EnqueueJob(ctx, doc.ID, ReprocessJobKind)
	claimed, _ := s.ClaimJobByID(ctx, job.ID)

	res, err := p.processJob(ctx, claimed, &IngestOptions{
		SourcePathOverride:  doc.SourcePath,
		ExistingVaultPath:   notePath,
		ArchivePathOverride: srcPath,
	})
	if err != nil {
		t.Fatalf("processJob: %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil result")
	}
}

func TestProcessJob_ReprocessJobKind_MissingArchive(t *testing.T) {
	dir := t.TempDir()
	s, _ := store.Open(filepath.Join(dir, "db.db"))
	defer s.Close()
	ctx := context.Background()

	doc, _, _ := s.CreateOrGet(ctx, "/tmp/doc.txt", "hash-pj2", "text/plain")
	s.SetVaultAndArchivePath(ctx, doc.ID, "/vault/doc.txt.md", "", "", nil, "", "")

	p := &Pipeline{Store: s, Writer: &writer.NoteWriter{Vault: filepath.Join(dir, "vault")}}

	job, _ := s.EnqueueJob(ctx, doc.ID, ReprocessJobKind)
	claimed, _ := s.ClaimJobByID(ctx, job.ID)

	_, err := p.processJob(ctx, claimed, nil)
	if err == nil {
		t.Fatal("expected error for missing archive path in reprocess job")
	}
}

func TestExtractText_UnsupportedKind_NilEngine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "scan.png")
	os.WriteFile(path, []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, 0o644)

	_, err := extractText(context.Background(), path, extract.KindPNG, nil)
	if err == nil {
		t.Fatal("expected error for unsupported kind with nil engine")
	}
}

func TestExtractText_UnsupportedKind_WithEngine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "scan.png")
	os.WriteFile(path, []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, 0o644)

	res, err := extractText(context.Background(), path, extract.KindPNG, &fakeEngine{result: &extract.Result{Text: "ocr"}})
	if err != nil {
		t.Fatalf("extractText: %v", err)
	}
	if res.Text != "ocr" {
		t.Errorf("text = %q, want ocr", res.Text)
	}
}
