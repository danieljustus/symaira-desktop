package paperlessimport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-ingest/internal/extract"
	"github.com/danieljustus/symaira-ingest/internal/ingest"
	"github.com/danieljustus/symaira-ingest/internal/paperless"
	"github.com/danieljustus/symaira-ingest/internal/store"
	"github.com/danieljustus/symaira-ingest/internal/writer"
)

type fakeEngine struct{}

func (fakeEngine) Extract(ctx context.Context, path string, kind extract.Kind) (*extract.Result, error) {
	return &extract.Result{Text: "extracted text", MIME: string(kind), Engine: "fake"}, nil
}

// handleEmptyLookups serves an empty paginated result for the four
// Paperless lookup endpoints that Run() always loads before importing.
func handleEmptyLookups(w http.ResponseWriter, r *http.Request) bool {
	switch r.URL.Path {
	case "/api/tags/", "/api/correspondents/", "/api/document_types/", "/api/storage_paths/":
		json.NewEncoder(w).Encode(map[string]any{"count": 0, "results": []any{}, "next": nil})
		return true
	default:
		return false
	}
}

func TestRun_DryRun(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleEmptyLookups(w, r) {
			return
		}
		switch r.URL.Path {
		case "/api/documents/":
			json.NewEncoder(w).Encode(map[string]any{
				"count": 1,
				"results": []map[string]any{
					{"id": 1, "title": "Test Doc", "created_date": "2026-01-15T00:00:00Z", "file_type": ".pdf"},
				},
				"next": nil,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	pipeline := &ingest.Pipeline{
		Engine:     fakeEngine{},
		Store:      s,
		Writer:     &writer.NoteWriter{Vault: filepath.Join(dir, "vault")},
		ArchiveDir: filepath.Join(dir, "archive"),
	}

	stats, err := Run(context.Background(), Options{
		BaseURL: srv.URL,
		Token:   "test-token",
		DryRun:  true,
	}, pipeline)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Total != 1 {
		t.Errorf("stats.Total = %d, want 1", stats.Total)
	}
	if stats.Skipped != 1 {
		t.Errorf("stats.Skipped = %d, want 1", stats.Skipped)
	}
	if stats.Imported != 0 {
		t.Errorf("stats.Imported = %d, want 0", stats.Imported)
	}
	if stats.Audit == nil {
		t.Fatal("stats.Audit = nil, want a populated audit report")
	}
	if stats.Audit.TotalDocuments != 1 {
		t.Errorf("stats.Audit.TotalDocuments = %d, want 1", stats.Audit.TotalDocuments)
	}
}

func TestRun_DryRun_AuditReport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/documents/":
			json.NewEncoder(w).Encode(map[string]any{
				"count": 3,
				"results": []map[string]any{
					{
						"id": 1, "title": "Invoice", "created_date": "2026-01-15",
						"file_type": ".pdf", "mime_type": "application/pdf",
						"tags":          []map[string]any{{"id": 1, "name": "financial"}},
						"correspondent": map[string]any{"id": 1, "name": "Acme Corp"},
						"document_type": map[string]any{"id": 1, "name": "Invoice"},
					},
					{
						"id": 2, "title": "Receipt", "created_date": "2026-01-16",
						"file_type": ".pdf", "mime_type": "application/pdf",
						"tags": []int{99}, // unresolved tag ID
					},
					{
						"id": 3, "title": "Spreadsheet", "created_date": "2026-01-17",
						"file_type": ".xlsx", "mime_type": "application/vnd.ms-excel",
					},
				},
				"next": nil,
			})
		case "/api/tags/":
			json.NewEncoder(w).Encode(map[string]any{
				"count": 1, "results": []map[string]any{{"id": 1, "name": "financial"}}, "next": nil,
			})
		case "/api/correspondents/", "/api/document_types/", "/api/storage_paths/":
			json.NewEncoder(w).Encode(map[string]any{"count": 0, "results": []any{}, "next": nil})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	pipeline := &ingest.Pipeline{
		Store:      s,
		Writer:     &writer.NoteWriter{Vault: filepath.Join(dir, "vault")},
		ArchiveDir: filepath.Join(dir, "archive"),
	}

	stats, err := Run(context.Background(), Options{
		BaseURL: srv.URL,
		Token:   "test-token",
		DryRun:  true,
	}, pipeline)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	audit := stats.Audit
	if audit == nil {
		t.Fatal("stats.Audit = nil, want a populated audit report")
	}
	if audit.TotalDocuments != 3 {
		t.Errorf("TotalDocuments = %d, want 3", audit.TotalDocuments)
	}
	if audit.ByMIME["application/pdf"] != 2 {
		t.Errorf("ByMIME[application/pdf] = %d, want 2", audit.ByMIME["application/pdf"])
	}
	if audit.TagCounts["financial"] != 1 {
		t.Errorf("TagCounts[financial] = %d, want 1", audit.TagCounts["financial"])
	}
	if audit.CorrespondentCounts["Acme Corp"] != 1 {
		t.Errorf("CorrespondentCounts[Acme Corp] = %d, want 1", audit.CorrespondentCounts["Acme Corp"])
	}
	if len(audit.UnresolvedTagIDs) != 1 || audit.UnresolvedTagIDs[0] != 99 {
		t.Errorf("UnresolvedTagIDs = %v, want [99]", audit.UnresolvedTagIDs)
	}
	if audit.UnsupportedFileTypes["xlsx"] != 0 {
		t.Errorf("UnsupportedFileTypes[xlsx] = %d, want 0 after native XLSX support", audit.UnsupportedFileTypes["xlsx"])
	}
	if stats.Imported != 0 {
		t.Errorf("stats.Imported = %d, want 0 (dry-run must not write anything)", stats.Imported)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "vault") + "/*.md")
	if len(matches) != 0 {
		t.Fatalf("dry-run must not write notes, found %d", len(matches))
	}
}

func TestRun_Import(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleEmptyLookups(w, r) {
			return
		}
		switch {
		case r.URL.Path == "/api/documents/":
			json.NewEncoder(w).Encode(map[string]any{
				"count": 1,
				"results": []map[string]any{
					{
						"id": 1, "title": "Invoice", "created_date": "2026-01-15T00:00:00Z",
						"file_type":     ".txt",
						"tags":          []map[string]any{{"id": 1, "name": "financial"}},
						"correspondent": map[string]any{"id": 1, "name": "Acme Corp"},
						"document_type": map[string]any{"id": 1, "name": "Invoice"},
					},
				},
				"next": nil,
			})
		case r.URL.Path == "/api/documents/1/download/":
			w.Write([]byte("invoice content"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	pipeline := &ingest.Pipeline{
		Engine:     fakeEngine{},
		Store:      s,
		Writer:     &writer.NoteWriter{Vault: filepath.Join(dir, "vault")},
		ArchiveDir: filepath.Join(dir, "archive"),
	}

	stats, err := Run(context.Background(), Options{
		BaseURL: srv.URL,
		Token:   "test-token",
	}, pipeline)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Imported != 1 {
		t.Errorf("stats.Imported = %d, want 1", stats.Imported)
	}
	if stats.Failed != 0 {
		t.Errorf("stats.Failed = %d, want 0", stats.Failed)
	}

	matches, _ := filepath.Glob(filepath.Join(dir, "vault") + "/*.md")
	if len(matches) != 1 {
		t.Fatalf("expected 1 note, got %d", len(matches))
	}
}

func TestRun_ResolvesIDsViaLookups(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/documents/":
			json.NewEncoder(w).Encode(map[string]any{
				"count": 1,
				"results": []map[string]any{
					{
						"id": 1, "title": "Invoice", "created_date": "2026-01-15",
						"file_type":     ".txt",
						"tags":          []int{1, 99},
						"correspondent": 2,
						"document_type": 3,
						"storage_path":  7,
					},
				},
				"next": nil,
			})
		case "/api/documents/1/download/":
			w.Write([]byte("invoice content"))
		case "/api/tags/":
			json.NewEncoder(w).Encode(map[string]any{
				"count": 1, "results": []map[string]any{{"id": 1, "name": "financial"}}, "next": nil,
			})
		case "/api/correspondents/":
			json.NewEncoder(w).Encode(map[string]any{
				"count": 1, "results": []map[string]any{{"id": 2, "name": "Acme Corp"}}, "next": nil,
			})
		case "/api/document_types/":
			json.NewEncoder(w).Encode(map[string]any{
				"count": 1, "results": []map[string]any{{"id": 3, "name": "Invoice"}}, "next": nil,
			})
		case "/api/storage_paths/":
			json.NewEncoder(w).Encode(map[string]any{"count": 0, "results": []any{}, "next": nil})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	pipeline := &ingest.Pipeline{
		Engine:     fakeEngine{},
		Store:      s,
		Writer:     &writer.NoteWriter{Vault: filepath.Join(dir, "vault")},
		ArchiveDir: filepath.Join(dir, "archive"),
	}

	stats, err := Run(context.Background(), Options{
		BaseURL: srv.URL,
		Token:   "test-token",
	}, pipeline)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Imported != 1 {
		t.Errorf("stats.Imported = %d, want 1", stats.Imported)
	}

	// Tag 99, correspondent and document type resolve via lookups; tag 99
	// and the storage path have no matching entry and must be flagged.
	if len(stats.Warnings) != 2 {
		t.Fatalf("stats.Warnings = %v, want 2 entries (unresolved tag 99, storage path 7)", stats.Warnings)
	}

	matches, _ := filepath.Glob(filepath.Join(dir, "vault") + "/*.md")
	if len(matches) != 1 {
		t.Fatalf("expected 1 note, got %d", len(matches))
	}
	contentBytes, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	content := string(contentBytes)
	if !contains(content, "financial") {
		t.Errorf("note content missing resolved tag name 'financial':\n%s", content)
	}
	if !contains(content, "Acme Corp") {
		t.Errorf("note content missing resolved correspondent name 'Acme Corp':\n%s", content)
	}
}

func TestRun_PreservesPaperlessMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/documents/":
			json.NewEncoder(w).Encode(map[string]any{
				"count": 1,
				"results": []map[string]any{
					{
						"id": 7, "title": "Migrated Invoice",
						"created_date": "2024-03-01", "created": "2024-03-01T09:00:00Z",
						"added": "2024-03-02T10:00:00Z", "modified": "2024-03-05T11:30:00Z",
						"file_type":          ".pdf",
						"original_file_name": "invoice-original.pdf",
						"archived_file_name": "invoice-archived.pdf",
						"page_count":         3,
						"storage_path":       map[string]any{"id": 7, "name": "Invoices/2024"},
					},
				},
				"next": nil,
			})
		case "/api/documents/7/download/":
			w.Write([]byte("invoice content"))
		case "/api/tags/", "/api/correspondents/", "/api/document_types/", "/api/storage_paths/":
			json.NewEncoder(w).Encode(map[string]any{"count": 0, "results": []any{}, "next": nil})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	pipeline := &ingest.Pipeline{
		Engine:     fakeEngine{},
		Store:      s,
		Writer:     &writer.NoteWriter{Vault: filepath.Join(dir, "vault")},
		ArchiveDir: filepath.Join(dir, "archive"),
	}

	stats, err := Run(context.Background(), Options{
		BaseURL: srv.URL,
		Token:   "test-token",
	}, pipeline)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Imported != 1 {
		t.Fatalf("stats.Imported = %d, want 1", stats.Imported)
	}

	matches, _ := filepath.Glob(filepath.Join(dir, "vault") + "/*.md")
	if len(matches) != 1 {
		t.Fatalf("expected 1 note, got %d", len(matches))
	}
	contentBytes, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	content := string(contentBytes)

	for _, needle := range []string{
		"paperless:",
		"document_id: 7",
		"title: Migrated Invoice",
		"created: 2024-03-01T09:00:00Z",
		"added: 2024-03-02T10:00:00Z",
		"modified: 2024-03-05T11:30:00Z",
		"storage_path: Invoices/2024",
		"original_file_name: invoice-original.pdf",
		"archived_file_name: invoice-archived.pdf",
		"page_count: 3",
		"url: " + srv.URL + "/documents/7",
	} {
		if !contains(content, needle) {
			t.Errorf("note content missing %q:\n%s", needle, content)
		}
	}
}

func TestRun_PaperlessTraceabilityAndDownloadHeaderExtension(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/documents/":
			json.NewEncoder(w).Encode(map[string]any{
				"count": 1,
				"results": []map[string]any{
					{
						"id": 9, "title": "Ledger",
						"created_date":       "2026-01-15",
						"file_type":          nil,
						"mime_type":          "application/pdf",
						"original_file_name": "metadata.pdf",
					},
				},
				"next": nil,
			})
		case "/api/documents/9/download/":
			w.Header().Set("Content-Type", "text/csv; charset=utf-8")
			w.Header().Set("Content-Disposition", `attachment; filename="ledger.csv"`)
			w.Write([]byte("a,b\n1,2\n"))
		case "/api/tags/", "/api/correspondents/", "/api/document_types/", "/api/storage_paths/":
			json.NewEncoder(w).Encode(map[string]any{"count": 0, "results": []any{}, "next": nil})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	pipeline := &ingest.Pipeline{
		Engine:     fakeEngine{},
		Store:      s,
		Writer:     &writer.NoteWriter{Vault: filepath.Join(dir, "vault")},
		ArchiveDir: filepath.Join(dir, "archive"),
	}

	stats, err := Run(context.Background(), Options{BaseURL: srv.URL, Token: "test-token"}, pipeline)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Imported != 1 || len(stats.Results) != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	res := stats.Results[0]
	if res.ExpectedExtension != ".csv" || res.ActualArchiveExtension != ".csv" {
		t.Fatalf("extensions = expected %q actual %q, want .csv/.csv", res.ExpectedExtension, res.ActualArchiveExtension)
	}
	if res.SourceURI != "paperless://documents/9" || res.DownloadURI != "paperless://documents/9/download" || res.ImportRunID == "" {
		t.Fatalf("traceability result missing stable URIs/run ID: %+v", res)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "vault") + "/*.md")
	if len(matches) != 1 {
		t.Fatalf("expected 1 note, got %d", len(matches))
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, needle := range []string{
		"source_path: paperless://documents/9/download",
		"imported_from: paperless",
		"source_uri: paperless://documents/9",
		"download_uri: paperless://documents/9/download",
	} {
		if !contains(content, needle) {
			t.Fatalf("note missing %q:\n%s", needle, content)
		}
	}
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "source_path:") || strings.HasPrefix(line, "source_uri:") || strings.HasPrefix(line, "download_uri:") {
			for _, forbidden := range []string{"/tmp", "/var/folders", "symingest-import-"} {
				if contains(line, forbidden) {
					t.Fatalf("note leaked temporary source metadata %q in line %q:\n%s", forbidden, line, content)
				}
			}
		}
	}
}

func TestRun_ResumesAfterPartialFailure(t *testing.T) {
	var failDownload bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/documents/":
			json.NewEncoder(w).Encode(map[string]any{
				"count": 2,
				"results": []map[string]any{
					{"id": 1, "title": "Good Doc", "created_date": "2026-01-15", "file_type": ".txt"},
					{"id": 2, "title": "Bad Doc", "created_date": "2026-01-16", "file_type": ".txt"},
				},
				"next": nil,
			})
		case "/api/documents/1/download/":
			w.Write([]byte("good content"))
		case "/api/documents/2/download/":
			if failDownload {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.Write([]byte("recovered content"))
		case "/api/tags/", "/api/correspondents/", "/api/document_types/", "/api/storage_paths/":
			json.NewEncoder(w).Encode(map[string]any{"count": 0, "results": []any{}, "next": nil})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	pipeline := &ingest.Pipeline{
		Engine:     fakeEngine{},
		Store:      s,
		Writer:     &writer.NoteWriter{Vault: filepath.Join(dir, "vault")},
		ArchiveDir: filepath.Join(dir, "archive"),
	}

	// First run: document 2's download fails.
	failDownload = true
	stats, err := Run(context.Background(), Options{BaseURL: srv.URL, Token: "test-token"}, pipeline)
	if err != nil {
		t.Fatalf("Run (first): %v", err)
	}
	if stats.Imported != 1 || stats.Failed != 1 {
		t.Fatalf("first run: imported=%d failed=%d, want imported=1 failed=1", stats.Imported, stats.Failed)
	}
	if len(stats.Results) != 2 || stats.Results[1].Status != "failed" || stats.Results[1].Error == "" || stats.Results[1].Stage != "download" {
		t.Fatalf("failed result missing error/stage: %+v", stats.Results)
	}

	status, found, err := s.PaperlessImportStatus(context.Background(), srv.URL, 2)
	if err != nil {
		t.Fatalf("PaperlessImportStatus: %v", err)
	}
	if !found || status != "failed" {
		t.Fatalf("document 2 status = %q, found = %v, want failed/true", status, found)
	}

	// Second run: download succeeds. Document 1 must not be re-downloaded
	// or duplicated; only document 2 should be retried.
	failDownload = false
	stats, err = Run(context.Background(), Options{BaseURL: srv.URL, Token: "test-token"}, pipeline)
	if err != nil {
		t.Fatalf("Run (second): %v", err)
	}
	if stats.Imported != 1 {
		t.Errorf("second run: imported = %d, want 1 (only the retried document)", stats.Imported)
	}
	if stats.Skipped != 1 {
		t.Errorf("second run: skipped = %d, want 1 (already-imported document 1)", stats.Skipped)
	}
	if stats.Failed != 0 {
		t.Errorf("second run: failed = %d, want 0", stats.Failed)
	}

	matches, _ := filepath.Glob(filepath.Join(dir, "vault") + "/*.md")
	if len(matches) != 2 {
		t.Fatalf("expected 2 notes total after resume, got %d", len(matches))
	}

	status, found, err = s.PaperlessImportStatus(context.Background(), srv.URL, 2)
	if err != nil {
		t.Fatalf("PaperlessImportStatus: %v", err)
	}
	if !found || status != "imported" {
		t.Fatalf("document 2 status after retry = %q, found = %v, want imported/true", status, found)
	}
}

func TestRun_RetryFailedUsesCurrentTargetAndCheckpoints(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleEmptyLookups(w, r) {
			return
		}
		switch r.URL.Path {
		case "/api/documents/":
			json.NewEncoder(w).Encode(map[string]any{
				"count": 3,
				"results": []map[string]any{
					{"id": 1, "title": "Already Done", "created_date": "2026-01-15", "file_type": ".txt"},
					{"id": 2, "title": "Retry Me", "created_date": "2026-01-16", "file_type": ".txt"},
					{"id": 3, "title": "Never Tried", "created_date": "2026-01-17", "file_type": ".txt"},
				},
				"next": nil,
			})
		case "/api/documents/2/download/":
			w.Write([]byte("recovered content"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	vault := filepath.Join(dir, "vault")
	archive := filepath.Join(dir, "archive")
	if err := s.UpsertPaperlessImportStateForTarget(context.Background(), srv.URL, vault, archive, 1, "imported", "", "", "", "fake-sha"); err != nil {
		t.Fatalf("seed imported: %v", err)
	}
	if err := s.UpsertPaperlessImportStateForTarget(context.Background(), srv.URL, vault, archive, 2, "failed", "download timeout", "", "", ""); err != nil {
		t.Fatalf("seed failed: %v", err)
	}
	// Same document ID but a different target must not make the current target
	// look imported.
	otherVault := filepath.Join(dir, "other-vault")
	if status, found, err := s.PaperlessImportStatusForTarget(context.Background(), srv.URL, otherVault, archive, 1); err != nil || found || status != "" {
		t.Fatalf("target mismatch status = %q found=%v err=%v, want not found", status, found, err)
	}

	pipeline := &ingest.Pipeline{
		Engine:     fakeEngine{},
		Store:      s,
		Writer:     &writer.NoteWriter{Vault: vault},
		ArchiveDir: archive,
	}
	var progress strings.Builder
	stats, err := Run(context.Background(), Options{
		BaseURL:         srv.URL,
		Token:           "test-token",
		RetryFailed:     true,
		Concurrency:     2,
		CheckpointEvery: 1,
		TargetVault:     vault,
		TargetArchive:   archive,
		Progress:        &progress,
	}, pipeline)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Imported != 1 || stats.Skipped != 2 || stats.Failed != 0 {
		t.Fatalf("stats imported=%d skipped=%d failed=%d, want 1/2/0", stats.Imported, stats.Skipped, stats.Failed)
	}
	if got := []string{stats.Results[0].Status, stats.Results[1].Status, stats.Results[2].Status}; got[0] != "skipped" || got[1] != "imported" || got[2] != "skipped" {
		t.Fatalf("results not deterministic/in input order: %+v", stats.Results)
	}
	if !strings.Contains(progress.String(), "checkpoint: processed=") || !strings.Contains(progress.String(), "eta=") {
		t.Fatalf("checkpoint output missing progress/eta: %s", progress.String())
	}
	status, found, err := s.PaperlessImportStatusForTarget(context.Background(), srv.URL, vault, archive, 2)
	if err != nil {
		t.Fatalf("PaperlessImportStatusForTarget: %v", err)
	}
	if !found || status != "imported" {
		t.Fatalf("document 2 status = %q found=%v, want imported/true", status, found)
	}
}

func TestRun_CancellationStopsDuringNextDownloadAfterStateRecorded(t *testing.T) {
	secondStarted := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleEmptyLookups(w, r) {
			return
		}
		switch r.URL.Path {
		case "/api/documents/":
			json.NewEncoder(w).Encode(map[string]any{
				"count": 2,
				"results": []map[string]any{
					{"id": 1, "title": "First Doc", "created_date": "2026-01-15", "file_type": ".txt"},
					{"id": 2, "title": "Second Doc", "created_date": "2026-01-16", "file_type": ".txt"},
				},
				"next": nil,
			})
		case "/api/documents/1/download/":
			w.Write([]byte("first content"))
		case "/api/documents/2/download/":
			select {
			case secondStarted <- struct{}{}:
			default:
			}
			<-r.Context().Done()
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-secondStarted
		cancel()
	}()
	pipeline := &ingest.Pipeline{
		Engine:     fakeEngine{},
		Store:      s,
		Writer:     &writer.NoteWriter{Vault: filepath.Join(dir, "vault")},
		ArchiveDir: filepath.Join(dir, "archive"),
	}

	stats, err := Run(ctx, Options{BaseURL: srv.URL, Token: "test-token"}, pipeline)
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if stats == nil || stats.Imported != 1 {
		t.Fatalf("stats = %+v, want Imported=1", stats)
	}
	status, found, err := s.PaperlessImportStatus(context.Background(), srv.URL, 1)
	if err != nil {
		t.Fatalf("PaperlessImportStatus doc1: %v", err)
	}
	if !found || status != "imported" {
		t.Fatalf("document 1 status = %q, found = %v, want imported/true", status, found)
	}
	_, found, err = s.PaperlessImportStatus(context.Background(), srv.URL, 2)
	if err != nil {
		t.Fatalf("PaperlessImportStatus doc2: %v", err)
	}
	if found {
		t.Fatal("document 2 should not be persisted as failed when cancellation aborts its download")
	}
}

func TestRun_SinceFilter(t *testing.T) {
	var requestedURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleEmptyLookups(w, r) {
			return
		}
		if r.URL.Path == "/api/documents/" {
			requestedURL = r.URL.String()
		}
		json.NewEncoder(w).Encode(map[string]any{
			"count": 0, "results": []any{}, "next": nil,
		})
	}))
	defer srv.Close()

	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	pipeline := &ingest.Pipeline{
		Store:      s,
		Writer:     &writer.NoteWriter{Vault: filepath.Join(dir, "vault")},
		ArchiveDir: filepath.Join(dir, "archive"),
	}

	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err = Run(context.Background(), Options{
		BaseURL: srv.URL,
		Token:   "test-token",
		Since:   since,
	}, pipeline)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !contains(requestedURL, "created__date__gte=2026-01-01") {
		t.Errorf("expected since filter in URL, got: %s", requestedURL)
	}
}

func TestRun_Limit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleEmptyLookups(w, r) {
			return
		}
		switch {
		case r.URL.Path == "/api/documents/":
			json.NewEncoder(w).Encode(map[string]any{
				"count": 3,
				"results": []map[string]any{
					{"id": 1, "title": "Doc 1", "created_date": "2026-01-15", "file_type": ".txt"},
					{"id": 2, "title": "Doc 2", "created_date": "2026-01-16", "file_type": ".txt"},
					{"id": 3, "title": "Doc 3", "created_date": "2026-01-17", "file_type": ".txt"},
				},
				"next": nil,
			})
		case r.URL.Path == "/api/documents/1/download/":
			w.Write([]byte("content 1"))
		case r.URL.Path == "/api/documents/2/download/":
			w.Write([]byte("content 2"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	pipeline := &ingest.Pipeline{
		Engine:     fakeEngine{},
		Store:      s,
		Writer:     &writer.NoteWriter{Vault: filepath.Join(dir, "vault")},
		ArchiveDir: filepath.Join(dir, "archive"),
	}

	stats, err := Run(context.Background(), Options{
		BaseURL: srv.URL,
		Token:   "test-token",
		Limit:   2,
	}, pipeline)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Total != 2 {
		t.Errorf("stats.Total = %d, want 2 (limit must cap the selection)", stats.Total)
	}
	if stats.Imported != 2 {
		t.Errorf("stats.Imported = %d, want 2", stats.Imported)
	}
	wantIDs := []int{1, 2}
	if len(stats.SelectedIDs) != 2 || stats.SelectedIDs[0] != wantIDs[0] || stats.SelectedIDs[1] != wantIDs[1] {
		t.Errorf("stats.SelectedIDs = %v, want %v", stats.SelectedIDs, wantIDs)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "vault") + "/*.md")
	if len(matches) != 2 {
		t.Fatalf("expected 2 notes (limited), got %d", len(matches))
	}
}

func TestRun_ExplicitIDs(t *testing.T) {
	var listCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleEmptyLookups(w, r) {
			return
		}
		switch r.URL.Path {
		case "/api/documents/":
			// A bounded --ids run must fetch documents individually, never
			// scan the full archive listing.
			listCalled = true
			w.WriteHeader(http.StatusInternalServerError)
		case "/api/documents/42/":
			json.NewEncoder(w).Encode(map[string]any{
				"id": 42, "title": "Doc 42", "created_date": "2026-01-15", "file_type": ".txt",
			})
		case "/api/documents/42/download/":
			w.Write([]byte("content 42"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	pipeline := &ingest.Pipeline{
		Engine:     fakeEngine{},
		Store:      s,
		Writer:     &writer.NoteWriter{Vault: filepath.Join(dir, "vault")},
		ArchiveDir: filepath.Join(dir, "archive"),
	}

	stats, err := Run(context.Background(), Options{
		BaseURL: srv.URL,
		Token:   "test-token",
		IDs:     []int{42},
	}, pipeline)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if listCalled {
		t.Error("explicit --ids run must not call the document list endpoint")
	}
	if stats.Total != 1 || stats.Imported != 1 {
		t.Errorf("stats total=%d imported=%d, want 1/1", stats.Total, stats.Imported)
	}
	if len(stats.SelectedIDs) != 1 || stats.SelectedIDs[0] != 42 {
		t.Errorf("stats.SelectedIDs = %v, want [42]", stats.SelectedIDs)
	}
}

func TestRun_ExplicitIDs_DryRunHonorsBound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleEmptyLookups(w, r) {
			return
		}
		switch r.URL.Path {
		case "/api/documents/":
			t.Error("dry-run with --ids must not list the full archive")
			w.WriteHeader(http.StatusInternalServerError)
		case "/api/documents/7/":
			json.NewEncoder(w).Encode(map[string]any{
				"id": 7, "title": "Doc 7", "created_date": "2026-01-15", "file_type": ".pdf",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	pipeline := &ingest.Pipeline{
		Store:      s,
		Writer:     &writer.NoteWriter{Vault: filepath.Join(dir, "vault")},
		ArchiveDir: filepath.Join(dir, "archive"),
	}

	stats, err := Run(context.Background(), Options{
		BaseURL: srv.URL,
		Token:   "test-token",
		IDs:     []int{7},
		DryRun:  true,
	}, pipeline)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Total != 1 || stats.Skipped != 1 || stats.Imported != 0 {
		t.Errorf("dry-run stats total=%d skipped=%d imported=%d, want 1/1/0", stats.Total, stats.Skipped, stats.Imported)
	}
	if len(stats.SelectedIDs) != 1 || stats.SelectedIDs[0] != 7 {
		t.Errorf("stats.SelectedIDs = %v, want [7]", stats.SelectedIDs)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "vault") + "/*.md")
	if len(matches) != 0 {
		t.Fatalf("dry-run must not write notes, found %d", len(matches))
	}
}

func TestRun_PreserveStoragePaths(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleEmptyLookups(w, r) {
			return
		}
		switch r.URL.Path {
		case "/api/documents/":
			json.NewEncoder(w).Encode(map[string]any{
				"count": 1,
				"results": []map[string]any{
					{
						"id": 5, "title": "Acme Invoice", "created_date": "2026-01-15",
						"file_type":          ".txt",
						"original_file_name": "acme.pdf",
						"storage_path":       map[string]any{"id": 11, "name": "Finance/Invoices"},
					},
				},
				"next": nil,
			})
		case "/api/documents/5/download/":
			w.Write([]byte("invoice content"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	pipeline := &ingest.Pipeline{
		Engine:     fakeEngine{},
		Store:      s,
		Writer:     &writer.NoteWriter{Vault: vault},
		ArchiveDir: filepath.Join(dir, "archive"),
	}

	stats, err := Run(context.Background(), Options{
		BaseURL:              srv.URL,
		Token:                "test-token",
		PreserveStoragePaths: true,
	}, pipeline)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Imported != 1 {
		t.Fatalf("stats.Imported = %d, want 1", stats.Imported)
	}

	// The note must live under the storage-path-derived subdirectory, not the
	// vault root.
	wantPath := filepath.Join(vault, "Finance", "Invoices", "acme.md")
	if _, err := os.Stat(wantPath); err != nil {
		rootMatches, _ := filepath.Glob(filepath.Join(vault, "*.md"))
		t.Fatalf("note not placed under storage path (%v); vault root has %v", err, rootMatches)
	}

	content, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	// Frontmatter must still record the original Paperless storage path.
	if !contains(string(content), "storage_path: Finance/Invoices") {
		t.Errorf("note frontmatter missing original storage path:\n%s", content)
	}
}

func TestRun_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("unauthorized"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	pipeline := &ingest.Pipeline{
		Store:      s,
		Writer:     &writer.NoteWriter{Vault: filepath.Join(dir, "vault")},
		ArchiveDir: filepath.Join(dir, "archive"),
	}

	_, err = Run(context.Background(), Options{
		BaseURL: srv.URL,
		Token:   "bad-token",
	}, pipeline)
	if err == nil {
		t.Fatal("expected error for 401 status")
	}
}

func TestRun_ImportsPaperlessCSVWhenFileTypeIsNull(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleEmptyLookups(w, r) {
			return
		}
		switch r.URL.Path {
		case "/api/documents/":
			json.NewEncoder(w).Encode(map[string]any{
				"count": 1,
				"results": []map[string]any{
					{
						"id":                 13,
						"title":              "Transactions",
						"created_date":       "2026-07-02",
						"mime_type":          "text/csv",
						"original_file_name": "transactions.csv",
					},
				},
				"next": nil,
			})
		case "/api/documents/13/download/":
			w.Header().Set("Content-Type", "text/csv")
			w.Write([]byte("date,description,amount\n2026-07-02,Test CSV,12.34\n"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	archive := filepath.Join(dir, "archive")
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	pipeline := &ingest.Pipeline{
		Engine:     fakeEngine{},
		Store:      s,
		Writer:     &writer.NoteWriter{Vault: filepath.Join(dir, "vault")},
		ArchiveDir: archive,
	}

	stats, err := Run(context.Background(), Options{BaseURL: srv.URL, Token: "test-token"}, pipeline)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Imported != 1 || stats.Failed != 0 {
		t.Fatalf("stats imported=%d failed=%d, want 1/0", stats.Imported, stats.Failed)
	}

	matches, _ := filepath.Glob(filepath.Join(dir, "vault", "*.md"))
	if len(matches) != 1 {
		t.Fatalf("expected 1 note, got %d", len(matches))
	}
	content, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(content), "mime: text/csv") || !contains(string(content), "Test CSV") {
		t.Fatalf("note does not preserve CSV MIME/content:\n%s", content)
	}
	archiveMatches, _ := filepath.Glob(filepath.Join(archive, "*.csv"))
	if len(archiveMatches) != 1 {
		t.Fatalf("expected archived CSV, got %v", archiveMatches)
	}
}

func TestBuildAuditReport_UsesEffectiveDownloadExtension(t *testing.T) {
	lu := &lookups{
		tags:           map[int]string{},
		correspondents: map[int]string{},
		documentTypes:  map[int]string{},
		storagePaths:   map[int]string{},
	}
	docs := []paperless.Document{
		{ID: 1, Title: "CSV", MimeType: "text/csv", OriginalFileName: "transactions.csv"},
		{ID: 2, Title: "PNG with archive", MimeType: "image/png", OriginalFileName: "scan.png", ArchivedFileName: "scan.pdf"},
		{ID: 3, Title: "TIFF raw extension", MimeType: "image/tiff", OriginalFileName: "scan.NEF"},
		{ID: 4, Title: "Unsupported", MimeType: "application/vnd.ms-excel", OriginalFileName: "sheet.xlsx"},
	}
	audit := buildAuditReport(docs, lu)
	if audit.UnsupportedFileTypes["unknown"] != 0 {
		t.Fatalf("unknown unsupported count = %d, want 0", audit.UnsupportedFileTypes["unknown"])
	}
	if audit.UnsupportedFileTypes["csv"] != 0 || audit.UnsupportedFileTypes["pdf"] != 0 || audit.UnsupportedFileTypes["nef"] != 0 {
		t.Fatalf("supported extensions flagged as unsupported: %v", audit.UnsupportedFileTypes)
	}
	if audit.UnsupportedFileTypes["xlsx"] != 0 {
		t.Fatalf("xlsx unsupported count = %d, want 0 after native XLSX support (all unsupported: %v)", audit.UnsupportedFileTypes["xlsx"], audit.UnsupportedFileTypes)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
