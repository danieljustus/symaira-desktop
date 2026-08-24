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

// TestParseFileCapturesStatForIndexFastPath guards the fields RefreshIndex's
// stat-based skip check depends on (issue #180): ParseFile must report the
// real on-disk size and mtime, and ParseBytes (used when a caller already
// has the bytes and no path to stat) must at least report the correct size
// while leaving ModTime zero rather than guessing.
func TestParseFileCapturesStatForIndexFastPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.md")
	content := []byte("---\ntitle: Note\n---\nBody")
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	doc, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile failed: %v", err)
	}
	if doc.Size != int64(len(content)) {
		t.Errorf("expected Size %d, got %d", len(content), doc.Size)
	}
	if !doc.ModTime.Equal(info.ModTime()) {
		t.Errorf("expected ModTime %v, got %v", info.ModTime(), doc.ModTime)
	}

	bytesDoc, err := ParseBytes(path, content)
	if err != nil {
		t.Fatalf("ParseBytes failed: %v", err)
	}
	if bytesDoc.Size != int64(len(content)) {
		t.Errorf("expected ParseBytes Size %d, got %d", len(content), bytesDoc.Size)
	}
	if !bytesDoc.ModTime.IsZero() {
		t.Errorf("expected ParseBytes to leave ModTime zero (unknown), got %v", bytesDoc.ModTime)
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
		{"attachment embed", "![[scan.png]] and ![[report.pdf]]", []string{"scan.png", "report.pdf"}},
		{"dataview code block ignored", "```dataview\nTABLE [[Fake Note]] FROM #tag\n```\n[[Real Note]]", []string{"Real Note"}},
		{"symdesk-base code block ignored", "```symdesk-base\nbase: [[Fake Base]]\nview: v1\n```\n[[Real Note]]", []string{"Real Note"}},
		{"templater code block ignored", "```templater\n<% tp.file.include(\"[[Template Link]]\") %>\n```\n![[embed.png]]", []string{"embed.png"}},
		{"inline code span ignored", "Code: `[[Inline Link]]` and prose [[Prose Link]]", []string{"Prose Link"}},
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

// TestWalkSkipsDependencyDirectories guards issue #227: pointing the vault at
// a folder that happens to contain coding projects must not walk into
// dependency/build directories such as node_modules, even though they are
// not dot-prefixed.
func TestWalkSkipsDependencyDirectories(t *testing.T) {
	root := t.TempDir()

	skipped := []string{"node_modules", "vendor", "dist", "build", "venv", ".venv", "__pycache__"}
	for _, dirName := range skipped {
		dir := filepath.Join(root, dirName)
		if err := os.MkdirAll(dir, 0755); err != nil { //nolint:gosec // test temp directory
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "readme.md"), []byte("# should be skipped"), 0644); err != nil { //nolint:gosec // test temp file
			t.Fatal(err)
		}
	}

	// A legitimate vault note at the root must still be visited.
	wantPath := filepath.Join(root, "note.md")
	if err := os.WriteFile(wantPath, []byte("# real note"), 0644); err != nil { //nolint:gosec // test temp file
		t.Fatal(err)
	}

	var visited []string
	err := Walk(root, func(p string) error {
		visited = append(visited, p)
		return nil
	})
	if err != nil {
		t.Fatalf("walk failed: %v", err)
	}

	if len(visited) != 1 || visited[0] != wantPath {
		t.Fatalf("expected only %q to be visited, got %v", wantPath, visited)
	}
}

