package vaultreview

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-ingest/internal/paperlessimport"
	"github.com/danieljustus/symaira-ingest/internal/symseek"
)

func TestValidateRequired_MissingKey(t *testing.T) {
	report := &ValidationReport{}
	meta := map[string]any{"source_path": "/tmp/doc.pdf", "sha256": "abc"}
	validateRequired(report, "note.md", meta)
	if len(report.Failures) != 2 {
		t.Fatalf("expected 2 failures, got %d: %v", len(report.Failures), report.Failures)
	}
}

func TestValidateRequired_EmptyValue(t *testing.T) {
	report := &ValidationReport{}
	meta := map[string]any{"source_path": "  ", "ingested_at": "2024-01-01", "sha256": "abc", "mime": "text/plain"}
	validateRequired(report, "note.md", meta)
	if len(report.Failures) != 1 {
		t.Fatalf("expected 1 failure for empty source_path, got %d: %v", len(report.Failures), report.Failures)
	}
	if report.Failures[0].Check != "required.source_path" {
		t.Errorf("check = %q, want required.source_path", report.Failures[0].Check)
	}
}

func TestValidateRequired_AllPresent(t *testing.T) {
	report := &ValidationReport{}
	meta := map[string]any{"source_path": "/tmp/doc.pdf", "ingested_at": "2024-01-01", "sha256": "abc", "mime": "text/plain"}
	validateRequired(report, "note.md", meta)
	if len(report.Failures) != 0 {
		t.Fatalf("expected 0 failures, got %d", len(report.Failures))
	}
}

func TestValidateSafeTypes_TagsNotList(t *testing.T) {
	report := &ValidationReport{}
	meta := map[string]any{"tags": "not-a-list"}
	validateSafeTypes(report, "note.md", meta)
	if len(report.Failures) != 1 {
		t.Fatalf("expected 1 failure, got %d: %v", len(report.Failures), report.Failures)
	}
	if report.Failures[0].Check != "tags.type" {
		t.Errorf("check = %q", report.Failures[0].Check)
	}
}

func TestValidateSafeTypes_TagsWithNonString(t *testing.T) {
	report := &ValidationReport{}
	meta := map[string]any{"tags": []any{"valid", 42}}
	validateSafeTypes(report, "note.md", meta)
	if len(report.Failures) != 1 {
		t.Fatalf("expected 1 failure, got %d: %v", len(report.Failures), report.Failures)
	}
}

func TestValidateSafeTypes_PaperlessNotMap(t *testing.T) {
	report := &ValidationReport{}
	meta := map[string]any{"paperless": "not-a-map"}
	validateSafeTypes(report, "note.md", meta)
	if len(report.Failures) != 1 {
		t.Fatalf("expected 1 failure, got %d: %v", len(report.Failures), report.Failures)
	}
	if report.Failures[0].Check != "paperless.type" {
		t.Errorf("check = %q", report.Failures[0].Check)
	}
}

func TestValidateSafeTypes_DocumentIDNotNumeric(t *testing.T) {
	report := &ValidationReport{}
	meta := map[string]any{"paperless": map[string]any{"document_id": "not-numeric"}}
	validateSafeTypes(report, "note.md", meta)
	if len(report.Failures) != 1 {
		t.Fatalf("expected 1 failure, got %d: %v", len(report.Failures), report.Failures)
	}
	if report.Failures[0].Check != "paperless.document_id.type" {
		t.Errorf("check = %q", report.Failures[0].Check)
	}
}

func TestValidateSafeTypes_ValidTypes(t *testing.T) {
	report := &ValidationReport{}
	meta := map[string]any{
		"tags":       []any{"a", "b"},
		"paperless":  map[string]any{"document_id": 42},
	}
	validateSafeTypes(report, "note.md", meta)
	if len(report.Failures) != 0 {
		t.Fatalf("expected 0 failures, got %d: %v", len(report.Failures), report.Failures)
	}
}

