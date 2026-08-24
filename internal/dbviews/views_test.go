package dbviews

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManager(t *testing.T) {
	vaultRoot := t.TempDir()
	mgr := NewManager(vaultRoot)

	// List empty
	views, err := mgr.List()
	if err != nil {
		t.Fatalf("List empty failed: %v", err)
	}
	if len(views) != 0 {
		t.Errorf("expected 0 views, got %d", len(views))
	}

	// Save new
	v1 := View{
		Name:    "Test View",
		Columns: []string{"title", "status"},
	}
	err = mgr.Save(v1)
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// List after save
	views, err = mgr.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("expected 1 view, got %d", len(views))
	}
	if views[0].ID == "" {
		t.Errorf("expected auto-generated ID")
	}

	savedID := views[0].ID

	// Get
	v, err := mgr.Get(savedID)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if v.Name != "Test View" {
		t.Errorf("expected 'Test View', got '%s'", v.Name)
	}

	// Save update
	v.Name = "Updated View"
	err = mgr.Save(*v)
	if err != nil {
		t.Fatalf("Save update failed: %v", err)
	}

	v2, err := mgr.Get(savedID)
	if err != nil {
		t.Fatalf("Get updated failed: %v", err)
	}
	if v2.Name != "Updated View" {
		t.Errorf("expected 'Updated View', got '%s'", v2.Name)
	}
}

