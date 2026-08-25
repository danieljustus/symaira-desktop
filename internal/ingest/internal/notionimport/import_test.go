package notionimport

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testdataDir returns the path to the test fixture export directory.
func testdataDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs("testdata/fixture")
	if err != nil {
		t.Fatalf("resolve testdata: %v", err)
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Fatalf("testdata directory not found: %s", dir)
	}
	return dir
}

func TestDiscoverExport(t *testing.T) {
	dir := testdataDir(t)
	entries, err := discoverExport(dir)
	if err != nil {
		t.Fatalf("discoverExport: %v", err)
	}

	if len(entries) < 5 {
		t.Fatalf("expected at least 5 entries (4 pages + 1 CSV), got %d", len(entries))
	}

	var pages, csvs int
	for _, e := range entries {
		switch e.Kind {
		case EntryPage:
			pages++
		case EntryCSV:
			csvs++
		}
	}
	if pages != 4 {
		t.Errorf("expected 4 pages, got %d", pages)
	}
	if csvs != 1 {
		t.Errorf("expected 1 CSV, got %d", csvs)
	}

	// Nested page should preserve its relative path.
	var nested *ExportEntry
	for i := range entries {
		if entries[i].DisplayName == "Nested Page" {
			nested = &entries[i]
			break
		}
	}
	if nested == nil {
		t.Fatal("Nested Page entry not found")
	}
	if !strings.Contains(nested.RelPath, "Deep") {
		t.Errorf("expected nested page RelPath to contain 'Deep', got %q", nested.RelPath)
	}
}

func TestParseMarkdownEntry(t *testing.T) {
	dir := testdataDir(t)
	// Find the "Project Notes" page.
	var found bool
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.Contains(d.Name(), "Project Notes") && strings.HasSuffix(d.Name(), ".md") {
			found = true
			entry := parseMarkdownEntry(path, "", dir)
			if entry.NotionID != "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4" {
				t.Errorf("expected NotionID 'a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4', got %q", entry.NotionID)
			}
			if entry.DisplayName != "Project Notes" {
				t.Errorf("expected DisplayName 'Project Notes', got %q", entry.DisplayName)
			}
			if entry.Kind != EntryPage {
				t.Errorf("expected Kind EntryPage, got %q", entry.Kind)
			}
		}
		return nil
	})
	if !found {
		t.Fatal("Project Notes page not found in testdata")
	}
}

func TestBuildPageNameMap(t *testing.T) {
	dir := testdataDir(t)
	entries, err := discoverExport(dir)
	if err != nil {
		t.Fatalf("discoverExport: %v", err)
	}

	m := buildPageNameMap(entries)
	if len(m) < 4 {
		t.Errorf("expected at least 4 entries in page name map, got %d", len(m))
	}

	// Check a known ID maps to a known name.
	if name, ok := m["a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4"]; !ok || name != "Project Notes" {
		t.Errorf("expected 'Project Notes' for known ID, got %q (found=%v)", name, ok)
	}
}