func TestAddVerifyFindings_IncludeAll(t *testing.T) {
	report := &ReviewReport{}
	v := paperlessimport.VerifyReport{
		Missing:           []int{1, 2},
		Duplicate:         []int{3},
		MissingArchive:    []int{4},
		HashMismatch:      []int{5},
		SourceHashMismatch: []int{6},
		DuplicateContent:  []int{7},
	}
	addVerifyFindings(report, v, ReviewFilters{})
	if len(report.Findings) != 7 {
		t.Fatalf("expected 7 findings, got %d", len(report.Findings))
	}
	if len(report.Documents) != 7 {
		t.Fatalf("expected 7 documents, got %d", len(report.Documents))
	}
}

func TestAddVerifyFindings_WithFilters(t *testing.T) {
	report := &ReviewReport{}
	v := paperlessimport.VerifyReport{
		Missing:          []int{1},
		DuplicateContent: []int{2},
	}
	addVerifyFindings(report, v, ReviewFilters{DuplicateContent: true})
	if len(report.Findings) != 1 {
		t.Fatalf("expected 1 finding (duplicate_content only), got %d: %v", len(report.Findings), report.Findings)
	}
	if report.Findings[0].Kind != "duplicate_content" {
		t.Errorf("kind = %q, want duplicate_content", report.Findings[0].Kind)
	}
}

func TestAddVerifyFindings_Mismatches(t *testing.T) {
	report := &ReviewReport{}
	v := paperlessimport.VerifyReport{
		Mismatches: []paperlessimport.VerifyMismatch{
			{DocumentID: 10, Field: "tags", Expected: "a", Got: "b"},
		},
	}
	addVerifyFindings(report, v, ReviewFilters{MissingMetadata: true})
	found := false
	for _, f := range report.Findings {
		if f.Kind == "metadata_mismatch" && f.ID == 10 {
			found = true
		}
	}
	if !found {
		t.Error("expected metadata_mismatch finding for document 10")
	}
}

func TestCheckDryRunEvidence_NilReport(t *testing.T) {
	r := &CutoverReport{}
	checkDryRunEvidence(r, nil, 100)
	if len(r.Checks) != 0 {
		t.Fatalf("expected no checks for nil report, got %d", len(r.Checks))
	}
}

func TestCheckDryRunEvidence_WrongMode(t *testing.T) {
	r := &CutoverReport{}
	dryRun := &paperlessimport.MigrationReport{DryRun: false, Mode: "real", Total: 200}
	checkDryRunEvidence(r, dryRun, 100)
	if len(r.Checks) != 1 || r.Checks[0].Status != CutoverStatusFail {
		t.Fatalf("expected fail for wrong mode, got %v", r.Checks)
	}
}

func TestCheckDryRunEvidence_BelowMinimum(t *testing.T) {
	r := &CutoverReport{}
	dryRun := &paperlessimport.MigrationReport{DryRun: true, Total: 50}
	checkDryRunEvidence(r, dryRun, 100)
	if len(r.Checks) != 1 || r.Checks[0].Status != CutoverStatusFail {
		t.Fatalf("expected fail for below minimum, got %v", r.Checks)
	}
}

func TestCheckDryRunEvidence_HasFailures(t *testing.T) {
	r := &CutoverReport{}
	dryRun := &paperlessimport.MigrationReport{DryRun: true, Total: 200, Failed: 5}
	checkDryRunEvidence(r, dryRun, 100)
	if len(r.Checks) != 1 || r.Checks[0].Status != CutoverStatusFail {
		t.Fatalf("expected fail for failed docs, got %v", r.Checks)
	}
}

