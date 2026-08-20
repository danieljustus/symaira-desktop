package ingest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-ingest/internal/extract"
	"github.com/danieljustus/symaira-ingest/internal/store"
	"github.com/danieljustus/symaira-ingest/internal/writer"
)

type fakePipelineEngine struct {
	result *extract.Result
	err    error
}

func (f *fakePipelineEngine) Extract(ctx context.Context, source string, kind extract.Kind) (*extract.Result, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

func TestPipeline_Deduplicates(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "docs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	vault := filepath.Join(dir, "vault")
	archive := filepath.Join(dir, "archive")
	p := &Pipeline{
		Engine:     nil,
		Store:      s,
		Writer:     &writer.NoteWriter{Vault: vault},
		ArchiveDir: archive,
	}

	path := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := p.Ingest(context.Background(), path, nil)
	if err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	if res.ArchivePath == "" {
		t.Fatal("expected ArchivePath to be populated in result")
	}
	if _, err := os.Stat(res.ArchivePath); err != nil {
		t.Fatalf("expected archived file to exist: %v", err)
	}

	// Test re-ingesting the exact same file path
	_, err = p.Ingest(context.Background(), path, nil)
	if err == nil {
		t.Fatal("expected duplicate error, got nil")
	}
	var dupErr *DuplicateError
	if !errors.As(err, &dupErr) {
		t.Fatalf("expected DuplicateError, got %T: %v", err, err)
	}
	if dupErr.VaultPath != res.VaultPath {
		t.Errorf("dupErr.VaultPath = %q, want %q", dupErr.VaultPath, res.VaultPath)
	}
	if dupErr.ArchivePath != res.ArchivePath {
		t.Errorf("dupErr.ArchivePath = %q, want %q", dupErr.ArchivePath, res.ArchivePath)
	}

	// Test duplicate from a different source path
	otherPath := filepath.Join(dir, "other.txt")
	if err := os.WriteFile(otherPath, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = p.Ingest(context.Background(), otherPath, nil)
	if err == nil {
		t.Fatal("expected duplicate error for different path, got nil")
	}
	if !errors.As(err, &dupErr) {
		t.Fatalf("expected DuplicateError, got %T: %v", err, err)
	}
	if dupErr.VaultPath != res.VaultPath {
		t.Errorf("dupErr.VaultPath = %q, want %q", dupErr.VaultPath, res.VaultPath)
	}
	if dupErr.ArchivePath != res.ArchivePath {
		t.Errorf("dupErr.ArchivePath = %q, want %q", dupErr.ArchivePath, res.ArchivePath)
	}

	matches, err := filepath.Glob(vault + "/*.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected 1 note, got %d", len(matches))
	}
}

func TestPipeline_ExtractsWithEngine(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "docs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	vault := filepath.Join(dir, "vault")
	eng := extract.Engine(&fakePipelineEngine{result: &extract.Result{Text: "ocr text"}})
	p := &Pipeline{
		Engine:     eng,
		Store:      s,
		Writer:     &writer.NoteWriter{Vault: vault},
		ArchiveDir: filepath.Join(dir, "archive"),
	}

	path := filepath.Join(dir, "scan.png")
	if err := os.WriteFile(path, []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := p.Ingest(context.Background(), path, nil)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if res.Extract.Text != "ocr text" {
		t.Fatalf("text = %q, want %q", res.Extract.Text, "ocr text")
	}
}

func TestPipeline_ClassifiesWithRules(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "docs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ctx := context.Background()
	// Add some classification rules
	_, _ = s.AddRule(ctx, "acme", "category", "invoices")
	_, _ = s.AddRule(ctx, "tax", "tag", "financial")
	_, _ = s.AddRule(ctx, "2026", "tag", "year2026")
	_, _ = s.AddRule(ctx, "irs", "correspondent", "Internal Revenue Service")
	_, _ = s.AddRule(ctx, "form", "document_type", "Tax Form")

	vault := filepath.Join(dir, "vault")
	eng := extract.Engine(&fakePipelineEngine{result: &extract.Result{Text: "Acme Tax return form for 2026 to the IRS"}})
	p := &Pipeline{
		Engine:     eng,
		Store:      s,
		Writer:     &writer.NoteWriter{Vault: vault},
		ArchiveDir: filepath.Join(dir, "archive"),
	}

	path := filepath.Join(dir, "scan.png")
	if err := os.WriteFile(path, []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := p.Ingest(ctx, path, nil)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	if res.Category != "invoices" {
		t.Errorf("res.Category = %q, want invoices", res.Category)
	}
	if len(res.Tags) != 2 || res.Tags[0] != "financial" || res.Tags[1] != "year2026" {
		t.Errorf("res.Tags = %v, want [financial, year2026]", res.Tags)
	}
	if res.Correspondent != "Internal Revenue Service" {
		t.Errorf("res.Correspondent = %q, want Internal Revenue Service", res.Correspondent)
	}
	if res.DocumentType != "Tax Form" {
		t.Errorf("res.DocumentType = %q, want Tax Form", res.DocumentType)
	}

	// Verify database record has metadata as well
	hash, err := hashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := s.ByHash(ctx, hash)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Category != "invoices" {
		t.Errorf("db category = %q, want invoices", doc.Category)
	}
	if len(doc.Tags) != 2 || doc.Tags[0] != "financial" || doc.Tags[1] != "year2026" {
		t.Errorf("db tags = %v, want [financial, year2026]", doc.Tags)
	}
}

func TestAtomicCopy_SourceNotExist(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "nonexistent.txt")
	dst := filepath.Join(dir, "dst.txt")

	err := atomicCopy(src, dst)
	if err == nil {
		t.Fatal("expected error for nonexistent source, got nil")
	}
}

func TestAtomicCopy_SourcePermDenied(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping permission test when running as root")
	}

	dir := t.TempDir()
	src := filepath.Join(dir, "readonly.txt")
	if err := os.WriteFile(src, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(src, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(src, 0o644)

	dst := filepath.Join(dir, "dst.txt")
	err := atomicCopy(src, dst)
	if err == nil {
		t.Fatal("expected error for permission denied source, got nil")
	}
}

func TestAtomicCopy_DirCreateFail(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping permission test when running as root")
	}

	dir := t.TempDir()
	src := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(src, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}

	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(blocker, "subdir", "file.txt")

	err := atomicCopy(src, dst)
	if err == nil {
		t.Fatal("expected error when destination dir creation fails, got nil")
	}
}

func TestAtomicCopy_RenameFail(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(src, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}

	dstDir := filepath.Join(dir, "dst")
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dstDir, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dstDir, 0o755)

	dst := filepath.Join(dstDir, "file.txt")
	err := atomicCopy(src, dst)
	if err == nil {
		t.Fatal("expected error when rename fails, got nil")
	}
}

func TestAtomicCopy_AlreadyExists(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(src, []byte("new content"), 0o644); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(dir, "existing.txt")
	if err := os.WriteFile(dst, []byte("old content"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := atomicCopy(src, dst)
	if err != nil {
		t.Fatalf("expected nil when destination exists, got: %v", err)
	}

	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "old content" {
		t.Errorf("content = %q, want %q", string(data), "old content")
	}
}

func TestPipeline_EnqueueSkippedJobFailure(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "docs.db"))
	if err != nil {
		t.Fatal(err)
	}

	vault := filepath.Join(dir, "vault")
	archive := filepath.Join(dir, "archive")
	p := &Pipeline{
		Engine:     nil,
		Store:      s,
		Writer:     &writer.NoteWriter{Vault: vault},
		ArchiveDir: archive,
	}

	path := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = p.Ingest(context.Background(), path, nil)
	if err != nil {
		t.Fatalf("first ingest: %v", err)
	}

	s.Close()

	_, err = p.Ingest(context.Background(), path, nil)
	if err == nil {
		t.Fatal("expected error on second ingest with closed DB, got nil")
	}
}

func TestPipeline_PresetOverridesClassification(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "docs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ctx := context.Background()
	_, _ = s.AddRule(ctx, "acme", "category", "invoices")
	_, _ = s.AddRule(ctx, "tax", "tag", "financial")
	_, _ = s.AddRule(ctx, "irs", "correspondent", "Internal Revenue Service")
	_, _ = s.AddRule(ctx, "form", "document_type", "Tax Form")

	vault := filepath.Join(dir, "vault")
	eng := extract.Engine(&fakePipelineEngine{result: &extract.Result{Text: "Acme Tax return form for 2026 to the IRS"}})
	p := &Pipeline{
		Engine:     eng,
		Store:      s,
		Writer:     &writer.NoteWriter{Vault: vault},
		ArchiveDir: filepath.Join(dir, "archive"),
	}

	path := filepath.Join(dir, "scan.png")
	if err := os.WriteFile(path, []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, 0o644); err != nil {
		t.Fatal(err)
	}

	opts := &IngestOptions{
		PresetCategory:      "custom-category",
		PresetTags:          []string{"custom-tag-1", "custom-tag-2"},
		PresetCorrespondent: "Custom Corp",
		PresetDocumentType:  "Custom Doc Type",
	}

	res, err := p.Ingest(ctx, path, opts)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	if res.Category != "custom-category" {
		t.Errorf("res.Category = %q, want custom-category", res.Category)
	}
	if len(res.Tags) != 2 || res.Tags[0] != "custom-tag-1" || res.Tags[1] != "custom-tag-2" {
		t.Errorf("res.Tags = %v, want [custom-tag-1, custom-tag-2]", res.Tags)
	}
	if res.Correspondent != "Custom Corp" {
		t.Errorf("res.Correspondent = %q, want Custom Corp", res.Correspondent)
	}
	if res.DocumentType != "Custom Doc Type" {
		t.Errorf("res.DocumentType = %q, want Custom Doc Type", res.DocumentType)
	}
}

func TestPipeline_PresetOnlyOverridesSetFields(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "docs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ctx := context.Background()
	_, _ = s.AddRule(ctx, "acme", "category", "invoices")
	_, _ = s.AddRule(ctx, "tax", "tag", "financial")
	_, _ = s.AddRule(ctx, "irs", "correspondent", "Internal Revenue Service")
	_, _ = s.AddRule(ctx, "form", "document_type", "Tax Form")

	vault := filepath.Join(dir, "vault")
	eng := extract.Engine(&fakePipelineEngine{result: &extract.Result{Text: "Acme Tax return form for 2026 to the IRS"}})
	p := &Pipeline{
		Engine:     eng,
		Store:      s,
		Writer:     &writer.NoteWriter{Vault: vault},
		ArchiveDir: filepath.Join(dir, "archive"),
	}

	path := filepath.Join(dir, "scan.png")
	if err := os.WriteFile(path, []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, 0o644); err != nil {
		t.Fatal(err)
	}

	opts := &IngestOptions{
		PresetCategory: "override-category",
	}

	res, err := p.Ingest(ctx, path, opts)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	if res.Category != "override-category" {
		t.Errorf("res.Category = %q, want override-category", res.Category)
	}
	if len(res.Tags) != 1 || res.Tags[0] != "financial" {
		t.Errorf("res.Tags = %v, want [financial] (rule preserved)", res.Tags)
	}
	if res.Correspondent != "Internal Revenue Service" {
		t.Errorf("res.Correspondent = %q, want Internal Revenue Service (rule preserved)", res.Correspondent)
	}
	if res.DocumentType != "Tax Form" {
		t.Errorf("res.DocumentType = %q, want Tax Form (rule preserved)", res.DocumentType)
	}
}

func TestPipeline_NilOptsBackwardCompatible(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "docs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ctx := context.Background()
	_, _ = s.AddRule(ctx, "acme", "category", "invoices")

	vault := filepath.Join(dir, "vault")
	eng := extract.Engine(&fakePipelineEngine{result: &extract.Result{Text: "Acme document"}})
	p := &Pipeline{
		Engine:     eng,
		Store:      s,
		Writer:     &writer.NoteWriter{Vault: vault},
		ArchiveDir: filepath.Join(dir, "archive"),
	}

	path := filepath.Join(dir, "doc.txt")
	if err := os.WriteFile(path, []byte("Acme invoice document"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := p.Ingest(ctx, path, nil)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	if res.Category != "invoices" {
		t.Errorf("res.Category = %q, want invoices", res.Category)
	}
}

func TestPipeline_ArchiveDirectoryFailure(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "docs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	vault := filepath.Join(dir, "vault")
	eng := extract.Engine(&fakePipelineEngine{result: &extract.Result{Text: "test"}})

	archiveDir := filepath.Join(dir, "archive")
	if err := os.WriteFile(archiveDir, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := &Pipeline{
		Engine:     eng,
		Store:      s,
		Writer:     &writer.NoteWriter{Vault: vault},
		ArchiveDir: archiveDir,
	}

	path := filepath.Join(dir, "doc.txt")
	if err := os.WriteFile(path, []byte("test content"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = p.Ingest(context.Background(), path, nil)
	if err == nil {
		t.Fatal("expected error when archive directory is a file, got nil")
	}
	if !strings.Contains(err.Error(), "archive") {
		t.Errorf("expected archive-related error, got: %v", err)
	}
}

func TestPipeline_AtomicCopySourceReadError(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "docs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	vault := filepath.Join(dir, "vault")
	eng := extract.Engine(&fakePipelineEngine{result: &extract.Result{Text: "test"}})
	p := &Pipeline{
		Engine:     eng,
		Store:      s,
		Writer:     &writer.NoteWriter{Vault: vault},
		ArchiveDir: filepath.Join(dir, "archive"),
	}

	path := filepath.Join(dir, "doc.txt")
	if err := os.WriteFile(path, []byte("test content"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(path, 0o644)

	_, err = p.Ingest(context.Background(), path, nil)
	if err == nil {
		t.Fatal("expected error when source file is unreadable, got nil")
	}
}

func TestPipeline_ClassificationRuleLoadingError(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "docs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	vault := filepath.Join(dir, "vault")
	eng := extract.Engine(&fakePipelineEngine{result: &extract.Result{Text: "test"}})
	p := &Pipeline{
		Engine:     eng,
		Store:      s,
		Writer:     &writer.NoteWriter{Vault: vault},
		ArchiveDir: filepath.Join(dir, "archive"),
	}

	path := filepath.Join(dir, "doc.txt")
	if err := os.WriteFile(path, []byte("test content"), 0o644); err != nil {
		t.Fatal(err)
	}

	s.Close()

	s2, err := store.Open(filepath.Join(dir, "docs2.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	p.Store = s2

	res, err := p.Ingest(context.Background(), path, nil)
	if err != nil {
		t.Fatalf("Ingest should succeed even with classification error: %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil result")
	}
}
