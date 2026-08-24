package service

import (
	"os"
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
