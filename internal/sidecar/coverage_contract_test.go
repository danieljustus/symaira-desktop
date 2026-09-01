package sidecar

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/searchquery"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

func TestTimelineDocsMatchesDocumentAndModifiedDates(t *testing.T) {
	db := setupTestDB(t)
	modOutside := time.Date(2026, time.January, 2, 12, 0, 0, 0, time.UTC)
	modInside := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	for _, doc := range []*vault.Document{
		{
			Path: "/vault/by-document-date.md", Title: "Document date", Created: "2026-01-01T00:00:00Z",
			ModTime: modOutside, DocumentDate: "2026-07-10", Person: "Daniel", Status: "open",
			SHA256: "timeline-a", Body: "document date body",
			Frontmatter: map[string]interface{}{"tags": []interface{}{"july"}},
		},
		{
			Path: "/vault/by-modified-date.md", Title: "Modified date", Created: "2026-01-01T00:00:00Z",
			ModTime: modInside, DocumentDate: "2026-06-01", SHA256: "timeline-b", Body: "modified date body",
		},
		{
			Path: "/vault/outside.md", Title: "Outside", Created: "2026-01-01T00:00:00Z",
			ModTime: modOutside, DocumentDate: "2026-08-01", SHA256: "timeline-c", Body: "outside body",
		},
	} {
		if err := db.IndexDocument(doc); err != nil {
			t.Fatalf("IndexDocument(%s): %v", doc.Path, err)
		}
	}

	results, err := db.TimelineDocs("2026-07-01", "2026-07-31")
	if err != nil {
		t.Fatalf("TimelineDocs: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("TimelineDocs returned %d results, want 2: %#v", len(results), results)
	}
	if results[0].Path != "/vault/by-document-date.md" || results[1].Path != "/vault/by-modified-date.md" {
		t.Fatalf("TimelineDocs paths = [%s, %s], want document-date then modified-date", results[0].Path, results[1].Path)
	}
	if results[0].Person != "Daniel" || results[0].Status != "open" || len(results[0].Tags) != 1 || results[0].Tags[0] != "july" {
		t.Fatalf("TimelineDocs metadata = %#v, want person/status/tags preserved", results[0])
	}
}

func TestAllSimhashesReturnsNonEmptyBodiesInPathOrder(t *testing.T) {
	db := setupTestDB(t)
	for _, doc := range []*vault.Document{
		{Path: "/vault/zeta.md", Title: "Zeta", SHA256: "zeta", Simhash: "1111111111111111", Body: "zeta body"},
		{Path: "/vault/empty.md", Title: "Empty", SHA256: "empty", Simhash: "2222222222222222", Body: "   "},
		{Path: "/vault/alpha.md", Title: "Alpha", SHA256: "alpha", Body: "alpha body"},
	} {
		if err := db.IndexDocument(doc); err != nil {
			t.Fatalf("IndexDocument(%s): %v", doc.Path, err)
		}
	}

	results, err := db.AllSimhashes()
	if err != nil {
		t.Fatalf("AllSimhashes: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("AllSimhashes returned %d results, want 2: %#v", len(results), results)
	}
	if results[0].Path != "/vault/alpha.md" || results[1].Path != "/vault/zeta.md" {
		t.Fatalf("AllSimhashes paths = [%s, %s], want sorted non-empty bodies", results[0].Path, results[1].Path)
	}
	if results[0].Body != "alpha body" || results[1].Body != "zeta body" {
		t.Fatalf("AllSimhashes bodies = [%q, %q], want indexed content", results[0].Body, results[1].Body)
	}
	if results[0].Simhash == "" {
		t.Fatal("non-empty body without a stored simhash did not receive a computed simhash")
	}
}

func TestSearchPlanAtAppliesMultiValueAndDateFilters(t *testing.T) {
	db := setupTestDB(t)
	for _, doc := range []*vault.Document{
		{
			Path: "/vault/invoice.md", Title: "Invoice", Type: "invoice", Created: "2026-07-15T10:00:00Z", SHA256: "invoice",
			ModTime: time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC), Status: "open", Body: "needle record",
			Frontmatter: map[string]interface{}{"document_type": "invoice"},
		},
		{
			Path: "/vault/receipt.md", Title: "Receipt", Type: "receipt", Created: "2026-06-15T10:00:00Z", SHA256: "receipt",
			ModTime: time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC), Status: "paid", Body: "needle record",
			Frontmatter: map[string]interface{}{"document_type": "receipt"},
		},
	} {
		if err := db.IndexDocument(doc); err != nil {
			t.Fatalf("IndexDocument(%s): %v", doc.Path, err)
		}
	}

	plan := searchquery.Plan{
		Terms: []searchquery.Term{{Value: "needle"}},
		Filters: []searchquery.Filter{
			{Field: searchquery.FieldPath, Value: "/vault/invoice.md, /vault/receipt.md"},
			{Field: searchquery.FieldStatus, Value: "open, paid"},
			{Field: searchquery.FieldType, Value: "invoice, receipt"},
			{Field: searchquery.FieldCreated, Value: "2026-07-15"},
			{Field: searchquery.FieldStatus, Value: "closed, void", Negated: true},
		},
	}
	results, err := db.SearchPlanAt(plan, time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("SearchPlanAt: %v", err)
	}
	if len(results) != 1 || results[0].Path != "/vault/invoice.md" {
		t.Fatalf("SearchPlanAt results = %#v, want only July invoice", results)
	}
	if matchesDateList("not-a-timestamp", "2026-07-15", time.Now()) {
		t.Fatal("invalid indexed timestamp unexpectedly matched a date filter")
	}
}

func TestDocsListIncludesAndFiltersIndexLifecycle(t *testing.T) {
	db := setupTestDB(t)
	doc := &vault.Document{Path: "/vault/failed.md", Title: "Failed", Created: "2026-01-01T00:00:00Z", SHA256: "failed", Body: "body"}
	if err := db.IndexDocument(doc); err != nil {
		t.Fatal(err)
	}
	if err := db.SetIndexStatus(doc.Path, IndexStateFailed, "backend unavailable"); err != nil {
		t.Fatal(err)
	}
	results, err := db.DocsList(DocsFilter{IndexState: string(IndexStateFailed)})
	if err != nil {
		t.Fatalf("DocsList: %v", err)
	}
	if len(results) != 1 || results[0].IndexState != string(IndexStateFailed) || results[0].IndexFailureReason != "backend unavailable" {
		t.Fatalf("DocsList lifecycle result = %#v, want failed status and reason", results)
	}
}

func TestOpenForVaultHonorsExplicitSidecarOverride(t *testing.T) {
	explicit := filepath.Join(t.TempDir(), "explicit", "sidecar.db")
	t.Setenv("SYMDESK_SIDECAR", explicit)
	db, err := OpenForVault(filepath.Join(t.TempDir(), "vault"))
	if err != nil {
		t.Fatalf("OpenForVault: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(explicit); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("explicit sidecar = %v, want regular file", err)
	}
}
