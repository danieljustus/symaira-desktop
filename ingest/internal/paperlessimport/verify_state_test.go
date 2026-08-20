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

	"github.com/danieljustus/symaira-ingest/internal/store"
)

func TestVerify_WithStoreUsesImportStateAndDetectsHashMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleEmptyLookups(w, r) {
			return
		}
		switch {
		case r.URL.Path == "/api/documents/":
			json.NewEncoder(w).Encode(map[string]any{
				"count": 1,
				"results": []map[string]any{{
					"id": 1, "title": "Doc 1",
					"created_date": "2026-01-15T00:00:00Z", "created": "2026-01-15T00:00:00Z",
					"file_type": ".txt",
				}},
				"next": nil,
			})
		case strings.HasPrefix(r.URL.Path, "/api/documents/") && strings.HasSuffix(r.URL.Path, "/download/"):
			w.Write([]byte("hello"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	archive := filepath.Join(dir, "archive")
	if err := os.MkdirAll(vault, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(archive, 0o700); err != nil {
		t.Fatal(err)
	}

	notePath := filepath.Join(vault, "doc1.md")
	archivePath := filepath.Join(archive, "doc1.txt")
	if err := os.WriteFile(archivePath, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(notePath, []byte("---\nsha256: 0000000000000000000000000000000000000000000000000000000000000000\narchive_path: "+archivePath+"\nmime: text/plain\nsource_path: sp\npaperless:\n  document_id: 1\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.UpsertPaperlessImportStateForTarget(context.Background(), srv.URL, vault, archive, 1, "imported", "", notePath, archivePath, "0000000000000000000000000000000000000000000000000000000000000000"); err != nil {
		t.Fatal(err)
	}

	report, err := Verify(context.Background(), Options{BaseURL: srv.URL, Token: "test-token"}, vault, st)
	if err != nil {
		t.Fatal(err)
	}
	if !containsInt(report.HashMismatch, 1) {
		t.Fatalf("expected hash mismatch for doc 1, got %+v", report)
	}
	if report.Verified != 0 {
		t.Fatalf("expected zero verified, got %d", report.Verified)
	}
}

func TestVerify_DuplicateContentWithSeparateIDs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleEmptyLookups(w, r) {
			return
		}
		switch {
		case r.URL.Path == "/api/documents/":
			json.NewEncoder(w).Encode(map[string]any{
				"count": 2,
				"results": []map[string]any{
					{"id": 1, "title": "Doc 1", "created_date": "2026-01-15T00:00:00Z", "created": "2026-01-15T00:00:00Z", "file_type": ".txt"},
					{"id": 2, "title": "Doc 2", "created_date": "2026-01-15T00:00:00Z", "created": "2026-01-15T00:00:00Z", "file_type": ".txt"},
				},
				"next": nil,
			})
		case strings.HasPrefix(r.URL.Path, "/api/documents/") && strings.HasSuffix(r.URL.Path, "/download/"):
			w.Write([]byte("same content"))
		case strings.HasPrefix(r.URL.Path, "/api/documents/"):
			json.NewEncoder(w).Encode(map[string]any{"id": 1, "title": "Doc 1", "created_date": "2026-01-15T00:00:00Z", "created": "2026-01-15T00:00:00Z", "file_type": ".txt"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	archive := filepath.Join(dir, "archive")
	if err := os.MkdirAll(vault, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(archive, 0o700); err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sha := "fake-sha-duplicate"
	if err := st.UpsertPaperlessImportStateForTarget(context.Background(), srv.URL, vault, archive, 1, "imported", "", filepath.Join(vault, "doc1.md"), filepath.Join(archive, "doc1.txt"), sha); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertPaperlessImportStateForTarget(context.Background(), srv.URL, vault, archive, 2, "imported", "", filepath.Join(vault, "doc1.md"), filepath.Join(archive, "doc1.txt"), sha); err != nil {
		t.Fatal(err)
	}

	report, err := Verify(context.Background(), Options{BaseURL: srv.URL, Token: "test-token"}, vault, st)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.DuplicateContent) != 1 || report.DuplicateContent[0] != 2 {
		t.Fatalf("expected duplicate content for doc 2, got %+v", report)
	}
}

func containsInt(haystack []int, needle int) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}