func TestCheckDryRunEvidence_UnsupportedTypes(t *testing.T) {
	r := &CutoverReport{}
	dryRun := &paperlessimport.MigrationReport{
		DryRun:               true,
		Total:                200,
		UnsupportedFileTypes: map[string]int{".doc": 10},
	}
	checkDryRunEvidence(r, dryRun, 100)
	if len(r.Checks) != 1 || r.Checks[0].Status != CutoverStatusFail {
		t.Fatalf("expected fail for unsupported types, got %v", r.Checks)
	}
}

func TestCheckImportEvidence_NilReport(t *testing.T) {
	r := &CutoverReport{}
	checkImportEvidence(r, nil, 100)
	if len(r.Checks) != 0 {
		t.Fatalf("expected no checks for nil report, got %d", len(r.Checks))
	}
}

func TestCheckImportEvidence_DryRunMode(t *testing.T) {
	r := &CutoverReport{}
	imp := &paperlessimport.MigrationReport{DryRun: true, Mode: "dry-run", Total: 200}
	checkImportEvidence(r, imp, 100)
	if len(r.Checks) != 1 || r.Checks[0].Status != CutoverStatusFail {
		t.Fatalf("expected fail for dry-run mode, got %v", r.Checks)
	}
}

func TestCheckImportEvidence_BelowMinimum(t *testing.T) {
	r := &CutoverReport{}
	imp := &paperlessimport.MigrationReport{Total: 50, Imported: 50}
	checkImportEvidence(r, imp, 100)
	if len(r.Checks) != 1 || r.Checks[0].Status != CutoverStatusFail {
		t.Fatalf("expected fail for below minimum, got %v", r.Checks)
	}
}

func TestCheckImportEvidence_HasFailures(t *testing.T) {
	r := &CutoverReport{}
	imp := &paperlessimport.MigrationReport{Total: 200, Failed: 5, Imported: 195}
	checkImportEvidence(r, imp, 100)
	if len(r.Checks) != 1 || r.Checks[0].Status != CutoverStatusFail {
		t.Fatalf("expected fail for failed docs, got %v", r.Checks)
	}
}

func TestCheckImportEvidence_ImportedSkippedMismatch(t *testing.T) {
	r := &CutoverReport{}
	imp := &paperlessimport.MigrationReport{Total: 200, Imported: 100, Skipped: 50}
	checkImportEvidence(r, imp, 100)
	if len(r.Checks) != 1 || r.Checks[0].Status != CutoverStatusFail {
		t.Fatalf("expected fail for imported+skipped != total, got %v", r.Checks)
	}
}

func TestCheckSearchEvidence_NilReport(t *testing.T) {
	r := &CutoverReport{}
	checkSearchEvidence(r, nil)
	if len(r.Checks) != 0 {
		t.Fatalf("expected no checks for nil report, got %d", len(r.Checks))
	}
}

func TestCheckSearchEvidence_ZeroTotal(t *testing.T) {
	r := &CutoverReport{}
	checkSearchEvidence(r, &symseek.ValidationReport{Total: 0, OK: true})
	if len(r.Checks) != 1 || r.Checks[0].Status != CutoverStatusFail {
		t.Fatalf("expected fail for zero total, got %v", r.Checks)
	}
}

func TestCheckSearchEvidence_HasFailures(t *testing.T) {
	r := &CutoverReport{}
	checkSearchEvidence(r, &symseek.ValidationReport{Total: 100, OK: true, Failed: 5})
	if len(r.Checks) != 1 || r.Checks[0].Status != CutoverStatusFail {
		t.Fatalf("expected fail for search failures, got %v", r.Checks)
	}
}

func TestCheckCountConsistency_MismatchedCounts(t *testing.T) {
	r := &CutoverReport{}
	dryRun := &paperlessimport.MigrationReport{Total: 200, DryRun: true, Mode: "dry-run", Documents: make([]paperlessimport.DocumentResult, 200)}
	imp := &paperlessimport.MigrationReport{Total: 180, Mode: "real", Imported: 180, Documents: make([]paperlessimport.DocumentResult, 180)}
	checkCountConsistency(r, dryRun, imp, nil)
	if len(r.Checks) == 0 {
		t.Fatal("expected at least one count consistency check")
	}
	for _, c := range r.Checks {
		if c.Status != CutoverStatusFail {
			t.Errorf("expected fail for count mismatch, got %v", c)
		}
	}
}

