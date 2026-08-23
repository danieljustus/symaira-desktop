package export

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/danieljustus/symaira-desktop/internal/dbviews"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

var (
	wikilinkRegex  = regexp.MustCompile(`\[\[([^\]]+)\]\]`)
	embedLinkRegex = regexp.MustCompile(`!\[\[([^\]]+)\]\]`)
)

// Options controls how a vault resource is exported.
type Options struct {
	Format  string // "pdf" or "html"
	Profile string // symprint profile for PDF
}

// pdfFrontmatter renders the YAML frontmatter block the PDF renderer's
// document contract expects.
//
// The PDF profiles require named fields — `report` needs a title, `meeting`
// additionally a language and date, `behoerde` a recipient — and read them
// from frontmatter, not from the Markdown body. Handing the renderer a bare
// "# Title" heading therefore fails the contract before a page is typeset,
// so the exported document carries the note's own frontmatter through, with
// the resolved title filled in when the note did not set one explicitly.
func pdfFrontmatter(front map[string]interface{}, title string) (string, error) {
	meta := make(map[string]interface{}, len(front)+1)
	for k, v := range front {
		meta[k] = v
	}
	if _, ok := meta["title"]; !ok && title != "" {
		meta["title"] = title
	}
	if len(meta) == 0 {
		return "", nil
	}

	encoded, err := yaml.Marshal(meta)
	if err != nil {
		return "", fmt.Errorf("failed to encode frontmatter: %w", err)
	}
	return "---\n" + string(encoded) + "---\n\n", nil
}

// noteMarkdown resolves a note to its exportable Markdown body, plus the
// parsed document so callers can build a frontmatter block from it.
func noteMarkdown(root, relPath string, opts Options) (*vault.Document, string, error) {
	absPath, err := vault.SecurePath(root, relPath)
	if err != nil {
		return nil, "", err
	}
	doc, err := vault.ParseFile(absPath)
	if err != nil {
		return nil, "", err
	}

	body, err := resolveTransclusions(root, doc.Body, opts)
	if err != nil {
		return nil, "", err
	}
	return doc, body, nil
}

// Note exports a single note to Markdown, resolving transclusions but not wikilinks.
func Note(root, relPath string, opts Options) ([]byte, error) {
	doc, body, err := noteMarkdown(root, relPath, opts)
	if err != nil {
		return nil, err
	}

	// The PDF templates typeset the title themselves from frontmatter, so
	// repeating it as a body heading would print it twice.
	if opts.Format == "pdf" {
		front, err := pdfFrontmatter(doc.Frontmatter, doc.Title)
		if err != nil {
			return nil, err
		}
		return []byte(front + body), nil
	}

	out := ""
	if doc.Title != "" {
		out += "# " + doc.Title + "\n\n"
	}
	out += body

	if opts.Format == "html" {
		return []byte(noteToHTML(out)), nil
	}
	return []byte(out), nil
}

// View exports a database view to a Markdown/HTML document.
func View(root string, view *dbviews.View, rows []map[string]string, opts Options) ([]byte, error) {
	var b strings.Builder

	// A PDF document carries exactly one frontmatter block, at the top; the
	// embedded notes contribute body only. Everything below therefore builds
	// the body, and the block is prepended once at the end.
	isPDF := opts.Format == "pdf"
	noteOpts := opts
	if isPDF {
		noteOpts.Format = ""
	}

	if view.Name != "" && !isPDF {
		fmt.Fprintf(&b, "# %s\n\n", view.Name)
	}
	if len(rows) == 0 {
		b.WriteString("No matching notes.\n")
	} else {
		writeMarkdownTable(&b, view.Columns, rows)
		b.WriteString("\n")
		for _, row := range rows {
			path := row["path"]
			if path == "" {
				continue
			}
			relPath := path
			if filepath.IsAbs(path) {
				relPath, _ = filepath.Rel(root, path)
			}
			md, err := Note(root, relPath, noteOpts)
			if err != nil {
				fmt.Fprintf(&b, "\n*Error loading %s: %v*\n", relPath, err)
				continue
			}
			// A horizontal rule between notes. In a PDF export this must not
			// be the first thing in the document: three dashes at the very
			// top would parse as the opening frontmatter fence.
			b.WriteString("---\n\n")
			b.Write(md)
			b.WriteString("\n")
		}
	}

	out := b.String()
	if isPDF {
		front, err := pdfFrontmatter(nil, view.Name)
		if err != nil {
			return nil, err
		}
		return []byte(front + out), nil
	}
	if opts.Format == "html" {
		return []byte(noteToHTML(out)), nil
	}
	return []byte(out), nil
}

