package export

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

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

// Note exports a single note to Markdown, resolving transclusions but not wikilinks.
func Note(root, relPath string, opts Options) ([]byte, error) {
	absPath, err := vault.SecurePath(root, relPath)
	if err != nil {
		return nil, err
	}
	doc, err := vault.ParseFile(absPath)
	if err != nil {
		return nil, err
	}

	body, err := resolveTransclusions(root, doc.Body, opts)
	if err != nil {
		return nil, err
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
	if view.Name != "" {
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
			md, err := Note(root, relPath, opts)
			if err != nil {
				fmt.Fprintf(&b, "\n*Error loading %s: %v*\n", relPath, err)
				continue
			}
			b.WriteString("---\n\n")
			b.Write(md)
			b.WriteString("\n")
		}
	}

	out := b.String()
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
