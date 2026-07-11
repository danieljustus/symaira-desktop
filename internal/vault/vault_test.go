package vault

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/config"
)

func TestParseFile(t *testing.T) {
	path, _ := filepath.Abs("../../testdata/vault/symingest-sample.md")
	doc, err := ParseFile(path)
	if err != nil {
		t.Fatalf("failed to parse file: %v", err)
	}

	if doc.Title != "Acme Corp Invoice July" {
		t.Errorf("expected title 'Acme Corp Invoice July', got '%s'", doc.Title)
	}

	if doc.Created != "2026-07-01T10:00:00Z" {
		t.Errorf("expected created '2026-07-01T10:00:00Z', got '%s'", doc.Created)
	}

	if len(doc.Tags) != 2 || doc.Tags[0] != "finance" || doc.Tags[1] != "receipt" {
		t.Errorf("unexpected tags: %v", doc.Tags)
	}

	if len(doc.Links) != 1 || doc.Links[0] != "Related Document" {
		t.Errorf("unexpected links: %v", doc.Links)
	}

	if !strings.Contains(doc.Body, "We should pay this by the end of the month.") {
		t.Errorf("body does not contain expected text")
	}

	if doc.Frontmatter["ocr_engine"] != "tesseract" {
		t.Errorf("expected ocr_engine 'tesseract', got '%v'", doc.Frontmatter["ocr_engine"])
	}
}

