package paperlessimport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-ingest/internal/ingest"
	"github.com/danieljustus/symaira-ingest/internal/paperless"
	"github.com/danieljustus/symaira-ingest/internal/store"
	"github.com/danieljustus/symaira-ingest/internal/writer"
)

// verifyDoc is a minimal Paperless document description for the fake API used
// by the verifier tests.
type verifyDoc struct {
	id            int
	title         string
	download      string
	correspondent map[string]any
	tags          []map[string]any
}

// newVerifyServer serves the document list, lookups, and downloads needed to
// import and then verify the given documents.
func newVerifyServer(t *testing.T, docs []verifyDoc) *httptest.Server {
	t.Helper()
	results := make([]map[string]any, 0, len(docs))
	for _, d := range docs {
		m := map[string]any{
			"id": d.id, "title": d.title,
			"created_date": "2026-01-15T00:00:00Z", "created": "2026-01-15T00:00:00Z",
			"file_type": ".txt",
		}
		if d.correspondent != nil {
			m["correspondent"] = d.correspondent
		}
		if d.tags != nil {
			m["tags"] = d.tags
		}
		results = append(results, m)
	}

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleEmptyLookups(w, r) {
			return
		}
		switch {
		case r.URL.Path == "/api/documents/":
			json.NewEncoder(w).Encode(map[string]any{
				"count": len(results), "results": results, "next": nil,
			})
		case strings.HasPrefix(r.URL.Path, "/api/documents/") && strings.HasSuffix(r.URL.Path, "/download/"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/documents/"), "/download/")
			for _, doc := range docs {
				if strconv.Itoa(doc.id) == id {
					body := doc.download
					if body == "" {
						body = "content of " + r.URL.Path
					}
					w.Write([]byte(body))
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
		case strings.HasPrefix(r.URL.Path, "/api/documents/"):
			// GetDocument by ID (trailing /?format=json)
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/documents/"), "/")
			for _, res := range results {
				if strconv.Itoa(res["id"].(int)) == id {
					json.NewEncoder(w).Encode(res)
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// importForVerify imports docs into a fresh vault and returns the vault dir.
func importForVerify(t *testing.T, srvURL string, opts Options) (vault string) {
	t.Helper()
	dir := t.TempDir()
	vault = filepath.Join(dir, "vault")
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	pipeline := &ingest.Pipeline{
		Engine:     fakeEngine{},
		Store:      s,
		Writer:     &writer.NoteWriter{Vault: vault},
		ArchiveDir: filepath.Join(dir, "archive"),
	}
	base := Options{BaseURL: srvURL, Token: "test-token"}
	base.IDs = opts.IDs
	stats, err := Run(context.Background(), base, pipeline)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Failed != 0 {
		t.Fatalf("import failed %d documents", stats.Failed)
	}
	return vault
}

func TestVerify_CompleteAfterImport(t *testing.T) {
	docs := []verifyDoc{{id: 1, title: "Doc 1"}, {id: 2, title: "Doc 2"}}
	srv := newVerifyServer(t, docs)
	defer srv.Close()

	vault := importForVerify(t, srv.URL, Options{})

	report, err := Verify(context.Background(), Options{BaseURL: srv.URL, Token: "test-token"}, vault, nil)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !report.Complete() {
		t.Fatalf("expected complete verification, got %+v", report)
	}
	if report.SourceDocuments != 2 || report.VaultNotes != 2 || report.Verified != 2 {
		t.Errorf("report = %+v, want source=2 notes=2 verified=2", report)
	}
	if report.RunID == "" || report.ToolVersion == "" || report.Source != "paperless" || report.SourceURL != srv.URL || report.Mode != "verify" {
		t.Errorf("verify report metadata incomplete: %+v", report)
	}
	if report.SchemaVersion != ReportSchemaVersion {
		t.Fatalf("schema_version = %d, want %d", report.SchemaVersion, ReportSchemaVersion)
	}
}

func TestVerify_AllowsDuplicateContentWhenEachPaperlessIDHasNote(t *testing.T) {
	docs := []verifyDoc{
		{id: 1, title: "Doc 1", download: "same original bytes", correspondent: map[string]any{"id": 1, "name": "Alpha GmbH"}, tags: []map[string]any{{"id": 1, "name": "alpha"}}},
		{id: 2, title: "Doc 2", download: "same original bytes", correspondent: map[string]any{"id": 2, "name": "Beta GmbH"}, tags: []map[string]any{{"id": 2, "name": "beta"}}},
	}
	srv := newVerifyServer(t, docs)
	defer srv.Close()

	vault := importForVerify(t, srv.URL, Options{})
	report, err := Verify(context.Background(), Options{BaseURL: srv.URL, Token: "test-token"}, vault, nil)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !report.Complete() {
		t.Fatalf("duplicate content with one note per Paperless ID should verify, got %+v", report)
	}
	if report.Verified != 2 {
		t.Fatalf("Verified = %d, want 2", report.Verified)
	}
	if len(report.DuplicateContent) != 1 || report.DuplicateContent[0] != 2 {
		t.Fatalf("DuplicateContent = %v, want [2] as informational", report.DuplicateContent)
	}
}

func TestVerify_DeepVerifyMatchesPaperlessDownload(t *testing.T) {
	docs := []verifyDoc{{id: 1, title: "Doc 1", download: "paperless original"}}
	srv := newVerifyServer(t, docs)
	defer srv.Close()

	vault := importForVerify(t, srv.URL, Options{})

	report, err := Verify(context.Background(), Options{BaseURL: srv.URL, Token: "test-token", DeepVerify: true}, vault, nil)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !report.Complete() {
		t.Fatalf("expected complete deep verification, got %+v", report)
	}
	if !report.DeepVerify || report.DeepVerified != 1 || len(report.SourceHashMismatch) != 0 {
		t.Fatalf("bad deep verification counters: %+v", report)
	}
}

func TestVerify_DeepVerifyDetectsPaperlessDownloadMismatch(t *testing.T) {
	docs := []verifyDoc{{id: 1, title: "Doc 1", download: "original during import"}}
	srv := newVerifyServer(t, docs)
	defer srv.Close()

	vault := importForVerify(t, srv.URL, Options{})
	docs[0].download = "changed source after import"

	report, err := Verify(context.Background(), Options{BaseURL: srv.URL, Token: "test-token", DeepVerify: true}, vault, nil)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if report.Complete() {
		t.Fatal("expected incomplete verification when Paperless download no longer matches archive")
	}
	if len(report.SourceHashMismatch) != 1 || report.SourceHashMismatch[0] != 1 {
		t.Fatalf("SourceHashMismatch = %v, want [1]", report.SourceHashMismatch)
	}
	if report.DeepVerified != 0 {
		t.Fatalf("DeepVerified = %d, want 0", report.DeepVerified)
	}
}

func TestVerify_MissingDocument(t *testing.T) {
	docs := []verifyDoc{{id: 1, title: "Doc 1"}, {id: 2, title: "Doc 2"}}
	srv := newVerifyServer(t, docs)
	defer srv.Close()

	// Import only document 1, leaving document 2 missing from the vault.
	vault := importForVerify(t, srv.URL, Options{IDs: []int{1}})

	report, err := Verify(context.Background(), Options{BaseURL: srv.URL, Token: "test-token"}, vault, nil)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if report.Complete() {
		t.Fatal("expected incomplete verification when a document is missing")
	}
	if len(report.Missing) != 1 || report.Missing[0] != 2 {
		t.Errorf("Missing = %v, want [2]", report.Missing)
	}
}

func TestVerify_MissingArchivedOriginal(t *testing.T) {
	docs := []verifyDoc{{id: 1, title: "Doc 1"}}
	srv := newVerifyServer(t, docs)
	defer srv.Close()

	vault := importForVerify(t, srv.URL, Options{})

	// Locate the note and delete the archived original it references.
	notes, err := scanVaultNotes(vault)
	if err != nil {
		t.Fatal(err)
	}
	note := notes[1][0]
	if note.ArchivePath == "" {
		t.Fatal("imported note has no archive path")
	}
	if err := os.Remove(note.ArchivePath); err != nil {
		t.Fatalf("remove archive: %v", err)
	}

	report, err := Verify(context.Background(), Options{BaseURL: srv.URL, Token: "test-token"}, vault, nil)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if report.Complete() {
		t.Fatal("expected incomplete verification when the archived original is gone")
	}
	if len(report.MissingArchive) != 1 || report.MissingArchive[0] != 1 {
		t.Errorf("MissingArchive = %v, want [1]", report.MissingArchive)
	}
}

func TestVerify_MetadataMismatch(t *testing.T) {
	docs := []verifyDoc{{
		id: 1, title: "Doc 1",
		correspondent: map[string]any{"id": 5, "name": "Acme Corp"},
	}}
	srv := newVerifyServer(t, docs)
	defer srv.Close()

	vault := importForVerify(t, srv.URL, Options{})

	// Corrupt the stored correspondent so it no longer matches the source.
	notes, _ := scanVaultNotes(vault)
	note := notes[1][0]
	tamperNoteCorrespondent(t, vault, note, "Wrong Corp")

	report, err := Verify(context.Background(), Options{BaseURL: srv.URL, Token: "test-token"}, vault, nil)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if report.Complete() {
		t.Fatal("expected incomplete verification on a metadata mismatch")
	}
	found := false
	for _, m := range report.Mismatches {
		if m.DocumentID == 1 && m.Field == "correspondent" && m.Expected == "Acme Corp" && m.Got == "Wrong Corp" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected correspondent mismatch (Acme Corp vs Wrong Corp), got %+v", report.Mismatches)
	}
}

// tamperNoteCorrespondent rewrites the single vault note in place, replacing
// the correspondent frontmatter value.
func tamperNoteCorrespondent(t *testing.T, vault string, note *writer.Note, newValue string) {
	t.Helper()
	matches, _ := filepath.Glob(filepath.Join(vault, "*.md"))
	if len(matches) != 1 {
		t.Fatalf("expected 1 note to tamper, got %d", len(matches))
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	out := strings.Replace(string(data), "correspondent: "+note.Correspondent, "correspondent: "+newValue, 1)
	if out == string(data) {
		t.Fatalf("could not find correspondent %q to replace in note", note.Correspondent)
	}
	if err := os.WriteFile(matches[0], []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
}

// --- Additional coverage tests ---

func TestVerify_DuplicateNotes(t *testing.T) {
	// Set up a server that returns one document.
	docs := []verifyDoc{{id: 1, title: "Doc 1"}}
	srv := newVerifyServer(t, docs)
	defer srv.Close()

	// Import once to create the vault.
	vault := importForVerify(t, srv.URL, Options{})

	// Manually create a second note for the same document ID to simulate a
	// duplicate. We write a valid frontmatter file with the same document_id.
	existing, err := filepath.Glob(filepath.Join(vault, "*.md"))
	if err != nil || len(existing) == 0 {
		t.Fatal("no notes found after import")
	}
	data, err := os.ReadFile(existing[0])
	if err != nil {
		t.Fatal(err)
	}
	// Write the duplicate note under a different filename.
	dupPath := filepath.Join(vault, "duplicate-note.md")
	if err := os.WriteFile(dupPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := Verify(context.Background(), Options{BaseURL: srv.URL, Token: "test-token"}, vault, nil)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if report.Complete() {
		t.Fatal("expected incomplete verification when a duplicate note exists")
	}
	if len(report.Duplicate) != 1 || report.Duplicate[0] != 1 {
		t.Errorf("Duplicate = %v, want [1]", report.Duplicate)
	}
	if report.Verified != 0 {
		t.Errorf("Verified = %d, want 0 when duplicates exist", report.Verified)
	}
}

func TestVerify_StoragePathMismatch(t *testing.T) {
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
					"file_type":    ".txt",
					"storage_path": map[string]any{"id": 7, "name": "Archive/2026"},
				}},
				"next": nil,
			})
		case strings.HasPrefix(r.URL.Path, "/api/documents/") && strings.HasSuffix(r.URL.Path, "/download/"):
			w.Write([]byte("content"))
		case strings.HasPrefix(r.URL.Path, "/api/documents/"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/documents/"), "/")
			if id == "1" {
				json.NewEncoder(w).Encode(map[string]any{
					"id": 1, "title": "Doc 1",
					"created_date": "2026-01-15T00:00:00Z", "created": "2026-01-15T00:00:00Z",
					"file_type":    ".txt",
					"storage_path": map[string]any{"id": 7, "name": "Archive/2026"},
				})
				return
			}
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	vault := importForVerify(t, srv.URL, Options{})

	matches, _ := filepath.Glob(filepath.Join(vault, "*.md"))
	if len(matches) == 0 {
		t.Fatal("no notes found after import")
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		if strings.Contains(line, "storage_path:") && strings.Contains(line, "Archive/2026") {
			lines[i] = strings.Replace(line, "Archive/2026", "Wrong/Path", 1)
			break
		}
	}
	if err := os.WriteFile(matches[0], []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := Verify(context.Background(), Options{BaseURL: srv.URL, Token: "test-token"}, vault, nil)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if report.Complete() {
		t.Fatal("expected incomplete verification on storage_path mismatch")
	}
	found := false
	for _, m := range report.Mismatches {
		if m.DocumentID == 1 && m.Field == "storage_path" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected storage_path mismatch, got %+v", report.Mismatches)
	}
}

func TestVerify_CreatedDateMismatch(t *testing.T) {
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
					"created_date": "2026-01-15T00:00:00Z",
					"created":      "2026-01-15T08:30:00Z",
					"file_type":    ".txt",
				}},
				"next": nil,
			})
		case strings.HasPrefix(r.URL.Path, "/api/documents/") && strings.HasSuffix(r.URL.Path, "/download/"):
			w.Write([]byte("content"))
		case strings.HasPrefix(r.URL.Path, "/api/documents/"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/documents/"), "/")
			if id == "1" {
				json.NewEncoder(w).Encode(map[string]any{
					"id": 1, "title": "Doc 1",
					"created_date": "2026-01-15T00:00:00Z",
					"created":      "2026-01-15T08:30:00Z",
					"file_type":    ".txt",
				})
				return
			}
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	vault := importForVerify(t, srv.URL, Options{})

	matches, _ := filepath.Glob(filepath.Join(vault, "*.md"))
	if len(matches) == 0 {
		t.Fatal("no notes found after import")
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		if strings.Contains(line, "created:") && strings.Contains(line, "2026-01-15T08:30:00Z") {
			lines[i] = strings.Replace(line, "2026-01-15T08:30:00Z", "2020-01-01T00:00:00Z", 1)
			break
		}
	}
	if err := os.WriteFile(matches[0], []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := Verify(context.Background(), Options{BaseURL: srv.URL, Token: "test-token"}, vault, nil)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if report.Complete() {
		t.Fatal("expected incomplete verification on created date mismatch")
	}
	found := false
	for _, m := range report.Mismatches {
		if m.DocumentID == 1 && m.Field == "created" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected created date mismatch, got %+v", report.Mismatches)
	}
}

func TestVerify_TagsMismatch(t *testing.T) {
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
					"created_date": "2026-01-15T00:00:00Z",
					"created":      "2026-01-15T00:00:00Z",
					"file_type":    ".txt",
					"tags":         []map[string]any{{"id": 1, "name": "important"}},
				}},
				"next": nil,
			})
		case strings.HasPrefix(r.URL.Path, "/api/documents/") && strings.HasSuffix(r.URL.Path, "/download/"):
			w.Write([]byte("content"))
		case strings.HasPrefix(r.URL.Path, "/api/documents/"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/documents/"), "/")
			if id == "1" {
				json.NewEncoder(w).Encode(map[string]any{
					"id": 1, "title": "Doc 1",
					"created_date": "2026-01-15T00:00:00Z",
					"created":      "2026-01-15T00:00:00Z",
					"file_type":    ".txt",
					"tags":         []map[string]any{{"id": 1, "name": "important"}},
				})
				return
			}
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	vault := importForVerify(t, srv.URL, Options{})
	if vault == "" {
		t.Fatal("importForVerify returned empty vault")
	}

	// Tamper: replace tags in the note.
	matches, _ := filepath.Glob(filepath.Join(vault, "*.md"))
	if len(matches) == 0 {
		t.Fatal("no notes found after import")
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		if strings.Contains(line, "tags:") && !strings.HasPrefix(line, "  ") {
			lines[i] = "tags:\n  - wrong-tag"
			break
		}
	}
	if err := os.WriteFile(matches[0], []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := Verify(context.Background(), Options{BaseURL: srv.URL, Token: "test-token"}, vault, nil)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if report.Complete() {
		t.Fatal("expected incomplete verification on tags mismatch")
	}
	found := false
	for _, m := range report.Mismatches {
		if m.DocumentID == 1 && m.Field == "tags" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected tags mismatch, got %+v", report.Mismatches)
	}
}

func TestVerify_DocumentTypeMismatch(t *testing.T) {
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
					"created_date":  "2026-01-15T00:00:00Z",
					"created":       "2026-01-15T00:00:00Z",
					"file_type":     ".txt",
					"document_type": map[string]any{"id": 2, "name": "Invoice"},
				}},
				"next": nil,
			})
		case strings.HasPrefix(r.URL.Path, "/api/documents/") && strings.HasSuffix(r.URL.Path, "/download/"):
			w.Write([]byte("content"))
		case strings.HasPrefix(r.URL.Path, "/api/documents/"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/documents/"), "/")
			if id == "1" {
				json.NewEncoder(w).Encode(map[string]any{
					"id": 1, "title": "Doc 1",
					"created_date":  "2026-01-15T00:00:00Z",
					"created":       "2026-01-15T00:00:00Z",
					"file_type":     ".txt",
					"document_type": map[string]any{"id": 2, "name": "Invoice"},
				})
				return
			}
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	vault := importForVerify(t, srv.URL, Options{})

	matches, _ := filepath.Glob(filepath.Join(vault, "*.md"))
	if len(matches) == 0 {
		t.Fatal("no notes found after import")
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		if strings.Contains(line, "document_type:") && strings.Contains(line, "Invoice") {
			lines[i] = strings.Replace(line, "Invoice", "Receipt", 1)
			break
		}
	}
	if err := os.WriteFile(matches[0], []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := Verify(context.Background(), Options{BaseURL: srv.URL, Token: "test-token"}, vault, nil)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if report.Complete() {
		t.Fatal("expected incomplete verification on document_type mismatch")
	}
	found := false
	for _, m := range report.Mismatches {
		if m.DocumentID == 1 && m.Field == "document_type" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected document_type mismatch, got %+v", report.Mismatches)
	}
}

func TestVerify_EmptyVault(t *testing.T) {
	docs := []verifyDoc{{id: 1, title: "Doc 1"}}
	srv := newVerifyServer(t, docs)
	defer srv.Close()

	// Use a non-existent vault directory.
	vault := filepath.Join(t.TempDir(), "nonexistent-vault")
	report, err := Verify(context.Background(), Options{BaseURL: srv.URL, Token: "test-token"}, vault, nil)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if report.SourceDocuments != 1 || report.VaultNotes != 0 || len(report.Missing) != 1 {
		t.Errorf("report = %+v, want 1 source, 0 notes, 1 missing", report)
	}
}

func TestVerify_NonDirectoryVault(t *testing.T) {
	docs := []verifyDoc{{id: 1, title: "Doc 1"}}
	srv := newVerifyServer(t, docs)
	defer srv.Close()

	// Use a file path (not a directory) as vault.
	vault := filepath.Join(t.TempDir(), "not-a-dir.md")
	if err := os.WriteFile(vault, []byte("some content"), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := Verify(context.Background(), Options{BaseURL: srv.URL, Token: "test-token"}, vault, nil)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if report.SourceDocuments != 1 || report.VaultNotes != 0 || len(report.Missing) != 1 {
		t.Errorf("report = %+v, want 1 source, 0 notes, 1 missing", report)
	}
}

func TestScanVaultNotes_EmptyVault(t *testing.T) {
	notes, err := scanVaultNotes("")
	if err != nil {
		t.Fatalf("scanVaultNotes: %v", err)
	}
	if len(notes) != 0 {
		t.Errorf("expected empty map, got %d entries", len(notes))
	}
}

func TestScanVaultNotes_NonExistentVault(t *testing.T) {
	notes, err := scanVaultNotes(filepath.Join(t.TempDir(), "nonexistent"))
	if err != nil {
		t.Fatalf("scanVaultNotes: %v", err)
	}
	if len(notes) != 0 {
		t.Errorf("expected empty map, got %d entries", len(notes))
	}
}

func TestScanVaultNotes_NonDirectoryVault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(path, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	notes, err := scanVaultNotes(path)
	if err != nil {
		t.Fatalf("scanVaultNotes: %v", err)
	}
	if len(notes) != 0 {
		t.Errorf("expected empty map for non-directory, got %d entries", len(notes))
	}
}

func TestScanVaultNotes_IgnoresNonMarkdown(t *testing.T) {
	dir := t.TempDir()
	// Write a non-markdown file.
	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	notes, err := scanVaultNotes(dir)
	if err != nil {
		t.Fatalf("scanVaultNotes: %v", err)
	}
	if len(notes) != 0 {
		t.Errorf("expected empty map for non-markdown files, got %d entries", len(notes))
	}
}

func TestScanVaultNotes_IgnoresNoFrontmatter(t *testing.T) {
	dir := t.TempDir()
	// Write a markdown file without frontmatter.
	if err := os.WriteFile(filepath.Join(dir, "note.md"), []byte("# Just a heading\n\nNo frontmatter here."), 0o644); err != nil {
		t.Fatal(err)
	}
	notes, err := scanVaultNotes(dir)
	if err != nil {
		t.Fatalf("scanVaultNotes: %v", err)
	}
	if len(notes) != 0 {
		t.Errorf("expected empty map for notes without frontmatter, got %d entries", len(notes))
	}
}

func TestScanVaultNotes_IgnoresMalformedYAML(t *testing.T) {
	dir := t.TempDir()
	// Write a markdown file with broken YAML frontmatter.
	bad := "---\ninvalid: yaml: [broken\n---\ncontent"
	if err := os.WriteFile(filepath.Join(dir, "bad.md"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	notes, err := scanVaultNotes(dir)
	if err != nil {
		t.Fatalf("scanVaultNotes: %v", err)
	}
	if len(notes) != 0 {
		t.Errorf("expected empty map for malformed YAML, got %d entries", len(notes))
	}
}

func TestScanVaultNotes_IgnoresMissingEndDelimiter(t *testing.T) {
	dir := t.TempDir()
	// Write a markdown file with frontmatter open but no close.
	noEnd := "---\ntitle: test\nsome_key: value\n# No closing ---"
	if err := os.WriteFile(filepath.Join(dir, "noend.md"), []byte(noEnd), 0o644); err != nil {
		t.Fatal(err)
	}
	notes, err := scanVaultNotes(dir)
	if err != nil {
		t.Fatalf("scanVaultNotes: %v", err)
	}
	if len(notes) != 0 {
		t.Errorf("expected empty map for missing end delimiter, got %d entries", len(notes))
	}
}

func TestParseNoteFrontmatter_MissingOpenDelimiter(t *testing.T) {
	note := parseNoteFrontmatter([]byte("# No frontmatter\nJust content"))
	if note != nil {
		t.Errorf("expected nil for missing open delimiter, got %+v", note)
	}
}

func TestParseNoteFrontmatter_MissingEndDelimiter(t *testing.T) {
	note := parseNoteFrontmatter([]byte("---\ntitle: test\n"))
	if note != nil {
		t.Errorf("expected nil for missing end delimiter, got %+v", note)
	}
}

func TestParseNoteFrontmatter_Valid(t *testing.T) {
	input := []byte("---\ntitle: Test\nsource_path: /tmp/test.txt\n---\nBody content")
	note := parseNoteFrontmatter(input)
	if note == nil {
		t.Fatal("expected non-nil for valid frontmatter")
	}
	if note.SourcePath != "/tmp/test.txt" {
		t.Errorf("SourcePath = %q, want /tmp/test.txt", note.SourcePath)
	}
}

func TestFormatDate_ZeroTime(t *testing.T) {
	got := formatDate(time.Time{})
	if got != "" {
		t.Errorf("formatDate(zero) = %q, want empty string", got)
	}
}

func TestFormatDate_ValidTime(t *testing.T) {
	ts := time.Date(2026, 6, 15, 10, 30, 0, 0, time.UTC)
	got := formatDate(ts)
	if got == "" {
		t.Error("formatDate(valid time) = empty string, want non-empty")
	}
	// Must be RFC3339 format.
	if _, err := time.Parse(time.RFC3339, got); err != nil {
		t.Errorf("formatDate(valid time) = %q, not valid RFC3339: %v", got, err)
	}
}

func TestVerify_MissingArchivePathEmpty(t *testing.T) {
	docs := []verifyDoc{{id: 1, title: "Doc 1"}}
	srv := newVerifyServer(t, docs)
	defer srv.Close()

	vault := importForVerify(t, srv.URL, Options{})

	matches, _ := filepath.Glob(filepath.Join(vault, "*.md"))
	if len(matches) == 0 {
		t.Fatal("no notes found after import")
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		if strings.Contains(line, "archive_path:") && !strings.HasPrefix(line, "---") {
			lines[i] = "archive_path: \"\""
			break
		}
	}
	if err := os.WriteFile(matches[0], []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := Verify(context.Background(), Options{BaseURL: srv.URL, Token: "test-token"}, vault, nil)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if report.Complete() {
		t.Fatal("expected incomplete verification when archive_path is empty")
	}
	if len(report.MissingArchive) != 1 || report.MissingArchive[0] != 1 {
		t.Errorf("MissingArchive = %v, want [1]", report.MissingArchive)
	}
}

func TestCompareMetadata_NilPaperless(t *testing.T) {
	lu := &lookups{
		tags:           map[int]string{1: "tag1"},
		correspondents: map[int]string{},
		documentTypes:  map[int]string{},
		storagePaths:   map[int]string{4: "Archive"},
	}
	doc := paperless.Document{
		ID:          1,
		Title:       "Doc 1",
		Created:     paperless.FlexDate{Time: time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)},
		Tags:        []paperless.Ref{{ID: 1}},
		StoragePath: &paperless.Ref{ID: 4},
	}
	note := &writer.Note{
		Tags:          []string{"tag1"},
		Correspondent: "",
		DocumentType:  "",
		Paperless:     nil,
	}
	mismatches := compareMetadata(doc, note, lu)
	foundStorage := false
	foundCreated := false
	for _, m := range mismatches {
		if m.Field == "storage_path" {
			foundStorage = true
		}
		if m.Field == "created" {
			foundCreated = true
		}
	}
	if !foundStorage {
		t.Errorf("expected storage_path mismatch when Paperless is nil, got %+v", mismatches)
	}
	if !foundCreated {
		t.Errorf("expected created mismatch when Paperless is nil, got %+v", mismatches)
	}
}

func TestCompareMetadata_MultipleMismatches(t *testing.T) {
	lu := &lookups{
		tags:           map[int]string{},
		correspondents: map[int]string{1: "Correct Corp"},
		documentTypes:  map[int]string{1: "Invoice"},
		storagePaths:   map[int]string{1: "Correct/Path"},
	}
	doc := paperless.Document{
		ID:            5,
		Title:         "Multi Mismatch",
		Correspondent: &paperless.Ref{ID: 1},
		DocumentType:  &paperless.Ref{ID: 1},
		StoragePath:   &paperless.Ref{ID: 1},
	}
	// Create a note with all fields wrong.
	note := &writer.Note{
		Tags:          []string{"wrong"},
		Correspondent: "Wrong Corp",
		DocumentType:  "Receipt",
		Paperless: &writer.PaperlessMeta{
			StoragePath: "Wrong/Path",
			Created:     time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		},
	}
	mismatches := compareMetadata(doc, note, lu)
	if len(mismatches) < 4 {
		t.Errorf("expected at least 4 mismatches (tags, correspondent, document_type, storage_path, created), got %d: %+v", len(mismatches), mismatches)
	}
	// Verify each field is represented.
	fields := map[string]bool{}
	for _, m := range mismatches {
		fields[m.Field] = true
	}
	for _, want := range []string{"tags", "correspondent", "document_type", "storage_path", "created"} {
		if !fields[want] {
			t.Errorf("missing mismatch for field %q", want)
		}
	}
}

func TestVerifyReport_Complete_Empty(t *testing.T) {
	r := &VerifyReport{}
	if !r.Complete() {
		t.Error("empty report should be complete")
	}
}

func TestVerifyReport_Complete_WithMissing(t *testing.T) {
	r := &VerifyReport{Missing: []int{1}}
	if r.Complete() {
		t.Error("report with missing should not be complete")
	}
}

func TestVerifyReport_Complete_WithDuplicate(t *testing.T) {
	r := &VerifyReport{Duplicate: []int{1}}
	if r.Complete() {
		t.Error("report with duplicate should not be complete")
	}
}

func TestVerifyReport_Complete_WithMissingArchive(t *testing.T) {
	r := &VerifyReport{MissingArchive: []int{1}}
	if r.Complete() {
		t.Error("report with missing archive should not be complete")
	}
}

func TestVerifyReport_Complete_WithMismatches(t *testing.T) {
	r := &VerifyReport{Mismatches: []VerifyMismatch{{DocumentID: 1, Field: "tags"}}}
	if r.Complete() {
		t.Error("report with mismatches should not be complete")
	}
}

func TestVerifyReport_Complete_WithSourceHashMismatch(t *testing.T) {
	r := &VerifyReport{SourceHashMismatch: []int{1}}
	if r.Complete() {
		t.Error("report with source hash mismatch should not be complete")
	}
}
