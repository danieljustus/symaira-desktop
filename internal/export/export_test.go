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

	// The PDF path carries the title in frontmatter, because that is where
	// the renderer's profiles read it from; TestNotePDFEmitsFrontmatter
	// covers that shape in detail. Plain Markdown keeps the heading.
	md, err := Note(root, "hello.md", Options{})
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

// TestNotePDFEmitsFrontmatter guards the renderer's document contract: the
// PDF profiles read their required fields (title, lang, recipient, …) from
// frontmatter, not from the Markdown body. Exporting a bare "# Title"
// heading fails the contract before typesetting starts, which is how PDF
// export was broken while the renderer was mocked in tests.
func TestNotePDFEmitsFrontmatter(t *testing.T) {
	root := t.TempDir()
	note := filepath.Join(root, "bericht.md")
	if err := os.WriteFile(note, []byte("---\ntitle: Testbericht\nlang: de\n---\n\nInhalt.\n"), 0600); err != nil {
		t.Fatal(err)
	}

	out, err := Note(root, "bericht.md", Options{Format: "pdf", Profile: "report"})
	if err != nil {
		t.Fatalf("Note: %v", err)
	}
	got := string(out)

	if !strings.HasPrefix(got, "---\n") {
		t.Fatalf("PDF export must open with a frontmatter fence, got:\n%s", got)
	}
	for _, want := range []string{"title: Testbericht", "lang: de"} {
		if !strings.Contains(got, want) {
			t.Errorf("frontmatter is missing %q, got:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "Inhalt.") {
		t.Errorf("body lost in PDF export, got:\n%s", got)
	}
	// The templates typeset the title themselves; repeating it as a heading
	// would print it twice.
	if strings.Contains(got, "# Testbericht") {
		t.Errorf("PDF export must not repeat the title as a body heading, got:\n%s", got)
	}
}

// TestNoteMarkdownKeepsHeading pins the non-PDF behaviour, which the change
// above must not disturb.
func TestNoteMarkdownKeepsHeading(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "n.md"), []byte("---\ntitle: Titel\n---\n\nText.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := Note(root, "n.md", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(out), "# Titel\n\n") {
		t.Errorf("markdown export must keep the title heading, got:\n%s", out)
	}
}
