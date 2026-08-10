package notebook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestVault(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

func writeVaultFile(t *testing.T, vaultRoot, rel, content string) {
	t.Helper()
	abs := filepath.Join(vaultRoot, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Research X":       "research-x",
		"  spaced  ":       "spaced",
		"Q3 Plan (draft)!": "q3-plan-draft",
		"---":              "notebook",
		"":                 "notebook",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNew_CreatesContractConformNote(t *testing.T) {
	vaultRoot := newTestVault(t)
	nb, err := New(vaultRoot, "Research X", "notes on X")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if nb.ID != "research-x" {
		t.Errorf("ID = %q, want research-x", nb.ID)
	}
	if nb.Path != filepath.Join("notebooks", "research-x.md") {
		t.Errorf("Path = %q", nb.Path)
	}

	abs := filepath.Join(vaultRoot, nb.Path)
	data, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("read created note: %v", err)
	}
	content := string(data)
	for _, want := range []string{"type: notebook", "title: Research X", "notebook_id: research-x"} {
		if !strings.Contains(content, want) {
			t.Errorf("content missing %q:\n%s", want, content)
		}
	}
}

func TestNew_RequiresTitle(t *testing.T) {
	vaultRoot := newTestVault(t)
	if _, err := New(vaultRoot, "   ", ""); err != ErrTitleRequired {
		t.Errorf("New with blank title: err = %v, want ErrTitleRequired", err)
	}
}

func TestNew_DeduplicatesSlug(t *testing.T) {
	vaultRoot := newTestVault(t)
	first, err := New(vaultRoot, "Research", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(vaultRoot, "Research", "")
	if err != nil {
		t.Fatal(err)
	}
	if first.Path == second.Path {
		t.Fatalf("expected distinct paths, both got %q", first.Path)
	}
	if second.ID != "research-2" {
		t.Errorf("second.ID = %q, want research-2", second.ID)
	}
}

func TestLoad_RoundTrips(t *testing.T) {
	vaultRoot := newTestVault(t)
	created, err := New(vaultRoot, "Round Trip", "desc")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(vaultRoot, created.Path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Title != "Round Trip" || loaded.Description != "desc" || loaded.ID != created.ID {
		t.Errorf("loaded = %+v, want title/description/id to match created %+v", loaded, created)
	}
}

func TestLoad_RejectsNonNotebookNote(t *testing.T) {
	vaultRoot := newTestVault(t)
	writeVaultFile(t, vaultRoot, "plain.md", "---\ntitle: Plain\ncreated: \"2026-01-01\"\ntags: []\n---\nbody\n")
	if _, err := Load(vaultRoot, "plain.md"); err == nil {
		t.Fatal("expected error loading a non-notebook note as a notebook")
	}
}

func TestResolve_AcceptsAllReferenceForms(t *testing.T) {
	vaultRoot := newTestVault(t)
	created, err := New(vaultRoot, "Multi Form", "")
	if err != nil {
		t.Fatal(err)
	}
	forms := []string{
		"multi-form",
		"multi-form.md",
		"notebooks/multi-form",
		"notebooks/multi-form.md",
	}
	for _, ref := range forms {
		nb, err := Resolve(vaultRoot, ref)
		if err != nil {
			t.Errorf("Resolve(%q): %v", ref, err)
			continue
		}
		if nb.Path != created.Path {
			t.Errorf("Resolve(%q).Path = %q, want %q", ref, nb.Path, created.Path)
		}
	}
}

func TestResolve_NotFound(t *testing.T) {
	vaultRoot := newTestVault(t)
	if _, err := Resolve(vaultRoot, "does-not-exist"); err != ErrNotFound {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestList_SortsByTitleAndSkipsNonNotebooks(t *testing.T) {
	vaultRoot := newTestVault(t)
	if _, err := New(vaultRoot, "Zeta", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := New(vaultRoot, "Alpha", ""); err != nil {
		t.Fatal(err)
	}
	writeVaultFile(t, vaultRoot, "notebooks/stray.md", "---\ntitle: Stray\ncreated: \"2026-01-01\"\ntags: []\n---\nnot a notebook\n")

	list, err := List(vaultRoot)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len(list) = %d, want 2 (stray non-notebook note must be skipped)", len(list))
	}
	if list[0].Title != "Alpha" || list[1].Title != "Zeta" {
		t.Errorf("list order = [%s, %s], want [Alpha, Zeta]", list[0].Title, list[1].Title)
	}
}

func TestList_EmptyVaultReturnsEmptySlice(t *testing.T) {
	vaultRoot := newTestVault(t)
	list, err := List(vaultRoot)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("len(list) = %d, want 0", len(list))
	}
}

func TestAddSource_RejectsPathTraversal(t *testing.T) {
	vaultRoot := newTestVault(t)
	nb, err := New(vaultRoot, "Scope Test", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := AddSource(vaultRoot, nb, "../../etc/passwd"); err == nil {
		t.Fatal("expected error adding a traversal path as a source")
	}
	if len(nb.Sources) != 0 {
		t.Errorf("Sources = %v, want empty after rejected add", nb.Sources)
	}
}

func TestAddSource_RejectsSelf(t *testing.T) {
	vaultRoot := newTestVault(t)
	nb, err := New(vaultRoot, "Self Ref", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := AddSource(vaultRoot, nb, nb.Path); err != ErrSourceIsSelf {
		t.Errorf("err = %v, want ErrSourceIsSelf", err)
	}
}

func TestAddSource_IsIdempotentAndPersists(t *testing.T) {
	vaultRoot := newTestVault(t)
	writeVaultFile(t, vaultRoot, "docs/invoice.md", "---\ntitle: Invoice\ncreated: \"2026-01-01\"\ntags: []\n---\nbody\n")
	nb, err := New(vaultRoot, "Invoices", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := AddSource(vaultRoot, nb, "docs/invoice.md"); err != nil {
		t.Fatalf("AddSource: %v", err)
	}
	if err := AddSource(vaultRoot, nb, "docs/invoice.md"); err != nil {
		t.Fatalf("AddSource (repeat): %v", err)
	}
	if len(nb.Sources) != 1 {
		t.Fatalf("Sources = %v, want exactly one entry after repeated add", nb.Sources)
	}

	reloaded, err := Load(vaultRoot, nb.Path)
	if err != nil {
		t.Fatalf("Load after AddSource: %v", err)
	}
	if len(reloaded.Sources) != 1 || reloaded.Sources[0] != "docs/invoice.md" {
		t.Errorf("reloaded.Sources = %v, want [docs/invoice.md]", reloaded.Sources)
	}

	abs := filepath.Join(vaultRoot, nb.Path)
	data, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "[[invoice]]") {
		t.Errorf("rendered body missing source wikilink:\n%s", data)
	}
}

func TestRemoveSource_NeverDeletesReferencedFile(t *testing.T) {
	vaultRoot := newTestVault(t)
	writeVaultFile(t, vaultRoot, "docs/invoice.md", "---\ntitle: Invoice\ncreated: \"2026-01-01\"\ntags: []\n---\nbody\n")
	nb, err := New(vaultRoot, "Invoices", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := AddSource(vaultRoot, nb, "docs/invoice.md"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveSource(vaultRoot, nb, "docs/invoice.md"); err != nil {
		t.Fatalf("RemoveSource: %v", err)
	}
	if len(nb.Sources) != 0 {
		t.Errorf("Sources = %v, want empty", nb.Sources)
	}
	if _, err := os.Stat(filepath.Join(vaultRoot, "docs/invoice.md")); err != nil {
		t.Errorf("referenced file must survive removal from notebook: %v", err)
	}
}

func TestRemoveSource_MissingIsNoop(t *testing.T) {
	vaultRoot := newTestVault(t)
	nb, err := New(vaultRoot, "Empty", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := RemoveSource(vaultRoot, nb, "docs/never-added.md"); err != nil {
		t.Errorf("RemoveSource on absent source should be a no-op, got err: %v", err)
	}
}

func TestResolveSources_ReportsMissingWithoutFailing(t *testing.T) {
	vaultRoot := newTestVault(t)
	writeVaultFile(t, vaultRoot, "docs/present.md", "---\ntitle: Present Doc\ncreated: \"2026-01-01\"\ntags: []\n---\nbody\n")
	nb, err := New(vaultRoot, "Mixed", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := AddSource(vaultRoot, nb, "docs/present.md"); err != nil {
		t.Fatal(err)
	}
	// Simulate drift: a source referenced by the notebook was deleted
	// independently, without going through RemoveSource.
	nb.Sources = append(nb.Sources, "docs/gone.md")

	refs, err := nb.ResolveSources(vaultRoot)
	if err != nil {
		t.Fatalf("ResolveSources: %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("len(refs) = %d, want 2", len(refs))
	}
	byPath := map[string]SourceRef{}
	for _, r := range refs {
		byPath[r.Path] = r
	}
	if byPath["docs/present.md"].Missing || byPath["docs/present.md"].Title != "Present Doc" {
		t.Errorf("present.md ref = %+v, want Missing=false Title=Present Doc", byPath["docs/present.md"])
	}
	if !byPath["docs/gone.md"].Missing {
		t.Errorf("gone.md ref = %+v, want Missing=true", byPath["docs/gone.md"])
	}
}
