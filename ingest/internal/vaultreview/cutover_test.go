package vaultreview

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-ingest/internal/paperlessimport"
	symseekint "github.com/danieljustus/symaira-ingest/internal/symseek"
)

func writeJSON(t *testing.T, path string, v any) string {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func validCutoverFixture(t *testing.T) (dir, dryRunPath, importPath, verifyPath, searchPath, vault string) {
	t.Helper()
	dir = t.TempDir()
	vault = filepath.Join(dir, "vault")
	archive := filepath.Join(dir, "archive")
	if err := os.MkdirAll(vault, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(archive, 0o700); err != nil {
		t.Fatal(err)
	}
	archivePath, sum := archiveFile(t, archive, "one.txt", "hello")
	testNote(t, vault, "one.md", archivePath, sum, 1, []string{"inbox"})

	doc := paperlessimport.DocumentResult{ID: 1, Status: "imported", MIME: "text/plain", ExpectedExtension: ".txt", VaultPath: filepath.Join(vault, "one.md"), ArchivePath: archivePath, SHA256: sum}
	dryRunPath = writeJSON(t, filepath.Join(dir, "dry-run.json"), &paperlessimport.MigrationReport{SchemaVersion: paperlessimport.ReportSchemaVersion, Mode: "dry-run", DryRun: true, Total: 1, Skipped: 1, Documents: []paperlessimport.DocumentResult{{ID: 1, Status: "would-import", MIME: "text/plain", ExpectedExtension: ".txt"}}})
	importPath = writeJSON(t, filepath.Join(dir, "import.json"), &paperlessimport.MigrationReport{SchemaVersion: paperlessimport.ReportSchemaVersion, Mode: "import", Total: 1, Imported: 1, Documents: []paperlessimport.DocumentResult{doc}})
	verifyPath = writeJSON(t, filepath.Join(dir, "verify.json"), &paperlessimport.VerifyReport{SchemaVersion: paperlessimport.ReportSchemaVersion, Mode: "verify", SourceDocuments: 1, VaultNotes: 1, Verified: 1})
	searchPath = writeJSON(t, filepath.Join(dir, "search.json"), &symseekint.ValidationReport{SchemaVersion: symseekint.ReportSchemaVersion, ToolVersion: "test", OK: true, Total: 1, Passed: 1, Checks: []symseekint.QueryCheck{{Query: "hello", OK: true, MinResults: 1, ResultCount: 1}}})
	return dir, dryRunPath, importPath, verifyPath, searchPath, vault
}

func TestBuildCutoverReportReady(t *testing.T) {
	_, dryRunPath, importPath, verifyPath, searchPath, vault := validCutoverFixture(t)
	report, err := BuildCutoverReport(CutoverOptions{DryRunReportPath: dryRunPath, ImportReportPath: importPath, VerifyReportPath: verifyPath, SearchReportPath: searchPath, VaultPath: vault, MinDocuments: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Ready || len(report.Blockers) != 0 {
		t.Fatalf("expected ready report, got %+v", report)
	}
	for _, want := range []string{"dry-run gate", "import gate", "verify gate", "search validation gate", "vault validation", "count consistency"} {
		found := false
		for _, c := range report.Checks {
			if c.Name == want && c.Status == CutoverStatusPass {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing passing check %q in %+v", want, report.Checks)
		}
	}
}

func TestBuildCutoverReportBlocksUnsafeEvidence(t *testing.T) {
	dir, dryRunPath, importPath, verifyPath, searchPath, vault := validCutoverFixture(t)
	writeJSON(t, dryRunPath, &paperlessimport.MigrationReport{SchemaVersion: paperlessimport.ReportSchemaVersion, Mode: "dry-run", DryRun: true, Total: 1, Failed: 0, UnsupportedFileTypes: map[string]int{"application/x-unknown": 1}, Documents: []paperlessimport.DocumentResult{{ID: 1, Status: "would-import"}}})
	writeJSON(t, importPath, &paperlessimport.MigrationReport{SchemaVersion: paperlessimport.ReportSchemaVersion, Mode: "dry-run", DryRun: true, Total: 1, Skipped: 1})
	writeJSON(t, verifyPath, &paperlessimport.VerifyReport{SchemaVersion: paperlessimport.ReportSchemaVersion, Mode: "verify", SourceDocuments: 1, VaultNotes: 1, Verified: 0, Missing: []int{1}})

	report, err := BuildCutoverReport(CutoverOptions{DryRunReportPath: dryRunPath, ImportReportPath: importPath, VerifyReportPath: verifyPath, SearchReportPath: searchPath, VaultPath: vault, MinDocuments: 1})
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready {
		t.Fatalf("expected blocked report, got %+v", report)
	}
	joined := strings.Join(report.Blockers, "\n")
	for _, want := range []string{"unsupported file types", "expected real import", "verified=0"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("blockers missing %q:\n%s", want, joined)
		}
	}
	_ = dir
}

func TestBuildCutoverReportRequiresAllEvidence(t *testing.T) {
	report, err := BuildCutoverReport(CutoverOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready {
		t.Fatalf("empty evidence must not be ready: %+v", report)
	}
	joined := strings.Join(report.Blockers, "\n")
	for _, want := range []string{"dry-run report", "import report", "verify report", "search validation report", "vault validation"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing blocker %q:\n%s", want, joined)
		}
	}
}
