package export

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/danieljustus/symaira-desktop/internal/retrieval"
)

// SearchHit is the export-facing subset of a service search result. Keeping
// this type in the renderer package prevents the Markdown format from knowing
// about the service or a database implementation.
type SearchHit struct {
	Path            string
	Title           string
	Snippet         string
	Score           float64
	Anchor          *retrieval.LocationAnchor
	MetadataMatches []string
}

// SearchResults renders a search result set as a Markdown note. The Markdown
// form is also the source passed to the existing PDF renderer, so both formats
// have exactly the same attribution and metadata. Source links use the
// existing [[path#anchor]] convention consumed by the native viewer.
func SearchResults(query, title, created string, hits []SearchHit, format string) ([]byte, error) {
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "md" {
		format = "markdown"
	}
	if format != "markdown" && format != "pdf" {
		return nil, fmt.Errorf("unsupported search export format %q: use markdown or pdf", format)
	}
	if strings.TrimSpace(title) == "" {
		title = "Search results"
	}
	if strings.TrimSpace(created) == "" {
		created = time.Now().UTC().Format(time.RFC3339)
	}

	frontmatter, err := yaml.Marshal(map[string]interface{}{
		"type":         "search_result_set",
		"title":        title,
		"query":        &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: query, Style: yaml.DoubleQuotedStyle},
		"created":      created,
		"result_count": len(hits),
	})
	if err != nil {
		return nil, fmt.Errorf("encode search result metadata: %w", err)
	}

	var b strings.Builder
	b.WriteString("---\n")
	b.Write(frontmatter)
	b.WriteString("---\n\n")
	if format != "pdf" {
		b.WriteString("# ")
		b.WriteString(headingText(title))
		b.WriteString("\n\n")
	}
	b.WriteString("Query: `")
	b.WriteString(codeText(query))
	b.WriteString("`\n\n")
	fmt.Fprintf(&b, "**%d result", len(hits))
	if len(hits) != 1 {
		b.WriteString("s")
	}
	b.WriteString("**\n\n")

	if len(hits) == 0 {
		b.WriteString("_No matching passages._\n")
		return []byte(b.String()), nil
	}
	for i, hit := range hits {
		fmt.Fprintf(&b, "## %d. %s\n\n", i+1, headingText(hit.Title))
		b.WriteString("**Source:** [[")
		b.WriteString(wikiTarget(hit))
		b.WriteString("]] (`")
		b.WriteString(codeText(hit.Path))
		b.WriteString("`)\n")
		if hit.Anchor != nil && hit.Anchor.Kind != "" && hit.Anchor.Value != "" {
			b.WriteString("**Location:** ")
			b.WriteString(inlineText(hit.Anchor.Kind))
			b.WriteString(": ")
			b.WriteString(inlineText(hit.Anchor.Value))
			b.WriteString("\n")
		} else {
			b.WriteString("**Location:** Not available\n")
		}
		fmt.Fprintf(&b, "**Score:** %.6f\n", hit.Score)
		if len(hit.MetadataMatches) > 0 {
			b.WriteString("**Metadata matches:** ")
			for i, field := range hit.MetadataMatches {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(inlineText(field))
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
		b.WriteString(blockquote(hit.Snippet))
		b.WriteString("\n\n")
	}
	return []byte(b.String()), nil
}

func wikiTarget(hit SearchHit) string {
	parts := strings.Split(hit.Path, "/")
	for i, part := range parts {
		parts[i] = escapeWikiPart(part)
	}
	target := strings.Join(parts, "/")
	if hit.Anchor != nil && hit.Anchor.Kind != "" && hit.Anchor.Value != "" {
		target += "#" + escapeWikiPart(hit.Anchor.Kind) + ":" + escapeWikiPart(hit.Anchor.Value)
	}
	return target
}

// url.PathEscape is applied per path component so vault separators remain
// navigable while brackets, pipes and non-ASCII anchor text cannot terminate a
// wikilink or become Markdown syntax.
func escapeWikiPart(value string) string {
	return strings.ReplaceAll(url.PathEscape(value), "&", "%26")
}

func headingText(value string) string {
	value = strings.NewReplacer("\r", " ", "\n", " ").Replace(strings.TrimSpace(value))
	value = strings.TrimLeft(value, "#")
	if value == "" {
		return "Untitled result"
	}
	return value
}

func inlineText(value string) string {
	value = strings.NewReplacer("\r", " ", "\n", " ").Replace(strings.TrimSpace(value))
	return strings.ReplaceAll(value, "`", "'")
}

func codeText(value string) string {
	return strings.NewReplacer("\r", " ", "\n", " ", "`", "'").Replace(value)
}

func blockquote(value string) string {
	lines := strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
	for i, line := range lines {
		lines[i] = "> " + line
	}
	return strings.Join(lines, "\n")
}