func TestExtractWikilinks(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{"plain", "See [[Note A]] and [[Note B]].", []string{"Note A", "Note B"}},
		{"display text", "See [[Target|shown text]].", []string{"Target"}},
		{"embed", "Inline embed: ![[Embedded Note]]", []string{"Embedded Note"}},
		{"embed with heading", "![[Note#Some Heading]]", []string{"Note"}},
		{"link with heading", "[[Note#Section]]", []string{"Note"}},
		{"block ref", "![[Note#^block-id]]", []string{"Note"}},
		{"deduplicated", "[[Note]] and ![[Note#Heading]]", []string{"Note"}},
		{"empty after strip", "[[#Heading only]]", nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractWikilinks(tc.body)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("link %d: got %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestWalk(t *testing.T) {
	path, _ := filepath.Abs("../../testdata/vault")
	count := 0
	err := Walk(path, func(p string) error {
		count++
		return nil
	})

	if err != nil {
		t.Fatalf("walk failed: %v", err)
	}

	if count != 8 {
		t.Fatalf("expected to find 8 files, found %d", count)
	}
}

func TestParseFileV2Metadata(t *testing.T) {
	path, _ := filepath.Abs("../../testdata/vault/v2-sample.md")
	doc, err := ParseFile(path)
	if err != nil {
		t.Fatalf("failed to parse v2 file: %v", err)
	}

	if doc.DocumentDate != "2026-08-01" {
		t.Errorf("expected document_date '2026-08-01', got '%s'", doc.DocumentDate)
	}
	if doc.Person != "Alice" {
		t.Errorf("expected person 'Alice', got '%s'", doc.Person)
	}
	if doc.Status != "open" {
		t.Errorf("expected status 'open', got '%s'", doc.Status)
	}
	if doc.DueDate != "2026-09-01" {
		t.Errorf("expected due_date '2026-09-01', got '%s'", doc.DueDate)
	}
	if doc.Confidence != 95 {
		t.Errorf("expected confidence 95, got %d", doc.Confidence)
	}
	if doc.OcrJSONPath != "/archive/utility-aug.ocr.json" {
		t.Errorf("expected ocr_json_path '/archive/utility-aug.ocr.json', got '%s'", doc.OcrJSONPath)
	}
	if doc.Simhash != "a1b2c3d4e5f6a7b8" {
		t.Errorf("expected simhash 'a1b2c3d4e5f6a7b8', got '%s'", doc.Simhash)
	}
}

func TestParseFileV1BackwardsCompatible(t *testing.T) {
	path, _ := filepath.Abs("../../testdata/vault/symingest-sample.md")
	doc, err := ParseFile(path)
	if err != nil {
		t.Fatalf("failed to parse v1 file: %v", err)
	}

	if doc.DocumentDate != "" {
		t.Errorf("expected empty document_date for v1 file, got '%s'", doc.DocumentDate)
	}
	if doc.Person != "" {
		t.Errorf("expected empty person for v1 file, got '%s'", doc.Person)
	}
	if doc.Status != "" {
		t.Errorf("expected empty status for v1 file, got '%s'", doc.Status)
	}
	if doc.Confidence != 0 {
		t.Errorf("expected confidence 0 for v1 file, got %d", doc.Confidence)
	}
}

func TestSetFrontmatterKeyReplaceExisting(t *testing.T) {
	tmpDir := t.TempDir()
	content := "---\ntitle: \"Test\"\nstatus: \"open\"\ntags:\n  - a\n---\n\nBody here.\n"
	path := filepath.Join(tmpDir, "test.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if err := SetFrontmatterKey(path, "status", "done"); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(path)
	result := string(data)
	if !strings.Contains(result, "status: \"done\"") {
		t.Errorf("expected status changed to done, got:\n%s", result)
	}
	if !strings.Contains(result, "tags:") {
		t.Errorf("tags should be preserved, got:\n%s", result)
	}
	if !strings.Contains(result, "Body here.") {
		t.Errorf("body should be preserved, got:\n%s", result)
	}

	doc, err := ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Status != "done" {
		t.Errorf("expected parsed status 'done', got '%s'", doc.Status)
	}
	if len(doc.Tags) != 1 || doc.Tags[0] != "a" {
		t.Errorf("expected tags preserved, got %v", doc.Tags)
	}
}

func TestSetFrontmatterKeyAddNew(t *testing.T) {
	tmpDir := t.TempDir()
	content := "---\ntitle: \"Test\"\ncreated: \"2026-01-01T00:00:00Z\"\n---\n\nBody.\n"
	path := filepath.Join(tmpDir, "test.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if err := SetFrontmatterKey(path, "due_date", "2026-12-31"); err != nil {
		t.Fatal(err)
	}

	doc, err := ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if doc.DueDate != "2026-12-31" {
		t.Errorf("expected due_date '2026-12-31', got '%s'", doc.DueDate)
	}
	if doc.Title != "Test" {
		t.Errorf("expected title preserved, got '%s'", doc.Title)
	}
}

func TestSetFrontmatterKeyNoFrontmatter(t *testing.T) {
	tmpDir := t.TempDir()
	content := "# Just a heading\n\nNo frontmatter here.\n"
	path := filepath.Join(tmpDir, "test.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if err := SetFrontmatterKey(path, "status", "open"); err != nil {
		t.Fatal(err)
	}

	doc, err := ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Status != "open" {
		t.Errorf("expected status 'open', got '%s'", doc.Status)
	}
}

func TestSetFrontmatterKeyNumericValue(t *testing.T) {
	tmpDir := t.TempDir()
	content := "---\ntitle: \"Test\"\nconfidence: 50\n---\n\nBody.\n"
	path := filepath.Join(tmpDir, "test.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if err := SetFrontmatterKey(path, "confidence", "95"); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(path)
	result := string(data)
	if !strings.Contains(result, "confidence: 95") {
		t.Errorf("expected confidence 95, got:\n%s", result)
	}
}

func TestSetFrontmatterValueReplaceStringPreservesOrder(t *testing.T) {
	tmpDir := t.TempDir()
	content := "---\ntitle: \"Test\"\nstatus: \"open\"\n# a comment\ntags:\n  - a\n---\n\nBody here.\n"
	path := filepath.Join(tmpDir, "test.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if err := SetFrontmatterValue(path, "status", "done"); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(path)
	result := string(data)
	if !strings.Contains(result, "status: \"done\"") {
		t.Errorf("expected status changed to done, got:\n%s", result)
	}
	if !strings.Contains(result, "title: \"Test\"") {
		t.Errorf("title should be preserved, got:\n%s", result)
	}
	if !strings.Contains(result, "# a comment") {
		t.Errorf("comment should be preserved, got:\n%s", result)
	}
	if !strings.Contains(result, "tags:") {
		t.Errorf("tags block should be preserved, got:\n%s", result)
	}
	if !strings.Contains(result, "Body here.") {
		t.Errorf("body should be preserved, got:\n%s", result)
	}

	doc, err := ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Status != "done" {
		t.Errorf("expected parsed status 'done', got '%s'", doc.Status)
	}
	if len(doc.Tags) != 1 || doc.Tags[0] != "a" {
		t.Errorf("expected tags preserved, got %v", doc.Tags)
	}
}

func TestSetFrontmatterValueAddNewPreservesOrder(t *testing.T) {
	tmpDir := t.TempDir()
	content := "---\ntitle: \"Test\"\ncreated: \"2026-01-01T00:00:00Z\"\n---\n\nBody.\n"
	path := filepath.Join(tmpDir, "test.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if err := SetFrontmatterValue(path, "due_date", "2026-12-31"); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(path)
	result := string(data)
	if !strings.Contains(result, "due_date: \"2026-12-31\"") {
		t.Errorf("expected due_date added, got:\n%s", result)
	}
	if !strings.Contains(result, "title: \"Test\"") {
		t.Errorf("expected title preserved, got:\n%s", result)
	}

	titleIdx := strings.Index(result, "title:")
	createdIdx := strings.Index(result, "created:")
	dueIdx := strings.Index(result, "due_date:")
	if !(titleIdx < createdIdx && createdIdx < dueIdx) {
		t.Errorf("expected key order title < created < due_date, got:\n%s", result)
	}

	doc, err := ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if doc.DueDate != "2026-12-31" {
		t.Errorf("expected due_date '2026-12-31', got '%s'", doc.DueDate)
	}
	if doc.Title != "Test" {
		t.Errorf("expected title preserved, got '%s'", doc.Title)
	}
}

func TestSetFrontmatterValueTypedValues(t *testing.T) {
	tmpDir := t.TempDir()
	content := "---\ntitle: \"Test\"\ncount: 1\nflag: false\ntags: []\n---\n\nBody.\n"
	path := filepath.Join(tmpDir, "test.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if err := SetFrontmatterValue(path, "count", 42); err != nil {
		t.Fatal(err)
	}
	if err := SetFrontmatterValue(path, "flag", true); err != nil {
		t.Fatal(err)
	}
	if err := SetFrontmatterValue(path, "tags", []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(path)
	result := string(data)
	if !strings.Contains(result, "count: 42") {
		t.Errorf("expected count 42, got:\n%s", result)
	}
	if !strings.Contains(result, "flag: true") {
		t.Errorf("expected flag true, got:\n%s", result)
	}
	if !strings.Contains(result, "tags: [\"a\", \"b\"]") {
		t.Errorf("expected inline tags array, got:\n%s", result)
	}

	doc, err := ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Frontmatter["count"] != 42 {
		t.Errorf("expected parsed count 42, got %v", doc.Frontmatter["count"])
	}
	if doc.Frontmatter["flag"] != true {
		t.Errorf("expected parsed flag true, got %v", doc.Frontmatter["flag"])
	}
	if len(doc.Tags) != 2 || doc.Tags[0] != "a" || doc.Tags[1] != "b" {
		t.Errorf("expected parsed tags [a b], got %v", doc.Tags)
	}
}

func TestSetFrontmatterValuePreservesCRLF(t *testing.T) {
	tmpDir := t.TempDir()
	content := "---\r\ntitle: \"Test\"\r\nstatus: \"open\"\r\n---\r\n\r\nBody.\r\n"
	path := filepath.Join(tmpDir, "test.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if err := SetFrontmatterValue(path, "status", "done"); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "\r\n") {
		t.Errorf("expected CRLF to be preserved")
	}
	lfCount := strings.Count(string(data), "\n")
	crlfCount := strings.Count(string(data), "\r\n")
	if lfCount != crlfCount {
		t.Errorf("expected every LF to be part of CRLF, got %d LF vs %d CRLF", lfCount, crlfCount)
	}

	doc, err := ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Status != "done" {
		t.Errorf("expected status 'done', got '%s'", doc.Status)
	}
}

func TestSetFrontmatterValueNoFrontmatter(t *testing.T) {
	tmpDir := t.TempDir()
	content := "# Just a heading\n\nNo frontmatter here.\n"
	path := filepath.Join(tmpDir, "test.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if err := SetFrontmatterValue(path, "status", "open"); err != nil {
		t.Fatal(err)
	}

	doc, err := ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Status != "open" {
		t.Errorf("expected status 'open', got '%s'", doc.Status)
	}
	if !strings.Contains(doc.Body, "# Just a heading") {
		t.Errorf("body should be preserved, got '%s'", doc.Body)
	}
}

func TestDeleteFrontmatterValue(t *testing.T) {
	tmpDir := t.TempDir()
	content := "---\ntitle: \"Test\"\nstatus: \"open\"\ntags:\n  - a\n---\n\nBody.\n"
	path := filepath.Join(tmpDir, "test.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if err := DeleteFrontmatterValue(path, "status"); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(path)
	result := string(data)
	if strings.Contains(result, "status:") {
		t.Errorf("expected status removed, got:\n%s", result)
	}
	if !strings.Contains(result, "title: \"Test\"") {
		t.Errorf("expected title preserved, got:\n%s", result)
	}
	if !strings.Contains(result, "tags:") {
		t.Errorf("expected tags preserved, got:\n%s", result)
	}
	if !strings.Contains(result, "Body.") {
		t.Errorf("expected body preserved, got:\n%s", result)
	}

	doc, err := ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Status != "" {
		t.Errorf("expected empty status, got '%s'", doc.Status)
	}
	if doc.Title != "Test" {
		t.Errorf("expected title preserved, got '%s'", doc.Title)
	}
}

func TestDeleteFrontmatterValueMissingKey(t *testing.T) {
	tmpDir := t.TempDir()
	content := "---\ntitle: \"Test\"\n---\n\nBody.\n"
	path := filepath.Join(tmpDir, "test.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if err := DeleteFrontmatterValue(path, "status"); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(path)
	result := string(data)
	if !strings.Contains(result, "title: \"Test\"") {
		t.Errorf("expected file unchanged, got:\n%s", result)
	}
}

// --- ResolveVaultRoot tests (coverage target: vault.go:56-81) ---

func TestResolveVaultRoot_MissingEnvAndConfig(t *testing.T) {
	_, err := ResolveVaultRoot("", nil)
	if err == nil {
		t.Fatal("expected error for empty flag and nil config")
	}
	if !strings.Contains(err.Error(), "vault path not configured") {
		t.Errorf("unexpected error: %v", err)
	}

	cfg := &config.Config{Vault: ""}
	_, err = ResolveVaultRoot("", cfg)
	if err == nil {
		t.Fatal("expected error for empty flag and empty config vault")
	}
	if !strings.Contains(err.Error(), "vault path not configured") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestResolveVaultRoot_MissingDirectory(t *testing.T) {
	_, err := ResolveVaultRoot("/nonexistent/path/to/vault", nil)
	if err == nil {
		t.Fatal("expected error for non-existent directory")
	}
	if !strings.Contains(err.Error(), "vault path does not exist") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestResolveVaultRoot_NonDirectoryPath(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "not-a-dir.md")
	if err := os.WriteFile(filePath, []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := ResolveVaultRoot(filePath, nil)
	if err == nil {
		t.Fatal("expected error for non-directory path")
	}
	if !strings.Contains(err.Error(), "vault path is not a directory") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestResolveVaultRoot_ExplicitFlagPrecedence(t *testing.T) {
	flagDir := t.TempDir()
	cfgDir := t.TempDir()

	cfg := &config.Config{Vault: cfgDir}
	got, err := ResolveVaultRoot(flagDir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	absFlag, _ := filepath.Abs(flagDir)
	if got != absFlag {
		t.Errorf("expected flag path %s, got %s", absFlag, got)
	}
}

func TestResolveVaultRoot_RelativePathResolvesToAbsolute(t *testing.T) {
	tmpDir := t.TempDir()
	marker := filepath.Join(tmpDir, "marker.txt")
	if err := os.WriteFile(marker, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	origDir, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)

	got, err := ResolveVaultRoot(".", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("expected absolute path, got %s", got)
	}
}

// --- SecurePath tests for absolute paths outside vault (coverage target: vault.go:215-231) ---

func TestSecurePath_AbsolutePathOutsideVault(t *testing.T) {
	root := t.TempDir()

	// filepath.Join treats the second arg as relative even if absolute,
	// so "/etc/passwd" becomes <root>/etc/passwd — inside the vault.
	// The real traversal test uses ".." (already in TestSecurePath).
	// Verify that an absolute relPath does not escape.
	abs, err := SecurePath(root, "/etc/passwd")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	canonicalRoot, _ := filepath.EvalSymlinks(root)
	if !strings.HasPrefix(abs, canonicalRoot) {
		t.Errorf("expected path inside vault, got %s", abs)
	}
}

func TestSecurePath_DeepTraversalDenied(t *testing.T) {
	root := t.TempDir()

	if _, err := SecurePath(root, "a/../../etc/passwd"); err == nil {
		t.Error("expected deep traversal to be denied")
	}
	if _, err := SecurePath(root, "notes/../../etc/shadow"); err == nil {
		t.Error("expected nested traversal to be denied")
	}
}

func TestSecurePath_WithinVault(t *testing.T) {
	root := t.TempDir()

	abs, err := SecurePath(root, "notes/subdir/file.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	canonicalRoot, _ := filepath.EvalSymlinks(root)
	expected := filepath.Join(canonicalRoot, "notes", "subdir", "file.md")
	if abs != expected {
		t.Errorf("expected %s, got %s", expected, abs)
	}
}