func resolveTransclusions(root, body string, opts Options) (string, error) {
	for {
		matches := embedLinkRegex.FindAllStringSubmatchIndex(body, -1)
		if len(matches) == 0 {
			break
		}
		m := matches[0]
		inner := body[m[2]:m[3]]

		// strip heading / block fragment
		ref := inner
		if idx := strings.Index(ref, "#"); idx >= 0 {
			ref = ref[:idx]
		}
		ref = strings.TrimSpace(ref)
		if !strings.HasSuffix(ref, ".md") {
			ref += ".md"
		}

		var replacement string
		candidate, err := vault.SecurePath(root, ref)
		if _, statErr := os.Stat(candidate); err == nil && statErr == nil {
			doc, err := vault.ParseFile(candidate)
			if err != nil {
				replacement = fmt.Sprintf("*embed error: %v*", err)
			} else {
				replacement = doc.Body
			}
		} else if err != nil {
			replacement = fmt.Sprintf("*embed error: %v*", err)
		} else {
			replacement = fmt.Sprintf("*embed not found: %s*", ref)
		}

		body = body[:m[0]] + replacement + body[m[1]:]
	}
	return body, nil
}

func noteToHTML(markdown string) string {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html>\n<head>\n<meta charset=\"utf-8\">\n")
	b.WriteString("<title>Exported Note</title>\n</head>\n<body>\n")

	lines := strings.Split(markdown, "\n")
	inList := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "# ") {
			b.WriteString("<h1>" + html.EscapeString(strings.TrimPrefix(trimmed, "# ")) + "</h1>\n")
		} else if strings.HasPrefix(trimmed, "## ") {
			b.WriteString("<h2>" + html.EscapeString(strings.TrimPrefix(trimmed, "## ")) + "</h2>\n")
		} else if strings.HasPrefix(trimmed, "### ") {
			b.WriteString("<h3>" + html.EscapeString(strings.TrimPrefix(trimmed, "### ")) + "</h3>\n")
		} else if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") {
			if !inList {
				b.WriteString("<ul>\n")
				inList = true
			}
			b.WriteString("<li>" + html.EscapeString(strings.TrimPrefix(strings.TrimPrefix(trimmed, "- "), "* ")) + "</li>\n")
		} else if trimmed == "" {
			if inList {
				b.WriteString("</ul>\n")
				inList = false
			}
			b.WriteString("\n")
		} else {
			if inList {
				b.WriteString("</ul>\n")
				inList = false
			}
			b.WriteString("<p>" + html.EscapeString(trimmed) + "</p>\n")
		}
	}
	if inList {
		b.WriteString("</ul>\n")
	}
	b.WriteString("</body>\n</html>")
	return b.String()
}

func writeMarkdownTable(b *strings.Builder, columns []string, rows []map[string]string) {
	// Build a stable column order: include any extra columns found in rows.
	seen := make(map[string]bool)
	var ordered []string
	for _, c := range columns {
		if !seen[c] {
			ordered = append(ordered, c)
			seen[c] = true
		}
	}
	for _, row := range rows {
		for k := range row {
			if !seen[k] {
				ordered = append(ordered, k)
				seen[k] = true
			}
		}
	}
	sort.Strings(ordered)

	b.WriteString("| " + strings.Join(ordered, " | ") + " |\n")
	b.WriteString("|" + strings.Repeat(" --- |", len(ordered)) + "\n")
	for _, row := range rows {
		cells := make([]string, len(ordered))
		for i, col := range ordered {
			cells[i] = strings.ReplaceAll(row[col], "|", "\\|")
		}
		b.WriteString("| " + strings.Join(cells, " | ") + " |\n")
	}
}

// ProfileList lists the available symprint profiles; if symprint is unavailable
// it returns a fallback set so the CLI can explain the limitation.
func ProfileList() []string {
	return []string{"brief", "behoerde", "report", "rechnung"}
}

// Wikilink references are left as plain text in the exported document. This
// function returns the first non-embedded wikilink target for consumers that need
// a human-readable link label.
func PlainLinks(body string) []string {
	matches := wikilinkRegex.FindAllStringSubmatch(body, -1)
	var out []string
	seen := make(map[string]bool)
	for _, m := range matches {
		if len(m) > 1 {
			target := strings.TrimSpace(m[1])
			if idx := strings.Index(target, "|"); idx >= 0 {
				target = target[:idx]
			}
			if idx := strings.Index(target, "#"); idx >= 0 {
				target = target[:idx]
			}
			if target != "" && !seen[target] {
				seen[target] = true
				out = append(out, target)
			}
		}
	}
	return out
}
