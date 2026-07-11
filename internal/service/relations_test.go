package service

import (
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/vault"
)

func indexRelationDoc(t *testing.T, svc *Service, name, title string, frontmatter map[string]interface{}, links []string) {
	t.Helper()
	doc := &vault.Document{
		Path:        filepath.Join(svc.VaultRoot, name),
		Title:       title,
		Body:        "body of " + title,
		SHA256:      name,
		Created:     "2026-01-01T00:00:00Z",
		Frontmatter: frontmatter,
		Links:       links,
	}
	if err := svc.DB.IndexDocument(doc); err != nil {
		t.Fatal(err)
	}
}

func TestRelationsInverseFromFrontmatterAndBody(t *testing.T) {
	svc := newTestService(t)

	indexRelationDoc(t, svc, "Target.md", "Target", nil, nil)
	// Frontmatter wikilink relation.
	indexRelationDoc(t, svc, "task.md", "Task", map[string]interface{}{
		"project": "[[Target]]",
		"status":  "open",
	}, nil)
	// Frontmatter list relation with alias syntax.
	indexRelationDoc(t, svc, "meeting.md", "Meeting", map[string]interface{}{
		"related": []interface{}{"[[Other]]", "[[Target|the target]]"},
	}, nil)
	// Bare title value.
	indexRelationDoc(t, svc, "note.md", "Note", map[string]interface{}{
		"parent": "Target",
	}, nil)
	// Body wikilink only.
	indexRelationDoc(t, svc, "journal.md", "Journal", nil, []string{"Target"})
	// Unrelated note.
	indexRelationDoc(t, svc, "misc.md", "Misc", map[string]interface{}{
		"project": "[[Elsewhere]]",
	}, nil)

	rels, err := svc.RelationsInverse("Target.md")
	if err != nil {
		t.Fatalf("RelationsInverse failed: %v", err)
	}

	got := make(map[string]string, len(rels))
	for _, r := range rels {
		got[r.Source+"#"+r.Property] = r.Title
	}
	want := map[string]string{
		"task.md#project":    "Task",
		"meeting.md#related": "Meeting",
		"note.md#parent":     "Note",
		"journal.md#_link":   "Journal",
	}
	if len(rels) != len(want) {
		t.Fatalf("expected %d relations, got %d: %+v", len(want), len(rels), rels)
	}
	for key, title := range want {
		if got[key] != title {
			t.Errorf("missing or wrong relation %s: got %q, want %q", key, got[key], title)
		}
	}
}

func TestRelationsInverseNoSelfReference(t *testing.T) {
	svc := newTestService(t)

	indexRelationDoc(t, svc, "Solo.md", "Solo", map[string]interface{}{
		"parent": "[[Solo]]",
	}, []string{"Solo"})

	rels, err := svc.RelationsInverse("Solo.md")
	if err != nil {
		t.Fatalf("RelationsInverse failed: %v", err)
	}
	if len(rels) != 0 {
		t.Errorf("expected no self relations, got %+v", rels)
	}
}

func TestRelationsInverseRejectsPathEscape(t *testing.T) {
	svc := newTestService(t)
	if _, err := svc.RelationsInverse("../outside.md"); err == nil {
		t.Error("expected error for path escape")
	}
}
