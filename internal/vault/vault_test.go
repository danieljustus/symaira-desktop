package vault

import (
	"path/filepath"
	"strings"
	"testing"
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

	if count != 2 {
		t.Errorf("expected to find 2 files, found %d", count)
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
