package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaults(t *testing.T) {
	cfg := Defaults()
	if cfg == nil {
		t.Fatal("Defaults() returned nil")
	}
	if cfg.OCRLang != "eng" {
		t.Errorf("OCRLang = %q, want %q", cfg.OCRLang, "eng")
	}
	if cfg.Vault != "" {
		t.Errorf("Vault = %q, want empty", cfg.Vault)
	}
	if cfg.DBPath != "" {
		t.Errorf("DBPath = %q, want empty", cfg.DBPath)
	}
	if cfg.ArchivePath != "" {
		t.Errorf("ArchivePath = %q, want empty", cfg.ArchivePath)
	}
	if cfg.Inbox != "" {
		t.Errorf("Inbox = %q, want empty", cfg.Inbox)
	}
	if cfg.PaperlessBaseURL != "" {
		t.Errorf("PaperlessBaseURL = %q, want empty", cfg.PaperlessBaseURL)
	}
	if cfg.SymseekEnabled {
		t.Error("SymseekEnabled = true, want false")
	}
	if cfg.SymseekBinary != "" {
		t.Errorf("SymseekBinary = %q, want empty", cfg.SymseekBinary)
	}
}

func TestLoad_EnvOverrides(t *testing.T) {
	Loader.ResetCache()
	defer Loader.ResetCache()

	t.Setenv("SYMINGEST_VAULT", "/env/vault")
	t.Setenv("SYMINGEST_OCR_LANG", "deu")
	t.Setenv("SYMINGEST_DB_PATH", "/env/test.db")
	t.Setenv("SYMINGEST_ARCHIVE_PATH", "/env/archive")
	t.Setenv("SYMINGEST_INBOX", "/env/inbox")
	t.Setenv("SYMINGEST_PAPERLESS_BASE_URL", "https://paperless.example")
	t.Setenv("SYMINGEST_SYMSEEK_ENABLED", "true")
	t.Setenv("SYMINGEST_SYMSEEK_BINARY", "/usr/local/bin/symseek")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.Vault != "/env/vault" {
		t.Errorf("Vault = %q, want /env/vault", cfg.Vault)
	}
	if cfg.OCRLang != "deu" {
		t.Errorf("OCRLang = %q, want deu", cfg.OCRLang)
	}
	if cfg.DBPath != "/env/test.db" {
		t.Errorf("DBPath = %q, want /env/test.db", cfg.DBPath)
	}
	if cfg.ArchivePath != "/env/archive" {
		t.Errorf("ArchivePath = %q, want /env/archive", cfg.ArchivePath)
	}
	if cfg.Inbox != "/env/inbox" {
		t.Errorf("Inbox = %q, want /env/inbox", cfg.Inbox)
	}
	if cfg.PaperlessBaseURL != "https://paperless.example" {
		t.Errorf("PaperlessBaseURL = %q, want https://paperless.example", cfg.PaperlessBaseURL)
	}
	if !cfg.SymseekEnabled {
		t.Error("SymseekEnabled = false, want true")
	}
	if cfg.SymseekBinary != "/usr/local/bin/symseek" {
		t.Errorf("SymseekBinary = %q, want /usr/local/bin/symseek", cfg.SymseekBinary)
	}
}

func TestLoad_ConfigFile(t *testing.T) {
	Loader.ResetCache()
	defer Loader.ResetCache()

	home := t.TempDir()
	t.Setenv("HOME", home)

	// Clear env vars so config file values are used.
	for _, key := range []string{
		"SYMINGEST_VAULT", "SYMINGEST_OCR_LANG", "SYMINGEST_DB_PATH",
		"SYMINGEST_ARCHIVE_PATH", "SYMINGEST_INBOX",
		"SYMINGEST_PAPERLESS_BASE_URL", "SYMINGEST_SYMSEEK_ENABLED",
		"SYMINGEST_SYMSEEK_BINARY",
	} {
		t.Setenv(key, "")
	}

	cfgDir := filepath.Join(home, ".config", "symingest")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	content := `vault = "/file/vault"
ocr_lang = "fra"
db_path = "/file/test.db"
archive_path = "/file/archive"
inbox = "/file/inbox"
paperless_base_url = "https://paperless.from-file"
symseek_enabled = true
symseek_binary = "/opt/symseek"
`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.Vault != "/file/vault" {
		t.Errorf("Vault = %q, want /file/vault", cfg.Vault)
	}
	if cfg.OCRLang != "fra" {
		t.Errorf("OCRLang = %q, want fra", cfg.OCRLang)
	}
	if cfg.DBPath != "/file/test.db" {
		t.Errorf("DBPath = %q, want /file/test.db", cfg.DBPath)
	}
	if cfg.ArchivePath != "/file/archive" {
		t.Errorf("ArchivePath = %q, want /file/archive", cfg.ArchivePath)
	}
	if cfg.Inbox != "/file/inbox" {
		t.Errorf("Inbox = %q, want /file/inbox", cfg.Inbox)
	}
	if cfg.PaperlessBaseURL != "https://paperless.from-file" {
		t.Errorf("PaperlessBaseURL = %q, want https://paperless.from-file", cfg.PaperlessBaseURL)
	}
	if !cfg.SymseekEnabled {
		t.Error("SymseekEnabled = false, want true")
	}
	if cfg.SymseekBinary != "/opt/symseek" {
		t.Errorf("SymseekBinary = %q, want /opt/symseek", cfg.SymseekBinary)
	}
}

func TestLoad_DefaultsWhenNothingSet(t *testing.T) {
	Loader.ResetCache()
	defer Loader.ResetCache()

	home := t.TempDir()
	t.Setenv("HOME", home)

	for _, key := range []string{
		"SYMINGEST_VAULT", "SYMINGEST_OCR_LANG", "SYMINGEST_DB_PATH",
		"SYMINGEST_ARCHIVE_PATH", "SYMINGEST_INBOX",
		"SYMINGEST_PAPERLESS_BASE_URL", "SYMINGEST_SYMSEEK_ENABLED",
		"SYMINGEST_SYMSEEK_BINARY",
	} {
		t.Setenv(key, "")
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.OCRLang != "eng" {
		t.Errorf("OCRLang = %q, want eng (default)", cfg.OCRLang)
	}
	if cfg.Vault != "" {
		t.Errorf("Vault = %q, want empty", cfg.Vault)
	}
	if cfg.SymseekEnabled {
		t.Error("SymseekEnabled = true, want false (default)")
	}
}
