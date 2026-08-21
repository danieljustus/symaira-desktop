package vaultreview

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testNote(t *testing.T, vault, name, archive, sum string, paperlessID int, tags []string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("source_path: paperless://documents/")
	b.WriteString(strings.TrimSuffix(name, ".md"))
	b.WriteString("\ningested_at: 2026-07-03T10:00:00Z\n")
	b.WriteString("sha256: ")
	b.WriteString(sum)
	b.WriteString("\nmime: text/plain\n")
	b.WriteString("archive_path: ")
	b.WriteString(archive)
	b.WriteString("\ntags:\n")
	for _, tag := range tags {
		b.WriteString("  - ")
		b.WriteString(tag)
		b.WriteByte('\n')
	}
	b.WriteString("paperless:\n  document_id: ")
	b.WriteString(string(rune('0' + paperlessID)))
	b.WriteString("\n---\nbody\n")
	path := filepath.Join(vault, name)
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func archiveFile(t *testing.T, dir, name, content string) (string, string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(content))
	return path, hex.EncodeToString(sum[:])
}

func hasFailure(r *ValidationReport, check string) bool {
	for _, f := range r.Failures {
		if f.Check == check {
			return true
		}
	}
	return false
}

func TestValidateVaultReportsArchiveBadYAMLAndDuplicatePaperlessID(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	archive := filepath.Join(dir, "archive")
	if err := os.MkdirAll(vault, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(archive, 0o700); err != nil {
		t.Fatal(err)
	}
	archivePath, sum := archiveFile(t, archive, "one.txt", "hello")
	testNote(t, vault, "one.md", archivePath, sum, 1, []string{"inbox"})
	testNote(t, vault, "dupe.md", archivePath, sum, 1, []string{"review"})
	testNote(t, vault, "missing-archive.md", filepath.Join(archive, "missing.txt"), sum, 2, []string{"review"})
	if err := os.WriteFile(filepath.Join(vault, "bad.md"), []byte("---\n: bad: yaml\n---\nbody"), 0o600); err != nil {
		t.Fatal(err)
	}

	report, err := ValidateVault(vault)
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []string{"archive.exists", "frontmatter", "paperless.document_id.unique"} {
		if !hasFailure(report, check) {
			t.Fatalf("missing failure %s in %+v", check, report.Failures)
		}
	}
}

func TestValidateVaultWithOptionsFlagsShortBodies(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	archive := filepath.Join(dir, "archive")
	if err := os.MkdirAll(vault, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(archive, 0o700); err != nil {
		t.Fatal(err)
	}
	archivePath, sum := archiveFile(t, archive, "one.txt", "hello")
	testNote(t, vault, "one.md", archivePath, sum, 1, []string{"inbox"})

	report, err := ValidateVaultWithOptions(vault, ValidationOptions{MinBodyLength: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !hasFailure(report, "body.min_length") {
		t.Fatalf("missing body.min_length failure: %+v", report.Failures)
	}

	report, err = ValidateVaultWithOptions(vault, ValidationOptions{MinBodyLength: 1})
	if err != nil {
		t.Fatal(err)
	}
	if hasFailure(report, "body.min_length") {
		t.Fatalf("unexpected body.min_length failure: %+v", report.Failures)
	}
}

func TestApplyCorrectionAndBulkUpdateProtectInbox(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	archive := filepath.Join(dir, "archive")
	if err := os.MkdirAll(vault, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(archive, 0o700); err != nil {
		t.Fatal(err)
	}
	archivePath, sum := archiveFile(t, archive, "one.txt", "hello")
	note := testNote(t, vault, "one.md", archivePath, sum, 1, []string{"inbox", "old"})
	testNote(t, vault, "two.md", archivePath, sum, 2, []string{"old"})

	if _, err := ApplyCorrection(vault, Correction{PaperlessID: 1, RemoveTags: []string{"inbox"}}, true); err == nil {
		t.Fatal("expected inbox removal to be refused")
	}
	corr := "Acme"
	res, err := ApplyCorrection(vault, Correction{PaperlessID: 1, AddTags: []string{"new"}, Correspondent: &corr}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.DryRun || res.Written {
		t.Fatalf("dry run result wrong: %+v", res)
	}
	data, _ := os.ReadFile(note)
	if strings.Contains(string(data), "new") {
		t.Fatal("dry run wrote changes")
	}
	backupDir := filepath.Join(dir, "undo")
	res, err = ApplyCorrectionWithOptions(vault, Correction{PaperlessID: 1, AddTags: []string{"new"}, Correspondent: &corr}, ApplyOptions{BackupDir: backupDir})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Written || res.BackupPath == "" {
		t.Fatalf("expected write with backup: %+v", res)
	}
	if _, err := os.Stat(res.BackupPath); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	data, _ = os.ReadFile(note)
	if !strings.Contains(string(data), "new") || !strings.Contains(string(data), "Acme") {
		t.Fatalf("update missing changes:\n%s", data)
	}

	results, err := BulkUpdateByTagWithOptions(vault, "old", Correction{AddTags: []string{"bulk"}}, BulkUpdateOptions{DryRun: true, RequireCount: 2, Max: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("bulk matched %d, want 2", len(results))
	}
	if _, err := BulkUpdateByTagWithOptions(vault, "old", Correction{AddTags: []string{"bulk"}}, BulkUpdateOptions{DryRun: true, RequireCount: 1}); err == nil {
		t.Fatal("expected require-count mismatch to fail")
	}
	if _, err := BulkUpdateByTagWithOptions(vault, "old", Correction{AddTags: []string{"bulk"}}, BulkUpdateOptions{DryRun: true, Max: 1}); err == nil {
		t.Fatal("expected max safety gate to fail")
	}
}

func TestApplyCorrectionsFileSchemaAndSafety(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	archive := filepath.Join(dir, "archive")
	if err := os.MkdirAll(vault, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(archive, 0o700); err != nil {
		t.Fatal(err)
	}
	archivePath, sum := archiveFile(t, archive, "one.txt", "hello")
	testNote(t, vault, "one.md", archivePath, sum, 1, []string{"old"})
	path := filepath.Join(dir, "corrections.yaml")
	data := []byte("schema_version: 1\ncorrections:\n  - paperless_id: 1\n    add_tags: [reviewed]\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	results, err := ApplyCorrectionsFileWithOptions(vault, path, ApplyOptions{DryRun: true, RequireCount: 1, Max: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].DryRun || results[0].PaperlessID != 1 {
		t.Fatalf("unexpected results: %+v", results)
	}
	if _, err := ApplyCorrectionsFileWithOptions(vault, path, ApplyOptions{DryRun: true, RequireCount: 2}); err == nil {
		t.Fatal("expected require-count mismatch")
	}
}

func TestBuildReviewReportAndHTML(t *testing.T) {
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "migration.json")
	htmlPath := filepath.Join(dir, "review.html")
	data := `{"schema_version":1,"run_id":"r1","unsupported_file_types":{"nef":1},"unresolved_tag_ids":[99],"documents":[{"id":1,"status":"imported","mime":"text/plain","vault_path":"v","archive_path":"a"},{"id":2,"status":"failed","expected_extension":".nef","error":"unsupported extraction format","warnings":["body.min_length below threshold","unresolved tag ID 99"]}]}`
	if err := os.WriteFile(jsonPath, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := BuildReviewReport(jsonPath, ReviewFilters{Unsupported: true})
	if err != nil {
		t.Fatal(err)
	}
	if report.SourceKind != "migration" || report.Total != 1 || report.Documents[0].ID != 2 || len(report.Findings) != 1 {
		t.Fatalf("unexpected unsupported report: %+v", report)
	}
	report, err = BuildReviewReport(jsonPath, ReviewFilters{LowBody: true})
	if err != nil {
		t.Fatal(err)
	}
	if report.Total != 1 || report.Documents[0].ID != 2 {
		t.Fatalf("unexpected low-body report: %+v", report)
	}
	if err := WriteReviewHTML(htmlPath, report); err != nil {
		t.Fatal(err)
	}
	html, _ := os.ReadFile(htmlPath)
	if !strings.Contains(string(html), "body.min_length") || !strings.Contains(string(html), "Document body text is intentionally not included") || strings.Contains(string(html), "SECRET-BODY") {
		t.Fatalf("bad html: %s", html)
	}
}

func TestBuildReviewReportFromVerifyDuplicateContent(t *testing.T) {
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "verify.json")
	data := `{"schema_version":1,"run_id":"v1","source_documents":2,"vault_notes":2,"verified":2,"duplicate_content":[2],"mismatches":[{"document_id":1,"field":"tags","expected":"a","got":"b"}]}`
	if err := os.WriteFile(jsonPath, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := BuildReviewReport(jsonPath, ReviewFilters{DuplicateContent: true})
	if err != nil {
		t.Fatal(err)
	}
	if report.SourceKind != "verify" || report.Total != 1 || report.Documents[0].ID != 2 || report.Documents[0].Status != "duplicate_content" {
		t.Fatalf("unexpected duplicate-content report: %+v", report)
	}
}

func TestRemoveTag(t *testing.T) {
	tests := []struct {
		name string
		tags []string
		tag  string
		want []string
	}{
		{"empty", nil, "foo", nil},
		{"no_match", []string{"a", "b"}, "c", []string{"a", "b"}},
		{"match", []string{"a", "b", "c"}, "b", []string{"a", "c"}},
		{"case_insensitive", []string{"A", "b"}, "a", []string{"b"}},
		{"all_removed", []string{"a", "a"}, "a", []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := removeTag(tt.tags, tt.tag)
			if len(got) != len(tt.want) {
				t.Fatalf("removeTag(%v, %q) = %v, want %v", tt.tags, tt.tag, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("removeTag(%v, %q)[%d] = %q, want %q", tt.tags, tt.tag, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestParseCorrections_Versioned(t *testing.T) {
	data := []byte(`schema_version: 1
corrections:
  - paperless_id: 1
    add_tags: [reviewed]
`)
	corrections, err := ParseCorrections(data)
	if err != nil {
		t.Fatalf("ParseCorrections: %v", err)
	}
	if len(corrections) != 1 {
		t.Fatalf("len(corrections) = %d, want 1", len(corrections))
	}
	if corrections[0].PaperlessID != 1 {
		t.Errorf("PaperlessID = %d, want 1", corrections[0].PaperlessID)
	}
	if len(corrections[0].AddTags) != 1 || corrections[0].AddTags[0] != "reviewed" {
		t.Errorf("AddTags = %v, want [reviewed]", corrections[0].AddTags)
	}
}

func TestParseCorrections_Legacy(t *testing.T) {
	data := []byte(`- paperless_id: 2
  correspondent: Test
`)
	corrections, err := ParseCorrections(data)
	if err != nil {
		t.Fatalf("ParseCorrections: %v", err)
	}
	if len(corrections) != 1 {
		t.Fatalf("len(corrections) = %d, want 1", len(corrections))
	}
	if corrections[0].PaperlessID != 2 {
		t.Errorf("PaperlessID = %d, want 2", corrections[0].PaperlessID)
	}
}

func TestParseCorrections_InvalidYAML(t *testing.T) {
	data := []byte(`{invalid yaml`)
	_, err := ParseCorrections(data)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestParseCorrections_WrongSchemaVersion(t *testing.T) {
	data := []byte(`schema_version: 99
corrections: []
`)
	_, err := ParseCorrections(data)
	if err == nil {
		t.Fatal("expected error for wrong schema version")
	}
	if !strings.Contains(err.Error(), "schema_version") {
		t.Errorf("error = %v, want mention of schema_version", err)
	}
}
