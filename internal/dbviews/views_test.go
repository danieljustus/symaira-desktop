package dbviews

import (
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
