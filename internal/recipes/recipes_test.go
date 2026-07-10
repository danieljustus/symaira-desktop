package recipes

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAndValidateRecipe(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daily.yml")
	if err := os.WriteFile(path, []byte("version: 1\nname: daily\ntriggers: [manual, save]\ntools: [desk_search]\nwrite_cap: 2\n"), 0644); err != nil {
		t.Fatal(err)
	}
	r, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if r.Name != "daily" || r.WriteCap != 2 {
		t.Fatalf("unexpected recipe: %#v", r)
	}
}

func TestValidateRejectsUnsafeRecipe(t *testing.T) {
	if err := Validate(Recipe{Version: 1, Name: "x", Triggers: []string{"magic"}}); err == nil {
		t.Fatal("expected unsupported trigger error")
	}
	if err := Validate(Recipe{Version: 1, Name: "x", Triggers: []string{"manual"}, WriteCap: -1}); err == nil {
		t.Fatal("expected write cap error")
	}
	if err := Validate(Recipe{Version: 1, Name: "x", Triggers: []string{"manual"}}); err == nil {
		t.Fatal("expected allow-list error")
	}
}

func TestAcceptAndRejectKeepChangesPendingUntilApproved(t *testing.T) {
	root := t.TempDir()
	m := Manifest{Request: Request{RunID: "run-1", Recipe: Recipe{Version: 1, Name: "x", Triggers: []string{"manual"}, Tools: []string{"desk_search"}, WriteCap: 1}}, Response: Response{ContractVersion: 1, Changes: []Change{{Path: "notes/a.md", Content: "approved"}}}, Status: "pending"}
	dir := runDir(root, "run-1")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(dir, "manifest.json"), m); err != nil {
		t.Fatal(err)
	}
	if err := Accept(root, "run-1"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, "notes", "a.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "approved" {
		t.Fatalf("got %q", b)
	}
	if err := Accept(root, "run-1"); err == nil {
		t.Fatal("accepted run must not be applied twice")
	}

	m.Request.RunID, m.Status = "run-2", "pending"
	dir = runDir(root, "run-2")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(dir, "manifest.json"), m); err != nil {
		t.Fatal(err)
	}
	if err := Reject(root, "run-2"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "notes", "a.md")); err != nil {
		t.Fatal(err)
	}
}

func TestChangeValidationEnforcesCapAndVaultBoundary(t *testing.T) {
	root := t.TempDir()
	if err := validateChanges(root, 1, []Change{{Path: "a.md"}, {Path: "b.md"}}); err == nil {
		t.Fatal("expected cap error")
	}
	if err := validateChanges(root, 1, []Change{{Path: "../outside.md"}}); err == nil {
		t.Fatal("expected path error")
	}
}