func TestRewriteNotionLinks(t *testing.T) {
	pageMap := map[string]string{
		"a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4": "Project Notes",
		"b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5": "Getting Started",
		"c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6": "API Reference",
	}

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "double bracket with ID",
			input:    "See [[Getting Started b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5]] for details.",
			expected: "See [[Getting Started]] for details.",
		},
		{
			name:     "double bracket without ID",
			input:    "See [[Some Page]] for details.",
			expected: "See [[Some Page]] for details.",
		},
		{
			name:     "markdown link with ID",
			input:    "[API docs](API%20Reference%20c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6.md)",
			expected: "[[API Reference]]",
		},
		{
			name:     "plain markdown link",
			input:    "[click here](some-page.md)",
			expected: "[[click here]]",
		},
		{
			name:     "no links",
			input:    "Just plain text.",
			expected: "Just plain text.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rewriteNotionLinks(tt.input, pageMap, "")
			if got != tt.expected {
				t.Errorf("rewriteNotionLinks(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestSanitizeBaseName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Normal Name", "Normal Name"},
		{"Name/With/Slashes", "Name_With_Slashes"},
		{"Name:Colon", "Name_Colon"},
		{"", "untitled"},
		{"  spaces  ", "spaces"},
		{`Name"Quote`, "Name_Quote"},
	}
	for _, tt := range tests {
		got := sanitizeBaseName(tt.input)
		if got != tt.expected {
			t.Errorf("sanitizeBaseName(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestRun_DryRun(t *testing.T) {
	dir := testdataDir(t)
	tmpVault := t.TempDir()

	opts := Options{
		SourceDir: dir,
		Vault:     tmpVault,
		DryRun:    true,
	}

	stats, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if stats.Mode != "dry-run" {
		t.Errorf("expected mode 'dry-run', got %q", stats.Mode)
	}
	if stats.Imported != stats.Total {
		t.Errorf("expected Imported==Total for dry-run, got Imported=%d Total=%d", stats.Imported, stats.Total)
	}
	if stats.Failed != 0 {
		t.Errorf("expected 0 failures in dry-run, got %d", stats.Failed)
	}

	// Verify nothing was written to vault.
	vaultFiles := countMDFiles(t, tmpVault)
	if vaultFiles != 0 {
		t.Errorf("expected 0 vault files in dry-run, got %d", vaultFiles)
	}
}

func TestRun_Import(t *testing.T) {
	dir := testdataDir(t)
	tmpVault := t.TempDir()

	opts := Options{
		SourceDir: dir,
		Vault:     tmpVault,
	}

	stats, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if stats.Imported == 0 {
		t.Error("expected at least 1 imported note")
	}
	if stats.Failed != 0 {
		t.Errorf("expected 0 failures, got %d: %v", stats.Failed, stats.Warnings)
	}
	if stats.RunID == "" {
		t.Error("expected non-empty RunID")
	}

	// Verify vault files were created.
	vaultFiles := countMDFiles(t, tmpVault)
	if vaultFiles == 0 {
		t.Error("expected vault files to be created")
	}

	// Verify frontmatter contract: every note has imported_from, import_run_id, sha256.
	_ = filepath.WalkDir(tmpVault, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		s := string(data)
		if !strings.HasPrefix(s, "---\n") {
			t.Errorf("%s: missing YAML frontmatter", d.Name())
			return nil
		}
		if !strings.Contains(s, "imported_from: notion") {
			t.Errorf("%s: missing imported_from: notion", d.Name())
		}
		if !strings.Contains(s, "import_run_id:") {
			t.Errorf("%s: missing import_run_id", d.Name())
		}
		if !strings.Contains(s, "sha256:") {
			t.Errorf("%s: missing sha256", d.Name())
		}
		return nil
	})
}

func TestRun_Idempotency(t *testing.T) {
	dir := testdataDir(t)
	tmpVault := t.TempDir()
	runID := "notion-test-idempotent-20260101"

	// First run.
	opts := Options{
		SourceDir:   dir,
		Vault:       tmpVault,
		ImportRunID: runID,
	}
	stats1, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if stats1.Imported == 0 {
		t.Fatal("first run: expected at least 1 import")
	}

	// Second run with the same import_run_id.
	stats2, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}

	if stats2.Imported != 0 {
		t.Errorf("expected 0 imports on second run, got %d", stats2.Imported)
	}
	if stats2.Skipped == 0 {
		t.Error("expected some notes to be skipped on second run")
	}
	if stats2.Failed != 0 {
		t.Errorf("expected 0 failures on second run, got %d", stats2.Failed)
	}
}

func TestConvertCSV(t *testing.T) {
	dir := testdataDir(t)
	tmpVault := t.TempDir()

	// Find the CSV entry.
	entries, err := discoverExport(dir)
	if err != nil {
		t.Fatalf("discoverExport: %v", err)
	}

	var csvEntry *ExportEntry
	for i, e := range entries {
		if e.Kind == EntryCSV {
			csvEntry = &entries[i]
			break
		}
	}
	if csvEntry == nil {
		t.Fatal("no CSV entry found in testdata")
	}

	results := convertCSV(*csvEntry, tmpVault, "test-run", false)
	if len(results) < 4 {
		t.Fatalf("expected at least 4 results (3 rows + 1 index), got %d", len(results))
	}

	// Count imported vs failed.
	var imported, failed int
	for _, r := range results {
		switch r.Status {
		case "imported":
			imported++
		case "failed":
			failed++
			t.Logf("failed: %s", r.Error)
		}
	}
	if failed > 0 {
		t.Errorf("expected 0 failures, got %d", failed)
	}
	if imported < 4 {
		t.Errorf("expected at least 4 imports (3 rows + 1 index), got %d", imported)
	}

	// Verify the index note exists.
	indexPath := filepath.Join(tmpVault, "databases", "Tasks", "_index.md")
	if _, err := os.Stat(indexPath); os.IsNotExist(err) {
		t.Error("index note not created at expected path")
	}

	// Verify row notes exist.
	rowPath := filepath.Join(tmpVault, "databases", "Tasks", "Task Alpha.md")
	if _, err := os.Stat(rowPath); os.IsNotExist(err) {
		t.Error("row note for 'Task Alpha' not created")
	}

	// Verify row frontmatter contains CSV columns as top-level properties.
	data, err := os.ReadFile(rowPath)
	if err != nil {
		t.Fatalf("read row note: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "Status: Done") {
		t.Errorf("expected frontmatter to contain Status property, got:\n%s", content)
	}
	if !strings.Contains(content, "Priority: High") {
		t.Errorf("expected frontmatter to contain Priority property, got:\n%s", content)
	}
	if !strings.Contains(content, "Assignee: Alice") {
		t.Errorf("expected frontmatter to contain Assignee property, got:\n%s", content)
	}
}

func TestHierarchyPreserved(t *testing.T) {
	dir := testdataDir(t)
	tmpVault := t.TempDir()

	opts := Options{
		SourceDir: dir,
		Vault:     tmpVault,
	}

	_, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	nestedPath := filepath.Join(tmpVault, "Deep", "Nested Page.md")
	if _, err := os.Stat(nestedPath); os.IsNotExist(err) {
		t.Errorf("expected nested page at %s", nestedPath)
	}

	rootPages := []string{
		filepath.Join(tmpVault, "Project Notes.md"),
		filepath.Join(tmpVault, "Getting Started.md"),
		filepath.Join(tmpVault, "API Reference.md"),
	}
	for _, p := range rootPages {
		if _, err := os.Stat(p); os.IsNotExist(err) {
			t.Errorf("expected root page at %s", p)
		}
	}
}

func TestCSVRowFrontmatter(t *testing.T) {
	props := map[string]string{
		"Name":   "Task Alpha",
		"Status": "Done",
	}
	entry := ExportEntry{SourcePath: "/tmp/Tasks.csv", Category: "database"}
	fm := buildCSVRowFrontmatter(entry, props, "test-run", "abc123")

	if fm["Name"] != "Task Alpha" {
		t.Errorf("expected Name property in frontmatter, got %v", fm["Name"])
	}
	if fm["Status"] != "Done" {
		t.Errorf("expected Status property in frontmatter, got %v", fm["Status"])
	}
	if fm["source_path"] != "/tmp/Tasks.csv" {
		t.Errorf("expected source_path in frontmatter, got %v", fm["source_path"])
	}
	if fm["sha256"] != "abc123" {
		t.Errorf("expected sha256 in frontmatter, got %v", fm["sha256"])
	}

	collisionProps := map[string]string{
		"source_path": "colliding value",
		"Name":        "Task Beta",
	}
	fm2 := buildCSVRowFrontmatter(entry, collisionProps, "test-run", "def456")
	if fm2["csv_source_path"] != "colliding value" {
		t.Errorf("expected colliding source_path to be renamed to csv_source_path, got %v", fm2["csv_source_path"])
	}
	if fm2["source_path"] != "/tmp/Tasks.csv" {
		t.Errorf("expected source_path to keep contract value, got %v", fm2["source_path"])
	}
}

func TestCSVRowBody(t *testing.T) {
	props := map[string]string{
		"Name":   "Task Alpha",
		"Status": "Done",
	}
	body := buildCSVRowBody(props)
	if !strings.Contains(body, "**Name:** Task Alpha") {
		t.Errorf("expected body to contain Name field, got: %s", body)
	}
	if !strings.Contains(body, "**Status:** Done") {
		t.Errorf("expected body to contain Status field, got: %s", body)
	}
}

func TestCSVIndexBody(t *testing.T) {
	entry := ExportEntry{DisplayName: "Test DB"}
	headers := []string{"Name", "Status"}
	body := buildCSVIndexBody(entry, headers, 5)
	if !strings.Contains(body, "# Test DB") {
		t.Errorf("expected body to contain title, got: %s", body)
	}
	if !strings.Contains(body, "5 rows") {
		t.Errorf("expected body to contain row count, got: %s", body)
	}
}

func TestParseNoteFrontmatter(t *testing.T) {
	note := `---
source_path: /some/path.md
import_run_id: notion-test-123
sha256: abc123
---
# Body here
`
	meta := parseNoteFrontmatter([]byte(note))
	if meta == nil {
		t.Fatal("parseNoteFrontmatter returned nil")
	}
	if meta.SourcePath != "/some/path.md" {
		t.Errorf("expected SourcePath '/some/path.md', got %q", meta.SourcePath)
	}
	if meta.ImportRunID != "notion-test-123" {
		t.Errorf("expected ImportRunID 'notion-test-123', got %q", meta.ImportRunID)
	}
}

func TestParseNoteFrontmatter_NoFrontmatter(t *testing.T) {
	meta := parseNoteFrontmatter([]byte("Just plain text\n"))
	if meta != nil {
		t.Error("expected nil for text without frontmatter")
	}
}

// countMDFiles counts .md files in dir recursively.
func countMDFiles(t *testing.T, dir string) int {
	t.Helper()
	count := 0
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.HasSuffix(d.Name(), ".md") {
			count++
		}
		return nil
	})
	return count
}
