package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/health"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

func runRootCmd(t *testing.T, args []string) (string, error) {
	t.Helper()

	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	cmd := newRootCmd()
	cmd.SetArgs(args)
	execErr := cmd.Execute()

	if err := w.Close(); err != nil {
		os.Stdout = origStdout
		t.Fatalf("close captured stdout: %v", err)
	}
	os.Stdout = origStdout

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String(), execErr
}

func TestVaultAdoptCmd_DryRun(t *testing.T) {
	vaultDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(vaultDir, "note.md"), []byte("# Hello Note\nBody content\n"), 0600); err != nil {
		t.Fatal(err)
	}

	origCfg := cfg
	cfg = &config.Config{Vault: vaultDir}
	t.Cleanup(func() { cfg = origCfg })
	jsonFlag = false

	out, err := runRootCmd(t, []string{"vault", "adopt", "--dry-run"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(out, "Vault adoption (dry-run)") {
		t.Errorf("expected header 'Vault adoption (dry-run)', got:\n%s", out)
	}
	if !strings.Contains(out, "+ title: \"Hello Note\"") {
		t.Errorf("expected title in output, got:\n%s", out)
	}

	// Verify note is unchanged on disk
	raw, err := os.ReadFile(filepath.Join(vaultDir, "note.md")) //nolint:gosec // vaultDir is a test fixture root
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "# Hello Note\nBody content\n" {
		t.Errorf("dry-run modified note on disk: %s", string(raw))
	}
}

func TestVaultAdoptCmd_Apply(t *testing.T) {
	vaultDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(vaultDir, "note.md"), []byte("# Hello Note\nBody content\n"), 0600); err != nil {
		t.Fatal(err)
	}

	origCfg := cfg
	cfg = &config.Config{Vault: vaultDir}
	t.Cleanup(func() { cfg = origCfg })
	jsonFlag = false

	out, err := runRootCmd(t, []string{"vault", "adopt"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(out, "Vault adoption") {
		t.Errorf("expected header 'Vault adoption', got:\n%s", out)
	}

	doc, err := vault.ParseFile(filepath.Join(vaultDir, "note.md"))
	if err != nil {
		t.Fatalf("parse note failed: %v", err)
	}
	if doc.Title != "Hello Note" {
		t.Errorf("expected title 'Hello Note', got %q", doc.Title)
	}
	if doc.Created == "" {
		t.Errorf("expected non-empty created")
	}
}

func TestVaultAdoptCmd_ExplicitDir(t *testing.T) {
	vaultDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(vaultDir, "daily.md"), []byte("Just plain text\n"), 0600); err != nil {
		t.Fatal(err)
	}

	origCfg := cfg
	cfg = &config.Config{Vault: ""}
	t.Cleanup(func() { cfg = origCfg })
	jsonFlag = false

	out, err := runRootCmd(t, []string{"vault", "adopt", vaultDir, "--dry-run"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(out, "Vault adoption (dry-run)") {
		t.Errorf("expected dry-run header, got:\n%s", out)
	}
	if !strings.Contains(out, "+ title: \"daily\"") {
		t.Errorf("expected title fallback to filename, got:\n%s", out)
	}
}

func TestVaultAdoptCmd_JSON(t *testing.T) {
	vaultDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(vaultDir, "2026-08-24.md"), []byte("# Daily Note\nToday's log\n"), 0600); err != nil {
		t.Fatal(err)
	}

	origCfg := cfg
	cfg = &config.Config{Vault: vaultDir}
	t.Cleanup(func() { cfg = origCfg })

	out, err := runRootCmd(t, []string{"vault", "adopt", "--dry-run", "--json"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var report health.AdoptReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("failed to unmarshal JSON report: %v\nOutput was:\n%s", err, out)
	}

	if report.SchemaVersion != health.AdoptReportSchemaVersion {
		t.Errorf("expected schema version %d, got %d", health.AdoptReportSchemaVersion, report.SchemaVersion)
	}
	if !report.DryRun {
		t.Errorf("expected DryRun true")
	}
	if report.Total != 1 || report.Adopted != 1 {
		t.Errorf("unexpected counts: total=%d, adopted=%d", report.Total, report.Adopted)
	}
	if len(report.Documents) != 1 {
		t.Fatalf("expected 1 document in report, got %d", len(report.Documents))
	}
	if report.Documents[0].Title != "Daily Note" {
		t.Errorf("expected title 'Daily Note', got %q", report.Documents[0].Title)
	}
	if report.Documents[0].Created != "2026-08-24T00:00:00Z" {
		t.Errorf("expected created '2026-08-24T00:00:00Z', got %q", report.Documents[0].Created)
	}
}
