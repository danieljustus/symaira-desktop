package vaultreview

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/danieljustus/symaira-ingest/internal/paperlessimport"
	symseekint "github.com/danieljustus/symaira-ingest/internal/symseek"
)

const (
	CutoverStatusPass = "pass"
	CutoverStatusWarn = "warn"
	CutoverStatusFail = "fail"
)

// CutoverOptions describes the evidence required before Paperless-ngx can stop
// being the source of truth. All report files are safe machine-readable outputs
// produced by symingest; they intentionally contain no document body text.
type CutoverOptions struct {
	DryRunReportPath string
	ImportReportPath string
	VerifyReportPath string
	SearchReportPath string
	VaultPath        string
	MinDocuments     int
	MinBodyLength    int
}

// CutoverCheck is one pass/warn/fail gate in a Paperless replacement decision.
type CutoverCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

// CutoverReport is the final migration gate. Ready is true only when every
// required report is present and clean, the vault validates, and counts agree.
type CutoverReport struct {
	SchemaVersion int            `json:"schema_version"`
	Ready         bool           `json:"ready"`
	Checks        []CutoverCheck `json:"checks"`
	Blockers      []string       `json:"blockers,omitempty"`
	Warnings      []string       `json:"warnings,omitempty"`
}

func (r *CutoverReport) add(name, status, message string) {
	r.Checks = append(r.Checks, CutoverCheck{Name: name, Status: status, Message: message})
	switch status {
	case CutoverStatusFail:
		r.Blockers = append(r.Blockers, fmt.Sprintf("%s: %s", name, message))
	case CutoverStatusWarn:
		r.Warnings = append(r.Warnings, fmt.Sprintf("%s: %s", name, message))
	}
}

// BuildCutoverReport validates the artifacts from the replacement runbook:
// full dry-run, real import, verifier output, and vault validation. It does not
// call Paperless or symseek; it consumes existing evidence so it can be used in
// CI/offline review and by the Swift app.
func BuildCutoverReport(opts CutoverOptions) (*CutoverReport, error) {
	if opts.MinDocuments < 0 {
		return nil, fmt.Errorf("min_documents must be zero or positive")
	}
	if opts.MinBodyLength < 0 {
		return nil, fmt.Errorf("min_body_length must be zero or positive")
	}

	report := &CutoverReport{SchemaVersion: paperlessimport.ReportSchemaVersion}

	dryRun := loadMigrationEvidence(report, "dry-run", opts.DryRunReportPath)
	imp := loadMigrationEvidence(report, "import", opts.ImportReportPath)
	verify := loadVerifyEvidence(report, opts.VerifyReportPath)
	search := loadSearchEvidence(report, opts.SearchReportPath)
	checkMigrationReportSchema(report, "dry-run", dryRun)
	checkMigrationReportSchema(report, "import", imp)
	checkVerifyReportSchema(report, verify)
	checkSearchReportSchema(report, search)
	validateVaultEvidence(report, opts.VaultPath, verify, opts.MinBodyLength)

	checkDryRunEvidence(report, dryRun, opts.MinDocuments)
	checkImportEvidence(report, imp, opts.MinDocuments)
	checkVerifyEvidence(report, verify, opts.MinDocuments)
	checkSearchEvidence(report, search)
	checkCountConsistency(report, dryRun, imp, verify)

	report.Ready = len(report.Blockers) == 0
	return report, nil
}

func checkMigrationReportSchema(r *CutoverReport, label string, report *paperlessimport.MigrationReport) {
	if report == nil {
		return
	}
	if report.SchemaVersion != paperlessimport.ReportSchemaVersion {
		r.add(label+" report schema", CutoverStatusFail, fmt.Sprintf("schema_version=%d; expected %d", report.SchemaVersion, paperlessimport.ReportSchemaVersion))
		return
	}
	r.add(label+" report schema", CutoverStatusPass, fmt.Sprintf("schema_version=%d", report.SchemaVersion))
}

func checkVerifyReportSchema(r *CutoverReport, report *paperlessimport.VerifyReport) {
	if report == nil {
		return
	}
	if report.SchemaVersion != paperlessimport.ReportSchemaVersion {
		r.add("verify report schema", CutoverStatusFail, fmt.Sprintf("schema_version=%d; expected %d", report.SchemaVersion, paperlessimport.ReportSchemaVersion))
		return
	}
	r.add("verify report schema", CutoverStatusPass, fmt.Sprintf("schema_version=%d", report.SchemaVersion))
}

func checkSearchReportSchema(r *CutoverReport, report *symseekint.ValidationReport) {
	if report == nil {
		return
	}
	if report.SchemaVersion != symseekint.ReportSchemaVersion {
		r.add("search report schema", CutoverStatusFail, fmt.Sprintf("schema_version=%d; expected %d", report.SchemaVersion, symseekint.ReportSchemaVersion))
		return
	}
	r.add("search report schema", CutoverStatusPass, fmt.Sprintf("schema_version=%d", report.SchemaVersion))
}

