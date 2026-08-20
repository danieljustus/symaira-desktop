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
	"github.com/danieljustus/symaira-ingest/internal/vaultreview"
)

func TestParseLaunchctlPID(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int
	}{
		{"empty", "", 0},
		{"no_pid_line", "some output\nmore output", 0},
		{"valid_pid", "pid = 12345\nother stuff", 12345},
		{"pid_with_spaces", "  pid = 42  ", 42},
		{"malformed_pid", "pid = notanumber", 0},
		{"too_few_fields", "pid =", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseLaunchctlPID(tt.input)
			if got != tt.want {
				t.Errorf("parseLaunchctlPID(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestReadLastLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.txt")
	content := "line1\nline2\nline3\nline4\nline5\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	lines, err := readLastLines(path, 3)
	if err != nil {
		t.Fatalf("readLastLines: %v", err)
	}
	if len(lines) != 3 {
		t.Fatalf("len(lines) = %d, want 3", len(lines))
	}
	want := []string{"line3", "line4", "line5"}
	for i, line := range lines {
		if line != want[i] {
			t.Errorf("lines[%d] = %q, want %q", i, line, want[i])
		}
	}
}

func TestReadLastLines_FewerThanN(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	lines, err := readLastLines(path, 10)
	if err != nil {
		t.Fatalf("readLastLines: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("len(lines) = %d, want 2", len(lines))
	}
}

func TestReadLastLines_FileNotFound(t *testing.T) {
	_, err := readLastLines(filepath.Join(t.TempDir(), "nonexistent.txt"), 5)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestQueueCounts(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")
	st, err := store.Open(tempDB)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	counts, err := queueCounts(tempDB)
	if err != nil {
		t.Fatalf("queueCounts: %v", err)
	}
	if counts == nil {
		t.Fatal("counts is nil")
	}
	// Empty DB should have no counts.
	if len(counts) != 0 {
		t.Errorf("len(counts) = %d, want 0", len(counts))
	}
}

func TestQueueCounts_EmptyPath(t *testing.T) {
	counts, err := queueCounts("")
	if err != nil {
		t.Fatalf("queueCounts: %v", err)
	}
	if counts != nil {
		t.Errorf("counts = %v, want nil for empty path", counts)
	}
}

func TestRunRules_Update(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	// First add a rule.
	sb := withCapturedStdout(t)
	if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", "category", "Invoices"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}

	// Get the rule ID.
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

	// Update the rule.
	sb.Reset()
	if err := run([]string{"rules", "-db", tempDB, "update", ruleID, "*.docx", "tag", "Documents"}); err != nil {
		t.Fatalf("rules update: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "Updated classification rule") {
		t.Errorf("update output missing confirmation, got %q", out)
	}
	if !strings.Contains(out, "*.docx") {
		t.Errorf("update output missing new pattern, got %q", out)
	}
}

func TestRunRules_UpdateMissingArgs(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")
	withCapturedStdout(t)

	err := run([]string{"rules", "-db", tempDB, "update", "1", "*.pdf"})
	if err == nil {
		t.Fatal("expected error for missing update arguments")
	}
	if !strings.Contains(err.Error(), "missing arguments") {
		t.Errorf("error = %v, want mention of 'missing arguments'", err)
	}
}

func TestRunRules_UpdateInvalidID(t *testing.T) {
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

func TestRunRules_Test(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	// Add a rule.
	sb := withCapturedStdout(t)
	if err := run([]string{"rules", "-db", tempDB, "add", "invoice", "category", "Finance"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}

	// Test matching text.
	sb.Reset()
	if err := run([]string{"rules", "-db", tempDB, "test", "This is an invoice document"}); err != nil {
		t.Fatalf("rules test: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "match rule") {
		t.Errorf("test output missing match, got %q", out)
	}
}

func TestRunRules_TestNoMatch(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	// Add a rule.
	sb := withCapturedStdout(t)
	if err := run([]string{"rules", "-db", tempDB, "add", "invoice", "category", "Finance"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}

	// Test non-matching text.
	sb.Reset()
	if err := run([]string{"rules", "-db", tempDB, "test", "This is a receipt"}); err != nil {
		t.Fatalf("rules test: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "No matching") {
		t.Errorf("test output missing no-match message, got %q", out)
	}
}

func TestRunRules_TestJSON(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	// Add a rule.
	sb := withCapturedStdout(t)
	if err := run([]string{"rules", "-db", tempDB, "add", "invoice", "category", "Finance"}); err != nil {
		t.Fatalf("rules add: %v", err)
	}

	// Test with JSON output.
	sb.Reset()
	if err := run([]string{"rules", "-db", tempDB, "-json", "test", "invoice document"}); err != nil {
		t.Fatalf("rules test -json: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "[") || !strings.Contains(out, "pattern") {
		t.Errorf("test JSON output unexpected, got %q", out)
	}
}

func TestRunRules_TestMissingText(t *testing.T) {
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

func TestPrintRulesUsage(t *testing.T) {
	sb := withCapturedStdout(t)
	if err := printRulesUsage(); err != nil {
		t.Fatalf("printRulesUsage: %v", err)
	}
	out := sb.String()
	for _, want := range []string{"Usage:", "list", "add", "delete", "Patterns are"} {
		if !strings.Contains(out, want) {
			t.Errorf("usage output missing %q, got %q", want, out)
		}
	}
}

func TestPrintUpdateResults(t *testing.T) {
	sb := withCapturedStdout(t)
	results := []vaultreview.UpdateResult{
		{PaperlessID: 1, File: "/vault/doc.md", Changes: []string{"tag: added"}, Written: true},
		{PaperlessID: 2, File: "/vault/doc2.md", Changes: []string{"tag: removed"}, DryRun: true},
		{PaperlessID: 3, File: "/vault/doc3.md", Changes: nil, Written: false},
		{PaperlessID: 4, File: "/vault/doc4.md", Changes: []string{"correspondent"}, Written: true, BackupPath: "/backup/doc4.md"},
	}
	printUpdateResults(results)
	out := sb.String()
	for _, want := range []string{"updated", "would update", "unchanged", "paperless_id=1", "backup="} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q, got %q", want, out)
		}
	}
}

func TestRunUpdate_MissingVault(t *testing.T) {
	withCapturedStdout(t)
	err := run([]string{"update", "-paperless-id", "1"})
	if err == nil {
		t.Fatal("expected error for missing --vault")
	}
	if !strings.Contains(err.Error(), "--vault is required") {
		t.Errorf("error = %v, want mention of '--vault is required'", err)
	}
}

func TestRunReviewReport_MissingArg(t *testing.T) {
	withCapturedStdout(t)
	err := run([]string{"review-report"})
	if err == nil {
		t.Fatal("expected error for missing report file")
	}
}

func TestRunReviewReport_WithMigrationReport(t *testing.T) {
	dir := t.TempDir()
	reportPath := filepath.Join(dir, "migration.json")
	data, err := json.Marshal(&paperlessimport.MigrationReport{
		SchemaVersion: paperlessimport.ReportSchemaVersion,
		ToolVersion:   "test",
		Mode:          "import",
		Total:         1,
		Imported:      1,
		Documents: []paperlessimport.DocumentResult{
			{ID: 1, Status: "imported", VaultPath: filepath.Join(dir, "doc.md")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reportPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	sb := withCapturedStdout(t)
	if err := run([]string{"review-report", reportPath}); err != nil {
		t.Fatalf("run(review-report): %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "document 1") {
		t.Errorf("output missing document, got %q", out)
	}
}

func TestRunReviewReport_JSON(t *testing.T) {
	dir := t.TempDir()
	reportPath := filepath.Join(dir, "migration.json")
	data, err := json.Marshal(&paperlessimport.MigrationReport{
		SchemaVersion: paperlessimport.ReportSchemaVersion,
		ToolVersion:   "test",
		Mode:          "import",
		Total:         0,
		Documents:     []paperlessimport.DocumentResult{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reportPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	sb := withCapturedStdout(t)
	if err := run([]string{"review-report", "-json", reportPath}); err != nil {
		t.Fatalf("run(review-report -json): %v", err)
	}
	out := sb.String()
	var report map[string]any
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
}

func TestRunBulkUpdate_MissingVault(t *testing.T) {
	withCapturedStdout(t)
	err := run([]string{"bulk-update", "-where", "tag:test"})
	if err == nil {
		t.Fatal("expected error for missing --vault")
	}
	if !strings.Contains(err.Error(), "--vault and --where") {
		t.Errorf("error = %v, want mention of '--vault and --where'", err)
	}
}

func TestRunBulkUpdate_MissingWhere(t *testing.T) {
	withCapturedStdout(t)
	err := run([]string{"bulk-update", "-vault", t.TempDir()})
	if err == nil {
		t.Fatal("expected error for missing --where")
	}
	if !strings.Contains(err.Error(), "--vault and --where") {
		t.Errorf("error = %v, want mention of '--vault and --where'", err)
	}
}

func TestRunApplyCorrections_MissingArgs(t *testing.T) {
	withCapturedStdout(t)
	err := run([]string{"apply-corrections"})
	if err == nil {
		t.Fatal("expected error for missing corrections file")
	}
}

func TestRunApplyCorrections_MissingVault(t *testing.T) {
	dir := t.TempDir()
	correctionsPath := filepath.Join(dir, "corrections.yaml")
	if err := os.WriteFile(correctionsPath, []byte("schema_version: 1\ncorrections: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	withCapturedStdout(t)
	err := run([]string{"apply-corrections", correctionsPath})
	if err == nil {
		t.Fatal("expected error for missing --vault")
	}
	if !strings.Contains(err.Error(), "--vault") {
		t.Errorf("error = %v, want mention of '--vault'", err)
	}
}
