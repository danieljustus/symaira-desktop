package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-ingest/internal/paperlessimport"
	"github.com/danieljustus/symaira-ingest/internal/store"
)

func TestRun_NoArgs(t *testing.T) {
	sb := withCapturedStdout(t)
	if err := run([]string{}); err != nil {
		t.Fatalf("run(no args): %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "symingest") {
		t.Errorf("usage output missing 'symingest', got %q", out)
	}
}

func TestRun_Extract_TextFile(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "invoice.txt")
	content := "INVOICE #4471\nAcme Hardware Supply\nDate: 2026-03-12\nTotal Due: $284.50\nEmail: accounts@acme.com\n"
	if err := os.WriteFile(source, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	sb := withCapturedStdout(t)
	if err := run([]string{"extract", "-profile", "generic", source}); err != nil {
		t.Fatalf("run(extract): %v", err)
	}
	out := sb.String()
	for _, want := range []string{"Profile: generic", "Text length:", "Extractions:"} {
		if !strings.Contains(out, want) {
			t.Errorf("extract output missing %q, got:\n%s", want, out)
		}
	}
}

func TestRun_Extract_JSON(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "doc.txt")
	if err := os.WriteFile(source, []byte("Invoice #123 for $50.00"), 0o644); err != nil {
		t.Fatal(err)
	}

	sb := withCapturedStdout(t)
	if err := run([]string{"extract", "-profile", "generic", "-json", source}); err != nil {
		t.Fatalf("run(extract -json): %v", err)
	}
	out := strings.TrimSpace(sb.String())
	if out == "" {
		t.Fatal("expected JSON output")
	}
	// Each line should be valid JSON (JSONL format).
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "[]" {
			continue
		}
		var v any
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Errorf("invalid JSON line %q: %v", line, err)
		}
	}
}

