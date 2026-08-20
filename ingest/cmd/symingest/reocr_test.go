package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-ingest/internal/store"
)

type reprocessResponseTest struct {
	SchemaVersion int    `json:"schema_version"`
	DocumentID    int64  `json:"document_id"`
	JobID         int64  `json:"job_id"`
	Status        string `json:"status"`
	OutputPath    string `json:"output_path"`
	Error         *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func setupReprocessFixture(t *testing.T) (dbPath, vault, archive, source string, doc *store.Document) {
	t.Helper()
	dir := t.TempDir()
	dbPath = filepath.Join(dir, "symingest.db")
	vault = filepath.Join(dir, "vault")
	archive = filepath.Join(dir, "archive")
	source = filepath.Join(dir, "source.txt")
	if err := os.WriteFile(source, []byte("archived source text\n"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}

	withCapturedStdout(t)
	if err := run([]string{"ingest", "-db", dbPath, "-vault", vault, "-archive", archive, source}); err != nil {
		t.Fatalf("initial ingest: %v", err)
	}

	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	jobs, err := st.ListJobs(context.Background(), 1)
	if err != nil {
		st.Close()
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs) != 1 {
		st.Close()
		t.Fatalf("expected one initial job, got %d", len(jobs))
	}
	doc, err = st.ByID(context.Background(), jobs[0].DocumentID)
	if err != nil {
		st.Close()
		t.Fatalf("get document: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	if doc.ArchivePath == nil || doc.VaultPath == nil {
		t.Fatalf("initial ingest did not record archive/vault paths: %+v", doc)
	}
	return dbPath, vault, archive, source, doc
}

func parseReprocessJSON(t *testing.T, output string) reprocessResponseTest {
	t.Helper()
	var response reprocessResponseTest
	if err := json.Unmarshal([]byte(output), &response); err != nil {
		t.Fatalf("parse reprocess JSON %q: %v", output, err)
	}
	return response
}

func TestRun_ReocrByDocumentIDPreservesFrontmatter(t *testing.T) {
	dbPath, vault, archive, _, doc := setupReprocessFixture(t)
	_ = archive

	notePath := *doc.VaultPath
	note, err := os.ReadFile(notePath)
	if err != nil {
		t.Fatalf("read note: %v", err)
	}
	updated := strings.Replace(string(note), "category: \"\"\n", "category: \"\"\ncustom_property: keep-me\n", 1)
	updated = strings.Replace(updated, "archived source text", "stale manually edited body", 1)
	if updated == string(note) {
		t.Fatal("fixture note was not changed")
	}
	if err := os.WriteFile(notePath, []byte(updated), 0o600); err != nil {
		t.Fatalf("write edited note: %v", err)
	}

	sb := withCapturedStdout(t)
	err = run([]string{"reocr", "--json", "-db", dbPath, "-vault", vault, "--document-id", "1"})
	if err != nil {
		t.Fatalf("reocr: %v", err)
	}
	response := parseReprocessJSON(t, sb.String())
	if response.SchemaVersion != 1 || response.DocumentID != doc.ID || response.JobID == 0 || response.Status != "completed" {
		t.Fatalf("unexpected response: %+v", response)
	}
	if response.OutputPath != notePath {
		t.Fatalf("output_path = %q, want %q", response.OutputPath, notePath)
	}

	result, err := os.ReadFile(notePath)
	if err != nil {
		t.Fatalf("read reprocessed note: %v", err)
	}
	if !strings.Contains(string(result), "custom_property: keep-me") {
		t.Fatalf("reprocessing dropped user frontmatter: %s", result)
	}
	if !strings.Contains(string(result), "archived source text") || strings.Contains(string(result), "stale manually edited body") {
		t.Fatalf("reprocessing did not replace note body: %s", result)
	}
}

func TestRun_ReocrByArchivePathIsRepeatableWithoutDuplicateDocuments(t *testing.T) {
	dbPath, vault, archive, _, doc := setupReprocessFixture(t)
	_ = archive
	sb := withCapturedStdout(t)
	if err := run([]string{"reocr", "--json", "-db", dbPath, "-vault", vault, *doc.ArchivePath}); err != nil {
		t.Fatalf("first reocr: %v", err)
	}
	first := parseReprocessJSON(t, sb.String())
	sb.Reset()
	if err := run([]string{"reocr", "--json", "-db", dbPath, "-vault", vault, *doc.ArchivePath}); err != nil {
		t.Fatalf("second reocr: %v", err)
	}
	second := parseReprocessJSON(t, sb.String())
	if first.Status != "completed" || second.Status != "completed" || first.OutputPath != second.OutputPath {
		t.Fatalf("unexpected repeat responses: first=%+v second=%+v", first, second)
	}

	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	jobs, err := st.ListJobs(context.Background(), 0)
	if err != nil {
		st.Close()
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs) != 3 {
		st.Close()
		t.Fatalf("expected one ingest job plus two explicit reocr jobs, got %d", len(jobs))
	}
	if _, err := st.ByID(context.Background(), doc.ID); err != nil {
		t.Fatalf("document was not retained: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
}

func TestRun_ReocrMissingArchivedSourceReturnsVersionedJSONError(t *testing.T) {
	dbPath, vault, archive, _, doc := setupReprocessFixture(t)
	_ = archive
	if err := os.Remove(*doc.ArchivePath); err != nil {
		t.Fatalf("remove archive: %v", err)
	}

	sb := withCapturedStdout(t)
	err := run([]string{"reocr", "--json", "-db", dbPath, "-vault", vault, "--document-id", "1"})
	if err == nil {
		t.Fatal("expected missing archive error")
	}
	response := parseReprocessJSON(t, sb.String())
	if response.SchemaVersion != 1 || response.DocumentID != doc.ID || response.Status != "failed" || response.Error == nil {
		t.Fatalf("unexpected error response: %+v", response)
	}
	if response.Error.Code != "source_missing" {
		t.Fatalf("error code = %q, want source_missing", response.Error.Code)
	}
}

func TestRun_ReocrInvalidDocumentIDReturnsVersionedJSONError(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "symingest.db")
	vault := t.TempDir()
	sb := withCapturedStdout(t)
	err := run([]string{"reocr", "--json", "-db", dbPath, "-vault", vault, "--document-id", "not-an-integer"})
	if err == nil {
		t.Fatal("expected invalid document ID error")
	}
	response := parseReprocessJSON(t, sb.String())
	if response.SchemaVersion != 1 || response.Status != "failed" || response.Error == nil {
		t.Fatalf("unexpected error response: %+v", response)
	}
	if response.Error.Code != "invalid_identifier" {
		t.Fatalf("error code = %q, want invalid_identifier", response.Error.Code)
	}
}
