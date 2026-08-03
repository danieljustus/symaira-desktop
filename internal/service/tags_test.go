package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTaggedNote(t *testing.T, root, name, tags string) {
	t.Helper()
	content := "---\ntitle: " + strings.TrimSuffix(name, ".md") + "\ntags: [" + tags + "]\n---\n\nBody\n"
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // test helper reading an explicit fixture path
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestTagsRenameRewritesEveryCarrierAndReindexes(t *testing.T) {
	svc := newTestService(t)
	writeTaggedNote(t, svc.VaultRoot, "a.md", "invoice")
	writeTaggedNote(t, svc.VaultRoot, "b.md", "invoice, urgent")
	writeTaggedNote(t, svc.VaultRoot, "c.md", "personal")

	results, err := svc.TagsRename("invoice", "receipt")
	if err != nil {
		t.Fatal(err)
	}

	updated := map[string]bool{}
	for _, r := range results {
		if r.Status == "updated" {
			updated[r.File] = true
		}
	}
	if !updated["a.md"] || !updated["b.md"] || updated["c.md"] {
		t.Fatalf("expected a.md and b.md updated only, got %#v", results)
	}

	if got := readFile(t, filepath.Join(svc.VaultRoot, "a.md")); !strings.Contains(got, "receipt") || strings.Contains(got, "invoice") {
		t.Fatalf("a.md not rewritten: %s", got)
	}
	if got := readFile(t, filepath.Join(svc.VaultRoot, "b.md")); !strings.Contains(got, "receipt") || strings.Contains(got, "invoice") || !strings.Contains(got, "urgent") {
		t.Fatalf("b.md not rewritten or lost second tag: %s", got)
	}

	// Reindexed: the new tag is indexed in file_properties, the old one is gone.
	for _, name := range []string{"a.md", "b.md"} {
		props, err := svc.DB.GetProperties(filepath.Join(svc.VaultRoot, name))
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := props["tags"].(string)
		if !strings.Contains(raw, "receipt") {
			t.Fatalf("%s: expected reindexed tags to contain receipt, got %q", name, raw)
		}
		if strings.Contains(raw, "invoice") {
			t.Fatalf("%s: expected no stale invoice row, got %q", name, raw)
		}
	}
}

func TestTagsRenameCaseInsensitiveMatchPreservesNewCase(t *testing.T) {
	svc := newTestService(t)
	writeTaggedNote(t, svc.VaultRoot, "a.md", "Invoice")

	_, err := svc.TagsRename("invoice", "Receipt")
	if err != nil {
		t.Fatal(err)
	}

	got := readFile(t, filepath.Join(svc.VaultRoot, "a.md"))
	if !strings.Contains(got, "Receipt") || strings.Contains(got, "Invoice") {
		t.Fatalf("expected case-insensitive rename to new case, got: %s", got)
	}
}

func TestTagsRenameValidatesArgs(t *testing.T) {
	svc := newTestService(t)
	for _, tc := range [][2]string{{"", "x"}, {"x", ""}, {"x", "x"}} {
		if _, err := svc.TagsRename(tc[0], tc[1]); err == nil {
			t.Fatalf("expected error for rename %q -> %q", tc[0], tc[1])
		}
	}
}

func TestTagsMergeMovesAndDeduplicates(t *testing.T) {
	svc := newTestService(t)
	writeTaggedNote(t, svc.VaultRoot, "a.md", "work")
	writeTaggedNote(t, svc.VaultRoot, "b.md", "work, urgent")
	writeTaggedNote(t, svc.VaultRoot, "c.md", "urgent")

	results, err := svc.TagsMerge("work", "urgent")
	if err != nil {
		t.Fatal(err)
	}

	updated := map[string]bool{}
	for _, r := range results {
		if r.Status == "updated" {
			updated[r.File] = true
		}
	}
	if !updated["a.md"] || !updated["b.md"] || updated["c.md"] {
		t.Fatalf("expected a.md and b.md updated only, got %#v", results)
	}

	// a.md now carries urgent; b.md has urgent exactly once.
	if got := readFile(t, filepath.Join(svc.VaultRoot, "a.md")); !strings.Contains(got, "urgent") {
		t.Fatalf("a.md missing merged tag: %s", got)
	}
	b := readFile(t, filepath.Join(svc.VaultRoot, "b.md"))
	if strings.Count(b, "urgent") != 1 {
		t.Fatalf("expected deduplicated urgent in b.md, got: %s", b)
	}
	if strings.Contains(b, "work") {
		t.Fatalf("b.md still carries merged-away tag: %s", b)
	}
}

func TestTagsDeleteRemovesFromAllCarriers(t *testing.T) {
	svc := newTestService(t)
	writeTaggedNote(t, svc.VaultRoot, "a.md", "invoice, paid")
	writeTaggedNote(t, svc.VaultRoot, "b.md", "invoice")
	writeTaggedNote(t, svc.VaultRoot, "c.md", "personal")

	results, err := svc.TagsDelete("invoice")
	if err != nil {
		t.Fatal(err)
	}

	updated := map[string]bool{}
	for _, r := range results {
		if r.Status == "updated" {
			updated[r.File] = true
		}
	}
	if !updated["a.md"] || !updated["b.md"] || updated["c.md"] {
		t.Fatalf("expected a.md and b.md updated only, got %#v", results)
	}

	a := readFile(t, filepath.Join(svc.VaultRoot, "a.md"))
	if strings.Contains(a, "invoice") || !strings.Contains(a, "paid") {
		t.Fatalf("a.md must keep paid and drop invoice, got: %s", a)
	}
	b := readFile(t, filepath.Join(svc.VaultRoot, "b.md"))
	if strings.Contains(b, "invoice") {
		t.Fatalf("b.md must drop invoice, got: %s", b)
	}

	matches, err := svc.DB.Search("invoice")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("expected 0 stale index rows for deleted tag, got %d", len(matches))
	}
}

func TestTagsOpsSkipFilesWithoutTheTag(t *testing.T) {
	svc := newTestService(t)
	writeTaggedNote(t, svc.VaultRoot, "a.md", "personal")

	results, err := svc.TagsRename("invoice", "receipt")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.Status != "skipped" {
			t.Fatalf("expected all skipped, got %#v", r)
		}
	}
}

func TestTagsOpsTolerateBrokenFilesWithoutAbortingBatch(t *testing.T) {
	svc := newTestService(t)
	writeTaggedNote(t, svc.VaultRoot, "a.md", "invoice")
	// A file with malformed frontmatter that fails to parse.
	if err := os.WriteFile(filepath.Join(svc.VaultRoot, "broken.md"), []byte("---\ntags: [unclosed\n---\nbody\n"), 0600); err != nil {
		t.Fatal(err)
	}

	results, err := svc.TagsRename("invoice", "receipt")
	if err != nil {
		t.Fatal(err)
	}

	var updated, errored bool
	for _, r := range results {
		if r.Status == "updated" && r.File == "a.md" {
			updated = true
		}
		if r.Status == "error" && r.File == "broken.md" {
			errored = true
		}
	}
	if !updated || !errored {
		t.Fatalf("expected a.md updated and broken.md errored, got %#v", results)
	}
}
