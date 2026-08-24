package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/dbviews"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
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

func TestViewsExecNestedFilterGroups(t *testing.T) {
	svc := newTestService(t)
	for _, doc := range []*vault.Document{
		{Path: filepath.Join(svc.VaultRoot, "open.md"), Title: "Open", Body: "", SHA256: "open", Created: "2026-01-01T00:00:00Z", Frontmatter: map[string]interface{}{"status": "open", "priority": "high"}},
		{Path: filepath.Join(svc.VaultRoot, "paid.md"), Title: "Paid", Body: "", SHA256: "paid", Created: "2026-01-01T00:00:00Z", Frontmatter: map[string]interface{}{"status": "paid", "priority": "low"}},
	} {
		if err := svc.DB.IndexDocument(doc); err != nil {
			t.Fatal(err)
		}
	}
	view := dbviews.View{ID: "nested", Name: "Nested", FilterGroup: &dbviews.FilterGroup{Operator: "all", Groups: []dbviews.FilterGroup{{Operator: "any", Filters: []dbviews.Filter{{Key: "status", Value: "open"}, {Key: "priority", Value: "urgent"}}}}, Filters: []dbviews.Filter{{Key: "priority", Operator: "not_equals", Value: "low"}}}}
	if err := svc.ViewsMgr.Save(view); err != nil {
		t.Fatal(err)
	}
	results, err := svc.ViewsExec("nested")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0]["_title"] != "Open" {
		t.Fatalf("expected only open document, got %#v", results)
	}
}