func loadMigrationEvidence(r *CutoverReport, label, path string) *paperlessimport.MigrationReport {
	if strings.TrimSpace(path) == "" {
		r.add(label+" report", CutoverStatusFail, "missing required report path")
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		r.add(label+" report", CutoverStatusFail, err.Error())
		return nil
	}
	var report paperlessimport.MigrationReport
	if err := json.Unmarshal(data, &report); err != nil {
		r.add(label+" report", CutoverStatusFail, "invalid JSON: "+err.Error())
		return nil
	}
	return &report
}

func loadVerifyEvidence(r *CutoverReport, path string) *paperlessimport.VerifyReport {
	if strings.TrimSpace(path) == "" {
		r.add("verify report", CutoverStatusFail, "missing required report path")
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		r.add("verify report", CutoverStatusFail, err.Error())
		return nil
	}
	var report paperlessimport.VerifyReport
	if err := json.Unmarshal(data, &report); err != nil {
		r.add("verify report", CutoverStatusFail, "invalid JSON: "+err.Error())
		return nil
	}
	return &report
}

func loadSearchEvidence(r *CutoverReport, path string) *symseekint.ValidationReport {
	if strings.TrimSpace(path) == "" {
		r.add("search validation report", CutoverStatusFail, "missing required report path")
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		r.add("search validation report", CutoverStatusFail, err.Error())
		return nil
	}
	var report symseekint.ValidationReport
	if err := json.Unmarshal(data, &report); err != nil {
		r.add("search validation report", CutoverStatusFail, "invalid JSON: "+err.Error())
		return nil
	}
	return &report
}

func validateVaultEvidence(r *CutoverReport, vault string, verify *paperlessimport.VerifyReport, minBodyLength int) {
	if strings.TrimSpace(vault) == "" {
		r.add("vault validation", CutoverStatusFail, "missing required vault path")
		return
	}
	vr, err := ValidateVaultWithOptions(vault, ValidationOptions{MinBodyLength: minBodyLength})
	if err != nil {
		r.add("vault validation", CutoverStatusFail, err.Error())
		return
	}
	if !vr.OK() {
		r.add("vault validation", CutoverStatusFail, fmt.Sprintf("%d validation failure(s)", len(vr.Failures)))
		return
	}
	if vr.Files == 0 {
		r.add("vault validation", CutoverStatusFail, "vault contains no Markdown notes")
		return
	}
	if verify != nil && verify.Verified > 0 && vr.Files < verify.Verified {
		r.add("vault validation", CutoverStatusFail, fmt.Sprintf("vault has %d Markdown notes but verifier confirmed %d documents", vr.Files, verify.Verified))
		return
	}
	r.add("vault validation", CutoverStatusPass, fmt.Sprintf("%d Markdown notes validated", vr.Files))
}

func checkDryRunEvidence(r *CutoverReport, dryRun *paperlessimport.MigrationReport, minDocuments int) {
	if dryRun == nil {
		return
	}
	if !dryRun.DryRun && dryRun.Mode != "dry-run" {
		r.add("dry-run gate", CutoverStatusFail, fmt.Sprintf("report mode is %q; expected dry-run", dryRun.Mode))
		return
	}
	if dryRun.Total < minDocuments {
		r.add("dry-run gate", CutoverStatusFail, fmt.Sprintf("total %d below required minimum %d", dryRun.Total, minDocuments))
		return
	}
	if dryRun.Failed != 0 {
		r.add("dry-run gate", CutoverStatusFail, fmt.Sprintf("dry-run has %d failed documents", dryRun.Failed))
		return
	}
	if len(dryRun.UnsupportedFileTypes) > 0 {
		r.add("dry-run gate", CutoverStatusFail, fmt.Sprintf("unsupported file types: %v", dryRun.UnsupportedFileTypes))
		return
	}
	if unresolvedCount(dryRun) > 0 {
		r.add("dry-run gate", CutoverStatusFail, "unresolved Paperless metadata IDs remain")
		return
	}
	missingExt := 0
	for _, d := range dryRun.Documents {
		if d.ExpectedExtension == "" {
			missingExt++
		}
	}
	if missingExt > 0 {
		r.add("dry-run gate", CutoverStatusFail, fmt.Sprintf("%d documents have no expected download extension", missingExt))
		return
	}
	r.add("dry-run gate", CutoverStatusPass, fmt.Sprintf("%d documents analyzed; no unsupported files or unresolved metadata", dryRun.Total))
}

