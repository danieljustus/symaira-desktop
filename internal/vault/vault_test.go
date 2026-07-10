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
	if strings.HasPrefix(abs, root) {
		return
	}
	t.Errorf("expected path inside vault, got %s", abs)
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
	expected := filepath.Join(root, "notes", "subdir", "file.md")
	if abs != expected {
		t.Errorf("expected %s, got %s", expected, abs)
	}
}
