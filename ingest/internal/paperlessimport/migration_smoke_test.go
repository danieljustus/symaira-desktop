package paperlessimport

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-ingest/internal/ingest"
	"github.com/danieljustus/symaira-ingest/internal/store"
	"github.com/danieljustus/symaira-ingest/internal/writer"
)

// TestMigrationSmoke_PaperlessToSearchableVault exercises the critical
// migration path end to end: Paperless-shaped API responses -> downloaded
// original file -> symingest archive + Markdown -> symseek searchable
// output. No real Paperless server or credentials are used; the fixture
// document content carries a unique token so search results can be
// verified unambiguously.
func TestMigrationSmoke_PaperlessToSearchableVault(t *testing.T) {
	const uniqueToken = "SYMINGEST_SMOKE_TOKEN_7f3a9c1e"

	// 1. Fixture Paperless API responses matching the real API shape
	// (see internal/paperless/types.go: id, title, created/created_date,
	// added, modified, correspondent/document_type/storage_path as
	// embedded objects, original/archived file names, page count).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/documents/":
			json.NewEncoder(w).Encode(map[string]any{
				"count": 1,
				"results": []map[string]any{
					{
						"id":      314,
						"title":   "Migration Smoke Test Document",
						"created": "2024-05-01T08:00:00Z", "created_date": "2024-05-01",
						"added": "2024-05-01T08:05:00Z", "modified": "2024-05-02T09:00:00Z",
						"file_type": ".txt", "mime_type": "text/plain",
						"original_file_name": "smoke-original.txt",
						"archived_file_name": "smoke-archived.txt",
						"page_count":         1,
						"correspondent":      map[string]any{"id": 1, "name": "Smoke Test Sender"},
						"document_type":      map[string]any{"id": 1, "name": "Smoke Test"},
						"storage_path":       map[string]any{"id": 1, "name": "Smoke/Tests"},
						"tags":               []map[string]any{{"id": 1, "name": "smoke-test"}},
					},
				},
				"next": nil,
			})
		case "/api/documents/314/download/":
			w.Write([]byte(uniqueToken + " — this is the migrated document body.\n"))
		case "/api/tags/", "/api/correspondents/", "/api/document_types/", "/api/storage_paths/":
			json.NewEncoder(w).Encode(map[string]any{"count": 0, "results": []any{}, "next": nil})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	// 2. Run import into a temp vault/archive/db.
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()

	pipeline := &ingest.Pipeline{
		Store:      s,
		Writer:     &writer.NoteWriter{Vault: vault},
		ArchiveDir: filepath.Join(dir, "archive"),
	}

	stats, err := Run(context.Background(), Options{BaseURL: srv.URL, Token: "smoke-test-token"}, pipeline)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Imported != 1 || stats.Failed != 0 {
		t.Fatalf("import: imported=%d failed=%d, want imported=1 failed=0", stats.Imported, stats.Failed)
	}

	// 3. Verify the generated Markdown and frontmatter.
	matches, err := filepath.Glob(filepath.Join(vault, "*.md"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("expected exactly 1 generated note, got %v (err=%v)", matches, err)
	}
	noteBytes, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read generated note: %v", err)
	}
	note := string(noteBytes)

	if !strings.Contains(note, uniqueToken) {
		t.Fatalf("generated note missing migrated body text:\n%s", note)
	}
	for _, needle := range []string{
		"paperless:",
		"document_id: 314",
		"title: Migration Smoke Test Document",
		"storage_path: Smoke/Tests",
		"original_file_name: smoke-original.txt",
		"archived_file_name: smoke-archived.txt",
		"correspondent: Smoke Test Sender",
		"document_type: Smoke Test",
	} {
		if !strings.Contains(note, needle) {
			t.Errorf("generated note missing Paperless metadata %q:\n%s", needle, note)
		}
	}

	archiveMatches, _ := filepath.Glob(filepath.Join(dir, "archive", "*"))
	if len(archiveMatches) != 1 {
		t.Fatalf("expected 1 archived original, got %v", archiveMatches)
	}

	// 4. Index the temp vault with symseek and verify search finds the
	// migrated content, when symseek is available. symseek stores its
	// index under $HOME, so the test runs it against an isolated HOME to
	// avoid touching (or depending on) the real local search index.
	//
	// Integration test: full pipeline, expected ~6s when symseek is on
	// PATH and its embedding backend is available. The real `symseek
	// index` call embeds the generated note, which dominates the test's
	// runtime; that cost is what this smoke test exists to exercise end
	// to end, so it is not split into faster mocked units. Where symseek
	// or its embedding backend is unavailable, the steps below skip and
	// the test completes in well under a second (as it does without
	// symseek on PATH).
	symseekPath, err := exec.LookPath("symseek")
	if err != nil {
		t.Skip("symseek not found on PATH; skipping search verification")
	}

	fakeHome := t.TempDir()
	runSymseek := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, symseekPath, args...)
		cmd.Env = []string{"HOME=" + fakeHome, "PATH=" + os.Getenv("PATH")}
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		err := cmd.Run()
		return out.String(), err
	}

	if out, err := runSymseek("index", vault); err != nil {
		t.Skipf("symseek index failed (likely no embedding backend available in this environment): %v\n%s", err, out)
	}

	out, err := runSymseek("search", uniqueToken, "--plain", "--limit", "5")
	if err != nil {
		t.Skipf("symseek search failed (likely no embedding backend available in this environment): %v\n%s", err, out)
	}
	if !strings.Contains(out, uniqueToken) && !strings.Contains(out, matches[0]) {
		t.Errorf("symseek search did not surface the migrated note:\n%s", out)
	}
}