func TestValidateVaultEvidence_EmptyVaultPath(t *testing.T) {
	r := &CutoverReport{}
	validateVaultEvidence(r, "", nil, 0)
	if len(r.Checks) != 1 || r.Checks[0].Status != CutoverStatusFail {
		t.Fatalf("expected fail for empty vault path, got %v", r.Checks)
	}
}

func TestValidateVaultEvidence_EmptyVaultDir(t *testing.T) {
	dir := t.TempDir()
	r := &CutoverReport{}
	validateVaultEvidence(r, dir, nil, 0)
	if len(r.Checks) != 1 || r.Checks[0].Status != CutoverStatusFail {
		t.Fatalf("expected fail for empty vault, got %v", r.Checks)
	}
}

func TestValidateVaultEvidence_VaultLessThanVerified(t *testing.T) {
	dir := t.TempDir()
	notePath := filepath.Join(dir, "note.md")
	os.WriteFile(notePath, []byte("---\nsource_path: /x\ningested_at: 2024-01-01\nsha256: abc\nmime: text/plain\ntags: []\ncategory: \"\"\n---\nbody\n"), 0o600)

	r := &CutoverReport{}
	verify := &paperlessimport.VerifyReport{Verified: 100}
	validateVaultEvidence(r, dir, verify, 0)
	if len(r.Checks) != 1 || r.Checks[0].Status != CutoverStatusFail {
		t.Fatalf("expected fail when vault files < verified, got %v", r.Checks)
	}
}

func TestAddMigrationFindings_Unresolved(t *testing.T) {
	report := &ReviewReport{}
	m := paperlessimport.MigrationReport{
		UnresolvedTagIDs:           []int{1, 2},
		UnresolvedCorrespondentIDs: []int{3},
		UnresolvedDocumentTypeIDs:  []int{4},
		UnresolvedStoragePathIDs:   []int{5},
	}
	addMigrationFindings(report, m, ReviewFilters{})
	if len(report.Findings) != 5 {
		t.Fatalf("expected 5 findings, got %d: %v", len(report.Findings), report.Findings)
	}
	kinds := make(map[string]bool)
	for _, f := range report.Findings {
		kinds[f.Kind] = true
	}
	for _, want := range []string{"unresolved_tag", "unresolved_correspondent", "unresolved_document_type", "unresolved_storage_path"} {
		if !kinds[want] {
			t.Errorf("missing finding kind %q", want)
		}
	}
}

func TestAddMigrationFindings_Unsupported(t *testing.T) {
	report := &ReviewReport{}
	m := paperlessimport.MigrationReport{
		UnsupportedFileTypes: map[string]int{".exe": 5, ".bin": 3},
	}
	addMigrationFindings(report, m, ReviewFilters{})
	if len(report.Findings) != 2 {
		t.Fatalf("expected 2 findings, got %d: %v", len(report.Findings), report.Findings)
	}
}

func TestAddMigrationFindings_FilteredOut(t *testing.T) {
	report := &ReviewReport{}
	m := paperlessimport.MigrationReport{
		UnsupportedFileTypes:       map[string]int{".exe": 5},
		UnresolvedTagIDs:           []int{1},
		UnresolvedCorrespondentIDs: []int{2},
		UnresolvedDocumentTypeIDs:  []int{3},
		UnresolvedStoragePathIDs:   []int{4},
	}
	addMigrationFindings(report, m, ReviewFilters{Failed: true})
	if len(report.Findings) != 0 {
		t.Fatalf("expected 0 findings when filtered, got %d: %v", len(report.Findings), report.Findings)
	}
}

