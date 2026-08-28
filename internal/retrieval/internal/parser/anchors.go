package parser

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ledongthuc/pdf"
)

// Anchor identifies a durable location in an extracted source. Value is
// intentionally opaque to the index: consumers may use page numbers, heading
// paths, or format-specific structural identifiers without changing the wire
// contract.
type Anchor struct {
	Kind  string
	Value string
}

// Section is an independently chunkable portion of a source document. Keeping
// sections separate prevents a chunk from crossing a page or heading boundary.
type Section struct {
	Text      string
	Start     int
	Anchor    Anchor
	Synthetic bool
}

// ParseFileSections is the anchor-aware counterpart to ParseFile. ParseFile is
// intentionally unchanged for callers that only need flattened text.
func ParseFileSections(path string) ([]Section, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".pdf":
		return parsePDFSections(path)
	case ".md", ".markdown":
		return parseMarkdownSections(path)
	default:
		text, err := ParseFile(path)
		if err != nil {
			return nil, err
		}
		return sectionsFromText(text), nil
	}
}

func sectionsFromText(text string) []Section {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return []Section{{Text: text, Anchor: Anchor{Kind: "text", Value: "offset:0"}}}
}

func parsePDFSections(path string) ([]Section, error) {
	f, r, err := pdf.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open PDF: %w", err)
	}
	defer func() { _ = f.Close() }()

	var sections []Section
	offset := 0
	for pageNumber := 1; pageNumber <= r.NumPage(); pageNumber++ {
		page := r.Page(pageNumber)
		if page.V.IsNull() {
			continue
		}
		content, err := page.GetPlainText(nil)
		if err != nil {
			continue
		}
		content = strings.TrimSpace(content)
		if content == "" {
			continue
		}
		sections = append(sections, Section{
			Text:   content,
			Start:  offset,
			Anchor: Anchor{Kind: "page", Value: fmt.Sprintf("%d", pageNumber)},
		})
		offset += len(content) + 2
	}
	if len(sections) == 0 {
		return nil, fmt.Errorf("PDF contains no extractable text (may be image-only)")
	}
	return sections, nil
}

func parseMarkdownSections(path string) ([]Section, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- parser receives a validated document path.
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}
	if len(data) > MaxIndexFileSize {
		return nil, fmt.Errorf("file %s exceeds %d byte limit (%d bytes)", path, MaxIndexFileSize, len(data))
	}
	text, baseOffset := stripMarkdownFrontmatter(string(data))
	lines := strings.SplitAfter(text, "\n")
	type heading struct {
		start int
		level int
		name  string
	}
	var headings []heading
	offset := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(strings.TrimSuffix(line, "\n"))
		if level, name, ok := markdownHeading(trimmed); ok {
			headings = append(headings, heading{start: offset, level: level, name: name})
		}
		offset += len(line)
	}
	if len(headings) == 0 {
		return sectionsFromTextAt(text, baseOffset), nil
	}

	sections := make([]Section, 0, len(headings)+1)
	if prefix := text[:headings[0].start]; strings.TrimSpace(prefix) != "" {
		sections = append(sections, Section{Text: strings.TrimSpace(prefix), Start: baseOffset, Anchor: Anchor{Kind: "text", Value: fmt.Sprintf("offset:%d", baseOffset)}})
	}
	pathParts := make([]string, 0, 6)
	for i, h := range headings {
		for len(pathParts) >= h.level {
			pathParts = pathParts[:len(pathParts)-1]
		}
		pathParts = append(pathParts, h.name)
		end := len(text)
		if i+1 < len(headings) {
			end = headings[i+1].start
		}
		body := strings.TrimSpace(text[h.start:end])
		if body == "" {
			continue
		}
		sections = append(sections, Section{
			Text:   body,
			Start:  baseOffset + h.start,
			Anchor: Anchor{Kind: "heading", Value: strings.Join(pathParts, " > ")},
		})
	}
	return sections, nil
}

func sectionsFromTextAt(text string, start int) []Section {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return []Section{{Text: text, Start: start, Anchor: Anchor{Kind: "text", Value: fmt.Sprintf("offset:%d", start)}}}
}

func stripMarkdownFrontmatter(text string) (string, int) {
	if !strings.HasPrefix(text, "---") {
		return text, 0
	}
	lines := strings.SplitAfter(text, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return text, 0
	}
	offset := len(lines[0])
	for _, line := range lines[1:] {
		if strings.TrimSpace(strings.TrimSuffix(line, "\n")) == "---" {
			end := offset + len(line)
			return text[end:], end
		}
		offset += len(line)
	}
	return text, 0
}

func markdownHeading(line string) (int, string, bool) {
	level := 0
	for level < len(line) && line[level] == '#' {
		level++
	}
	if level == 0 || level > 6 || level >= len(line) || line[level] != ' ' {
		return 0, "", false
	}
	name := strings.TrimSpace(line[level:])
	return level, name, name != ""
}