func TestWalkAll(t *testing.T) {
	root := t.TempDir()

	// Note
	notePath := filepath.Join(root, "note.md")
	if err := os.WriteFile(notePath, []byte("# Note"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Attachment in assets
	assetsDir := filepath.Join(root, "assets")
	if err := os.MkdirAll(assetsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	imgPath := filepath.Join(assetsDir, "scan.png")
	if err := os.WriteFile(imgPath, []byte("png-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Canvas
	canvasPath := filepath.Join(root, "diagram.canvas")
	if err := os.WriteFile(canvasPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Skipped node_modules attachment
	nmDir := filepath.Join(root, "node_modules", "pkg")
	if err := os.MkdirAll(nmDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nmDir, "ignored.png"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Hidden file
	if err := os.WriteFile(filepath.Join(root, ".hidden.png"), []byte("hidden"), 0o600); err != nil {
		t.Fatal(err)
	}

	var visited []string
	err := WalkAll(root, func(p string, d os.DirEntry) error {
		if !d.IsDir() {
			visited = append(visited, p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkAll failed: %v", err)
	}

	if len(visited) != 3 {
		t.Fatalf("expected 3 visited files, got %d: %v", len(visited), visited)
	}
}

func TestParseFileExcalidrawExcludesBody(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sketch.excalidraw.md")
	content := `---
title: "System Architecture"
tags:
  - diagram
---

# Drawing Data
` + "```json\n{\"elements\": [{\"type\": \"rectangle\", \"id\": \"123\"}]}\n```\n"

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	doc, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile failed: %v", err)
	}

	if doc.Title != "System Architecture" {
		t.Errorf("expected Title 'System Architecture', got %q", doc.Title)
	}
	if len(doc.Tags) != 1 || doc.Tags[0] != "diagram" {
		t.Errorf("expected Tags ['diagram'], got %v", doc.Tags)
	}
	if doc.Body != "" {
		t.Errorf("expected Body to be empty for .excalidraw.md, got %q", doc.Body)
	}
	if len(doc.Links) != 0 {
		t.Errorf("expected 0 links for .excalidraw.md, got %v", doc.Links)
	}
	if doc.Size != int64(len(content)) {
		t.Errorf("expected Size %d, got %d", len(content), doc.Size)
	}
}

func TestDelegatedFormatHelpers(t *testing.T) {
	if !IsExcalidrawFile("my-drawing.excalidraw.md") {
		t.Errorf("expected true for my-drawing.excalidraw.md")
	}
	if !IsExcalidrawFile("PATH/TO/DRAWING.EXCALIDRAW.MD") {
		t.Errorf("expected true for case-insensitive excalidraw")
	}
	if IsExcalidrawFile("note.md") {
		t.Errorf("expected false for note.md")
	}

	if !IsCanvasFile("board.canvas") {
		t.Errorf("expected true for board.canvas")
	}
	if !IsCanvasFile("BOARDS/PROJECT.CANVAS") {
		t.Errorf("expected true for case-insensitive canvas")
	}
	if IsCanvasFile("board.md") {
		t.Errorf("expected false for board.md")
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

func TestParseFile_DerivedArtifact(t *testing.T) {
	root := t.TempDir()

	// 1. Note with derived_from string
	p1 := filepath.Join(root, "derived1.md")
	if err := os.WriteFile(p1, []byte("---\ntitle: \"Summary\"\nderived_from: \"source.md\"\n---\nBody"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc1, err := ParseFile(p1)
	if err != nil {
		t.Fatal(err)
	}
	if !doc1.IsDerived() || !doc1.Derived || doc1.DerivedFrom != "source.md" {
		t.Errorf("expected derived document from source.md, got Derived=%v DerivedFrom=%q", doc1.Derived, doc1.DerivedFrom)
	}

	// 2. Note with derived: true
	p2 := filepath.Join(root, "derived2.md")
	if err := os.WriteFile(p2, []byte("---\ntitle: \"Generated\"\nderived: true\n---\nBody"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc2, err := ParseFile(p2)
	if err != nil {
		t.Fatal(err)
	}
	if !doc2.IsDerived() || !doc2.Derived {
		t.Errorf("expected IsDerived=true, got %v", doc2.IsDerived())
	}

	// 3. Regular note
	p3 := filepath.Join(root, "regular.md")
	if err := os.WriteFile(p3, []byte("---\ntitle: \"Regular\"\n---\nBody"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc3, err := ParseFile(p3)
	if err != nil {
		t.Fatal(err)
	}
	if doc3.IsDerived() || doc3.Derived || doc3.DerivedFrom != "" {
		t.Errorf("expected regular non-derived document, got IsDerived=%v", doc3.IsDerived())
	}
}

func TestParseFile_BaseKind(t *testing.T) {
	root := t.TempDir()

	// 1. Explicit type: base
	p1 := filepath.Join(root, "base1.md")
	if err := os.WriteFile(p1, []byte("---\ntitle: \"Invoices Base\"\ntype: base\nbase_id: invoices\n---\n# Invoices"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc1, err := ParseFile(p1)
	if err != nil {
		t.Fatal(err)
	}
	if doc1.Type != "base" {
		t.Errorf("expected doc1.Type='base', got %q", doc1.Type)
	}

	// 2. Inferred from base_id
	p2 := filepath.Join(root, "base2.md")
	if err := os.WriteFile(p2, []byte("---\ntitle: \"Tasks Base\"\nbase_id: tasks\n---\n# Tasks"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc2, err := ParseFile(p2)
	if err != nil {
		t.Fatal(err)
	}
	if doc2.Type != "base" {
		t.Errorf("expected doc2.Type='base' inferred from base_id, got %q", doc2.Type)
	}
}
