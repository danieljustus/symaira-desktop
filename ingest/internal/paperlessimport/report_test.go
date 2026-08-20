package paperlessimport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-ingest/internal/ingest"
	"github.com/danieljustus/symaira-ingest/internal/store"
	"github.com/danieljustus/symaira-ingest/internal/writer"
)

func TestBuildMigrationReport_RealImport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleEmptyLookups(w, r) {
			return
		}
		switch {
		case r.URL.Path == "/api/documents/":
			json.NewEncoder(w).Encode(map[string]any{
				"count": 1,
				"results": []map[string]any{
					{"id": 7, "title": "Doc 7", "created_date": "2026-01-15", "file_type": ".txt"},
				},
				"next": nil,
			})
		case r.URL.Path == "/api/documents/7/download/":
			w.Write([]byte("content 7"))
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

	report := stats.BuildMigrationReport(false)
	if report.SchemaVersion != ReportSchemaVersion {
		t.Fatalf("schema_version = %d, want %d", report.SchemaVersion, ReportSchemaVersion)
	}
	if report.DryRun {
		t.Error("report.DryRun = true, want false")
	}
	if report.Total != 1 || report.Imported != 1 {
		t.Errorf("report total=%d imported=%d, want 1/1", report.Total, report.Imported)
	}
	if len(report.Documents) != 1 {
		t.Fatalf("len(report.Documents) = %d, want 1", len(report.Documents))
	}
	d := report.Documents[0]
	if d.ID != 7 || d.Status != "imported" {
		t.Errorf("document = %+v, want ID=7 status=imported", d)
	}
	if d.VaultPath == "" || d.ArchivePath == "" {
		t.Errorf("imported document should record vault and archive paths, got %+v", d)
	}
	if report.RunID == "" || report.ToolVersion == "" || report.Source != "paperless" || report.SourceURL != srv.URL || report.Mode != "import" {
		t.Errorf("report metadata incomplete: %+v", report)
	}
	if d.SHA256 == "" || d.ExpectedExtension == "" || d.ActualArchiveExtension == "" || d.ImportRunID == "" || d.SourceURI == "" || d.DownloadURI == "" {
		t.Errorf("document report missing audit fields: %+v", d)
	}
}

func TestBuildMigrationReport_DryRunCarriesAudit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/documents/":
			json.NewEncoder(w).Encode(map[string]any{
				"count": 1,
				"results": []map[string]any{
					{
						"id": 3, "title": "Spreadsheet", "created_date": "2026-01-17",
						"file_type": ".xlsx", "mime_type": "application/vnd.ms-excel",
						"tags": []int{99}, // unresolved
					},
				},
				"next": nil,
			})
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
		Store:      s,
		Writer:     &writer.NoteWriter{Vault: filepath.Join(dir, "vault")},
		ArchiveDir: filepath.Join(dir, "archive"),
	}

	stats, err := Run(context.Background(), Options{BaseURL: srv.URL, Token: "test-token", DryRun: true}, pipeline)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	report := stats.BuildMigrationReport(true)
	if !report.DryRun {
		t.Error("report.DryRun = false, want true")
	}
	if len(report.Documents) != 1 || report.Documents[0].Status != "would-import" {
		t.Errorf("documents = %+v, want one would-import entry", report.Documents)
	}
	if report.UnsupportedFileTypes["xlsx"] != 0 {
		t.Errorf("UnsupportedFileTypes[xlsx] = %d, want 0 after native XLSX support", report.UnsupportedFileTypes["xlsx"])
	}
	if len(report.UnresolvedTagIDs) != 1 || report.UnresolvedTagIDs[0] != 99 {
		t.Errorf("UnresolvedTagIDs = %v, want [99]", report.UnresolvedTagIDs)
	}
}

func TestWriteMigrationReport_WritesValidJSON(t *testing.T) {
	report := &MigrationReport{
		Total: 2, Imported: 1, Skipped: 1,
		Documents: []DocumentResult{
			{ID: 1, Status: "imported", VaultPath: "/vault/1.md"},
			{ID: 2, Status: "skipped", Reason: "duplicate content; note already present in the vault"},
		},
	}
	path := filepath.Join(t.TempDir(), "report.json")
	if err := WriteMigrationReport(path, report); err != nil {
		t.Fatalf("WriteMigrationReport: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got MigrationReport
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("report is not valid JSON: %v", err)
	}
	if got.Total != 2 || len(got.Documents) != 2 || got.Documents[0].ID != 1 {
		t.Errorf("round-tripped report = %+v, want Total=2 with 2 documents", got)
	}
}