func checkImportEvidence(r *CutoverReport, imp *paperlessimport.MigrationReport, minDocuments int) {
	if imp == nil {
		return
	}
	if imp.DryRun || imp.Mode == "dry-run" || imp.Mode == "plan" {
		r.add("import gate", CutoverStatusFail, fmt.Sprintf("report mode is %q; expected real import", imp.Mode))
		return
	}
	if imp.Total < minDocuments {
		r.add("import gate", CutoverStatusFail, fmt.Sprintf("total %d below required minimum %d", imp.Total, minDocuments))
		return
	}
	if imp.Failed != 0 {
		r.add("import gate", CutoverStatusFail, fmt.Sprintf("import has %d failed documents", imp.Failed))
		return
	}
	if imp.Imported+imp.Skipped != imp.Total {
		r.add("import gate", CutoverStatusFail, fmt.Sprintf("imported+skipped=%d but total=%d", imp.Imported+imp.Skipped, imp.Total))
		return
	}
	failedDocs := 0
	missingMappings := 0
	for _, d := range imp.Documents {
		if d.Status == "failed" {
			failedDocs++
		}
		if (d.Status == "imported" || d.Status == "skipped") && (d.VaultPath == "" || d.ArchivePath == "" || d.SHA256 == "") {
			missingMappings++
		}
	}
	if failedDocs > 0 {
		r.add("import gate", CutoverStatusFail, fmt.Sprintf("%d document result(s) are failed", failedDocs))
		return
	}
	if missingMappings > 0 {
		r.add("import gate", CutoverStatusFail, fmt.Sprintf("%d imported/skipped document result(s) lack vault_path, archive_path, or sha256", missingMappings))
		return
	}
	r.add("import gate", CutoverStatusPass, fmt.Sprintf("%d/%d documents imported or already present", imp.Imported+imp.Skipped, imp.Total))
}

func checkVerifyEvidence(r *CutoverReport, verify *paperlessimport.VerifyReport, minDocuments int) {
	if verify == nil {
		return
	}
	if verify.SourceDocuments < minDocuments {
		r.add("verify gate", CutoverStatusFail, fmt.Sprintf("source_documents %d below required minimum %d", verify.SourceDocuments, minDocuments))
		return
	}
	if verify.Verified != verify.SourceDocuments {
		r.add("verify gate", CutoverStatusFail, fmt.Sprintf("verified=%d but source_documents=%d", verify.Verified, verify.SourceDocuments))
		return
	}
	if !verify.Complete() {
		r.add("verify gate", CutoverStatusFail, fmt.Sprintf("discrepancies: missing=%d duplicate=%d duplicate_content=%d missing_archive=%d hash_mismatch=%d mismatches=%d",
			len(verify.Missing), len(verify.Duplicate), len(verify.DuplicateContent), len(verify.MissingArchive), len(verify.HashMismatch), len(verify.Mismatches)))
		return
	}
	if len(verify.DuplicateContent) > 0 {
		r.add("duplicate content", CutoverStatusWarn, fmt.Sprintf("%d Paperless document(s) share original bytes with another ID; allowed when each ID has its own note", len(verify.DuplicateContent)))
	}
	r.add("verify gate", CutoverStatusPass, fmt.Sprintf("%d source documents verified", verify.Verified))
}

func checkSearchEvidence(r *CutoverReport, search *symseekint.ValidationReport) {
	if search == nil {
		return
	}
	if search.Total == 0 {
		r.add("search validation gate", CutoverStatusFail, "search validation report contains no query fixtures")
		return
	}
	if !search.OK || search.Failed > 0 {
		r.add("search validation gate", CutoverStatusFail, fmt.Sprintf("%d/%d search fixture(s) failed", search.Failed, search.Total))
		return
	}
	r.add("search validation gate", CutoverStatusPass, fmt.Sprintf("%d search fixture(s) passed", search.Passed))
}

func checkCountConsistency(r *CutoverReport, dryRun, imp *paperlessimport.MigrationReport, verify *paperlessimport.VerifyReport) {
	var pairs []string
	if dryRun != nil && imp != nil && dryRun.Total != imp.Total {
		pairs = append(pairs, fmt.Sprintf("dry-run total %d != import total %d", dryRun.Total, imp.Total))
	}
	if dryRun != nil && verify != nil && dryRun.Total != verify.SourceDocuments {
		pairs = append(pairs, fmt.Sprintf("dry-run total %d != verify source_documents %d", dryRun.Total, verify.SourceDocuments))
	}
	if imp != nil && verify != nil && imp.Total != verify.SourceDocuments {
		pairs = append(pairs, fmt.Sprintf("import total %d != verify source_documents %d", imp.Total, verify.SourceDocuments))
	}
	if len(pairs) > 0 {
		r.add("count consistency", CutoverStatusFail, strings.Join(pairs, "; "))
		return
	}
	r.add("count consistency", CutoverStatusPass, "dry-run, import and verify counts agree")
}

func unresolvedCount(r *paperlessimport.MigrationReport) int {
	return len(r.UnresolvedTagIDs) + len(r.UnresolvedCorrespondentIDs) + len(r.UnresolvedDocumentTypeIDs) + len(r.UnresolvedStoragePathIDs)
}
