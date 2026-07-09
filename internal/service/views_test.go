package service

import (
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/dbviews"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

func TestViewsListEmpty(t *testing.T) {
	svc := newTestService(t)

	views, err := svc.ViewsList()
	if err != nil {
		t.Fatalf("ViewsList failed: %v", err)
	}
	if len(views) != 0 {
		t.Errorf("expected 0 views, got %d", len(views))
	}
}

func TestViewsSaveAndGet(t *testing.T) {
	svc := newTestService(t)

	data := []byte(`{"id":"v1","name":"Invoices","type":"table","columns":["title","status"]}`)
	if err := svc.ViewsSave(data); err != nil {
		t.Fatalf("ViewsSave failed: %v", err)
	}

	views, err := svc.ViewsList()
	if err != nil {
		t.Fatalf("ViewsList failed: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("expected 1 view, got %d", len(views))
	}
	if views[0].ID != "v1" || views[0].Name != "Invoices" {
		t.Errorf("unexpected view %+v", views[0])
	}

	got, err := svc.ViewsGet("v1")
	if err != nil {
		t.Fatalf("ViewsGet failed: %v", err)
	}
	if got.Name != "Invoices" {
		t.Errorf("expected name 'Invoices', got '%s'", got.Name)
	}
}

func TestViewsGetNotFound(t *testing.T) {
	svc := newTestService(t)

	if _, err := svc.ViewsGet("missing"); err == nil {
		t.Error("expected error for missing view")
	}
}

func TestViewsSaveInvalidJSON(t *testing.T) {
	svc := newTestService(t)

	if err := svc.ViewsSave([]byte("{not json")); err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestViewsExecFiltersAndFormula(t *testing.T) {
	svc := newTestService(t)

	docs := []*vault.Document{
		{
			Path:    filepath.Join(svc.VaultRoot, "a.md"),
			Title:   "A",
			Body:    "a",
			SHA256:  "a1",
			Created: "2026-01-01T00:00:00Z",
			Frontmatter: map[string]interface{}{
				"status": "open",
			},
		},
		{
			Path:    filepath.Join(svc.VaultRoot, "b.md"),
			Title:   "B",
			Body:    "b",
			SHA256:  "b1",
			Created: "2026-01-01T00:00:00Z",
			Frontmatter: map[string]interface{}{
				"status": "paid",
			},
		},
	}
	for _, d := range docs {
		if err := svc.DB.IndexDocument(d); err != nil {
			t.Fatal(err)
		}
	}

	view := dbviews.View{
		ID:      "v2",
		Name:    "Open",
		Type:    "table",
		Filters: []dbviews.Filter{{Key: "status", Value: "open"}},
		Computed: map[string]dbviews.ComputedColumn{
			"label": {Formula: "{status} - {_title}"},
		},
	}
	if err := svc.ViewsMgr.Save(view); err != nil {
		t.Fatal(err)
	}

	results, err := svc.ViewsExec("v2")
	if err != nil {
		t.Fatalf("ViewsExec failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0]["_title"] != "A" {
		t.Errorf("expected title 'A', got %v", results[0]["_title"])
	}
	if results[0]["label"] != "open - A" {
		t.Errorf("expected formula result 'open - A', got %v", results[0]["label"])
	}
}

func TestViewsExecRollups(t *testing.T) {
	svc := newTestService(t)

	parent := &vault.Document{
		Path:    filepath.Join(svc.VaultRoot, "parent.md"),
		Title:   "Parent",
		Body:    "parent",
		SHA256:  "p1",
		Created: "2026-01-01T00:00:00Z",
		Frontmatter: map[string]interface{}{
			"status": "open",
		},
	}
	child1 := &vault.Document{
		Path:    filepath.Join(svc.VaultRoot, "child1.md"),
		Title:   "Child 1",
		Body:    "child1",
		SHA256:  "c1",
		Created: "2026-01-01T00:00:00Z",
		Frontmatter: map[string]interface{}{
			"amount": "10.5",
		},
	}
	parent.Links = []string{
		filepath.Join(svc.VaultRoot, "child1.md"),
		filepath.Join(svc.VaultRoot, "child2.md"),
	}

	child2 := &vault.Document{
		Path:    filepath.Join(svc.VaultRoot, "child2.md"),
		Title:   "Child 2",
		Body:    "child2",
		SHA256:  "c2",
		Created: "2026-01-01T00:00:00Z",
		Frontmatter: map[string]interface{}{
			"amount": "20.0",
		},
	}

	for _, d := range []*vault.Document{parent, child1, child2} {
		if err := svc.DB.IndexDocument(d); err != nil {
			t.Fatal(err)
		}
	}

	view := dbviews.View{
		ID:   "v3",
		Name: "Parent rollup",
		Type: "table",
		Computed: map[string]dbviews.ComputedColumn{
			"link_count": {Rollup: "count"},
			"total":      {Rollup: "sum(links.amount)"},
			"names":      {Rollup: "list(links._title)"},
		},
	}
	if err := svc.ViewsMgr.Save(view); err != nil {
		t.Fatal(err)
	}

	results, err := svc.ViewsExec("v3")
	if err != nil {
		t.Fatalf("ViewsExec failed: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	var parentRow map[string]interface{}
	for _, r := range results {
		if r["_title"] == "Parent" {
			parentRow = r
			break
		}
	}
	if parentRow == nil {
		t.Fatal("parent row not found")
	}
	if parentRow["link_count"] != "2" {
		t.Errorf("expected link_count '2', got %v", parentRow["link_count"])
	}
	if parentRow["total"] != "30.50" {
		t.Errorf("expected total '30.50', got %v", parentRow["total"])
	}
	if parentRow["names"] != "Child 1, Child 2" {
		t.Errorf("expected names 'Child 1, Child 2', got %v", parentRow["names"])
	}

	// child rows have no outgoing links, so rollups should be empty
	for _, r := range results {
		if r["_title"] != "Parent" {
			if r["link_count"] != "" || r["total"] != "" || r["names"] != "" {
				t.Errorf("expected empty rollups for child, got %v", r)
			}
		}
	}
}

func TestViewsExecMissingView(t *testing.T) {
	svc := newTestService(t)

	if _, err := svc.ViewsExec("missing"); err == nil {
		t.Error("expected error for missing view")
	}
}

func TestViewsExecDBError(t *testing.T) {
	svc := newTestService(t)

	view := dbviews.View{ID: "v4", Name: "DB Error", Type: "table"}
	if err := svc.ViewsMgr.Save(view); err != nil {
		t.Fatal(err)
	}

	if err := svc.DB.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.ViewsExec("v4"); err == nil {
		t.Error("expected error when DB is closed")
	}
}

func TestViewsSaveUpdatesExisting(t *testing.T) {
	svc := newTestService(t)

	data := []byte(`{"id":"v5","name":"Original","type":"table"}`)
	if err := svc.ViewsSave(data); err != nil {
		t.Fatal(err)
	}
	updated := []byte(`{"id":"v5","name":"Updated","type":"table"}`)
	if err := svc.ViewsSave(updated); err != nil {
		t.Fatal(err)
	}

	got, err := svc.ViewsGet("v5")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Updated" {
		t.Errorf("expected name 'Updated', got '%s'", got.Name)
	}
}

func TestViewsExecNoComputedColumns(t *testing.T) {
	svc := newTestService(t)

	doc := &vault.Document{
		Path:    filepath.Join(svc.VaultRoot, "plain.md"),
		Title:   "Plain",
		Body:    "plain",
		SHA256:  "p1",
		Created: "2026-01-01T00:00:00Z",
	}
	if err := svc.DB.IndexDocument(doc); err != nil {
		t.Fatal(err)
	}

	view := dbviews.View{ID: "v6", Name: "Plain", Type: "table"}
	if err := svc.ViewsMgr.Save(view); err != nil {
		t.Fatal(err)
	}

	results, err := svc.ViewsExec("v6")
	if err != nil {
		t.Fatalf("ViewsExec failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0]["_title"] != "Plain" {
		t.Errorf("expected title 'Plain', got %v", results[0]["_title"])
	}
}
