package retrieval

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/retrieval/internal/db"
)

func TestFacadeOpenAndCloseUsesIsolatedHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	client, err := Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if client == nil || client.db == nil {
		t.Fatal("Open returned an empty client")
	}
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := (*Client)(nil).Close(); err != nil {
		t.Fatalf("nil Close: %v", err)
	}
}

func TestFacadeDirectoryLifecycleDoesNotModifySource(t *testing.T) {
	dir := t.TempDir()
	dbClient, err := db.OpenAt(filepath.Join(t.TempDir(), "retrieval.db"))
	if err != nil {
		t.Fatalf("db.OpenAt: %v", err)
	}
	defer func() { _ = dbClient.Close() }()

	path := filepath.Join(dir, "invoice.md")
	original := "# Invoice\n\nAmount: 42 EUR"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &Client{db: dbClient, embedder: &fakeEmbedder{dim: 8, model: "test-model"}}

	if err := client.IndexDirectory(dir); err != nil {
		t.Fatalf("IndexDirectory: %v", err)
	}
	indexed, err := dbClient.GetDocument(path)
	if err != nil {
		t.Fatalf("GetDocument after IndexDirectory: %v", err)
	}
	if indexed == nil || indexed.Path != path {
		t.Fatalf("indexed document = %#v, want %s", indexed, path)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := client.WatchDirectory(ctx, dir); err != nil {
		t.Fatalf("WatchDirectory with cancelled context: %v", err)
	}

	removed, err := client.RemoveDirectory(dir)
	if err != nil {
		t.Fatalf("RemoveDirectory: %v", err)
	}
	if removed != 1 {
		t.Fatalf("RemoveDirectory removed %d documents, want 1", removed)
	}
	if got, err := os.ReadFile(path); err != nil { // #nosec G304 -- path is a t.TempDir fixture
		t.Fatalf("read source after RemoveDirectory: %v", err)
	} else if string(got) != original {
		t.Fatalf("source changed after directory lifecycle: %q", got)
	}
}

func TestArchivePathFromMarkdown(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{name: "archive path", content: "---\narchive_path: \"archive/invoice.pdf\"\n---\n# Invoice", want: "archive/invoice.pdf"},
		{name: "no archive path", content: "---\ntitle: Invoice\n---\n# Invoice", want: ""},
		{name: "no frontmatter", content: "# Invoice\nbody", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.name+".md")
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := archivePathFromMarkdown(path); got != tc.want {
				t.Errorf("archivePathFromMarkdown = %q, want %q", got, tc.want)
			}
		})
	}
	if got := archivePathFromMarkdown(filepath.Join(dir, "missing.md")); got != "" {
		t.Errorf("missing file archive path = %q, want empty", got)
	}
}

func TestFacadeIndexUsesBodyFallbackForNonLocalSource(t *testing.T) {
	dbClient, err := db.OpenAt(filepath.Join(t.TempDir(), "retrieval.db"))
	if err != nil {
		t.Fatalf("db.OpenAt: %v", err)
	}
	defer func() { _ = dbClient.Close() }()

	client := &Client{db: dbClient, embedder: &fakeEmbedder{dim: 8, model: "test-model"}}
	if err := client.Index("stdin:invoice", strings.Repeat("invoice body ", 10)); err != nil {
		t.Fatalf("Index body fallback: %v", err)
	}
	if doc, err := dbClient.GetDocument("stdin:invoice"); err != nil {
		t.Fatalf("GetDocument: %v", err)
	} else if doc == nil {
		t.Fatal("body fallback did not create a document")
	}
}
