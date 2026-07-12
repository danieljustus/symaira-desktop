package export

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/dbviews"
)

func TestNoteMarkdownHTML(t *testing.T) {
	root := t.TempDir()
	notePath := filepath.Join(root, "hello.md")
	content := `---
title: Hello
---
# Body

- item one
- item two
`
	if err := os.WriteFile(notePath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	md, err := Note(root, "hello.md", Options{Format: "pdf"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(md), "# Hello") {
		t.Fatalf("expected title in markdown, got:\n%s", md)
	}

	htmlBytes, err := Note(root, "hello.md", Options{Format: "html"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(htmlBytes), "<h1>Hello</h1>") {
		t.Fatalf("expected html title, got:\n%s", htmlBytes)
	}
}

func TestNoteTransclusion(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("common content"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.md"), []byte("![[a.md]]\n"), 0644); err != nil {
		t.Fatal(err)
	}

	md, err := Note(root, "b.md", Options{Format: "pdf"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(md), "common content") {
		t.Fatalf("expected embedded content, got:\n%s", md)
	}
}

func TestViewToMarkdown(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("---\ntitle: Rechnung A\nstatus: paid\n---\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.md"), []byte("---\ntitle: Rechnung B\nstatus: open\n---\n"), 0644); err != nil {
		t.Fatal(err)
	}
	view := &dbviews.View{ID: "v1", Name: "Rechnungen", Columns: []string{"status", "title"}}
	rows := []map[string]string{
		{"title": "Rechnung A", "status": "paid", "path": "a.md"},
		{"title": "Rechnung B", "status": "open", "path": "b.md"},
	}

	md, err := View(root, view, rows, Options{Format: "pdf"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(md), "| path | status | title |") {
		t.Fatalf("expected markdown table header, got:\n%s", md)
	}
	if !strings.Contains(string(md), "Rechnung A") {
		t.Fatalf("expected row content, got:\n%s", md)
	}
}

func TestWikilinks(t *testing.T) {
	body := "See [[Note A|label]] and [[Note B#Section]]."
	links := PlainLinks(body)
	if len(links) != 2 {
		t.Fatalf("expected 2 links, got %d", len(links))
	}
	if links[0] != "Note A" || links[1] != "Note B" {
		t.Fatalf("unexpected links: %v", links)
	}
}
