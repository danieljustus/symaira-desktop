package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/ai"
	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/dataset"
	"github.com/danieljustus/symaira-desktop/internal/dbviews"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

func setupAutofillTest(t *testing.T) (*Service, string) {
	root := t.TempDir()
	ai.PromptOne = func(cfg *config.Config, prompt string) (string, error) {
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
	if err := os.MkdirAll(repo, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sidecar.Open(filepath.Join(repo, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

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
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
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

func TestAutofillRejectsDatasetViewsBeforeExecutionOrPrompt(t *testing.T) {
	sources := []string{
		"dataset:public",
		"dataset:internal",
		"dataset:confidential",
		"dataset:restricted",
		"dataset:",          // malformed source / missing handle slug
		"dataset:missing",   // missing handle
		"dataset:malformed", // malformed handle
	}
	for _, source := range sources {
		for _, dryRun := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/dry_run=%t", source, dryRun), func(t *testing.T) {
				svc := newTestService(t)
				slug := strings.TrimPrefix(source, "dataset:")
				if sensitivity, ok := map[string]string{
					"public":       dataset.SensitivityPublic,
					"internal":     dataset.SensitivityInternal,
					"confidential": dataset.SensitivityConfidential,
					"restricted":   dataset.SensitivityRestricted,
				}[slug]; ok {
					handle := &dataset.Handle{Slug: slug, Title: slug, Source: "datasets/" + slug + "/source.csv", Sensitivity: sensitivity, RetentionRule: dataset.DefaultRetentionRule}
					encoded, err := handle.Render()
					if err != nil {
						t.Fatal(err)
					}
					if err := os.MkdirAll(filepath.Join(svc.VaultRoot, dataset.RawDir), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(svc.VaultRoot, dataset.RawDir, slug+".md"), encoded, 0600); err != nil {
						t.Fatal(err)
					}
				} else if slug == "malformed" {
					if err := os.MkdirAll(filepath.Join(svc.VaultRoot, dataset.RawDir), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(svc.VaultRoot, dataset.RawDir, slug+".md"), []byte("not a dataset handle"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if err := svc.ViewsMgr.Save(dbviews.View{ID: "dataset-view", Name: "Dataset", Source: source}); err != nil {
					t.Fatal(err)
				}

				// If Autofill reaches ViewsExec, the nil sidecar would panic. The
				// guard must reject the source immediately after ViewsGet.
				svc.DB = nil
				var promptCalls int
				previousPromptOne := ai.PromptOne
				ai.PromptOne = func(*config.Config, string) (string, error) {
					promptCalls++
					return "unexpected", nil
				}
				t.Cleanup(func() { ai.PromptOne = previousPromptOne })

				_, err := svc.Autofill("dataset-view", "author", "", dryRun)
				if err == nil || !strings.Contains(err.Error(), "unsupported dataset source") {
					t.Fatalf("Autofill(%q, dryRun=%t) error = %v, want unsupported dataset source", source, dryRun, err)
				}
				if promptCalls != 0 {
					t.Fatalf("ai.PromptOne called %d times for rejected source", promptCalls)
				}
			})
		}
	}
}
