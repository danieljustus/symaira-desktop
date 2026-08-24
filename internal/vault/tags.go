package vault

import (
	"fmt"
	"os"
	"strings"
)

// TagSpan represents an occurrence of an inline tag in markdown text.
type TagSpan struct {
	Start int    // byte offset of '#'
	End   int    // byte offset right after the tag name
	Tag   string // tag name without leading '#'
}

// ExtractInlineTags parses inline #tag occurrences from markdown body text,
// skipping code blocks, inline code, ATX headings, URL fragments, wikilinks,
// and digit-only issue references (#123).
// Returned tags are deduplicated preserving the order of first appearance.
func ExtractInlineTags(body string) []string {
	spans := FindInlineTagSpans(body)
	var tags []string
	seen := make(map[string]bool)
	for _, s := range spans {
		lower := strings.ToLower(s.Tag)
		if !seen[lower] {
			seen[lower] = true
			tags = append(tags, s.Tag)
		}
	}
	return tags
}

// FindInlineTagSpans locates all valid inline tag occurrences in markdown body text.
func FindInlineTagSpans(body string) []TagSpan {
	var spans []TagSpan
	n := len(body)
	if n == 0 {
		return nil
	}

	i := 0
	inCodeBlock := false
	var fenceChar byte
	fenceLen := 0

	for i < n {
		lineStart := i

		// Check leading spaces (up to 3) for code block fence
		spaces := 0
		temp := i
		for temp < n && (body[temp] == ' ' || body[temp] == '\t') && spaces < 3 {
			if body[temp] == ' ' {
				spaces++
			} else {
				spaces += 4
			}
			temp++
		}

		// Check for code fence at line start
		if temp < n && (body[temp] == '`' || body[temp] == '~') {
			ch := body[temp]
			count := 0
			for temp < n && body[temp] == ch {
				count++
				temp++
			}
			if count >= 3 {
				if !inCodeBlock {
					// Opening code fence
					inCodeBlock = true
					fenceChar = ch
					fenceLen = count
					i = temp
					for i < n && body[i] != '\n' {
						i++
					}
					if i < n && body[i] == '\n' {
						i++
					}
					continue
				} else if ch == fenceChar && count >= fenceLen {
					// Check if only whitespace until end of line
					onlyWhitespace := true
					for temp < n && body[temp] != '\n' && body[temp] != '\r' {
						if body[temp] != ' ' && body[temp] != '\t' {
							onlyWhitespace = false
							break
						}
						temp++
					}
					if onlyWhitespace {
						// Closing code fence
						inCodeBlock = false
						i = temp
						if i < n && body[i] == '\r' {
							i++
						}
						if i < n && body[i] == '\n' {
							i++
						}
						continue
					}
				}
			}
		}

		if inCodeBlock {
			// Skip this entire line
			for i < n && body[i] != '\n' {
				i++
			}
			if i < n && body[i] == '\n' {
				i++
			}
			continue
		}

		// Reset i to lineStart to scan this line
		i = lineStart

		// Check for ATX heading at line start: 0-3 spaces followed by 1-6 '#' followed by space/tab/newline/EOF
		spaces = 0
		temp = i
		for temp < n && body[temp] == ' ' && spaces < 3 {
			spaces++
			temp++
		}
		hashCount := 0
		for temp < n && body[temp] == '#' {
			hashCount++
			temp++
		}
		if hashCount >= 1 && hashCount <= 6 && (temp == n || body[temp] == ' ' || body[temp] == '\t' || body[temp] == '\r' || body[temp] == '\n') {
			// Skip heading marker and following spaces
			i = temp
			for i < n && (body[i] == ' ' || body[i] == '\t') {
				i++
			}
		}

		// Scan line characters
		for i < n && body[i] != '\n' {
			// Inline code span `
			if body[i] == '`' {
				btCount := 0
				for i < n && body[i] == '`' {
					btCount++
					i++
				}
				closingIdx := findClosingBackticks(body, i, btCount)
				if closingIdx != -1 {
					i = closingIdx + btCount
					continue
				}
				continue
			}

			// Wikilink [[...]]
			if body[i] == '[' && i+1 < n && body[i+1] == '[' {
				closeIdx := strings.Index(body[i+2:], "]]")
				if closeIdx != -1 {
					i = i + 2 + closeIdx + 2
					continue
				}
				i += 2
				continue
			}

			// Markdown link destination ](url...)
			if body[i] == ']' && i+1 < n && body[i+1] == '(' {
				closeIdx := strings.Index(body[i+2:], ")")
				if closeIdx != -1 {
					i = i + 2 + closeIdx + 1
					continue
				}
				i += 2
				continue
			}

			// Autolink <http://...>, <https://...>, <mailto:...>, <ftp://...>
			if body[i] == '<' && (hasPrefixCaseInsensitive(body[i+1:], "http://") || hasPrefixCaseInsensitive(body[i+1:], "https://") || hasPrefixCaseInsensitive(body[i+1:], "mailto:") || hasPrefixCaseInsensitive(body[i+1:], "ftp://")) {
				closeIdx := strings.Index(body[i+1:], ">")
				if closeIdx != -1 {
					i = i + 1 + closeIdx + 1
					continue
				}
			}

			// Bare URLs
			if hasPrefixCaseInsensitive(body[i:], "http://") || hasPrefixCaseInsensitive(body[i:], "https://") || hasPrefixCaseInsensitive(body[i:], "ftp://") || hasPrefixCaseInsensitive(body[i:], "file://") {
				for i < n && !isURLTerminator(body[i]) {
					i++
				}
				continue
			}

			// Potential inline tag '#'
			if body[i] == '#' {
				// Check preceding character boundary
				validPreceding := false
				if i == 0 {
					validPreceding = true
				} else {
					prev := body[i-1]
					if prev == ' ' || prev == '\t' || prev == '\n' || prev == '\r' ||
						prev == '(' || prev == '[' || prev == '{' || prev == '<' ||
						prev == '"' || prev == '\'' || prev == '`' || prev == ',' ||
						prev == ';' || prev == '*' || prev == '_' || prev == '~' {
						validPreceding = true
					}
				}

				if !validPreceding {
					i++
					continue
				}

				// Check following character
				if i+1 >= n {
					i++
					continue
				}
				next := body[i+1]
				if next == ' ' || next == '\t' || next == '\r' || next == '\n' ||
					next == '#' || next == '/' || isPunctuation(next) {
					i++
					continue
				}

				// Scan tag characters
				tagStart := i + 1
				tagEnd := tagStart
				for tagEnd < n && isTagChar(body[tagEnd]) {
					tagEnd++
				}

				rawTag := body[tagStart:tagEnd]
				// If contains "//", truncate before "//"
				if idx := strings.Index(rawTag, "//"); idx != -1 {
					rawTag = rawTag[:idx]
				}
				// Trim trailing '/', '-', '_'
				rawTag = strings.TrimRight(rawTag, "/-_")

				if rawTag == "" {
					i++
					continue
				}

				// Check digits-only rule
				if isDigitsOnly(rawTag) {
					i++
					continue
				}

				// Valid tag found
				spanEnd := tagStart + len(rawTag)
				spans = append(spans, TagSpan{
					Start: i,
					End:   spanEnd,
					Tag:   rawTag,
				})
				i = spanEnd
				continue
			}

			i++
		}

		if i < n && body[i] == '\n' {
			i++
		}
	}

	return spans
}