func TestApplyCorrectionToMeta_TagOperations(t *testing.T) {
	meta := map[string]any{
		"source_path":  "/doc.pdf",
		"ingested_at":  "2024-01-01",
		"sha256":       "abc",
		"mime":         "application/pdf",
		"tags":         []any{"old_tag"},
		"category":     "Finance",
		"correspondent": "Old Corp",
		"document_type": "Invoice",
		"ocr_engine":   "tesseract",
		"archive_path": "/archive/abc.pdf",
	}
	c := Correction{
		PaperlessID:    1,
		AddTags:        []string{"new_tag"},
		RemoveTags:     []string{"old_tag"},
		Correspondent:  strPtr("New Corp"),
		DocumentType:   strPtr("Receipt"),
		StoragePath:    strPtr("Invoices/2024"),
	}
	applyCorrectionToMeta(meta, c, &UpdateResult{})

	tags, ok := meta["tags"].([]any)
	if !ok {
		t.Fatalf("tags type = %T, want []any", meta["tags"])
	}
	for _, tag := range tags {
		if tag == "old_tag" {
			t.Error("old_tag should have been removed")
		}
	}
	hasNew := false
	for _, tag := range tags {
		if tag == "new_tag" {
			hasNew = true
		}
	}
	if !hasNew {
		t.Error("new_tag should have been added")
	}
	if meta["correspondent"] != "New Corp" {
		t.Errorf("correspondent = %v, want New Corp", meta["correspondent"])
	}
	if meta["document_type"] != "Receipt" {
		t.Errorf("document_type = %v, want Receipt", meta["document_type"])
	}
}

func TestApplyCorrectionToMeta_NilPaperless(t *testing.T) {
	meta := map[string]any{
		"source_path": "/doc.pdf",
		"tags":        []any{"a"},
	}
	c := Correction{
		PaperlessID: 1,
		StoragePath: strPtr("New/Path"),
	}
	applyCorrectionToMeta(meta, c, &UpdateResult{})
	pm, ok := meta["paperless"].(map[string]any)
	if !ok {
		t.Fatalf("paperless not created, meta = %v", meta)
	}
	if pm["storage_path"] != "New/Path" {
		t.Errorf("storage_path = %v, want New/Path", pm["storage_path"])
	}
}

func TestValidateSafeTypes_AllValid(t *testing.T) {
	report := &ValidationReport{}
	meta := map[string]any{
		"tags":      []any{"a", "b"},
		"paperless": map[string]any{"document_id": 42},
	}
	validateSafeTypes(report, "note.md", meta)
	if len(report.Failures) != 0 {
		t.Fatalf("expected 0 failures, got %d: %v", len(report.Failures), report.Failures)
	}
}

func TestValidateArchive_ArchiveExistsButHashMismatch(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "doc.pdf")
	os.WriteFile(archivePath, []byte("content"), 0o600)

	report := &ValidationReport{}
	meta := map[string]any{
		"archive_path": archivePath,
		"sha256":       "wronghash",
	}
	validateArchive(report, "note.md", meta)
	if len(report.Failures) != 1 {
		t.Fatalf("expected 1 failure for hash mismatch, got %d: %v", len(report.Failures), report.Failures)
	}
	if report.Failures[0].Check != "archive.hash" {
		t.Errorf("check = %q, want archive.hash", report.Failures[0].Check)
	}
}

func TestValidateArchive_MissingArchive(t *testing.T) {
	report := &ValidationReport{}
	meta := map[string]any{
		"archive_path": "/nonexistent/path.pdf",
	}
	validateArchive(report, "note.md", meta)
	if len(report.Failures) != 1 {
		t.Fatalf("expected 1 failure for missing archive, got %d: %v", len(report.Failures), report.Failures)
	}
	if report.Failures[0].Check != "archive.exists" {
		t.Errorf("check = %q, want archive.exists", report.Failures[0].Check)
	}
}

func strPtr(s string) *string { return &s }