func TestViewsExecAnyFilterGroup(t *testing.T) {
	svc := newTestService(t)
	for _, doc := range []*vault.Document{
		{Path: filepath.Join(svc.VaultRoot, "open.md"), Title: "Open", Body: "", SHA256: "open", Created: "2026-01-01T00:00:00Z", Frontmatter: map[string]interface{}{"status": "open"}},
		{Path: filepath.Join(svc.VaultRoot, "urgent.md"), Title: "Urgent", Body: "", SHA256: "urgent", Created: "2026-01-01T00:00:00Z", Frontmatter: map[string]interface{}{"priority": "urgent"}},
		{Path: filepath.Join(svc.VaultRoot, "done.md"), Title: "Done", Body: "", SHA256: "done", Created: "2026-01-01T00:00:00Z", Frontmatter: map[string]interface{}{"status": "done"}},
	} {
		if err := svc.DB.IndexDocument(doc); err != nil {
			t.Fatal(err)
		}
	}
	view := dbviews.View{ID: "any", Name: "Any", FilterGroup: &dbviews.FilterGroup{
		Operator: "any",
		Filters: []dbviews.Filter{
			{Key: "status", Value: "open"},
			{Key: "priority", Value: "urgent"},
		},
	}}
	if err := svc.ViewsMgr.Save(view); err != nil {
		t.Fatal(err)
	}
	results, err := svc.ViewsExec("any")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d: %#v", len(results), results)
	}
	titles := map[interface{}]bool{results[0]["_title"]: true, results[1]["_title"]: true}
	if !titles["Open"] || !titles["Urgent"] {
		t.Fatalf("expected Open and Urgent, got %v", titles)
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

func TestViewsNewEntryAppliesTemplateDefaultsAndFilters(t *testing.T) {
	svc := newTestService(t)
	view := dbviews.View{ID: "inbox", Name: "Inbox", Filters: []dbviews.Filter{{Key: "status", Operator: "equals", Value: "open"}}, Template: &dbviews.Template{Defaults: map[string]string{"priority": "high"}}}
	if err := svc.ViewsMgr.Save(view); err != nil {
		t.Fatal(err)
	}
	path, err := svc.ViewsNewEntry("inbox", "Created from view")
	if err != nil {
		t.Fatal(err)
	}
	props, err := svc.DB.GetProperties(filepath.Join(svc.VaultRoot, path))
	if err != nil {
		t.Fatal(err)
	}
	if props["status"] != "open" || props["priority"] != "high" {
		t.Fatalf("unexpected properties: %#v", props)
	}
}

func TestViewsSiblingsUseSourceOnly(t *testing.T) {
	svc := newTestService(t)
	for _, view := range []dbviews.View{{ID: "a", Source: "tasks"}, {ID: "b", Source: "tasks"}, {ID: "c", Source: "notes"}} {
		if err := svc.ViewsMgr.Save(view); err != nil {
			t.Fatal(err)
		}
	}
	siblings, err := svc.ViewsSiblings("a")
	if err != nil {
		t.Fatal(err)
	}
	if len(siblings) != 2 {
		t.Fatalf("expected two task views, got %d", len(siblings))
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

func TestViewsExecScopedToFolder(t *testing.T) {
	svc := newTestService(t)

	// Create directories on disk
	invoicesDir := filepath.Join(svc.VaultRoot, "invoices")
	archiveDir := filepath.Join(svc.VaultRoot, "archive")
	if err := os.MkdirAll(invoicesDir, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(archiveDir, 0750); err != nil {
		t.Fatal(err)
	}

	docs := []*vault.Document{
		{
			Path:        filepath.Join(invoicesDir, "inv1.md"),
			Title:       "Invoice 1",
			Body:        "inv1",
			SHA256:      "i1",
			Created:     "2026-01-01T00:00:00Z",
			Frontmatter: map[string]interface{}{"status": "open", "amount": "100"},
		},
		{
			Path:        filepath.Join(invoicesDir, "inv2.md"),
			Title:       "Invoice 2",
			Body:        "inv2",
			SHA256:      "i2",
			Created:     "2026-01-01T00:00:00Z",
			Frontmatter: map[string]interface{}{"status": "paid", "amount": "200"},
		},
		{
			Path:        filepath.Join(archiveDir, "inv3.md"),
			Title:       "Invoice 3 Archived",
			Body:        "inv3",
			SHA256:      "i3",
			Created:     "2026-01-01T00:00:00Z",
			Frontmatter: map[string]interface{}{"status": "open", "amount": "300"},
		},
		{
			Path:        filepath.Join(svc.VaultRoot, "root.md"),
			Title:       "Root Open Note",
			Body:        "root",
			SHA256:      "r1",
			Created:     "2026-01-01T00:00:00Z",
			Frontmatter: map[string]interface{}{"status": "open", "amount": "400"},
		},
	}
	for _, d := range docs {
		if err := svc.DB.IndexDocument(d); err != nil {
			t.Fatal(err)
		}
	}

	// 1. View with source "invoices/" and filter status=open
	// Should only return inv1.md under invoices/, excluding matching notes in archive/ and root
	view := dbviews.View{
		ID:      "v_folder",
		Name:    "Open Invoices",
		Type:    "table",
		Source:  "invoices/",
		Filters: []dbviews.Filter{{Key: "status", Value: "open"}},
	}
	if err := svc.ViewsMgr.Save(view); err != nil {
		t.Fatal(err)
	}

	results, err := svc.ViewsExec("v_folder")
	if err != nil {
		t.Fatalf("ViewsExec failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result from invoices/, got %d: %#v", len(results), results)
	}
	if results[0]["_title"] != "Invoice 1" {
		t.Errorf("expected Invoice 1, got %v", results[0]["_title"])
	}

	// 2. View with source "invoices" without trailing slash
	viewNoSlash := dbviews.View{
		ID:     "v_folder_noslash",
		Name:   "All Invoices",
		Type:   "table",
		Source: "invoices",
	}
	if err := svc.ViewsMgr.Save(viewNoSlash); err != nil {
		t.Fatal(err)
	}

	resultsNoSlash, err := svc.ViewsExec("v_folder_noslash")
	if err != nil {
		t.Fatalf("ViewsExec without slash failed: %v", err)
	}
	if len(resultsNoSlash) != 2 {
		t.Fatalf("expected 2 results from invoices directory, got %d", len(resultsNoSlash))
	}
}

func TestViewsExecScopedToTag(t *testing.T) {
	svc := newTestService(t)

	docs := []*vault.Document{
		{
			Path:        filepath.Join(svc.VaultRoot, "a.md"),
			Title:       "Doc A",
			Body:        "body a",
			SHA256:      "a1",
			Created:     "2026-01-01T00:00:00Z",
			Frontmatter: map[string]interface{}{"tags": []string{"invoice", "urgent"}, "status": "open"},
		},
		{
			Path:        filepath.Join(svc.VaultRoot, "b.md"),
			Title:       "Doc B",
			Body:        "body b",
			SHA256:      "b1",
			Created:     "2026-01-01T00:00:00Z",
			Frontmatter: map[string]interface{}{"tags": []string{"invoice"}, "status": "paid"},
		},
		{
			Path:        filepath.Join(svc.VaultRoot, "c.md"),
			Title:       "Doc C",
			Body:        "body c",
			SHA256:      "c1",
			Created:     "2026-01-01T00:00:00Z",
			Frontmatter: map[string]interface{}{"tags": []string{"personal"}, "status": "open"},
		},
		{
			Path:        filepath.Join(svc.VaultRoot, "d.md"),
			Title:       "Doc D",
			Body:        "body d",
			SHA256:      "d1",
			Created:     "2026-01-01T00:00:00Z",
			Frontmatter: map[string]interface{}{"status": "open"},
		},
	}
	for _, d := range docs {
		if err := svc.DB.IndexDocument(d); err != nil {
			t.Fatal(err)
		}
	}

	// 1. Tag source "tag:invoice" with filter status=open -> only Doc A
	view := dbviews.View{
		ID:      "v_tag",
		Name:    "Open Tagged Invoices",
		Type:    "table",
		Source:  "tag:invoice",
		Filters: []dbviews.Filter{{Key: "status", Value: "open"}},
	}
	if err := svc.ViewsMgr.Save(view); err != nil {
		t.Fatal(err)
	}

	results, err := svc.ViewsExec("v_tag")
	if err != nil {
		t.Fatalf("ViewsExec tag scope failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d: %#v", len(results), results)
	}
	if results[0]["_title"] != "Doc A" {
		t.Errorf("expected Doc A, got %v", results[0]["_title"])
	}

	// 2. Tag source case-insensitivity: "tag:INVOICE" without filter -> Doc A and Doc B
	viewCase := dbviews.View{
		ID:     "v_tag_case",
		Name:   "All Invoices Tag",
		Type:   "table",
		Source: "tag:INVOICE",
	}
	if err := svc.ViewsMgr.Save(viewCase); err != nil {
		t.Fatal(err)
	}

	resultsCase, err := svc.ViewsExec("v_tag_case")
	if err != nil {
		t.Fatalf("ViewsExec tag case failed: %v", err)
	}
	if len(resultsCase) != 2 {
		t.Fatalf("expected 2 results for tag:INVOICE, got %d", len(resultsCase))
	}
}

func TestViewsExecScopedToNotebook(t *testing.T) {
	svc := newTestService(t)

	// Create real notes
	noteA, err := svc.NoteNew("Note A", "body a", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.PropsEdit(noteA, "status", "open"); err != nil {
		t.Fatal(err)
	}

	noteB, err := svc.NoteNew("Note B", "body b", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.PropsEdit(noteB, "status", "closed"); err != nil {
		t.Fatal(err)
	}

	noteC, err := svc.NoteNew("Note C", "body c", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.PropsEdit(noteC, "status", "open"); err != nil {
		t.Fatal(err)
	}

	// Create notebook containing only Note A and Note B
	nb, err := svc.NotebookNew("Research Project", "desc")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.NotebookAddSource(nb.ID, noteA); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.NotebookAddSource(nb.ID, noteB); err != nil {
		t.Fatal(err)
	}

	// View scoped to notebook:research-project
	view := dbviews.View{
		ID:      "v_nb",
		Name:    "Notebook Scope",
		Type:    "table",
		Source:  "notebook:" + nb.ID,
		Filters: []dbviews.Filter{{Key: "status", Value: "open"}},
	}
	if err := svc.ViewsMgr.Save(view); err != nil {
		t.Fatal(err)
	}

	results, err := svc.ViewsExec("v_nb")
	if err != nil {
		t.Fatalf("ViewsExec notebook scope failed: %v", err)
	}
	// Only Note A should be returned (Note C is not in the notebook, Note B is closed)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d: %#v", len(results), results)
	}
	if results[0]["_title"] != "Note A" {
		t.Errorf("expected Note A, got %v", results[0]["_title"])
	}
}

func TestViewsExecEmptySourceRegression(t *testing.T) {
	svc := newTestService(t)

	// Notes across multiple folders and tags
	invoicesDir := filepath.Join(svc.VaultRoot, "invoices")
	if err := os.MkdirAll(invoicesDir, 0750); err != nil {
		t.Fatal(err)
	}

	docs := []*vault.Document{
		{
			Path:        filepath.Join(invoicesDir, "1.md"),
			Title:       "Inv 1",
			Body:        "1",
			SHA256:      "s1",
			Created:     "2026-01-01T00:00:00Z",
			Frontmatter: map[string]interface{}{"status": "open"},
		},
		{
			Path:        filepath.Join(svc.VaultRoot, "2.md"),
			Title:       "Root 2",
			Body:        "2",
			SHA256:      "s2",
			Created:     "2026-01-01T00:00:00Z",
			Frontmatter: map[string]interface{}{"status": "open"},
		},
	}
	for _, d := range docs {
		if err := svc.DB.IndexDocument(d); err != nil {
			t.Fatal(err)
		}
	}

	// Empty source means entire vault
	view := dbviews.View{
		ID:      "v_empty",
		Name:    "All Vault Open",
		Type:    "table",
		Source:  "",
		Filters: []dbviews.Filter{{Key: "status", Value: "open"}},
	}
	if err := svc.ViewsMgr.Save(view); err != nil {
		t.Fatal(err)
	}

	results, err := svc.ViewsExec("v_empty")
	if err != nil {
		t.Fatalf("ViewsExec empty source failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results from across entire vault, got %d", len(results))
	}
}

func TestViewsExecUnresolvableSourceErrors(t *testing.T) {
	svc := newTestService(t)

	doc := &vault.Document{
		Path:    filepath.Join(svc.VaultRoot, "file.md"),
		Title:   "A File",
		Body:    "file",
		SHA256:  "f1",
		Created: "2026-01-01T00:00:00Z",
	}
	if err := svc.DB.IndexDocument(doc); err != nil {
		t.Fatal(err)
	}

	testCases := []struct {
		id     string
		source string
		desc   string
	}{
		{"err_missing_folder", "does-not-exist/", "nonexistent folder"},
		{"err_file_as_folder", "file.md", "file instead of folder"},
		{"err_missing_nb", "notebook:nonexistent-nb-id", "unknown notebook"},
		{"err_empty_nb", "notebook:", "empty notebook ref"},
		{"err_empty_tag", "tag:", "empty tag name"},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			view := dbviews.View{
				ID:     tc.id,
				Name:   tc.desc,
				Type:   "table",
				Source: tc.source,
			}
			if err := svc.ViewsMgr.Save(view); err != nil {
				t.Fatal(err)
			}

			results, err := svc.ViewsExec(tc.id)
			if err == nil {
				t.Fatalf("expected error for unresolvable source %q, got results: %#v", tc.source, results)
			}
			if len(results) != 0 {
				t.Fatalf("expected no rows on unresolvable source error, got %d rows", len(results))
			}
		})
	}
}

func TestViewsExecPropertyFetchProportionalToScope(t *testing.T) {
	svc := newTestService(t)

	invoicesDir := filepath.Join(svc.VaultRoot, "invoices")
	otherDir := filepath.Join(svc.VaultRoot, "other")
	if err := os.MkdirAll(invoicesDir, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(otherDir, 0750); err != nil {
		t.Fatal(err)
	}

	// 1 scoped file in invoices/
	if err := svc.DB.IndexDocument(&vault.Document{
		Path:        filepath.Join(invoicesDir, "target.md"),
		Title:       "Target",
		Body:        "target",
		SHA256:      "t1",
		Created:     "2026-01-01T00:00:00Z",
		Frontmatter: map[string]interface{}{"status": "active"},
	}); err != nil {
		t.Fatal(err)
	}

	// 20 files in other/
	for i := 0; i < 20; i++ {
		p := filepath.Join(otherDir, filepath.Clean(filepath.Join(string(rune('a'+i))+".md")))
		if err := svc.DB.IndexDocument(&vault.Document{
			Path:        p,
			Title:       "Other",
			Body:        "other",
			SHA256:      p,
			Created:     "2026-01-01T00:00:00Z",
			Frontmatter: map[string]interface{}{"status": "active"},
		}); err != nil {
			t.Fatal(err)
		}
	}

	view := dbviews.View{
		ID:     "v_proportional",
		Name:   "Only Invoices",
		Type:   "table",
		Source: "invoices/",
	}
	if err := svc.ViewsMgr.Save(view); err != nil {
		t.Fatal(err)
	}

	results, err := svc.ViewsExec("v_proportional")
	if err != nil {
		t.Fatalf("ViewsExec failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected exactly 1 scoped row, got %d", len(results))
	}
	if results[0]["_title"] != "Target" {
		t.Errorf("expected Target, got %v", results[0]["_title"])
	}
}

func TestBaseNotesIndexedInSearchAndGraph(t *testing.T) {
	svc := newTestService(t)

	// Create a base note via BaseNew
	base, err := svc.BaseNew("Accounting Dashboard", "Central hub for financial views")
	if err != nil {
		t.Fatalf("BaseNew failed: %v", err)
	}

	// Add a view to the base
	base.Views = append(base.Views, dbviews.View{
		ID:     "v_acct_open",
		Name:   "Open Invoices View",
		Type:   "table",
		Source: "invoices/",
	})
	if err := svc.BaseSave(base); err != nil {
		t.Fatalf("BaseSave failed: %v", err)
	}

	// 1. Verify search returns the base note
	searchResults, err := svc.Search("Accounting Dashboard")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	foundInSearch := false
	for _, res := range searchResults {
		if strings.Contains(res.Path, "accounting-dashboard.md") {
			foundInSearch = true
			if res.Title != "Accounting Dashboard" {
				t.Errorf("expected title 'Accounting Dashboard', got %q", res.Title)
			}
			break
		}
	}
	if !foundInSearch {
		t.Errorf("expected base note to be found in search results: %#v", searchResults)
	}

	// 2. Verify graph includes the base node
	graphData, err := svc.Graph()
	if err != nil {
		t.Fatalf("Graph failed: %v", err)
	}
	foundInGraph := false
	for _, node := range graphData.Nodes {
		if node.ID == "bases/accounting-dashboard.md" {
			foundInGraph = true
			break
		}
	}
	if !foundInGraph {
		t.Errorf("expected base note in graph nodes: %#v", graphData.Nodes)
	}
}

func TestBaseNotesWikilinksAndBacklinks(t *testing.T) {
	svc := newTestService(t)

	// Create target note
	targetPath, err := svc.NoteNew("invoice_target", "Invoice details here", "")
	if err != nil {
		t.Fatal(err)
	}

	// Create a base note whose description links to the target note
	base, err := svc.BaseNew("Client Billing", "See [[invoice_target]] for details")
	if err != nil {
		t.Fatal(err)
	}
	base.Views = []dbviews.View{
		{ID: "v_client", Name: "Client View", Type: "table"},
	}
	if err := svc.BaseSave(base); err != nil {
		t.Fatal(err)
	}

	// Backlinks for target note should contain the base note
	backlinks, err := svc.Backlinks(targetPath)
	if err != nil {
		t.Fatalf("Backlinks failed: %v", err)
	}
	foundBacklink := false
	for _, bl := range backlinks {
		if bl == "bases/client-billing.md" {
			foundBacklink = true
			break
		}
	}
	if !foundBacklink {
		t.Errorf("expected bases/client-billing.md in backlinks of target note, got: %v", backlinks)
	}
}

func TestBaseNotesInNotebookSources(t *testing.T) {
	svc := newTestService(t)

	// Create a base note
	base, err := svc.BaseNew("Research Base", "Database views for research")
	if err != nil {
		t.Fatal(err)
	}

	// Create a notebook and add the base note as a source
	nb, err := svc.NotebookNew("Q3 Project Notebook", "Description")
	if err != nil {
		t.Fatal(err)
	}

	updatedNb, err := svc.NotebookAddSource(nb.ID, base.Path)
	if err != nil {
		t.Fatalf("NotebookAddSource failed: %v", err)
	}
	if len(updatedNb.Sources) != 1 || updatedNb.Sources[0] != "bases/research-base.md" {
		t.Fatalf("expected bases/research-base.md in notebook sources, got: %v", updatedNb.Sources)
	}

	// Resolve sources
	sources, err := updatedNb.ResolveSources(svc.VaultRoot)
	if err != nil {
		t.Fatalf("ResolveSources failed: %v", err)
	}
	if len(sources) != 1 || sources[0].Missing || sources[0].Title != "Research Base" {
		t.Errorf("unexpected resolved sources: %+v", sources)
	}
}

func TestBaseMutationHistoryAndRestore(t *testing.T) {
	svc := newTestService(t)

	// Save view 1
	v1 := []byte(`{"id":"v_hist","name":"Original Title","type":"table","columns":["title"]}`)
	if err := svc.ViewsSave(v1); err != nil {
		t.Fatal(err)
	}

	// Mutate view
	v2 := []byte(`{"id":"v_hist","name":"Modified Title","type":"table","columns":["title","status"]}`)
	if err := svc.ViewsSave(v2); err != nil {
		t.Fatal(err)
	}

	got, err := svc.ViewsGet("v_hist")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Modified Title" {
		t.Fatalf("expected Modified Title, got %s", got.Name)
	}

	// Find the base note path
	bases, err := svc.BaseList()
	if err != nil || len(bases) == 0 {
		t.Fatalf("failed to find base: %v", err)
	}
	basePath := bases[0].Path

	// Verify history exists
	entries, err := svc.History.List(basePath)
	if err != nil {
		t.Fatalf("History.List failed: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected history snapshot to be created on mutation")
	}

	// Restore prior snapshot
	if _, err := svc.History.Restore(basePath, entries[0].ID); err != nil {
		t.Fatalf("History.Restore failed: %v", err)
	}

	// Reinitialize service to refresh memory state from restored note
	svcRestored := New(svc.VaultRoot, svc.DB)
	restoredView, err := svcRestored.ViewsGet("v_hist")
	if err != nil {
		t.Fatalf("ViewsGet after restore failed: %v", err)
	}
	if restoredView.Name != "Original Title" {
		t.Errorf("expected restored name 'Original Title', got %q", restoredView.Name)
	}
}

func TestServiceStartupMigratesLegacyViews(t *testing.T) {
	root := t.TempDir()

	// Write legacy .symdesk/views.json
	symdeskDir := filepath.Join(root, ".symdesk")
	if err := os.MkdirAll(symdeskDir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacyJSON := []byte(`[{"id":"legacy_svc_1","name":"Legacy Svc View","type":"table","source":"invoices/"}]`)
	legacyFile := filepath.Join(symdeskDir, "views.json")
	if err := os.WriteFile(legacyFile, legacyJSON, 0o600); err != nil {
		t.Fatal(err)
	}

	// Create service
	dbPath := filepath.Join(root, "test.db")
	db, err := sidecar.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	svc := New(root, db)

	// List should return the migrated view
	views, err := svc.ViewsList()
	if err != nil {
		t.Fatalf("ViewsList failed: %v", err)
	}
	if len(views) != 1 || views[0].ID != "legacy_svc_1" {
		t.Fatalf("unexpected views after migration: %+v", views)
	}

	// Check that .symdesk/views.json is still intact
	// #nosec G304 -- legacyFile is created below t.TempDir.
	onDisk, err := os.ReadFile(legacyFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != string(legacyJSON) {
		t.Error("legacy views.json was modified")
	}

	// Check base file exists in bases/
	basesDir := filepath.Join(root, "bases")
	entries, err := os.ReadDir(basesDir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("expected base note in bases/, got %v (err: %v)", entries, err)
	}
}

func TestViewsExecNumericFiltering(t *testing.T) {
	svc := newTestService(t)

	docs := []*vault.Document{
		{
			Path:        filepath.Join(svc.VaultRoot, "inv1.md"),
			Title:       "Invoice 1",
			Frontmatter: map[string]interface{}{"amount": "100.50", "status": "open"},
		},
		{
			Path:        filepath.Join(svc.VaultRoot, "inv2.md"),
			Title:       "Invoice 2",
			Frontmatter: map[string]interface{}{"amount": "25.00", "status": "open"},
		},
		{
			Path:        filepath.Join(svc.VaultRoot, "inv3.md"),
			Title:       "Invoice 3",
			Frontmatter: map[string]interface{}{"amount": "5.00", "status": "open"},
		},
		{
			Path:        filepath.Join(svc.VaultRoot, "inv4.md"),
			Title:       "Invoice 4",
			Frontmatter: map[string]interface{}{"amount": "100.50", "status": "paid"},
		},
	}
	for _, d := range docs {
		if err := svc.DB.IndexDocument(d); err != nil {
			t.Fatal(err)
		}
	}

	// 1. Greater than (amount > 20) -> should match inv1, inv2, inv4
	vGt := dbviews.View{
		ID:      "v_gt",
		Name:    "GT 20",
		Filters: []dbviews.Filter{{Key: "amount", Operator: ">", Value: "20"}},
	}
	if err := svc.ViewsMgr.Save(vGt); err != nil {
		t.Fatal(err)
	}
	resGt, err := svc.ViewsExec("v_gt")
	if err != nil {
		t.Fatal(err)
	}
	if len(resGt) != 3 {
		t.Fatalf("expected 3 results > 20, got %d", len(resGt))
	}

	// 2. Less than (amount < 25) -> should match inv3 (5.00)
	vLt := dbviews.View{
		ID:      "v_lt",
		Name:    "LT 25",
		Filters: []dbviews.Filter{{Key: "amount", Operator: "less_than", Value: "25"}},
	}
	if err := svc.ViewsMgr.Save(vLt); err != nil {
		t.Fatal(err)
	}
	resLt, err := svc.ViewsExec("v_lt")
	if err != nil {
		t.Fatal(err)
	}
	if len(resLt) != 1 || resLt[0]["_title"] != "Invoice 3" {
		t.Fatalf("expected Invoice 3 for < 25, got: %+v", resLt)
	}

	// 3. Greater than or equal (amount >= 100.50) -> should match inv1 and inv4
	vGte := dbviews.View{
		ID:      "v_gte",
		Name:    "GTE 100.50",
		Filters: []dbviews.Filter{{Key: "amount", Operator: ">=", Value: "100.50"}},
	}
	if err := svc.ViewsMgr.Save(vGte); err != nil {
		t.Fatal(err)
	}
	resGte, err := svc.ViewsExec("v_gte")
	if err != nil {
		t.Fatal(err)
	}
	if len(resGte) != 2 {
		t.Fatalf("expected 2 results >= 100.50, got %d", len(resGte))
	}
}

func TestViewsExecDateFiltering(t *testing.T) {
	svc := newTestService(t)

	docs := []*vault.Document{
		{
			Path:        filepath.Join(svc.VaultRoot, "d1.md"),
			Title:       "Doc 1",
			Frontmatter: map[string]interface{}{"document_date": "2026-07-01"},
		},
		{
			Path:        filepath.Join(svc.VaultRoot, "d2.md"),
			Title:       "Doc 2",
			Frontmatter: map[string]interface{}{"document_date": "2026-08-15"},
		},
		{
			Path:        filepath.Join(svc.VaultRoot, "d3.md"),
			Title:       "Doc 3",
			Frontmatter: map[string]interface{}{"document_date": "2026-09-30"},
		},
	}
	for _, d := range docs {
		if err := svc.DB.IndexDocument(d); err != nil {
			t.Fatal(err)
		}
	}

	// After 2026-08-01 -> Doc 2 and Doc 3
	vAfter := dbviews.View{
		ID:      "v_after",
		Name:    "After Aug 1",
		Filters: []dbviews.Filter{{Key: "document_date", Operator: "after", Value: "2026-08-01"}},
	}
	if err := svc.ViewsMgr.Save(vAfter); err != nil {
		t.Fatal(err)
	}
	resAfter, err := svc.ViewsExec("v_after")
	if err != nil {
		t.Fatal(err)
	}
	if len(resAfter) != 2 {
		t.Fatalf("expected 2 docs after 2026-08-01, got %d", len(resAfter))
	}

	// Before 2026-08-01 -> Doc 1
	vBefore := dbviews.View{
		ID:      "v_before",
		Name:    "Before Aug 1",
		Filters: []dbviews.Filter{{Key: "document_date", Operator: "before", Value: "2026-08-01"}},
	}
	if err := svc.ViewsMgr.Save(vBefore); err != nil {
		t.Fatal(err)
	}
	resBefore, err := svc.ViewsExec("v_before")
	if err != nil {
		t.Fatal(err)
	}
	if len(resBefore) != 1 || resBefore[0]["_title"] != "Doc 1" {
		t.Fatalf("expected Doc 1 before 2026-08-01, got %+v", resBefore)
	}
}

func TestViewsExecSetFiltering(t *testing.T) {
	svc := newTestService(t)

	docs := []*vault.Document{
		{
			Path:        filepath.Join(svc.VaultRoot, "t1.md"),
			Title:       "T1",
			Frontmatter: map[string]interface{}{"tags": []string{"finance", "tax", "2026"}},
		},
		{
			Path:        filepath.Join(svc.VaultRoot, "t2.md"),
			Title:       "T2",
			Frontmatter: map[string]interface{}{"tags": []string{"finance", "payroll"}},
		},
		{
			Path:        filepath.Join(svc.VaultRoot, "t3.md"),
			Title:       "T3",
			Frontmatter: map[string]interface{}{"tags": []string{"legal"}},
		},
	}
	for _, d := range docs {
		if err := svc.DB.IndexDocument(d); err != nil {
			t.Fatal(err)
		}
	}

	// 1. contains_all "finance, tax" -> T1 only
	vAll := dbviews.View{
		ID:      "v_set_all",
		Name:    "All",
		Filters: []dbviews.Filter{{Key: "tags", Operator: "contains_all", Value: "finance, tax"}},
	}
	if err := svc.ViewsMgr.Save(vAll); err != nil {
		t.Fatal(err)
	}
	resAll, err := svc.ViewsExec("v_set_all")
	if err != nil {
		t.Fatal(err)
	}
	if len(resAll) != 1 || resAll[0]["_title"] != "T1" {
		t.Fatalf("expected T1, got %+v", resAll)
	}

	// 2. contains_any "payroll, legal" -> T2 and T3
	vAny := dbviews.View{
		ID:      "v_set_any",
		Name:    "Any",
		Filters: []dbviews.Filter{{Key: "tags", Operator: "contains_any", Value: "payroll, legal"}},
	}
	if err := svc.ViewsMgr.Save(vAny); err != nil {
		t.Fatal(err)
	}
	resAny, err := svc.ViewsExec("v_set_any")
	if err != nil {
		t.Fatal(err)
	}
	if len(resAny) != 2 {
		t.Fatalf("expected 2 results for contains_any, got %d", len(resAny))
	}

	// 3. contains_none "finance" -> T3 only
	vNone := dbviews.View{
		ID:      "v_set_none",
		Name:    "None",
		Filters: []dbviews.Filter{{Key: "tags", Operator: "contains_none", Value: "finance"}},
	}
	if err := svc.ViewsMgr.Save(vNone); err != nil {
		t.Fatal(err)
	}
	resNone, err := svc.ViewsExec("v_set_none")
	if err != nil {
		t.Fatal(err)
	}
	if len(resNone) != 1 || resNone[0]["_title"] != "T3" {
		t.Fatalf("expected T3 for contains_none finance, got %+v", resNone)
	}
}

func TestExecuteBaseEmbed(t *testing.T) {
	svc := newTestService(t)

	// Create base with view
	base, err := svc.BaseNew("Project Tasks", "Tasks base")
	if err != nil {
		t.Fatal(err)
	}
	tasksDir := filepath.Join(svc.VaultRoot, "tasks")
	if err := os.MkdirAll(tasksDir, 0750); err != nil {
		t.Fatal(err)
	}

	base.Properties = map[string]dbviews.PropertyConfig{
		"priority": {Type: "select", Label: "Priority Level"},
	}
	base.Views = []dbviews.View{
		{
			ID:      "v_tasks_all",
			Name:    "All Tasks",
			Type:    "table",
			Source:  "tasks/",
			Columns: []string{"title", "priority", "status"},
		},
	}
	if err := svc.BaseSave(base); err != nil {
		t.Fatal(err)
	}

	// Index 5 task documents in tasks/
	for i := 1; i <= 5; i++ {
		doc := &vault.Document{
			Path:        filepath.Join(tasksDir, fmt.Sprintf("task_%d.md", i)),
			Title:       fmt.Sprintf("Task %d", i),
			Frontmatter: map[string]interface{}{"priority": "high", "status": "open"},
		}
		if err := svc.DB.IndexDocument(doc); err != nil {
			t.Fatal(err)
		}
	}

	// 1. Execute embed with limit: 2
	specYAML := `
base: project-tasks
view: v_tasks_all
limit: 2
`
	res, err := svc.ExecuteBaseEmbed(specYAML)
	if err != nil {
		t.Fatalf("ExecuteBaseEmbed failed: %v", err)
	}
	if res.BaseID != "project-tasks" {
		t.Errorf("expected BaseID project-tasks, got %s", res.BaseID)
	}
	if len(res.Rows) != 2 {
		t.Errorf("expected 2 rows due to limit, got %d", len(res.Rows))
	}
	if res.TotalRows != 5 {
		t.Errorf("expected TotalRows 5, got %d", res.TotalRows)
	}
	if !res.Capped {
		t.Error("expected Capped to be true")
	}
	if !strings.Contains(res.Markdown, "| Priority Level |") {
		t.Errorf("expected Priority Level column header in markdown:\n%s", res.Markdown)
	}
	if !strings.Contains(res.Markdown, "Showing 2 of 5 rows") {
		t.Errorf("expected Showing 2 of 5 rows in markdown footer:\n%s", res.Markdown)
	}
	if !strings.Contains(res.Markdown, "[[bases/project-tasks.md|Open Project Tasks]]") {
		t.Errorf("expected link to base in markdown footer:\n%s", res.Markdown)
	}

	// 2. Error cases
	// Missing base
	_, err = svc.ExecuteBaseEmbed("base: nonexistent-base\n")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected base not found error, got: %v", err)
	}

	// Missing view
	_, err = svc.ExecuteBaseEmbed("base: project-tasks\nview: nonexistent-view\n")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected view not found error, got: %v", err)
	}

	// Empty spec
	_, err = svc.ExecuteBaseEmbed("")
	if err == nil {
		t.Error("expected error for empty embed spec")
	}
}

func TestViewsExportCSV(t *testing.T) {
	svc := newTestService(t)

	// Index notes
	doc1 := &vault.Document{
		Path:        filepath.Join(svc.VaultRoot, "note1.md"),
		Title:       "Note One",
		Frontmatter: map[string]interface{}{"category": "work", "amount": "100.00"},
	}
	doc2 := &vault.Document{
		Path:        filepath.Join(svc.VaultRoot, "note2.md"),
		Title:       "Note Two, with comma",
		Frontmatter: map[string]interface{}{"category": "home", "amount": "50.00"},
	}
	if err := svc.DB.IndexDocument(doc1); err != nil {
		t.Fatal(err)
	}
	if err := svc.DB.IndexDocument(doc2); err != nil {
		t.Fatal(err)
	}

	view := dbviews.View{
		ID:      "v_export",
		Name:    "Export View",
		Type:    "table",
		Columns: []string{"title", "category", "amount"},
		Computed: map[string]dbviews.ComputedColumn{
			"summary": {Formula: "{category} - {amount}"},
		},
	}
	if err := svc.ViewsMgr.Save(view); err != nil {
		t.Fatal(err)
	}

	csvBytes, err := svc.ViewsExportCSV("v_export")
	if err != nil {
		t.Fatalf("ViewsExportCSV failed: %v", err)
	}

	csvStr := string(csvBytes)
	lines := strings.Split(strings.TrimSpace(csvStr), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 CSV lines (header + 2 rows), got %d:\n%s", len(lines), csvStr)
	}

	if lines[0] != "title,category,amount" {
		t.Errorf("unexpected CSV header: %s", lines[0])
	}
	if !strings.Contains(csvStr, "\"Note Two, with comma\"") {
		t.Errorf("expected quoted note title with comma, got:\n%s", csvStr)
	}
}

func TestCSVImportDryRunAndApply(t *testing.T) {
	svc := newTestService(t)

	csvContent := `Title,Date,Amount,Status
Invoice Alpha,2026-08-01,150.00,open
Invoice Beta,2026-08-02,250.00,paid
`

	// 1. Dry-run mode (Apply: false)
	optsDry := CSVImportOptions{
		CSVData:  strings.NewReader(csvContent),
		Apply:    false,
		Folder:   "finance",
		BaseName: "Finance Database",
	}

	reportDry, err := svc.CSVImport(optsDry)
	if err != nil {
		t.Fatalf("CSVImport dry-run failed: %v", err)
	}

	if !reportDry.DryRun {
		t.Error("expected DryRun=true in report")
	}
	if reportDry.ImportedCount != 2 {
		t.Errorf("expected 2 imported preview rows, got %d", reportDry.ImportedCount)
	}
	if len(reportDry.Rows) != 2 {
		t.Fatalf("expected 2 row results, got %d", len(reportDry.Rows))
	}
	if reportDry.Rows[0].Status != "preview" {
		t.Errorf("expected status 'preview', got %s", reportDry.Rows[0].Status)
	}

	// Verify no files written to disk
	targetFile := filepath.Join(svc.VaultRoot, "finance", "invoice-alpha.md")
	if _, err := os.Stat(targetFile); !os.IsNotExist(err) {
		t.Error("expected no file written during dry-run")
	}

	// 2. Apply mode (Apply: true)
	optsApply := CSVImportOptions{
		CSVData:  strings.NewReader(csvContent),
		Apply:    true,
		Folder:   "finance",
		BaseName: "Finance Database",
	}

	reportApply, err := svc.CSVImport(optsApply)
	if err != nil {
		t.Fatalf("CSVImport apply failed: %v", err)
	}

	if reportApply.DryRun {
		t.Error("expected DryRun=false in apply report")
	}
	if reportApply.ImportedCount != 2 {
		t.Errorf("expected 2 created notes, got %d", reportApply.ImportedCount)
	}

	// Verify file was written with frontmatter
	data, err := os.ReadFile(targetFile) // #nosec G304 -- targetFile is inside the test vault.
	if err != nil {
		t.Fatalf("target file was not created: %v", err)
	}
	docStr := string(data)
	if !strings.Contains(docStr, "title: Invoice Alpha") || !strings.Contains(docStr, "amount: \"150.00\"") {
		t.Errorf("unexpected note content:\n%s", docStr)
	}

	// Verify base was created
	baseFile := filepath.Join(svc.VaultRoot, "bases", "finance-database.md")
	baseData, err := os.ReadFile(baseFile) // #nosec G304 -- baseFile is inside the test vault.
	if err != nil {
		t.Fatalf("base note was not created: %v", err)
	}
	if !strings.Contains(string(baseData), "type: base") {
		t.Errorf("unexpected base content:\n%s", string(baseData))
	}

	// 3. Collision handling (re-import with same CSV)
	optsCollision := CSVImportOptions{
		CSVData:     strings.NewReader(csvContent),
		Apply:       true,
		Folder:      "finance",
		OnCollision: "suffix",
	}
	reportCollision, err := svc.CSVImport(optsCollision)
	if err != nil {
		t.Fatalf("CSVImport collision failed: %v", err)
	}
	if reportCollision.CollisionCount != 2 {
		t.Errorf("expected 2 collisions, got %d", reportCollision.CollisionCount)
	}
	suffixedFile := filepath.Join(svc.VaultRoot, "finance", "invoice-alpha-2.md")
	if _, err := os.Stat(suffixedFile); err != nil {
		t.Errorf("expected suffixed file invoice-alpha-2.md to exist: %v", err)
	}
}