func TestManagerDelete(t *testing.T) {
	mgr := NewManager(t.TempDir())
	if err := mgr.Save(View{ID: "delete-me", Name: "Delete me"}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Delete("delete-me"); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Get("delete-me"); err == nil {
		t.Fatal("deleted view was still available")
	}
}

func TestBaseRenderAndParse(t *testing.T) {
	b := &Base{
		ID:          "finance-tracker",
		Title:       "Finance Tracker",
		Description: "Tracks open invoices and payments.",
		Created:     "2026-08-24T20:00:00Z",
		Tags:        []string{"base", "finance"},
		Views: []View{
			{
				ID:           "v_table",
				Name:         "All Invoices",
				Type:         "table",
				Source:       "invoices/",
				DateProperty: "document_date",
				Columns:      []string{"title", "status", "amount"},
				Sorts:        []Sort{{Key: "document_date", Ascending: false}},
				Filters: []Filter{
					{Key: "status", Operator: "equals", Value: "open"},
				},
				FilterGroup: &FilterGroup{
					Operator: "any",
					Filters: []Filter{
						{Key: "priority", Operator: "equals", Value: "high"},
					},
				},
				Computed: map[string]ComputedColumn{
					"total": {Rollup: "sum(links.amount)"},
				},
				Template: &Template{
					Ref:      "invoice-template",
					Defaults: map[string]string{"status": "open"},
				},
			},
			{
				ID:     "v_nb",
				Name:   "Notebook Scope View",
				Type:   "board",
				Source: "notebook:q3-briefing",
			},
		},
	}

	rendered, err := RenderBase(b)
	if err != nil {
		t.Fatalf("RenderBase failed: %v", err)
	}

	renderedStr := string(rendered)
	if !strings.Contains(renderedStr, "type: base") {
		t.Errorf("expected type: base in frontmatter, got:\n%s", renderedStr)
	}
	if !strings.Contains(renderedStr, "base_id: finance-tracker") {
		t.Errorf("expected base_id: finance-tracker in frontmatter, got:\n%s", renderedStr)
	}
	if !strings.Contains(renderedStr, "## Views") {
		t.Errorf("expected ## Views section, got:\n%s", renderedStr)
	}
	if !strings.Contains(renderedStr, "- **All Invoices** (`table`)") {
		t.Errorf("expected view bullet for All Invoices, got:\n%s", renderedStr)
	}
	if !strings.Contains(renderedStr, "Source: [[q3-briefing]]") {
		t.Errorf("expected wikilink to notebook source [[q3-briefing]], got:\n%s", renderedStr)
	}

	parsed, err := ParseBase("bases/finance-tracker.md", rendered)
	if err != nil {
		t.Fatalf("ParseBase failed: %v", err)
	}

	if parsed.ID != b.ID {
		t.Errorf("expected ID %q, got %q", b.ID, parsed.ID)
	}
	if parsed.Title != b.Title {
		t.Errorf("expected Title %q, got %q", b.Title, parsed.Title)
	}
	if parsed.Description != b.Description {
		t.Errorf("expected Description %q, got %q", b.Description, parsed.Description)
	}
	if len(parsed.Views) != 2 {
		t.Fatalf("expected 2 views, got %d", len(parsed.Views))
	}
	v1 := parsed.Views[0]
	if v1.Name != "All Invoices" || v1.Type != "table" || v1.Source != "invoices/" {
		t.Errorf("unexpected parsed view 0: %+v", v1)
	}
	if len(v1.Filters) != 1 || v1.Filters[0].Key != "status" {
		t.Errorf("unexpected filters: %+v", v1.Filters)
	}
	if v1.FilterGroup == nil || v1.FilterGroup.Operator != "any" {
		t.Errorf("unexpected filter group: %+v", v1.FilterGroup)
	}
	if v1.Template == nil || v1.Template.Ref != "invoice-template" {
		t.Errorf("unexpected template: %+v", v1.Template)
	}
}

func TestBaseExtrasPreserved(t *testing.T) {
	raw := `---
type: base
title: Extras Base
base_id: extras-base
created: "2026-08-24T20:00:00Z"
custom_field: "preserved_value"
views:
  - id: v1
    name: Simple View
    type: table
---

# Extras Base

## Views

- **Simple View** (` + "`table`" + `)
`

	b, err := ParseBase("bases/extras-base.md", []byte(raw))
	if err != nil {
		t.Fatalf("ParseBase failed: %v", err)
	}

	if b.Extras["custom_field"] != "preserved_value" {
		t.Errorf("expected custom_field 'preserved_value', got %v", b.Extras["custom_field"])
	}

	rendered, err := RenderBase(b)
	if err != nil {
		t.Fatalf("RenderBase failed: %v", err)
	}
	if !strings.Contains(string(rendered), "custom_field: preserved_value") {
		t.Errorf("expected custom_field preserved in rendered output, got:\n%s", string(rendered))
	}
}

func TestManagerLegacyMigration(t *testing.T) {
	root := t.TempDir()

	// Write legacy .symdesk/views.json
	symdeskDir := filepath.Join(root, ".symdesk")
	if err := os.MkdirAll(symdeskDir, 0755); err != nil {
		t.Fatal(err)
	}
	legacyViews := []byte(`[
  {
    "id": "legacy_inv_1",
    "name": "Open Invoices",
    "type": "table",
    "source": "invoices/",
    "filters": [{"key": "status", "operator": "equals", "value": "open"}]
  },
  {
    "id": "legacy_inv_2",
    "name": "Paid Invoices",
    "type": "table",
    "source": "invoices/",
    "filters": [{"key": "status", "operator": "equals", "value": "paid"}]
  },
  {
    "id": "legacy_all",
    "name": "All Notes",
    "type": "list"
  }
]`)
	legacyFile := filepath.Join(symdeskDir, "views.json")
	if err := os.WriteFile(legacyFile, legacyViews, 0644); err != nil {
		t.Fatal(err)
	}

	// Initialize Manager -> should trigger migration
	mgr := NewManager(root)

	// Verify .symdesk/views.json is intact
	intactData, err := os.ReadFile(legacyFile)
	if err != nil {
		t.Fatalf("legacy file missing after migration: %v", err)
	}
	if string(intactData) != string(legacyViews) {
		t.Errorf("legacy file was modified during migration")
	}

	// Verify bases are created
	bases, err := mgr.ListBases()
	if err != nil {
		t.Fatalf("ListBases failed: %v", err)
	}
	if len(bases) != 2 {
		t.Fatalf("expected 2 bases (invoices and all-notes), got %d", len(bases))
	}

	// Verify views are accessible via List and Get
	views, err := mgr.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(views) != 3 {
		t.Fatalf("expected 3 views across bases, got %d", len(views))
	}

	v1, err := mgr.Get("legacy_inv_1")
	if err != nil {
		t.Fatalf("Get legacy_inv_1 failed: %v", err)
	}
	if v1.Name != "Open Invoices" || v1.Source != "invoices/" {
		t.Errorf("unexpected view data: %+v", v1)
	}
}

func TestManagerSnapshotCallback(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(root)

	var snapshotted []string
	mgr.SetSnapshotFn(func(absPath string) {
		snapshotted = append(snapshotted, absPath)
	})

	if err := mgr.Save(View{ID: "v1", Name: "View 1"}); err != nil {
		t.Fatal(err)
	}
	if len(snapshotted) != 1 {
		t.Fatalf("expected snapshot callback before save, got %d calls", len(snapshotted))
	}

	if err := mgr.Save(View{ID: "v1", Name: "View 1 Updated"}); err != nil {
		t.Fatal(err)
	}
	if len(snapshotted) != 2 {
		t.Fatalf("expected second snapshot callback before update, got %d calls", len(snapshotted))
	}
}

func TestBaseCRUD(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(root)

	base := &Base{
		ID:          "project-alpha",
		Title:       "Project Alpha",
		Description: "Alpha views",
		Views: []View{
			{ID: "v_alpha", Name: "Alpha View", Type: "table"},
		},
	}
	if err := mgr.SaveBase(base); err != nil {
		t.Fatalf("SaveBase failed: %v", err)
	}

	got, err := mgr.GetBase("project-alpha")
	if err != nil {
		t.Fatalf("GetBase failed: %v", err)
	}
	if got.Title != "Project Alpha" || len(got.Views) != 1 {
		t.Errorf("unexpected base: %+v", got)
	}

	bases, err := mgr.ListBases()
	if err != nil {
		t.Fatalf("ListBases failed: %v", err)
	}
	if len(bases) != 1 {
		t.Fatalf("expected 1 base, got %d", len(bases))
	}

	if err := mgr.DeleteBase("project-alpha"); err != nil {
		t.Fatalf("DeleteBase failed: %v", err)
	}

	if _, err := mgr.GetBase("project-alpha"); err == nil {
		t.Fatal("expected error getting deleted base")
	}
}
