package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/ai"
	"github.com/danieljustus/symaira-desktop/internal/dbviews"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

func setupAutofillTest(t *testing.T) (*Service, string) {
	root := t.TempDir()
	ai.PromptOne = func(prompt string) (string, error) {
		if strings.Contains(prompt, "author") {
			return "AI Author", nil
		}
		return "", nil
	}
	t.Cleanup(func() { ai.PromptOne = ai.PromptOneReal })

	writeNote(t, root, "a.md", "---\ntitle: Note A\n---\ncontent A\n")
	writeNote(t, root, "b.md", "---\ntitle: Note B\nauthor: Existing Author\n---\ncontent B\n")
	writeNote(t, root, "c.md", "---\ntitle: Note C\n---\ncontent C\n")

	repo := filepath.Join(root, ".symdesk")
	if err := os.MkdirAll(repo, 0755); err != nil {
		t.Fatal(err)
	}
	db, err := sidecar.Open(filepath.Join(repo, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	svc := New(root, db)

	for _, name := range []string{"a.md", "b.md", "c.md"} {
		doc, err := vault.ParseFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.IndexDocument(doc); err != nil {
			t.Fatal(err)
		}
	}

	mgr := dbviews.NewManager(root)
	if err := mgr.Save(dbviews.View{
		ID:      "all",
		Name:    "All notes",
		Columns: []string{"title", "author"},
	}); err != nil {
		t.Fatal(err)
	}

	return svc, root
}

func writeNote(t *testing.T, root, name, content string) {
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestAutofillDryRun(t *testing.T) {
	svc, _ := setupAutofillTest(t)
	res, err := svc.Autofill("all", "author", "", true)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("result: %+v", res)
	for _, c := range res.Changes {
		t.Logf("change: %+v", c)
	}
	if res.Total != 3 {
		t.Fatalf("expected total 3, got %d", res.Total)
	}
	if res.Filled != 2 {
		t.Fatalf("expected filled 2, got %d", res.Filled)
	}
	if res.Skipped != 1 {
		t.Fatalf("expected skipped 1, got %d", res.Skipped)
	}
	if !res.DryRun {
		t.Fatalf("expected dry run")
	}
}

func TestAutofillApply(t *testing.T) {
	svc, root := setupAutofillTest(t)
	res, err := svc.Autofill("all", "author", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 3 {
		t.Fatalf("expected total 3, got %d", res.Total)
	}
	if res.Filled != 2 {
		t.Fatalf("expected filled 2, got %d", res.Filled)
	}
	if res.Skipped != 1 {
		t.Fatalf("expected skipped 1, got %d", res.Skipped)
	}

	doc, err := vault.ParseFile(filepath.Join(root, "a.md"))
	if err != nil {
		t.Fatal(err)
	}
	if doc.Frontmatter["author"] != "AI Author" {
		t.Fatalf("expected author set on a.md, got %v", doc.Frontmatter["author"])
	}

	docB, err := vault.ParseFile(filepath.Join(root, "b.md"))
	if err != nil {
		t.Fatal(err)
	}
	if docB.Frontmatter["author"] != "Existing Author" {
		t.Fatalf("expected b unchanged, got %v", docB.Frontmatter["author"])
	}
}