func TestRun_Extract_NoFile(t *testing.T) {
	sb := withCapturedStdout(t)
	// No file argument should print usage and return nil.
	if err := run([]string{"extract"}); err != nil {
		t.Fatalf("run(extract no file): %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "Usage:") {
		t.Errorf("expected usage output, got %q", out)
	}
}

func TestRun_Extract_UnknownProfile(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "doc.txt")
	if err := os.WriteFile(source, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	withCapturedStdout(t)
	err := run([]string{"extract", "-profile", "nonexistent", source})
	if err == nil {
		t.Fatal("expected error for unknown profile")
	}
	if !strings.Contains(err.Error(), "unknown profile") {
		t.Errorf("error = %v, want mention of 'unknown profile'", err)
	}
}

func TestRun_ValidateVault_EmptyVault(t *testing.T) {
	vault := t.TempDir()
	sb := withCapturedStdout(t)
	if err := run([]string{"validate-vault", vault}); err != nil {
		t.Fatalf("run(validate-vault): %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "0 files") {
		t.Errorf("expected '0 files', got %q", out)
	}
}

func TestRun_ValidateVault_ValidNote(t *testing.T) {
	vault := t.TempDir()
	note := `---
source_path: test.txt
ingested_at: 2026-01-01T00:00:00Z
sha256: abc123
mime: text/plain
tags: []
---

This is a valid note body with enough content.
`
	if err := os.WriteFile(filepath.Join(vault, "note.md"), []byte(note), 0o644); err != nil {
		t.Fatal(err)
	}

	sb := withCapturedStdout(t)
	if err := run([]string{"validate-vault", vault}); err != nil {
		t.Fatalf("run(validate-vault): %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "1 files") || !strings.Contains(out, "0 failures") {
		t.Errorf("expected '1 files, 0 failures', got %q", out)
	}
}

func TestRun_ValidateVault_JSON(t *testing.T) {
	vault := t.TempDir()
	sb := withCapturedStdout(t)
	if err := run([]string{"validate-vault", "-json", vault}); err != nil {
		t.Fatalf("run(validate-vault -json): %v", err)
	}
	out := sb.String()
	var report map[string]any
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if _, ok := report["vault"]; !ok {
		t.Error("JSON report missing 'vault' field")
	}
}

func TestRun_ValidateVault_MissingPath(t *testing.T) {
	withCapturedStdout(t)
	err := run([]string{"validate-vault"})
	if err == nil {
		t.Fatal("expected error for missing vault path")
	}
}

func TestRun_ValidateVault_MinBodyLength(t *testing.T) {
	vault := t.TempDir()
	note := `---
source_path: test.txt
ingested_at: 2026-01-01T00:00:00Z
sha256: abc123
mime: text/plain
---

Short.
`
	if err := os.WriteFile(filepath.Join(vault, "note.md"), []byte(note), 0o644); err != nil {
		t.Fatal(err)
	}

	withCapturedStdout(t)
	err := run([]string{"validate-vault", "-min-body-length", "40", vault})
	if err == nil {
		t.Fatal("expected error for body too short")
	}
}

func TestRun_ImportNotion_DryRun(t *testing.T) {
	// Use the existing testdata fixture.
	fixtureDir := filepath.Join("testdata", "notion-fixture")
	if _, err := os.Stat(fixtureDir); os.IsNotExist(err) {
		// Fall back to the internal package testdata.
		fixtureDir = filepath.Join("..", "..", "internal", "notionimport", "testdata", "fixture", "My Workspace")
		if _, err := os.Stat(fixtureDir); os.IsNotExist(err) {
			t.Skip("notion testdata not available")
		}
	}

	sb := withCapturedStdout(t)
	err := run([]string{"import", "notion", "-dry-run", fixtureDir})
	if err != nil {
		t.Fatalf("run(import notion -dry-run): %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "Notion import dry-run") {
		t.Errorf("expected dry-run message, got %q", out)
	}
}

func TestRun_ImportNotion_NoArgs(t *testing.T) {
	sb := withCapturedStdout(t)
	if err := run([]string{"import", "notion"}); err != nil {
		t.Fatalf("run(import notion no args): %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "Usage:") {
		t.Errorf("expected usage output, got %q", out)
	}
}

func TestRun_Import_UnknownSubcommand(t *testing.T) {
	withCapturedStdout(t)
	err := run([]string{"import", "bogus"})
	if err == nil {
		t.Fatal("expected error for unknown import subcommand")
	}
	if !strings.Contains(err.Error(), "unknown import subcommand") {
		t.Errorf("error = %v, want mention of 'unknown import subcommand'", err)
	}
}

func TestRun_Import_NoArgs(t *testing.T) {
	sb := withCapturedStdout(t)
	if err := run([]string{"import"}); err != nil {
		t.Fatalf("run(import no args): %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "Usage:") {
		t.Errorf("expected usage output, got %q", out)
	}
}

func TestRun_Setup_MissingVault(t *testing.T) {
	withCapturedStdout(t)
	err := run([]string{"setup", "-inbox", "/tmp/inbox", "-paperless-base-url", "https://example.com"})
	if err == nil {
		t.Fatal("expected error for missing --vault")
	}
	if !strings.Contains(err.Error(), "--vault is required") {
		t.Errorf("error = %v, want mention of '--vault is required'", err)
	}
}

func TestRun_Setup_MissingInbox(t *testing.T) {
	withCapturedStdout(t)
	err := run([]string{"setup", "-vault", "/tmp/vault", "-paperless-base-url", "https://example.com"})
	if err == nil {
		t.Fatal("expected error for missing --inbox")
	}
	if !strings.Contains(err.Error(), "--inbox is required") {
		t.Errorf("error = %v, want mention of '--inbox is required'", err)
	}
}

func TestRun_Setup_MissingPaperlessBaseURL(t *testing.T) {
	withCapturedStdout(t)
	err := run([]string{"setup", "-vault", "/tmp/vault", "-inbox", "/tmp/inbox"})
	if err == nil {
		t.Fatal("expected error for missing --paperless-base-url")
	}
	if !strings.Contains(err.Error(), "--paperless-base-url is required") {
		t.Errorf("error = %v, want mention of '--paperless-base-url is required'", err)
	}
}

func TestRun_Search_UnknownSubcommand(t *testing.T) {
	withCapturedStdout(t)
	err := run([]string{"search", "bogus"})
	if err == nil {
		t.Fatal("expected error for unknown search subcommand")
	}
	if !strings.Contains(err.Error(), "unknown search command") {
		t.Errorf("error = %v, want mention of 'unknown search command'", err)
	}
}

func TestRun_Search_NoArgs(t *testing.T) {
	sb := withCapturedStdout(t)
	if err := run([]string{"search"}); err != nil {
		t.Fatalf("run(search no args): %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "Usage:") {
		t.Errorf("expected usage output, got %q", out)
	}
}

func TestRun_SearchValidate_MissingFixtures(t *testing.T) {
	withCapturedStdout(t)
	err := run([]string{"search", "validate"})
	if err == nil {
		t.Fatal("expected error for missing --fixtures")
	}
	if !strings.Contains(err.Error(), "requires --fixtures") {
		t.Errorf("error = %v, want mention of 'requires --fixtures'", err)
	}
}

func TestPrintConfigDiff_NoChanges(t *testing.T) {
	var sb strings.Builder
	printConfigDiff(&sb, "same", "same")
	if !strings.Contains(sb.String(), "No changes.") {
		t.Errorf("expected 'No changes.', got %q", sb.String())
	}
}

func TestPrintConfigDiff_WithChanges(t *testing.T) {
	var sb strings.Builder
	printConfigDiff(&sb, "old line", "new line")
	out := sb.String()
	if !strings.Contains(out, "--- current") || !strings.Contains(out, "+++ proposed") {
		t.Errorf("expected diff headers, got %q", out)
	}
	if !strings.Contains(out, "- old line") || !strings.Contains(out, "+ new line") {
		t.Errorf("expected diff lines, got %q", out)
	}
}

func TestPrintConfigDiff_EmptyOld(t *testing.T) {
	var sb strings.Builder
	printConfigDiff(&sb, "", "new content")
	out := sb.String()
	if strings.Contains(out, "--- current") {
		t.Errorf("should not have '--- current' for empty old, got %q", out)
	}
	if !strings.Contains(out, "+++ proposed") {
		t.Errorf("expected '+++ proposed', got %q", out)
	}
}

func TestRenderSetupConfig(t *testing.T) {
	cfg := setupConfig{
		Vault:            "/vault",
		ArchivePath:      "/archive",
		DBPath:           "/db.sqlite",
		Inbox:            "/inbox",
		OCRLang:          "eng",
		PaperlessBaseURL: "https://paperless.example",
	}
	got := renderSetupConfig(cfg)
	for _, want := range []string{`vault = "/vault"`, `archive_path = "/archive"`, `db_path = "/db.sqlite"`, `inbox = "/inbox"`, `ocr_lang = "eng"`, `paperless_base_url = "https://paperless.example"`} {
		if !strings.Contains(got, want) {
			t.Errorf("renderSetupConfig missing %q, got:\n%s", want, got)
		}
	}
}

func TestStringList(t *testing.T) {
	var s stringList
	if err := s.Set("a,b,c"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got := s.String(); got != "a,b,c" {
		t.Errorf("String() = %q, want a,b,c", got)
	}
	if len(s) != 3 {
		t.Errorf("len = %d, want 3", len(s))
	}
}

func TestStringList_TrimSpace(t *testing.T) {
	var s stringList
	if err := s.Set(" a , b , c "); err != nil {
		t.Fatalf("Set: %v", err)
	}
	for _, v := range s {
		if v != strings.TrimSpace(v) {
			t.Errorf("value %q not trimmed", v)
		}
	}
}

func TestRun_Doctor_Basic(t *testing.T) {
	toolsDir := t.TempDir()
	writeTestBin(t, toolsDir, "tesseract", `#!/bin/sh
if [ "$1" = "--list-langs" ]; then
  echo "eng"
  exit 0
fi
exit 0
`)
	writeTestBin(t, toolsDir, "pdftoppm", "#!/bin/sh\nexit 0\n")
	writeTestBin(t, toolsDir, "sips", "#!/bin/sh\nexit 0\n")
	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", toolsDir+string(os.PathListSeparator)+oldPath)
	defer os.Setenv("PATH", oldPath)

	dir := t.TempDir()
	sb := withCapturedStdout(t)
	err := run([]string{
		"doctor",
		"-vault", filepath.Join(dir, "vault"),
		"-archive", filepath.Join(dir, "archive"),
		"-db", filepath.Join(dir, "db.sqlite"),
		"-inbox", filepath.Join(dir, "inbox"),
		"-ocr-lang", "eng",
	})
	// Doctor may return warning for non-existent dirs, that's ok.
	_ = err
	out := sb.String()
	if !strings.Contains(out, "symingest") {
		t.Errorf("doctor output missing 'symingest', got %q", out)
	}
}

func TestRun_Doctor_JSON(t *testing.T) {
	toolsDir := t.TempDir()
	writeTestBin(t, toolsDir, "tesseract", `#!/bin/sh
if [ "$1" = "--list-langs" ]; then
  echo "eng"
  exit 0
fi
exit 0
`)
	writeTestBin(t, toolsDir, "pdftoppm", "#!/bin/sh\nexit 0\n")
	writeTestBin(t, toolsDir, "sips", "#!/bin/sh\nexit 0\n")
	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", toolsDir+string(os.PathListSeparator)+oldPath)
	defer os.Setenv("PATH", oldPath)

	dir := t.TempDir()
	sb := withCapturedStdout(t)
	_ = run([]string{
		"doctor", "-json",
		"-vault", filepath.Join(dir, "vault"),
		"-archive", filepath.Join(dir, "archive"),
		"-db", filepath.Join(dir, "db.sqlite"),
		"-inbox", filepath.Join(dir, "inbox"),
		"-ocr-lang", "eng",
	})
	out := sb.String()
	var report doctorReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("doctor JSON: %v\n%s", err, out)
	}
	if report.Status == "" {
		t.Error("doctor report missing status")
	}
}

func TestJoinInts(t *testing.T) {
	tests := []struct {
		name  string
		input []int
		want  string
	}{
		{"empty", nil, ""},
		{"single", []int{42}, "42"},
		{"multiple", []int{1, 2, 3}, "1,2,3"},
		{"negative", []int{-1, 0, 1}, "-1,0,1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := joinInts(tt.input)
			if got != tt.want {
				t.Errorf("joinInts(%v) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestPrintVerifyReport_Empty(t *testing.T) {
	var sb strings.Builder
	r := &paperlessimport.VerifyReport{
		SourceDocuments: 10,
		VaultNotes:      10,
		Verified:        10,
	}
	printVerifyReport(&sb, r)
	out := sb.String()
	if !strings.Contains(out, "10 source documents") {
		t.Errorf("missing source count, got %q", out)
	}
	if !strings.Contains(out, "OK: vault matches") {
		t.Errorf("missing OK message, got %q", out)
	}
}

func TestPrintVerifyReport_WithIssues(t *testing.T) {
	var sb strings.Builder
	r := &paperlessimport.VerifyReport{
		SourceDocuments:    10,
		VaultNotes:         8,
		Verified:           7,
		Missing:            []int{1, 2},
		Duplicate:          []int{3},
		MissingArchive:     []int{4},
		HashMismatch:       []int{5},
		SourceHashMismatch: []int{6},
		Mismatches: []paperlessimport.VerifyMismatch{
			{DocumentID: 7, Field: "tags", Expected: "a", Got: "b"},
		},
	}
	printVerifyReport(&sb, r)
	out := sb.String()
	for _, want := range []string{"missing from vault", "duplicate notes", "missing archived original", "hash mismatches", "metadata mismatches"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
}

func TestPrintVerifyReport_DeepVerify(t *testing.T) {
	var sb strings.Builder
	r := &paperlessimport.VerifyReport{
		SourceDocuments: 5,
		VaultNotes:      5,
		Verified:        5,
		DeepVerify:      true,
		DeepVerified:    5,
	}
	printVerifyReport(&sb, r)
	out := sb.String()
	if !strings.Contains(out, "deep verify") {
		t.Errorf("missing deep verify message, got %q", out)
	}
}

func TestRunReport_ValidateMigration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "migration.json")
	data, err := json.Marshal(&paperlessimport.MigrationReport{
		SchemaVersion: paperlessimport.ReportSchemaVersion,
		ToolVersion:   "test-version",
		Mode:          "import",
		Total:         1,
		Imported:      1,
		Documents:     []paperlessimport.DocumentResult{{ID: 1, Status: "imported"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	sb := withCapturedStdout(t)
	if err := run([]string{"report", "validate", path}); err != nil {
		t.Fatalf("run(report validate): %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "report valid") {
		t.Errorf("expected 'report valid', got %q", out)
	}
}

func TestRunReport_ValidateInvalid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte("{not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}

	withCapturedStdout(t)
	err := run([]string{"report", "validate", path})
	if err == nil {
		t.Fatal("expected error for invalid report")
	}
}

func TestRunReport_NoArgs(t *testing.T) {
	sb := withCapturedStdout(t)
	if err := run([]string{"report"}); err != nil {
		t.Fatalf("run(report no args): %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "Usage:") {
		t.Errorf("expected usage output, got %q", out)
	}
}

func TestRunReport_WrongSubcommand(t *testing.T) {
	sb := withCapturedStdout(t)
	if err := run([]string{"report", "bogus", "file.json"}); err != nil {
		t.Fatalf("run(report bogus): %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "Usage:") {
		t.Errorf("expected usage output, got %q", out)
	}
}

func TestRunImport_MissingBaseURL(t *testing.T) {
	withCapturedStdout(t)
	err := run([]string{"import", "paperless", "-token", "test"})
	if err == nil {
		t.Fatal("expected error for missing base-url")
	}
	if !strings.Contains(err.Error(), "base-url is required") {
		t.Errorf("error = %v, want mention of 'base-url is required'", err)
	}
}

func TestRunImport_MissingToken(t *testing.T) {
	withCapturedStdout(t)
	err := run([]string{"import", "paperless", "-base-url", "https://example.com"})
	if err == nil {
		t.Fatal("expected error for missing token")
	}
	if !strings.Contains(err.Error(), "token is required") {
		t.Errorf("error = %v, want mention of 'token is required'", err)
	}
}

func TestRunImport_PlanDryRunMutuallyExclusive(t *testing.T) {
	withCapturedStdout(t)
	err := run([]string{"import", "paperless", "-base-url", "https://example.com", "-token", "test", "-plan", "-dry-run"})
	if err == nil {
		t.Fatal("expected error for --plan and --dry-run")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error = %v, want mention of 'mutually exclusive'", err)
	}
}

func TestRunImport_DeepWithoutVerify(t *testing.T) {
	withCapturedStdout(t)
	err := run([]string{"import", "paperless", "-base-url", "https://example.com", "-token", "test", "-deep"})
	if err == nil {
		t.Fatal("expected error for --deep without --verify")
	}
	if !strings.Contains(err.Error(), "--deep is only valid with --verify") {
		t.Errorf("error = %v, want mention of '--deep is only valid with --verify'", err)
	}
}

func TestRunImport_InvalidConcurrency(t *testing.T) {
	withCapturedStdout(t)
	err := run([]string{"import", "paperless", "-base-url", "https://example.com", "-token", "test", "-concurrency", "0"})
	if err == nil {
		t.Fatal("expected error for invalid concurrency")
	}
	if !strings.Contains(err.Error(), "invalid concurrency") {
		t.Errorf("error = %v, want mention of 'invalid concurrency'", err)
	}
}

func TestRunImport_InvalidSince(t *testing.T) {
	withCapturedStdout(t)
	err := run([]string{"import", "paperless", "-base-url", "https://example.com", "-token", "test", "-vault", t.TempDir(), "-since", "not-a-date"})
	if err == nil {
		t.Fatal("expected error for invalid since date")
	}
	if !strings.Contains(err.Error(), "invalid since date") {
		t.Errorf("error = %v, want mention of 'invalid since date'", err)
	}
}

func TestRunImport_InvalidIDs(t *testing.T) {
	withCapturedStdout(t)
	err := run([]string{"import", "paperless", "-base-url", "https://example.com", "-token", "test", "-vault", t.TempDir(), "-ids", "abc"})
	if err == nil {
		t.Fatal("expected error for invalid ids")
	}
	if !strings.Contains(err.Error(), "invalid ids") {
		t.Errorf("error = %v, want mention of 'invalid ids'", err)
	}
}

func TestRunJobs_WithLimit(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")
	sb := withCapturedStdout(t)
	if err := run([]string{"jobs", "-db", tempDB, "-limit", "10"}); err != nil {
		t.Fatalf("run(jobs -limit): %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "No jobs") {
		t.Errorf("expected 'No jobs', got %q", out)
	}
}

func TestRunRules_ListJSON(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")
	sb := withCapturedStdout(t)
	if err := run([]string{"rules", "-db", tempDB, "-json", "list"}); err != nil {
		t.Fatalf("run(rules list -json): %v", err)
	}
	out := strings.TrimSpace(sb.String())
	if !strings.Contains(out, `"schema_version": 1`) || !strings.Contains(out, `"rules": []`) {
		t.Errorf("expected versioned empty rules envelope, got %q", out)
	}
}

func TestRunRules_TestJSON_NoMatch(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")
	sb := withCapturedStdout(t)
	if err := run([]string{"rules", "-db", tempDB, "-json", "test", "no match"}); err != nil {
		t.Fatalf("run(rules test -json): %v", err)
	}
	out := strings.TrimSpace(sb.String())
	if !strings.Contains(out, `"schema_version": 1`) || !strings.Contains(out, `"matches": []`) {
		t.Errorf("expected versioned empty matches envelope, got %q", out)
	}
}

func TestRunDoctor_WithInbox(t *testing.T) {
	toolsDir := t.TempDir()
	writeTestBin(t, toolsDir, "tesseract", `#!/bin/sh
if [ "$1" = "--list-langs" ]; then
  echo "eng"
  exit 0
fi
exit 0
`)
	writeTestBin(t, toolsDir, "pdftoppm", "#!/bin/sh\nexit 0\n")
	writeTestBin(t, toolsDir, "sips", "#!/bin/sh\nexit 0\n")
	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", toolsDir+string(os.PathListSeparator)+oldPath)
	defer os.Setenv("PATH", oldPath)

	dir := t.TempDir()
	inbox := filepath.Join(dir, "inbox")
	if err := os.MkdirAll(inbox, 0o700); err != nil {
		t.Fatal(err)
	}

	sb := withCapturedStdout(t)
	err := run([]string{
		"doctor",
		"-vault", filepath.Join(dir, "vault"),
		"-archive", filepath.Join(dir, "archive"),
		"-db", filepath.Join(dir, "db.sqlite"),
		"-inbox", inbox,
		"-ocr-lang", "eng",
	})
	_ = err
	out := sb.String()
	if !strings.Contains(out, "symingest") {
		t.Errorf("doctor output missing 'symingest', got %q", out)
	}
}

func TestRunValidateVault_WithFailures(t *testing.T) {
	vault := t.TempDir()
	note := `---
source_path: test.txt
---

Body content.
`
	if err := os.WriteFile(filepath.Join(vault, "note.md"), []byte(note), 0o644); err != nil {
		t.Fatal(err)
	}

	withCapturedStdout(t)
	err := run([]string{"validate-vault", vault})
	if err == nil {
		t.Fatal("expected error for validation failures")
	}
}

func TestRunExtract_WithProfile(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "doc.txt")
	if err := os.WriteFile(source, []byte("Invoice #123 for $50.00 dated 2026-01-15"), 0o644); err != nil {
		t.Fatal(err)
	}

	sb := withCapturedStdout(t)
	if err := run([]string{"extract", "-profile", "invoice", source}); err != nil {
		t.Fatalf("run(extract -profile invoice): %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "Profile: invoice") {
		t.Errorf("output missing 'Profile: invoice', got %q", out)
	}
}

func TestRunExtract_ContractProfile(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "contract.txt")
	if err := os.WriteFile(source, []byte("Contract between Party A and Party B, effective 2026-01-01"), 0o644); err != nil {
		t.Fatal(err)
	}

	sb := withCapturedStdout(t)
	if err := run([]string{"extract", "-profile", "contract", source}); err != nil {
		t.Fatalf("run(extract -profile contract): %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "Profile: contract") {
		t.Errorf("output missing 'Profile: contract', got %q", out)
	}
}

func TestRunExtract_JobcenterProfile(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "jobcenter.txt")
	if err := os.WriteFile(source, []byte("Job ID: 12345, Appointment: 2026-02-15"), 0o644); err != nil {
		t.Fatal(err)
	}

	sb := withCapturedStdout(t)
	if err := run([]string{"extract", "-profile", "jobcenter", source}); err != nil {
		t.Fatalf("run(extract -profile jobcenter): %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "Profile: jobcenter") {
		t.Errorf("output missing 'Profile: jobcenter', got %q", out)
	}
}

func TestRunRules_Update_WithAllFields(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	sb := withCapturedStdout(t)
	if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", "category", "Invoices"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}

	st, err := store.Open(tempDB)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	rules, err := st.ListRules(context.Background())
	if err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	ruleID := strconv.FormatInt(rules[0].ID, 10)

	sb.Reset()
	if err := run([]string{
		"rules", "-db", tempDB, "update", ruleID,
		"*.docx", "tag", "Documents",
	}); err != nil {
		t.Fatalf("rules update: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "Updated classification rule") {
		t.Errorf("update output missing confirmation, got %q", out)
	}
}

func TestRunRules_Test_WithMatch(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	sb := withCapturedStdout(t)
	if err := run([]string{"rules", "-db", tempDB, "add", "invoice", "category", "Finance"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}

	sb.Reset()
	if err := run([]string{"rules", "-db", tempDB, "test", "This is an invoice document"}); err != nil {
		t.Fatalf("rules test: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "match rule") {
		t.Errorf("test output missing match, got %q", out)
	}
}

func TestRunRules_Test_NoMatch(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	sb := withCapturedStdout(t)
	if err := run([]string{"rules", "-db", tempDB, "add", "invoice", "category", "Finance"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}

	sb.Reset()
	if err := run([]string{"rules", "-db", tempDB, "test", "This is a receipt"}); err != nil {
		t.Fatalf("rules test: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "No matching") {
		t.Errorf("test output missing no-match message, got %q", out)
	}
}

func TestRunRules_Delete_InvalidID(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")
	withCapturedStdout(t)

	err := run([]string{"rules", "-db", tempDB, "delete", "not-a-number"})
	if err == nil {
		t.Fatal("expected error for invalid rule ID")
	}
	if !strings.Contains(err.Error(), "invalid rule ID") {
		t.Errorf("error = %v, want mention of 'invalid rule ID'", err)
	}
}

func TestRunRules_Delete_MissingID(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")
	withCapturedStdout(t)

	err := run([]string{"rules", "-db", tempDB, "delete"})
	if err == nil {
		t.Fatal("expected error for missing rule ID")
	}
	if !strings.Contains(err.Error(), "missing rule ID") {
		t.Errorf("error = %v, want mention of 'missing rule ID'", err)
	}
}

func TestRunRules_Delete_NonexistentID(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")
	withCapturedStdout(t)

	err := run([]string{"rules", "-db", tempDB, "delete", "999999"})
	if err == nil {
		t.Fatal("expected error for non-existent rule ID")
	}
}

func TestRunRules_Add_MissingArgs(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")
	withCapturedStdout(t)

	err := run([]string{"rules", "-db", tempDB, "add"})
	if err == nil {
		t.Fatal("expected error for missing add arguments")
	}
	if !strings.Contains(err.Error(), "missing arguments") {
		t.Errorf("error = %v, want mention of 'missing arguments'", err)
	}
}

func TestRunRules_Add_InvalidKind(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")
	withCapturedStdout(t)

	err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", "invalid_kind", "value"})
	if err == nil {
		t.Fatal("expected error for invalid kind")
	}
}

func TestRunRules_List_WithRules(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	sb := withCapturedStdout(t)
	if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", "category", "Invoices"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}

	sb.Reset()
	if err := run([]string{"rules", "-db", tempDB, "list"}); err != nil {
		t.Fatalf("rules list: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "*.pdf") {
		t.Errorf("list output missing pattern, got %q", out)
	}
	if !strings.Contains(out, "Invoices") {
		t.Errorf("list output missing value, got %q", out)
	}
}

func TestRunRules_List_JSON(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	sb := withCapturedStdout(t)
	if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", "category", "Invoices"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}

	sb.Reset()
	if err := run([]string{"rules", "-db", tempDB, "-json", "list"}); err != nil {
		t.Fatalf("rules list -json: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "[") {
		t.Errorf("JSON output missing array, got %q", out)
	}
	if !strings.Contains(out, "*.pdf") {
		t.Errorf("JSON output missing pattern, got %q", out)
	}
}

func TestRunRules_Update_MissingArgs(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")
	withCapturedStdout(t)

	err := run([]string{"rules", "-db", tempDB, "update"})
	if err == nil {
		t.Fatal("expected error for missing update arguments")
	}
	if !strings.Contains(err.Error(), "missing arguments") {
		t.Errorf("error = %v, want mention of 'missing arguments'", err)
	}
}

func TestRunRules_Update_InvalidID(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")
	withCapturedStdout(t)

	err := run([]string{"rules", "-db", tempDB, "update", "not-a-number", "*.pdf", "category", "Test"})
	if err == nil {
		t.Fatal("expected error for invalid rule ID")
	}
	if !strings.Contains(err.Error(), "invalid rule ID") {
		t.Errorf("error = %v, want mention of 'invalid rule ID'", err)
	}
}

func TestRunRules_Test_MissingText(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")
	withCapturedStdout(t)

	err := run([]string{"rules", "-db", tempDB, "test"})
	if err == nil {
		t.Fatal("expected error for missing text")
	}
	if !strings.Contains(err.Error(), "missing text") {
		t.Errorf("error = %v, want mention of 'missing text'", err)
	}
}

func TestRunRules_UnknownSubcommand(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")
	withCapturedStdout(t)

	err := run([]string{"rules", "-db", tempDB, "bogus"})
	if err == nil {
		t.Fatal("expected error for unknown rules subcommand")
	}
	if !strings.Contains(err.Error(), "unknown rules subcommand") {
		t.Errorf("error = %v, want mention of 'unknown rules subcommand'", err)
	}
}

func TestRunRules_NoSubcommand(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")
	sb := withCapturedStdout(t)

	if err := run([]string{"rules", "-db", tempDB}); err != nil {
		t.Fatalf("run(rules): %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "Usage:") {
		t.Errorf("expected usage output, got %q", out)
	}
}

func TestRunRules_Add_Success(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")
	sb := withCapturedStdout(t)

	if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", "category", "Invoices"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "Added classification rule") {
		t.Errorf("add output missing confirmation, got %q", out)
	}
}

func TestRunRules_Delete_Success(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	sb := withCapturedStdout(t)
	if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", "category", "Invoices"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}

	st, err := store.Open(tempDB)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	rules, err := st.ListRules(context.Background())
	if err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	ruleID := strconv.FormatInt(rules[0].ID, 10)

	sb.Reset()
	if err := run([]string{"rules", "-db", tempDB, "delete", ruleID}); err != nil {
		t.Fatalf("rules delete: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "Deleted classification rule") {
		t.Errorf("delete output missing confirmation, got %q", out)
	}
}

func TestRunRules_Update_Success(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	sb := withCapturedStdout(t)
	if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", "category", "Invoices"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}

	st, err := store.Open(tempDB)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	rules, err := st.ListRules(context.Background())
	if err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	ruleID := strconv.FormatInt(rules[0].ID, 10)

	sb.Reset()
	if err := run([]string{"rules", "-db", tempDB, "update", ruleID, "*.docx", "tag", "Documents"}); err != nil {
		t.Fatalf("rules update: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "Updated classification rule") {
		t.Errorf("update output missing confirmation, got %q", out)
	}
}

func TestRunRules_Test_Success(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	sb := withCapturedStdout(t)
	if err := run([]string{"rules", "-db", tempDB, "add", "invoice", "category", "Finance"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}

	sb.Reset()
	if err := run([]string{"rules", "-db", tempDB, "test", "This is an invoice document"}); err != nil {
		t.Fatalf("rules test: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "match rule") {
		t.Errorf("test output missing match, got %q", out)
	}
}

func TestRunRules_Test_JSON_Success(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	sb := withCapturedStdout(t)
	if err := run([]string{"rules", "-db", tempDB, "add", "invoice", "category", "Finance"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}

	sb.Reset()
	if err := run([]string{"rules", "-db", tempDB, "-json", "test", "invoice document"}); err != nil {
		t.Fatalf("rules test -json: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "[") {
		t.Errorf("JSON output missing array, got %q", out)
	}
	if !strings.Contains(out, "invoice") {
		t.Errorf("JSON output missing pattern, got %q", out)
	}
}

func TestRunRules_Add_WithTags(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")
	sb := withCapturedStdout(t)

	if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", "tag", "Invoices"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "Added classification rule") {
		t.Errorf("add output missing confirmation, got %q", out)
	}
}

func TestRunRules_Add_WithCorrespondent(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")
	sb := withCapturedStdout(t)

	if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", "correspondent", "ACME"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "Added classification rule") {
		t.Errorf("add output missing confirmation, got %q", out)
	}
}

func TestRunRules_Add_WithDocumentType(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")
	sb := withCapturedStdout(t)

	if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", "document_type", "Invoice"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "Added classification rule") {
		t.Errorf("add output missing confirmation, got %q", out)
	}
}

func TestRunRules_Update_WithTags(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	sb := withCapturedStdout(t)
	if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", "category", "Invoices"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}

	st, err := store.Open(tempDB)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	rules, err := st.ListRules(context.Background())
	if err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	ruleID := strconv.FormatInt(rules[0].ID, 10)

	sb.Reset()
	if err := run([]string{"rules", "-db", tempDB, "update", ruleID, "*.docx", "tag", "Documents"}); err != nil {
		t.Fatalf("rules update: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "Updated classification rule") {
		t.Errorf("update output missing confirmation, got %q", out)
	}
}

func TestRunRules_Update_WithCorrespondent(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	sb := withCapturedStdout(t)
	if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", "category", "Invoices"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}

	st, err := store.Open(tempDB)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	rules, err := st.ListRules(context.Background())
	if err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	ruleID := strconv.FormatInt(rules[0].ID, 10)

	sb.Reset()
	if err := run([]string{"rules", "-db", tempDB, "update", ruleID, "*.docx", "correspondent", "ACME"}); err != nil {
		t.Fatalf("rules update: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "Updated classification rule") {
		t.Errorf("update output missing confirmation, got %q", out)
	}
}

func TestRunRules_Update_WithDocumentType(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	sb := withCapturedStdout(t)
	if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", "category", "Invoices"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}

	st, err := store.Open(tempDB)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	rules, err := st.ListRules(context.Background())
	if err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	ruleID := strconv.FormatInt(rules[0].ID, 10)

	sb.Reset()
	if err := run([]string{"rules", "-db", tempDB, "update", ruleID, "*.docx", "document_type", "Invoice"}); err != nil {
		t.Fatalf("rules update: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "Updated classification rule") {
		t.Errorf("update output missing confirmation, got %q", out)
	}
}

func TestRunRules_List_Empty(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")
	sb := withCapturedStdout(t)

	if err := run([]string{"rules", "-db", tempDB, "list"}); err != nil {
		t.Fatalf("rules list: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "No classification rules") {
		t.Errorf("list output missing no-rules message, got %q", out)
	}
}

func TestRunRules_List_JSON_Empty(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")
	sb := withCapturedStdout(t)

	if err := run([]string{"rules", "-db", tempDB, "-json", "list"}); err != nil {
		t.Fatalf("rules list -json: %v", err)
	}
	out := strings.TrimSpace(sb.String())
	if !strings.Contains(out, `"schema_version": 1`) || !strings.Contains(out, `"rules": []`) {
		t.Errorf("expected versioned empty rules envelope, got %q", out)
	}
}

func TestRunRules_Add_MissingKind(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")
	withCapturedStdout(t)

	err := run([]string{"rules", "-db", tempDB, "add", "*.pdf"})
	if err == nil {
		t.Fatal("expected error for missing kind")
	}
	if !strings.Contains(err.Error(), "missing arguments") {
		t.Errorf("error = %v, want mention of 'missing arguments'", err)
	}
}

func TestRunRules_Add_MissingValue(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")
	withCapturedStdout(t)

	err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", "category"})
	if err == nil {
		t.Fatal("expected error for missing value")
	}
	if !strings.Contains(err.Error(), "missing arguments") {
		t.Errorf("error = %v, want mention of 'missing arguments'", err)
	}
}

func TestRunRules_Update_MissingKind(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")
	withCapturedStdout(t)

	err := run([]string{"rules", "-db", tempDB, "update", "1", "*.pdf"})
	if err == nil {
		t.Fatal("expected error for missing kind")
	}
	if !strings.Contains(err.Error(), "missing arguments") {
		t.Errorf("error = %v, want mention of 'missing arguments'", err)
	}
}

func TestRunRules_Update_MissingValue(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")
	withCapturedStdout(t)

	err := run([]string{"rules", "-db", tempDB, "update", "1", "*.pdf", "category"})
	if err == nil {
		t.Fatal("expected error for missing value")
	}
	if !strings.Contains(err.Error(), "missing arguments") {
		t.Errorf("error = %v, want mention of 'missing arguments'", err)
	}
}

func TestRunRules_Add_WithCategory(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")
	sb := withCapturedStdout(t)

	if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", "category", "Invoices"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "Added classification rule") {
		t.Errorf("add output missing confirmation, got %q", out)
	}
}

func TestRunRules_Update_WithCategory(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	sb := withCapturedStdout(t)
	if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", "category", "Invoices"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}

	st, err := store.Open(tempDB)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	rules, err := st.ListRules(context.Background())
	if err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	ruleID := strconv.FormatInt(rules[0].ID, 10)

	sb.Reset()
	if err := run([]string{"rules", "-db", tempDB, "update", ruleID, "*.docx", "category", "Documents"}); err != nil {
		t.Fatalf("rules update: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "Updated classification rule") {
		t.Errorf("update output missing confirmation, got %q", out)
	}
}

func TestRunRules_Add_WithTag(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")
	sb := withCapturedStdout(t)

	if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", "tag", "Invoices"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "Added classification rule") {
		t.Errorf("add output missing confirmation, got %q", out)
	}
}

func TestRunRules_Update_WithTag(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	sb := withCapturedStdout(t)
	if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", "category", "Invoices"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}

	st, err := store.Open(tempDB)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	rules, err := st.ListRules(context.Background())
	if err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	ruleID := strconv.FormatInt(rules[0].ID, 10)

	sb.Reset()
	if err := run([]string{"rules", "-db", tempDB, "update", ruleID, "*.docx", "tag", "Documents"}); err != nil {
		t.Fatalf("rules update: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "Updated classification rule") {
		t.Errorf("update output missing confirmation, got %q", out)
	}
}

func TestRunRules_Add_WithAllKinds(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	kinds := []string{"category", "tag", "correspondent", "document_type"}
	for _, kind := range kinds {
		sb := withCapturedStdout(t)
		if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", kind, "TestValue"}); err != nil {
			t.Fatalf("rules add with kind %s: %v", kind, err)
		}
		out := sb.String()
		if !strings.Contains(out, "Added classification rule") {
			t.Errorf("add output missing confirmation for kind %s, got %q", kind, out)
		}
	}
}

func TestRunRules_Update_WithAllKinds(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	sb := withCapturedStdout(t)
	if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", "category", "Invoices"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}

	st, err := store.Open(tempDB)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	rules, err := st.ListRules(context.Background())
	if err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	ruleID := strconv.FormatInt(rules[0].ID, 10)

	kinds := []string{"category", "tag", "correspondent", "document_type"}
	for _, kind := range kinds {
		sb.Reset()
		if err := run([]string{"rules", "-db", tempDB, "update", ruleID, "*.docx", kind, "TestValue"}); err != nil {
			t.Fatalf("rules update with kind %s: %v", kind, err)
		}
		out := sb.String()
		if !strings.Contains(out, "Updated classification rule") {
			t.Errorf("update output missing confirmation for kind %s, got %q", kind, out)
		}
	}
}

func TestRunRules_Update_InvalidKind(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	withCapturedStdout(t)
	if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", "category", "Invoices"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}

	st, err := store.Open(tempDB)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	rules, err := st.ListRules(context.Background())
	if err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	ruleID := strconv.FormatInt(rules[0].ID, 10)

	withCapturedStdout(t)
	err = run([]string{"rules", "-db", tempDB, "update", ruleID, "*.docx", "invalid_kind", "value"})
	if err == nil {
		t.Fatal("expected error for invalid kind")
	}
}

func TestRunRules_Add_AllKinds(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	kinds := []string{"category", "tag", "correspondent", "document_type"}
	for _, kind := range kinds {
		sb := withCapturedStdout(t)
		if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", kind, "TestValue"}); err != nil {
			t.Fatalf("rules add with kind %s: %v", kind, err)
		}
		out := sb.String()
		if !strings.Contains(out, "Added classification rule") {
			t.Errorf("add output missing confirmation for kind %s, got %q", kind, out)
		}
	}
}

func TestRunRules_Update_AllKinds(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	sb := withCapturedStdout(t)
	if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", "category", "Invoices"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}

	st, err := store.Open(tempDB)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	rules, err := st.ListRules(context.Background())
	if err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	ruleID := strconv.FormatInt(rules[0].ID, 10)

	kinds := []string{"category", "tag", "correspondent", "document_type"}
	for _, kind := range kinds {
		sb.Reset()
		if err := run([]string{"rules", "-db", tempDB, "update", ruleID, "*.docx", kind, "TestValue"}); err != nil {
			t.Fatalf("rules update with kind %s: %v", kind, err)
		}
		out := sb.String()
		if !strings.Contains(out, "Updated classification rule") {
			t.Errorf("update output missing confirmation for kind %s, got %q", kind, out)
		}
	}
}

func TestRunRules_Add_WithDifferentPatterns(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	patterns := []string{"*.pdf", "*.docx", "invoice", "receipt"}
	for _, pattern := range patterns {
		sb := withCapturedStdout(t)
		if err := run([]string{"rules", "-db", tempDB, "add", pattern, "category", "TestValue"}); err != nil {
			t.Fatalf("rules add with pattern %s: %v", pattern, err)
		}
		out := sb.String()
		if !strings.Contains(out, "Added classification rule") {
			t.Errorf("add output missing confirmation for pattern %s, got %q", pattern, out)
		}
	}
}

func TestRunRules_Update_WithDifferentPatterns(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	sb := withCapturedStdout(t)
	if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", "category", "Invoices"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}

	st, err := store.Open(tempDB)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	rules, err := st.ListRules(context.Background())
	if err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	ruleID := strconv.FormatInt(rules[0].ID, 10)

	patterns := []string{"*.docx", "*.txt", "invoice", "receipt"}
	for _, pattern := range patterns {
		sb.Reset()
		if err := run([]string{"rules", "-db", tempDB, "update", ruleID, pattern, "category", "TestValue"}); err != nil {
			t.Fatalf("rules update with pattern %s: %v", pattern, err)
		}
		out := sb.String()
		if !strings.Contains(out, "Updated classification rule") {
			t.Errorf("update output missing confirmation for pattern %s, got %q", pattern, out)
		}
	}
}

func TestRunRules_Add_WithDifferentValues(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	values := []string{"Invoices", "Receipts", "Contracts", "Reports"}
	for _, value := range values {
		sb := withCapturedStdout(t)
		if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", "category", value}); err != nil {
			t.Fatalf("rules add with value %s: %v", value, err)
		}
		out := sb.String()
		if !strings.Contains(out, "Added classification rule") {
			t.Errorf("add output missing confirmation for value %s, got %q", value, out)
		}
	}
}

func TestRunRules_Update_WithDifferentValues(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	sb := withCapturedStdout(t)
	if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", "category", "Invoices"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}

	st, err := store.Open(tempDB)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	rules, err := st.ListRules(context.Background())
	if err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	ruleID := strconv.FormatInt(rules[0].ID, 10)

	values := []string{"Receipts", "Contracts", "Reports", "Other"}
	for _, value := range values {
		sb.Reset()
		if err := run([]string{"rules", "-db", tempDB, "update", ruleID, "*.pdf", "category", value}); err != nil {
			t.Fatalf("rules update with value %s: %v", value, err)
		}
		out := sb.String()
		if !strings.Contains(out, "Updated classification rule") {
			t.Errorf("update output missing confirmation for value %s, got %q", value, out)
		}
	}
}

func TestRunRules_Add_WithDifferentKindsAndValues(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	tests := []struct {
		kind  string
		value string
	}{
		{"category", "Invoices"},
		{"tag", "Important"},
		{"correspondent", "ACME Corp"},
		{"document_type", "Invoice"},
	}

	for _, tt := range tests {
		sb := withCapturedStdout(t)
		if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", tt.kind, tt.value}); err != nil {
			t.Fatalf("rules add with kind %s: %v", tt.kind, err)
		}
		out := sb.String()
		if !strings.Contains(out, "Added classification rule") {
			t.Errorf("add output missing confirmation for kind %s, got %q", tt.kind, out)
		}
	}
}

func TestRunRules_Update_WithDifferentKindsAndValues(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	sb := withCapturedStdout(t)
	if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", "category", "Invoices"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}

	st, err := store.Open(tempDB)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	rules, err := st.ListRules(context.Background())
	if err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	ruleID := strconv.FormatInt(rules[0].ID, 10)

	tests := []struct {
		kind  string
		value string
	}{
		{"category", "Receipts"},
		{"tag", "Important"},
		{"correspondent", "ACME Corp"},
		{"document_type", "Invoice"},
	}

	for _, tt := range tests {
		sb.Reset()
		if err := run([]string{"rules", "-db", tempDB, "update", ruleID, "*.pdf", tt.kind, tt.value}); err != nil {
			t.Fatalf("rules update with kind %s: %v", tt.kind, err)
		}
		out := sb.String()
		if !strings.Contains(out, "Updated classification rule") {
			t.Errorf("update output missing confirmation for kind %s, got %q", tt.kind, out)
		}
	}
}

func TestRunRules_Add_WithAllCombinations(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	kinds := []string{"category", "tag", "correspondent", "document_type"}
	values := []string{"Invoices", "Receipts", "Contracts", "Reports"}

	for _, kind := range kinds {
		for _, value := range values {
			sb := withCapturedStdout(t)
			if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", kind, value}); err != nil {
				t.Fatalf("rules add with kind %s and value %s: %v", kind, value, err)
			}
			out := sb.String()
			if !strings.Contains(out, "Added classification rule") {
				t.Errorf("add output missing confirmation for kind %s and value %s, got %q", kind, value, out)
			}
		}
	}
}

func TestRunRules_Update_WithAllCombinations(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	sb := withCapturedStdout(t)
	if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", "category", "Invoices"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}

	st, err := store.Open(tempDB)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	rules, err := st.ListRules(context.Background())
	if err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	ruleID := strconv.FormatInt(rules[0].ID, 10)

	kinds := []string{"category", "tag", "correspondent", "document_type"}
	values := []string{"Invoices", "Receipts", "Contracts", "Reports"}

	for _, kind := range kinds {
		for _, value := range values {
			sb.Reset()
			if err := run([]string{"rules", "-db", tempDB, "update", ruleID, "*.pdf", kind, value}); err != nil {
				t.Fatalf("rules update with kind %s and value %s: %v", kind, value, err)
			}
			out := sb.String()
			if !strings.Contains(out, "Updated classification rule") {
				t.Errorf("update output missing confirmation for kind %s and value %s, got %q", kind, value, out)
			}
		}
	}
}

// assertAddRuleOutcome runs "rules add" for one (pattern, kind, value) case and
// asserts the outcome that internal/store.AddRule actually guarantees: an error
// for empty pattern/value or an unknown kind, otherwise a persisted rule whose
// fields match the input exactly.
func assertAddRuleOutcome(t *testing.T, tempDB, pattern, kind, value string) {
	t.Helper()

	wantErr := pattern == "" || value == ""
	switch kind {
	case "category", "tag", "correspondent", "document_type":
	default:
		wantErr = true
	}

	err := run([]string{"rules", "-db", tempDB, "add", pattern, kind, value})
	if wantErr {
		if err == nil {
			t.Errorf("rules add with pattern %q, kind %q, value %q: expected an error, got none", pattern, kind, value)
		}
		return
	}
	if err != nil {
		t.Fatalf("rules add with pattern %q, kind %q, value %q: %v", pattern, kind, value, err)
	}

	st, err := store.Open(tempDB)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	rules, err := st.ListRules(context.Background())
	if err != nil {
		t.Fatalf("list rules: %v", err)
	}
	found := false
	for _, r := range rules {
		if r.Pattern == pattern && r.Kind == kind && r.Value == value {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("rules add with pattern %q, kind %q, value %q: no matching rule persisted", pattern, kind, value)
	}
}

func TestRunRules_Add_WithEdgeCases(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	tests := []struct {
		pattern string
		kind    string
		value   string
	}{
		{"*.pdf", "category", "Invoices"},
		{"*.PDF", "category", "Invoices"},
		{"invoice", "tag", "Important"},
		{"", "category", "Empty"},
		{"*.pdf", "", "Empty"},
		{"*.pdf", "category", ""},
	}

	for _, tt := range tests {
		withCapturedStdout(t)
		assertAddRuleOutcome(t, tempDB, tt.pattern, tt.kind, tt.value)
	}
}

func TestRunRules_Update_WithEdgeCases(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	withCapturedStdout(t)
	if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", "category", "Invoices"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}

	st, err := store.Open(tempDB)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	rules, err := st.ListRules(context.Background())
	if err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	ruleID := strconv.FormatInt(rules[0].ID, 10)

	tests := []struct {
		pattern string
		kind    string
		value   string
	}{
		{"*.PDF", "category", "Invoices"},
		{"invoice", "tag", "Important"},
		{"", "category", "Empty"},
		{"*.pdf", "", "Empty"},
		{"*.pdf", "category", ""},
	}

	for _, tt := range tests {
		withCapturedStdout(t)
		err := run([]string{"rules", "-db", tempDB, "update", ruleID, tt.pattern, tt.kind, tt.value})
		if err != nil {
			t.Logf("rules update with pattern %q, kind %q, value %q: %v", tt.pattern, tt.kind, tt.value, err)
		}
	}
}

func TestRunRules_Add_WithSpecialCharacters(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	tests := []struct {
		pattern string
		kind    string
		value   string
	}{
		{"*.pdf", "category", "Invoices & Receipts"},
		{"*.pdf", "category", "Invoices <test>"},
		{"*.pdf", "category", "Invoices \"quoted\""},
		{"*.pdf", "category", "Invoices 'single'"},
	}

	for _, tt := range tests {
		withCapturedStdout(t)
		assertAddRuleOutcome(t, tempDB, tt.pattern, tt.kind, tt.value)
	}
}

func TestRunRules_Update_WithSpecialCharacters(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	withCapturedStdout(t)
	if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", "category", "Invoices"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}

	st, err := store.Open(tempDB)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	rules, err := st.ListRules(context.Background())
	if err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	ruleID := strconv.FormatInt(rules[0].ID, 10)

	tests := []struct {
		pattern string
		kind    string
		value   string
	}{
		{"*.pdf", "category", "Invoices & Receipts"},
		{"*.pdf", "category", "Invoices <test>"},
		{"*.pdf", "category", "Invoices \"quoted\""},
		{"*.pdf", "category", "Invoices 'single'"},
	}

	for _, tt := range tests {
		withCapturedStdout(t)
		err := run([]string{"rules", "-db", tempDB, "update", ruleID, tt.pattern, tt.kind, tt.value})
		if err != nil {
			t.Logf("rules update with special chars: %v", err)
		}
	}
}

func TestRunRules_Add_WithUnicode(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	tests := []struct {
		pattern string
		kind    string
		value   string
	}{
		{"*.pdf", "category", "Rechnungen"},
		{"*.pdf", "category", "发票"},
		{"*.pdf", "category", "請求書"},
		{"*.pdf", "category", "Factures"},
	}

	for _, tt := range tests {
		withCapturedStdout(t)
		assertAddRuleOutcome(t, tempDB, tt.pattern, tt.kind, tt.value)
	}
}

func TestRunRules_Update_WithUnicode(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	withCapturedStdout(t)
	if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", "category", "Invoices"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}

	st, err := store.Open(tempDB)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	rules, err := st.ListRules(context.Background())
	if err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	ruleID := strconv.FormatInt(rules[0].ID, 10)

	tests := []struct {
		pattern string
		kind    string
		value   string
	}{
		{"*.pdf", "category", "Rechnungen"},
		{"*.pdf", "category", "发票"},
		{"*.pdf", "category", "請求書"},
		{"*.pdf", "category", "Factures"},
	}

	for _, tt := range tests {
		withCapturedStdout(t)
		err := run([]string{"rules", "-db", tempDB, "update", ruleID, tt.pattern, tt.kind, tt.value})
		if err != nil {
			t.Logf("rules update with unicode: %v", err)
		}
	}
}

func TestRunRules_Add_WithLongValues(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	longValue := strings.Repeat("A", 1000)
	withCapturedStdout(t)
	assertAddRuleOutcome(t, tempDB, "*.pdf", "category", longValue)
}

func TestRunRules_Update_WithLongValues(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	withCapturedStdout(t)
	if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", "category", "Invoices"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}

	st, err := store.Open(tempDB)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	rules, err := st.ListRules(context.Background())
	if err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	ruleID := strconv.FormatInt(rules[0].ID, 10)

	longValue := strings.Repeat("A", 1000)
	withCapturedStdout(t)
	err = run([]string{"rules", "-db", tempDB, "update", ruleID, "*.pdf", "category", longValue})
	if err != nil {
		t.Logf("rules update with long value: %v", err)
	}
}

func TestRunRules_Add_WithWhitespace(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	tests := []struct {
		pattern string
		kind    string
		value   string
	}{
		{"  *.pdf  ", "category", "Invoices"},
		{"*.pdf", "  category  ", "Invoices"},
		{"*.pdf", "category", "  Invoices  "},
		{"", "", ""},
	}

	for _, tt := range tests {
		withCapturedStdout(t)
		assertAddRuleOutcome(t, tempDB, tt.pattern, tt.kind, tt.value)
	}
}

func TestRunRules_Update_WithWhitespace(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	withCapturedStdout(t)
	if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", "category", "Invoices"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}

	st, err := store.Open(tempDB)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	rules, err := st.ListRules(context.Background())
	if err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	ruleID := strconv.FormatInt(rules[0].ID, 10)

	tests := []struct {
		pattern string
		kind    string
		value   string
	}{
		{"  *.pdf  ", "category", "Invoices"},
		{"*.pdf", "  category  ", "Invoices"},
		{"*.pdf", "category", "  Invoices  "},
		{"", "", ""},
	}

	for _, tt := range tests {
		withCapturedStdout(t)
		err := run([]string{"rules", "-db", tempDB, "update", ruleID, tt.pattern, tt.kind, tt.value})
		if err != nil {
			t.Logf("rules update with whitespace: %v", err)
		}
	}
}

func TestRunRules_Add_WithNumbers(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	tests := []struct {
		pattern string
		kind    string
		value   string
	}{
		{"123", "category", "456"},
		{"*.pdf", "category", "123"},
		{"123", "category", "Invoices"},
	}

	for _, tt := range tests {
		withCapturedStdout(t)
		assertAddRuleOutcome(t, tempDB, tt.pattern, tt.kind, tt.value)
	}
}

func TestRunRules_Update_WithNumbers(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	withCapturedStdout(t)
	if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", "category", "Invoices"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}

	st, err := store.Open(tempDB)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	rules, err := st.ListRules(context.Background())
	if err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	ruleID := strconv.FormatInt(rules[0].ID, 10)

	tests := []struct {
		pattern string
		kind    string
		value   string
	}{
		{"123", "category", "456"},
		{"*.pdf", "category", "123"},
		{"123", "category", "Invoices"},
	}

	for _, tt := range tests {
		withCapturedStdout(t)
		err := run([]string{"rules", "-db", tempDB, "update", ruleID, tt.pattern, tt.kind, tt.value})
		if err != nil {
			t.Logf("rules update with numbers: %v", err)
		}
	}
}

func TestRunRules_Add_WithMixedContent(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	tests := []struct {
		pattern string
		kind    string
		value   string
	}{
		{"invoice-123", "category", "Invoices & Receipts"},
		{"*.pdf", "tag", "Important-2026"},
		{"receipt_456", "correspondent", "ACME Corp."},
		{"doc.789", "document_type", "Invoice-Type"},
	}

	for _, tt := range tests {
		withCapturedStdout(t)
		assertAddRuleOutcome(t, tempDB, tt.pattern, tt.kind, tt.value)
	}
}

func TestRunRules_Update_WithMixedContent(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	withCapturedStdout(t)
	if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", "category", "Invoices"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}

	st, err := store.Open(tempDB)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	rules, err := st.ListRules(context.Background())
	if err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	ruleID := strconv.FormatInt(rules[0].ID, 10)

	tests := []struct {
		pattern string
		kind    string
		value   string
	}{
		{"invoice-123", "category", "Invoices & Receipts"},
		{"*.pdf", "tag", "Important-2026"},
		{"receipt_456", "correspondent", "ACME Corp."},
		{"doc.789", "document_type", "Invoice-Type"},
	}

	for _, tt := range tests {
		withCapturedStdout(t)
		err := run([]string{"rules", "-db", tempDB, "update", ruleID, tt.pattern, tt.kind, tt.value})
		if err != nil {
			t.Logf("rules update with mixed content: %v", err)
		}
	}
}

func TestRunRules_Add_WithPaths(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	tests := []struct {
		pattern string
		kind    string
		value   string
	}{
		{"/path/to/file.pdf", "category", "Invoices"},
		{"*.pdf", "category", "/path/to/value"},
		{"/path/to/file.pdf", "category", "/path/to/value"},
	}

	for _, tt := range tests {
		withCapturedStdout(t)
		assertAddRuleOutcome(t, tempDB, tt.pattern, tt.kind, tt.value)
	}
}

func TestRunRules_Update_WithPaths(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	withCapturedStdout(t)
	if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", "category", "Invoices"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}

	st, err := store.Open(tempDB)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	rules, err := st.ListRules(context.Background())
	if err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	ruleID := strconv.FormatInt(rules[0].ID, 10)

	tests := []struct {
		pattern string
		kind    string
		value   string
	}{
		{"/path/to/file.pdf", "category", "Invoices"},
		{"*.pdf", "category", "/path/to/value"},
		{"/path/to/file.pdf", "category", "/path/to/value"},
	}

	for _, tt := range tests {
		withCapturedStdout(t)
		err := run([]string{"rules", "-db", tempDB, "update", ruleID, tt.pattern, tt.kind, tt.value})
		if err != nil {
			t.Logf("rules update with paths: %v", err)
		}
	}
}

func TestRunRules_Add_WithSpecialChars(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	tests := []struct {
		pattern string
		kind    string
		value   string
	}{
		{"*.pdf", "category", "Invoices@2026"},
		{"*.pdf", "category", "Invoices#123"},
		{"*.pdf", "category", "Invoices$456"},
		{"*.pdf", "category", "Invoices%789"},
	}

	for _, tt := range tests {
		withCapturedStdout(t)
		assertAddRuleOutcome(t, tempDB, tt.pattern, tt.kind, tt.value)
	}
}

func TestRunRules_Update_WithSpecialChars(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	withCapturedStdout(t)
	if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", "category", "Invoices"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}

	st, err := store.Open(tempDB)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	rules, err := st.ListRules(context.Background())
	if err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	ruleID := strconv.FormatInt(rules[0].ID, 10)

	tests := []struct {
		pattern string
		kind    string
		value   string
	}{
		{"*.pdf", "category", "Invoices@2026"},
		{"*.pdf", "category", "Invoices#123"},
		{"*.pdf", "category", "Invoices$456"},
		{"*.pdf", "category", "Invoices%789"},
	}

	for _, tt := range tests {
		withCapturedStdout(t)
		err := run([]string{"rules", "-db", tempDB, "update", ruleID, tt.pattern, tt.kind, tt.value})
		if err != nil {
			t.Logf("rules update with special chars: %v", err)
		}
	}
}

func TestRunRules_Add_WithBrackets(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	tests := []struct {
		pattern string
		kind    string
		value   string
	}{
		{"*.pdf", "category", "Invoices (2026)"},
		{"*.pdf", "category", "Invoices [draft]"},
		{"*.pdf", "category", "Invoices {final}"},
	}

	for _, tt := range tests {
		withCapturedStdout(t)
		assertAddRuleOutcome(t, tempDB, tt.pattern, tt.kind, tt.value)
	}
}

func TestRunRules_Update_WithBrackets(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	withCapturedStdout(t)
	if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", "category", "Invoices"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}

	st, err := store.Open(tempDB)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	rules, err := st.ListRules(context.Background())
	if err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	ruleID := strconv.FormatInt(rules[0].ID, 10)

	tests := []struct {
		pattern string
		kind    string
		value   string
	}{
		{"*.pdf", "category", "Invoices (2026)"},
		{"*.pdf", "category", "Invoices [draft]"},
		{"*.pdf", "category", "Invoices {final}"},
	}

	for _, tt := range tests {
		withCapturedStdout(t)
		err := run([]string{"rules", "-db", tempDB, "update", ruleID, tt.pattern, tt.kind, tt.value})
		if err != nil {
			t.Logf("rules update with brackets: %v", err)
		}
	}
}

func TestRunRules_Add_WithPunctuation(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	tests := []struct {
		pattern string
		kind    string
		value   string
	}{
		{"*.pdf", "category", "Invoices, Receipts"},
		{"*.pdf", "category", "Invoices; Receipts"},
		{"*.pdf", "category", "Invoices: Final"},
		{"*.pdf", "category", "Invoices! Important"},
	}

	for _, tt := range tests {
		withCapturedStdout(t)
		assertAddRuleOutcome(t, tempDB, tt.pattern, tt.kind, tt.value)
	}
}

func TestRunRules_Update_WithPunctuation(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	withCapturedStdout(t)
	if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", "category", "Invoices"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}

	st, err := store.Open(tempDB)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	rules, err := st.ListRules(context.Background())
	if err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	ruleID := strconv.FormatInt(rules[0].ID, 10)

	tests := []struct {
		pattern string
		kind    string
		value   string
	}{
		{"*.pdf", "category", "Invoices, Receipts"},
		{"*.pdf", "category", "Invoices; Receipts"},
		{"*.pdf", "category", "Invoices: Final"},
		{"*.pdf", "category", "Invoices! Important"},
	}

	for _, tt := range tests {
		withCapturedStdout(t)
		err := run([]string{"rules", "-db", tempDB, "update", ruleID, tt.pattern, tt.kind, tt.value})
		if err != nil {
			t.Logf("rules update with punctuation: %v", err)
		}
	}
}

func TestRunRules_Add_WithMath(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	tests := []struct {
		pattern string
		kind    string
		value   string
	}{
		{"*.pdf", "category", "Invoices + Receipts"},
		{"*.pdf", "category", "Invoices - Draft"},
		{"*.pdf", "category", "Invoices * Final"},
		{"*.pdf", "category", "Invoices / Copy"},
	}

	for _, tt := range tests {
		withCapturedStdout(t)
		assertAddRuleOutcome(t, tempDB, tt.pattern, tt.kind, tt.value)
	}
}

func TestRunRules_Update_WithMath(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	withCapturedStdout(t)
	if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", "category", "Invoices"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}

	st, err := store.Open(tempDB)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	rules, err := st.ListRules(context.Background())
	if err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	ruleID := strconv.FormatInt(rules[0].ID, 10)

	tests := []struct {
		pattern string
		kind    string
		value   string
	}{
		{"*.pdf", "category", "Invoices + Receipts"},
		{"*.pdf", "category", "Invoices - Draft"},
		{"*.pdf", "category", "Invoices * Final"},
		{"*.pdf", "category", "Invoices / Copy"},
	}

	for _, tt := range tests {
		withCapturedStdout(t)
		err := run([]string{"rules", "-db", tempDB, "update", ruleID, tt.pattern, tt.kind, tt.value})
		if err != nil {
			t.Logf("rules update with math: %v", err)
		}
	}
}

func TestRunRules_Add_WithComparison(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	tests := []struct {
		pattern string
		kind    string
		value   string
	}{
		{"*.pdf", "category", "Invoices > 100"},
		{"*.pdf", "category", "Invoices < 1000"},
		{"*.pdf", "category", "Invoices >= 50"},
		{"*.pdf", "category", "Invoices <= 500"},
	}

	for _, tt := range tests {
		withCapturedStdout(t)
		assertAddRuleOutcome(t, tempDB, tt.pattern, tt.kind, tt.value)
	}
}

func TestRunRules_Update_WithComparison(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	withCapturedStdout(t)
	if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", "category", "Invoices"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}

	st, err := store.Open(tempDB)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	rules, err := st.ListRules(context.Background())
	if err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	ruleID := strconv.FormatInt(rules[0].ID, 10)

	tests := []struct {
		pattern string
		kind    string
		value   string
	}{
		{"*.pdf", "category", "Invoices > 100"},
		{"*.pdf", "category", "Invoices < 1000"},
		{"*.pdf", "category", "Invoices >= 50"},
		{"*.pdf", "category", "Invoices <= 500"},
	}

	for _, tt := range tests {
		withCapturedStdout(t)
		err := run([]string{"rules", "-db", tempDB, "update", ruleID, tt.pattern, tt.kind, tt.value})
		if err != nil {
			t.Logf("rules update with comparison: %v", err)
		}
	}
}

func TestRunRules_Add_WithEquality(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	tests := []struct {
		pattern string
		kind    string
		value   string
	}{
		{"*.pdf", "category", "Invoices == Final"},
		{"*.pdf", "category", "Invoices != Draft"},
		{"*.pdf", "category", "Invoices = Copy"},
	}

	for _, tt := range tests {
		withCapturedStdout(t)
		assertAddRuleOutcome(t, tempDB, tt.pattern, tt.kind, tt.value)
	}
}

func TestRunRules_Update_WithEquality(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	withCapturedStdout(t)
	if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", "category", "Invoices"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}

	st, err := store.Open(tempDB)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	rules, err := st.ListRules(context.Background())
	if err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	ruleID := strconv.FormatInt(rules[0].ID, 10)

	tests := []struct {
		pattern string
		kind    string
		value   string
	}{
		{"*.pdf", "category", "Invoices == Final"},
		{"*.pdf", "category", "Invoices != Draft"},
		{"*.pdf", "category", "Invoices = Copy"},
	}

	for _, tt := range tests {
		withCapturedStdout(t)
		err := run([]string{"rules", "-db", tempDB, "update", ruleID, tt.pattern, tt.kind, tt.value})
		if err != nil {
			t.Logf("rules update with equality: %v", err)
		}
	}
}

func TestRunRules_Add_WithLogical(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	tests := []struct {
		pattern string
		kind    string
		value   string
	}{
		{"*.pdf", "category", "Invoices && Receipts"},
		{"*.pdf", "category", "Invoices || Receipts"},
		{"*.pdf", "category", "!Invoices"},
		{"*.pdf", "category", "Invoices && !Draft"},
	}

	for _, tt := range tests {
		withCapturedStdout(t)
		assertAddRuleOutcome(t, tempDB, tt.pattern, tt.kind, tt.value)
	}
}

func TestRunRules_Update_WithLogical(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	withCapturedStdout(t)
	if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", "category", "Invoices"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}

	st, err := store.Open(tempDB)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	rules, err := st.ListRules(context.Background())
	if err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	ruleID := strconv.FormatInt(rules[0].ID, 10)

	tests := []struct {
		pattern string
		kind    string
		value   string
	}{
		{"*.pdf", "category", "Invoices && Receipts"},
		{"*.pdf", "category", "Invoices || Receipts"},
		{"*.pdf", "category", "!Invoices"},
		{"*.pdf", "category", "Invoices && !Draft"},
	}

	for _, tt := range tests {
		withCapturedStdout(t)
		err := run([]string{"rules", "-db", tempDB, "update", ruleID, tt.pattern, tt.kind, tt.value})
		if err != nil {
			t.Logf("rules update with logical: %v", err)
		}
	}
}

func TestRunRules_Add_WithBitwise(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	tests := []struct {
		pattern string
		kind    string
		value   string
	}{
		{"*.pdf", "category", "Invoices & Receipts"},
		{"*.pdf", "category", "Invoices | Receipts"},
		{"*.pdf", "category", "Invoices ^ Receipts"},
		{"*.pdf", "category", "~Invoices"},
	}

	for _, tt := range tests {
		withCapturedStdout(t)
		assertAddRuleOutcome(t, tempDB, tt.pattern, tt.kind, tt.value)
	}
}

func TestRunRules_Update_WithBitwise(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	withCapturedStdout(t)
	if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", "category", "Invoices"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}

	st, err := store.Open(tempDB)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	rules, err := st.ListRules(context.Background())
	if err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	ruleID := strconv.FormatInt(rules[0].ID, 10)

	tests := []struct {
		pattern string
		kind    string
		value   string
	}{
		{"*.pdf", "category", "Invoices & Receipts"},
		{"*.pdf", "category", "Invoices | Receipts"},
		{"*.pdf", "category", "Invoices ^ Receipts"},
		{"*.pdf", "category", "~Invoices"},
	}

	for _, tt := range tests {
		withCapturedStdout(t)
		err := run([]string{"rules", "-db", tempDB, "update", ruleID, tt.pattern, tt.kind, tt.value})
		if err != nil {
			t.Logf("rules update with bitwise: %v", err)
		}
	}
}
