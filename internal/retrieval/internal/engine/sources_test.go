package engine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/retrieval/internal/db"
)

func TestIndexDirectoryUsesCanonicalRootAndRejectsSymlinkEscapes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	realRoot := filepath.Join(home, "library")
	if err := os.Mkdir(realRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(realRoot, "note.md")
	if err := os.WriteFile(file, []byte("in place content"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(home, "library-alias")
	if err := os.Symlink(realRoot, alias); err != nil {
		t.Fatal(err)
	}

	dbClient, err := db.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dbClient.Close() }()
	if err := IndexDirectory(dbClient, &fakeEmbedder{dim: 8}, alias); err != nil {
		t.Fatalf("IndexDirectory via alias: %v", err)
	}
	canonicalFile, err := filepath.EvalSymlinks(file)
	if err != nil {
		t.Fatal(err)
	}
	if doc, err := dbClient.GetDocument(canonicalFile); err != nil || doc == nil {
		t.Fatalf("canonical document = %+v, err=%v; want indexed", doc, err)
	}
	if doc, err := dbClient.GetDocument(filepath.Join(alias, "note.md")); err != nil {
		t.Fatal(err)
	} else if doc != nil {
		t.Fatalf("alias document = %+v, want no duplicate identity", doc)
	}

	outside := filepath.Join(home, "outside.md")
	if err := os.WriteFile(outside, []byte("must not escape"), 0o600); err != nil {
		t.Fatal(err)
	}
	escape := filepath.Join(realRoot, "escape.md")
	if err := os.Symlink(outside, escape); err != nil {
		t.Fatal(err)
	}
	if err := IndexDirectory(dbClient, &fakeEmbedder{dim: 8}, realRoot); err != nil {
		t.Fatalf("IndexDirectory with symlink: %v", err)
	}
	if doc, err := dbClient.GetDocument(escape); err != nil {
		t.Fatal(err)
	} else if doc != nil {
		t.Fatalf("symlink escape document = %+v, want not indexed", doc)
	}

	canonicalRoot, err := filepath.EvalSymlinks(realRoot)
	if err != nil {
		t.Fatal(err)
	}
	removed, err := RemoveDirectory(dbClient, canonicalRoot)
	if err != nil {
		t.Fatalf("RemoveDirectory: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if doc, err := dbClient.GetDocument(canonicalFile); err != nil {
		t.Fatal(err)
	} else if doc != nil {
		t.Fatalf("document after RemoveDirectory = %+v, want nil", doc)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("external file after RemoveDirectory: %v", err)
	}
}
