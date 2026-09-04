package ingest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestVersion(t *testing.T) {
	v := Version()
	if v == "" {
		t.Error("expected non-empty version")
	}
}

func TestOptionsResolve(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	opts := Options{
		Vault:         "/custom/vault",
		Archive:       "/custom/archive",
		DBPath:        "/custom/db.sqlite",
		OCRLang:       "deu",
		OllamaBaseURL: "http://localhost:11434",
		OllamaModel:   "qwen3-vl",
	}

	r, err := opts.resolve()
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if r.vault != "/custom/vault" || r.archive != "/custom/archive" || r.dbPath != "/custom/db.sqlite" {
		t.Errorf("unexpected resolved paths: %+v", r)
	}
	if r.ocrLang != "deu" || r.ollamaBaseURL != "http://localhost:11434" || r.ollamaModel != "qwen3-vl" {
		t.Errorf("unexpected resolved ocr options: %+v", r)
	}

	// Test DisableVLM clears Ollama options
	opts.DisableVLM = true
	rDisabled, err := opts.resolve()
	if err != nil {
		t.Fatalf("resolve with DisableVLM failed: %v", err)
	}
	if rDisabled.ollamaBaseURL != "" || rDisabled.ollamaModel != "" {
		t.Errorf("expected empty ollama options when DisableVLM is true: %+v", rDisabled)
	}
}

func TestArchivePath(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	path, err := ArchivePath()
	if err != nil {
		t.Fatalf("ArchivePath failed: %v", err)
	}
	expected := filepath.Join(dataHome, "symdesk", "archive")
	if path != expected {
		t.Errorf("ArchivePath = %q, want %q", path, expected)
	}

	// Legacy fallback: pre-existing legacy symingest archive is preserved as fallback.
	legacyDir := filepath.Join(dataHome, "symingest", "archive")
	if err := os.MkdirAll(legacyDir, 0700); err != nil {
		t.Fatal(err)
	}
	// With no symdesk/archive present, legacy should be used.
	legacyPath, err := ArchivePath()
	if err != nil {
		t.Fatalf("ArchivePath failed: %v", err)
	}
	if legacyPath != legacyDir {
		t.Errorf("ArchivePath = %q, want legacy fallback %q", legacyPath, legacyDir)
	}
}

// TestOptionsResolveVaultRelativeArchive verifies #660: when the caller sets
// only Vault and no explicit Archive, the default archive lives under
// `<vault>/archive/ingest` so a copied or synced vault stays self-contained
// (the same shape the Paperless importer already uses for `<vault>/archive/paperless`).
func TestOptionsResolveVaultRelativeArchive(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	vault := t.TempDir()
	opts := Options{Vault: vault}
	r, err := opts.resolve()
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	want := filepath.Join(vault, "archive", "ingest")
	if r.archive != want {
		t.Errorf("r.archive = %q, want %q (vault-relative default)", r.archive, want)
	}
	// The DB stays under XDG_DATA_HOME (not vault-relative) — that is a
	// shared, derived index, not a per-vault artefact.
	if r.dbPath == "" {
		t.Error("r.dbPath is empty; expected XDG default")
	}
}

// TestOptionsResolveExplicitArchiveWins verifies that a caller who explicitly
// sets Options.Archive (for example a shared archive outside the vault) is
// respected and not silently overridden by the vault-relative default.
func TestOptionsResolveExplicitArchiveWins(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	opts := Options{Vault: t.TempDir(), Archive: "/shared/archive"}
	r, err := opts.resolve()
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if r.archive != "/shared/archive" {
		t.Errorf("r.archive = %q, want explicit %q", r.archive, "/shared/archive")
	}
}

