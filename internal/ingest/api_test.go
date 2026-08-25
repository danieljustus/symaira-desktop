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
	expected := filepath.Join(dataHome, "symingest", "archive")
	if path != expected {
		t.Errorf("ArchivePath = %q, want %q", path, expected)
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
