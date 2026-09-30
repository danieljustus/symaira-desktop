package retrieval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/retrieval/internal/db"
)

func TestLocalFileAliasIdentityAndScope(t *testing.T) {
	actual := filepath.Join(t.TempDir(), "actual")
	nested := filepath.Join(actual, "nested")
	if err := os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(filepath.Dir(actual), "alias")
	if err := os.Symlink(actual, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	canonical, err := filepath.EvalSymlinks(nested)
	if err != nil {
		t.Fatal(err)
	}
	store, err := db.OpenAt(filepath.Join(t.TempDir(), "retrieval.db"))
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{db: store, embedder: &fakeEmbedder{dim: 8, model: "fixture-model"}}
	defer func() { _ = client.Close() }()
	for _, mode := range []string{"plain", "metadata", "confined"} {
		name := "plain.md"
		if mode != "plain" {
			name = "metadata.md"
		}
		aliasPath := filepath.Join(alias, "nested", name)
		if err := os.WriteFile(aliasPath, []byte("# Heading\n\ncoordinate needle"), 0600); err != nil {
			t.Fatal(err)
		}
		if mode == "confined" {
			// Deliberately different from disk: byte ingestion must not reopen.
			err = client.IndexMarkdownWithMetadata(aliasPath, "# Heading\n\ncoordinate needle confined payload", SearchMetadata{})
		} else if mode == "metadata" {
			err = client.IndexWithMetadata(aliasPath, "", SearchMetadata{})
		} else {
			err = client.Index(aliasPath, "")
		}
		if err != nil {
			t.Fatal(err)
		}
		docs, err := store.ListDocuments()
		if err != nil {
			t.Fatal(err)
		}
		if len(docs) != 1 || docs[0].Path != filepath.Join(canonical, name) {
			t.Fatalf("indexed alias identity = %+v", docs)
		}
		for _, scope := range []string{nested, filepath.Join(alias, "nested"), canonical} {
			hits, err := client.SearchInPaths("coordinate needle", []string{scope}, 5)
			if err != nil {
				t.Fatal(err)
			}
			if len(hits) == 0 || hits[0].Path != filepath.Join(canonical, name) || hits[0].Score <= 0 {
				t.Fatalf("scope %q = %+v", scope, hits)
			}
			if mode == "confined" && !strings.Contains(hits[0].Snippet, "confined payload") {
				t.Fatalf("confined bytes replaced by disk content: %+v", hits)
			}
		}
		if err := os.Remove(aliasPath); err != nil {
			t.Fatal(err)
		}
		if err := client.Delete(aliasPath); err != nil {
			t.Fatal(err)
		}
		docs, err = store.ListDocuments()
		if err != nil || len(docs) != 0 {
			t.Fatalf("alias delete after removal = %+v %v", docs, err)
		}
	}
	for _, label := range []string{"stdin-label", "relative/note.md", "https://example.invalid/note"} {
		if got := canonicalLocalSource(label); got != label {
			t.Fatalf("label changed: %q to %q", label, got)
		}
	}
}