// TestOptionsResolveNoVaultFallsBackToXDG verifies the legacy behaviour: when
// the caller does not pass a Vault, the archive still falls back to the
// XDG_DATA_HOME default so an embedded consumer without a vault keeps
// working.
func TestOptionsResolveNoVaultFallsBackToXDG(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	opts := Options{}
	r, err := opts.resolve()
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	want := filepath.Join(dataHome, "symdesk", "archive")
	if r.archive != want {
		t.Errorf("r.archive = %q, want XDG default %q", r.archive, want)
	}

	// When legacy directory exists, it falls back to symingest/archive.
	legacyArchive := filepath.Join(dataHome, "symingest", "archive")
	if err := os.MkdirAll(legacyArchive, 0700); err != nil {
		t.Fatal(err)
	}
	rLegacy, err := opts.resolve()
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if rLegacy.archive != legacyArchive {
		t.Errorf("rLegacy.archive = %q, want legacy fallback %q", rLegacy.archive, legacyArchive)
	}
}

func TestIngestDirect(t *testing.T) {
	tempDir := t.TempDir()
	vaultDir := filepath.Join(tempDir, "vault")
	archiveDir := filepath.Join(tempDir, "archive")
	dbPath := filepath.Join(tempDir, "docs.db")

	if err := os.MkdirAll(vaultDir, 0750); err != nil {
		t.Fatal(err)
	}

	srcFile := filepath.Join(tempDir, "document.txt")
	if err := os.WriteFile(srcFile, []byte("Hello world, this is a plain text file."), 0600); err != nil {
		t.Fatal(err)
	}

	opts := Options{
		Vault:   vaultDir,
		Archive: archiveDir,
		DBPath:  dbPath,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res, err := Ingest(ctx, srcFile, opts)
	if err != nil {
		t.Fatalf("Ingest failed: %v", err)
	}
	if res.VaultPath == "" || res.SHA256 == "" {
		t.Errorf("unexpected Ingest result: %+v", res)
	}

	// Ingesting the same file again must return ErrDuplicate
	_, err = Ingest(ctx, srcFile, opts)
	if !errors.Is(err, ErrDuplicate) {
		t.Errorf("expected ErrDuplicate on duplicate ingest, got %v", err)
	}
}

func TestExtractTextDirect(t *testing.T) {
	tempDir := t.TempDir()
	srcFile := filepath.Join(tempDir, "sample.txt")
	content := "Simple text extraction test content."
	if err := os.WriteFile(srcFile, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ext, err := ExtractText(ctx, srcFile, Options{})
	if err != nil {
		t.Fatalf("ExtractText failed: %v", err)
	}
	if ext.Text != content {
		t.Errorf("ExtractText = %q, want %q", ext.Text, content)
	}
}

func TestJobsAndRetryDirect(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "jobs.db")
	opts := Options{DBPath: dbPath}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	jobs, err := Jobs(ctx, opts, 10)
	if err != nil {
		t.Fatalf("Jobs failed: %v", err)
	}
	if len(jobs) != 0 {
		t.Errorf("expected empty job queue, got %d jobs", len(jobs))
	}

	// Retry on non-existent job should return error
	if err := RetryJob(ctx, opts, 9999); err == nil {
		t.Error("expected error retrying nonexistent job")
	}
}

func TestMailAccountsAndFetchMailDirect(t *testing.T) {
	tempDir := t.TempDir()
	nonExistentConfig := filepath.Join(tempDir, "missing.toml")

	accounts, err := MailAccounts(nonExistentConfig)
	if err != nil {
		t.Fatalf("MailAccounts on missing config returned error: %v", err)
	}
	if len(accounts) != 0 {
		t.Errorf("expected 0 accounts for missing config, got %d", len(accounts))
	}

	stagingDir := filepath.Join(tempDir, "mail_stage")
	res, err := FetchMail(context.Background(), MailFetchOptions{
		ConfigPath: nonExistentConfig,
		StagingDir: stagingDir,
	})
	if err != nil {
		t.Fatalf("FetchMail failed on empty accounts: %v", err)
	}
	if len(res.Attachments) != 0 {
		t.Errorf("expected 0 attachments, got %d", len(res.Attachments))
	}
}
