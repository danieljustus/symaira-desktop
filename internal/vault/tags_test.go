package vault

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestExtractInlineTags(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "single inline tag",
			body: "This is a note with #invoice tag.",
			want: []string{"invoice"},
		},
		{
			name: "multiple inline tags",
			body: "Tags: #invoice, #receipt and #finance/2026.",
			want: []string{"invoice", "receipt", "finance/2026"},
		},
		{
			name: "nested tag",
			body: "Working on #project/symaira today.",
			want: []string{"project/symaira"},
		},
		{
			name: "deduplicated case-insensitively preserving first order",
			body: "First #invoice, then #Invoice, then #INVOICE and #receipt.",
			want: []string{"invoice", "receipt"},
		},
		{
			name: "skip fenced code block backticks",
			body: "Before code\n```go\n#not-a-tag\nvar x = 1\n```\nAfter code with #valid-tag",
			want: []string{"valid-tag"},
		},
		{
			name: "skip fenced code block tildes",
			body: "Before code\n~~~\n#not-a-tag\n~~~\nAfter code with #valid-tag",
			want: []string{"valid-tag"},
		},
		{
			name: "skip inline code span single backtick",
			body: "Use `#not-a-tag` for code, but use #real-tag here.",
			want: []string{"real-tag"},
		},
		{
			name: "skip inline code span double backtick",
			body: "Use ``#not-a-tag`` in documentation.",
			want: nil,
		},
		{
			name: "skip ATX headings",
			body: "# Heading 1\n## Heading 2\n### Heading 3\nBody with #actual-tag",
			want: []string{"actual-tag"},
		},
		{
			name: "skip digits-only issue references",
			body: "Fixes #123 and addresses #456, but see #v1 and #issue-789.",
			want: []string{"v1", "issue-789"},
		},
		{
			name: "skip URL fragments",
			body: "Visit https://example.com/page#section or <http://site.org#frag> or [text](doc.md#heading). Here is #tag.",
			want: []string{"tag"},
		},
		{
			name: "skip wikilink headings",
			body: "See [[My Note#Some Heading]] and ![[Embed#Section]]. Here is #real.",
			want: []string{"real"},
		},
		{
			name: "skip word-embedded hash",
			body: "Programming in C# and F# or foo#bar. Only #valid is a tag.",
			want: []string{"valid"},
		},
		{
			name: "tags in parentheses and quotes",
			body: "Enclosed (#tag1) and \"#tag2\" and '#tag3' and [#tag4].",
			want: []string{"tag1", "tag2", "tag3", "tag4"},
		},
		{
			name: "trailing punctuation stripped",
			body: "#tag1. #tag2, #tag3! #tag4? #tag5; #tag6:",
			want: []string{"tag1", "tag2", "tag3", "tag4", "tag5", "tag6"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractInlineTags(tt.body)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ExtractInlineTags() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseBytesWithInlineTags(t *testing.T) {
	// Note with no frontmatter tags, but body has #invoice
	content := []byte("---\ntitle: Sample Note\n---\n\nHere is an inline #invoice tag and #project/symaira.")
	doc, err := ParseBytes("/path/sample.md", content)
	if err != nil {
		t.Fatalf("ParseBytes failed: %v", err)
	}

	wantTags := []string{"invoice", "project/symaira"}
	if !reflect.DeepEqual(doc.Tags, wantTags) {
		t.Errorf("doc.Tags = %v, want %v", doc.Tags, wantTags)
	}
}

func TestParseBytesDeduplicatesFrontmatterAndInline(t *testing.T) {
	// Frontmatter has 'invoice', body has '#invoice' and '#finance'
	content := []byte("---\ntitle: Sample\ntags:\n  - invoice\n---\n\nBody with #Invoice and #finance.")
	doc, err := ParseBytes("/path/sample.md", content)
	if err != nil {
		t.Fatalf("ParseBytes failed: %v", err)
	}

	wantTags := []string{"invoice", "finance"}
	if !reflect.DeepEqual(doc.Tags, wantTags) {
		t.Errorf("doc.Tags = %v, want %v", doc.Tags, wantTags)
	}
}

func TestRewriteInlineTags(t *testing.T) {
	t.Run("rename tag", func(t *testing.T) {
		body := "Note mentioning #invoice and #finance. Also `#invoice` in code."
		newBody, changed := RewriteInlineTags(body, func(tag string) (string, bool) {
			if strings.EqualFold(tag, "invoice") {
				return "receipt", true
			}
			return tag, true
		})
		if !changed {
			t.Fatal("expected changed == true")
		}
		expected := "Note mentioning #receipt and #finance. Also `#invoice` in code."
		if newBody != expected {
			t.Errorf("got %q, want %q", newBody, expected)
		}
	})

	t.Run("delete tag", func(t *testing.T) {
		body := "Note mentioning #invoice and #finance."
		newBody, changed := RewriteInlineTags(body, func(tag string) (string, bool) {
			if strings.EqualFold(tag, "invoice") {
				return "", false
			}
			return tag, true
		})
		if !changed {
			t.Fatal("expected changed == true")
		}
		if strings.Contains(newBody, "invoice") {
			t.Errorf("newBody still contains invoice: %q", newBody)
		}
	})
}

func TestRewriteDocumentTagsAndBody(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "note.md")
	content := "---\ntitle: Test\ntags: [invoice]\n---\n\nHere is #invoice and #urgent.\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	doc, err := ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}

	changed, err := RewriteDocumentTagsAndBody(
		path,
		doc,
		func(fmTags []string) ([]string, bool) {
			var out []string
			for _, t := range fmTags {
				if t == "invoice" {
					out = append(out, "receipt")
				} else {
					out = append(out, t)
				}
			}
			return out, true
		},
		func(body string) (string, bool) {
			return RewriteInlineTags(body, func(tag string) (string, bool) {
				if tag == "invoice" {
					return "receipt", true
				}
				return tag, true
			})
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected changed == true")
	}

	reparsed, err := ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if containsTagCaseInsensitive(reparsed.Tags, "invoice") {
		t.Errorf("reparsed still has invoice: %v", reparsed.Tags)
	}
	if !containsTagCaseInsensitive(reparsed.Tags, "receipt") {
		t.Errorf("reparsed missing receipt: %v", reparsed.Tags)
	}
	if !containsTagCaseInsensitive(reparsed.Tags, "urgent") {
		t.Errorf("reparsed missing urgent: %v", reparsed.Tags)
	}
}
