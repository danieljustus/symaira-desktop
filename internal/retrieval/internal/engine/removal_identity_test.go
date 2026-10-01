package engine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/retrieval/internal/db"
)

func TestRemoveDirectoryAliasIdentities(t *testing.T) {
	for _, removal := range []string{"lexical", "canonical", "source-alias", "missing"} {
		t.Run(removal, func(t *testing.T) {
			home := t.TempDir()
			realParent := filepath.Join(home, "real")
			root := filepath.Join(realParent, "source")
			if err := os.MkdirAll(root, 0o700); err != nil {
				t.Fatal(err)
			}
			alias := filepath.Join(home, "alias")
			if err := os.Symlink(realParent, alias); err != nil {
				t.Fatal(err)
			}
			lexicalRoot := filepath.Join(alias, "source")
			sourceAlias := filepath.Join(home, "source-alias")
			if err := os.Symlink(root, sourceAlias); err != nil {
				t.Fatal(err)
			}
			canonicalRoot, err := filepath.EvalSymlinks(root)
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(root, "note.md")
			if err := os.WriteFile(file, []byte("source survives"), 0o600); err != nil {
				t.Fatal(err)
			}
			database, err := db.OpenAt(filepath.Join(home, "index.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = database.Close() }()
			identities := []string{filepath.Join(lexicalRoot, "note.md"), filepath.Join(canonicalRoot, "note.md")}
			// A sibling prefix, an unrelated file symlink and non-local labels
			// must survive removal. No file-symlink target is followed.
			outside := filepath.Join(home, "outside.md")
			if err := os.Symlink(file, outside); err != nil {
				t.Fatal(err)
			}
			loop := filepath.Join(home, "unrelated-loop")
			if err := os.Symlink(loop, loop); err != nil {
				t.Fatal(err)
			}
			controls := []string{canonicalRoot + "2/note.md", outside, filepath.Join(loop, "note.md"), "stdin-source", "https://example.invalid/note.md"}
			for _, path := range append(append([]string{}, identities...), controls...) {
				if err := database.SaveDocument(&db.Document{Path: path, Hash: "test"}); err != nil {
					t.Fatal(err)
				}
				chunks := buildChunks(&fakeEmbedder{dim: 8}, path, "removal-marker")
				if err := database.SaveChunks(chunks); err != nil {
					t.Fatal(err)
				}
			}
			removeRoot := lexicalRoot
			if removal == "canonical" {
				removeRoot = canonicalRoot
			}
			if removal == "source-alias" {
				removeRoot = sourceAlias
			}
			if removal == "missing" {
				// Rename instead of deleting: prove removal does not need the source.
				if err := os.Rename(root, root+"-retained"); err != nil {
					t.Fatal(err)
				}
			}
			removed, err := RemoveDirectory(database, removeRoot)
			if err != nil || removed != 1 {
				t.Fatalf("removed=%d err=%v, want one distinct identity", removed, err)
			}
			for _, path := range identities {
				if doc, err := database.GetDocument(path); err != nil || doc != nil {
					t.Fatalf("remaining document %q: %+v, %v", path, doc, err)
				}
				if chunks, err := database.GetChunksForDocument(path); err != nil || len(chunks) != 0 {
					t.Fatalf("remaining chunks %q: %d, %v", path, len(chunks), err)
				}
			}
			for _, path := range controls {
				if doc, err := database.GetDocument(path); err != nil || doc == nil {
					t.Fatalf("control %q: %+v, %v", path, doc, err)
				}
				if chunks, err := database.GetChunksForDocument(path); err != nil || len(chunks) != 1 || chunks[0].Content != "removal-marker" {
					t.Fatalf("control chunks %q: %+v, %v", path, chunks, err)
				}
			}
			if removal == "missing" {
				file = filepath.Join(root+"-retained", "note.md")
			}
			fileRoot, err := os.OpenRoot(filepath.Dir(file))
			if err != nil {
				t.Fatal(err)
			}
			content, readErr := fileRoot.ReadFile(filepath.Base(file))
			if err := fileRoot.Close(); err != nil {
				t.Fatal(err)
			}
			if readErr != nil || string(content) != "source survives" {
				t.Fatalf("source changed: %q, %v", content, readErr)
			}
			if removed, err := RemoveDirectory(database, removeRoot); err != nil || removed != 0 {
				t.Fatalf("repeat removal=%d, %v", removed, err)
			}
		})
	}
}

func TestRemoveDirectoryRejectsSymlinkLoop(t *testing.T) {
	home := t.TempDir()
	loop := filepath.Join(home, "loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}
	database, err := db.OpenAt(filepath.Join(home, "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	if removed, err := RemoveDirectory(database, loop); err == nil || removed != 0 {
		t.Fatalf("loop removal=%d, %v", removed, err)
	}
}