// RewriteInlineTags replaces or removes inline tags in the markdown body using the mutate callback.
// If mutate returns keep == false, the tag is removed.
// If mutate returns a different tag, the tag is replaced with #newTag.
// Returns the modified body and true if any change was made, or the original body and false.
func RewriteInlineTags(body string, mutate func(tag string) (newTag string, keep bool)) (string, bool) {
	spans := FindInlineTagSpans(body)
	if len(spans) == 0 {
		return body, false
	}

	changed := false
	var buf strings.Builder
	lastEnd := 0

	for _, span := range spans {
		newTag, keep := mutate(span.Tag)
		if keep && newTag == span.Tag {
			continue
		}

		changed = true
		if keep {
			buf.WriteString(body[lastEnd:span.Start])
			buf.WriteString("#")
			buf.WriteString(newTag)
			lastEnd = span.End
		} else {
			cutStart := span.Start
			cutEnd := span.End

			if cutStart > 0 && body[cutStart-1] == ' ' && cutEnd < len(body) && body[cutEnd] == ' ' {
				cutStart-- // remove preceding space
			} else if cutStart > 0 && body[cutStart-1] == ' ' && (cutEnd == len(body) || body[cutEnd] == '\n' || body[cutEnd] == '\r') {
				cutStart-- // remove preceding space at line end
			} else if (cutStart == 0 || body[cutStart-1] == '\n') && cutEnd < len(body) && body[cutEnd] == ' ' {
				cutEnd++ // remove following space at line start
			}

			buf.WriteString(body[lastEnd:cutStart])
			lastEnd = cutEnd
		}
	}

	if !changed {
		return body, false
	}

	buf.WriteString(body[lastEnd:])
	return buf.String(), true
}

