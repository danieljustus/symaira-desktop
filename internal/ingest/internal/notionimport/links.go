package notionimport

import (
	"fmt"
	"regexp"
	"strings"
)

// notionLinkRe matches Notion-style internal links in various formats:
//   - [[Page Name <id>]]
//   - [Display Text](Page%20Name%20<id>.md)
//   - [Display Text](<id>.md)
var notionLinkRe = regexp.MustCompile(`\[\[([^\]]+)\]\]|\[([^\]]+)\]\(([^)]+\.md)\)`)

// rewriteNotionLinks rewrites internal Notion links to clean wikilinks.
func rewriteNotionLinks(body string, pageNameMap map[string]string, _ string) string {
	return notionLinkRe.ReplaceAllStringFunc(body, func(match string) string {
		submatches := notionLinkRe.FindStringSubmatch(match)
		if submatches == nil {
			return match
		}

		// [[Page Name <id>]] format
		if submatches[1] != "" {
			return rewriteDoubleBracket(submatches[1], pageNameMap)
		}

		// [Display](file.md) format
		display := submatches[2]
		fileRef := submatches[3]

		// Try to extract Notion ID from the file reference.
		if m := NotionIDRe.FindStringSubmatch(fileRef); m != nil {
			if name, ok := pageNameMap[m[1]]; ok {
				return fmt.Sprintf("[[%s]]", name)
			}
			return fmt.Sprintf("[[%s]]", display)
		}

		// URL-decoded reference — try to find the page.
		decoded := strings.ReplaceAll(fileRef, "%20", " ")
		decoded = strings.TrimSuffix(decoded, ".md")
		if m := NotionIDRe.FindStringSubmatch(decoded); m != nil {
			if name, ok := pageNameMap[m[1]]; ok {
				return fmt.Sprintf("[[%s]]", name)
			}
		}

		// Plain markdown link to .md file — convert to wikilink.
		return fmt.Sprintf("[[%s]]", display)
	})
}

// rewriteDoubleBracket handles the content inside [[...]].
func rewriteDoubleBracket(content string, pageNameMap map[string]string) string {
	// Extract Notion ID if present.
	if m := NotionIDRe.FindStringSubmatch(content); m != nil {
		if name, ok := pageNameMap[m[1]]; ok {
			return fmt.Sprintf("[[%s]]", name)
		}
		// ID not in map — use cleaned content.
		cleaned := strings.TrimSuffix(content, m[0])
		return fmt.Sprintf("[[%s]]", strings.TrimSpace(cleaned))
	}
	return fmt.Sprintf("[[%s]]", strings.TrimSpace(content))
}