// RewriteDocumentTagsAndBody modifies frontmatter tags (if present) and inline tags in body,
// writing back to disk atomically if any changes occurred.
func RewriteDocumentTagsAndBody(
	filePath string,
	doc *Document,
	mutateFM func(fmTags []string) ([]string, bool),
	mutateBody func(body string) (string, bool),
) (bool, error) {
	data, err := os.ReadFile(filePath) //nolint:gosec // filePath is supplied by the vault walk
	if err != nil {
		return false, fmt.Errorf("read file: %w", err)
	}

	lineEnding := detectLineEnding(data)
	content := string(data)
	var lines []string
	if lineEnding == "\r\n" {
		lines = strings.Split(content, "\r\n")
	} else {
		lines = strings.Split(content, "\n")
	}

	fmStart, fmEnd := findFrontmatterBounds(lines)

	var newFmTags []string
	fmChanged := false

	if fmStart != -1 && fmEnd != -1 && doc.Frontmatter != nil {
		if tagsRaw, ok := doc.Frontmatter["tags"]; ok {
			var currentFMTags []string
			switch tv := tagsRaw.(type) {
			case []interface{}:
				for _, item := range tv {
					if s, ok := item.(string); ok {
						currentFMTags = append(currentFMTags, s)
					}
				}
			case string:
				currentFMTags = append(currentFMTags, tv)
			}
			if mutateFM != nil {
				newFmTags, fmChanged = mutateFM(currentFMTags)
			}
		}
	}

	var newBody string
	bodyChanged := false
	if mutateBody != nil {
		newBody, bodyChanged = mutateBody(doc.Body)
	}

	if !fmChanged && !bodyChanged {
		return false, nil
	}

	if fmChanged {
		valueStr, err := yamlValueString(newFmTags)
		if err != nil {
			return false, fmt.Errorf("marshal tags: %w", err)
		}
		keyPrefix := "tags: "
		replaced := false
		for i := fmStart + 1; i < fmEnd; i++ {
			trimmed := strings.TrimRight(lines[i], "\r")
			if strings.HasPrefix(trimmed, keyPrefix) || trimmed == "tags" {
				lines[i] = keyPrefix + valueStr
				replaced = true
				break
			}
		}
		if !replaced {
			insert := append([]string{keyPrefix + valueStr}, lines[fmEnd:]...)
			lines = append(lines[:fmEnd], insert...)
			fmEnd++
		}
	}

	var outputContent string
	if fmStart != -1 && fmEnd != -1 {
		fmLines := lines[:fmEnd+1]
		fmText := strings.Join(fmLines, lineEnding)
		if bodyChanged {
			outputContent = fmText + lineEnding + newBody
		} else {
			bodyLines := lines[fmEnd+1:]
			outputContent = fmText + lineEnding + strings.Join(bodyLines, lineEnding)
		}
	} else {
		if bodyChanged {
			outputContent = newBody
		} else {
			outputContent = content
		}
	}

	if err := writeFileAtomic(filePath, []byte(outputContent)); err != nil {
		return false, err
	}
	return true, nil
}

func isTagChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_' || b == '-' || b == '/'
}

func isPunctuation(b byte) bool {
	return b == '.' || b == ',' || b == ';' || b == ':' || b == '!' || b == '?' ||
		b == ')' || b == ']' || b == '}' || b == '>' || b == '"' || b == '\''
}

func isURLTerminator(b byte) bool {
	return b == ' ' || b == '\t' || b == '\r' || b == '\n' || b == '<' || b == '>' ||
		b == '"' || b == '\'' || b == ')' || b == ']'
}

func isDigitsOnly(s string) bool {
	hasNonDigit := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' || c == '-' {
			hasNonDigit = true
			break
		}
	}
	return !hasNonDigit
}

func hasPrefixCaseInsensitive(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	return strings.EqualFold(s[:len(prefix)], prefix)
}

func findClosingBackticks(s string, start int, count int) int {
	n := len(s)
	i := start
	for i < n {
		if s[i] == '\n' {
			return -1
		}
		if s[i] == '`' {
			btCount := 0
			btStart := i
			for i < n && s[i] == '`' {
				btCount++
				i++
			}
			if btCount == count {
				return btStart
			}
			continue
		}
		i++
	}
	return -1
}
