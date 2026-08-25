package engine

import (
	"strings"
	"unicode/utf8"
)

// DefaultSnippetBound is the default maximum number of characters (Unicode code
// points) in a query-focused snippet.
const DefaultSnippetBound = 300

// BuildSnippet creates a bounded, query-focused snippet from content. It finds
// the first case-insensitive match of any query term and returns a window up to
// bound characters around that match. When no query terms match, or when the
// content is shorter than bound, the full content (or its first bound
// characters) is returned as a deterministic fallback.
//
// The returned snippet preserves Unicode character boundaries so that offsets
// into the original content are never invalidated by mid-rune cuts.
func BuildSnippet(content string, queryTerms []string, bound int) string {
	if bound <= 0 {
		bound = DefaultSnippetBound
	}

	// Work with runes for Unicode-safe slicing.
	runes := []rune(content)
	runeLen := len(runes)

	if runeLen <= bound {
		return content
	}

	// Build non-empty, deduplicated query terms.
	terms := cleanupTerms(queryTerms)
	if len(terms) == 0 {
		return string(runes[:bound])
	}

	// Find the earliest byte-level match of any term in lowercased content.
	lowerContent := strings.ToLower(content)
	bestBytePos := -1
	for _, term := range terms {
		pos := strings.Index(lowerContent, strings.ToLower(term))
		if pos >= 0 && (bestBytePos < 0 || pos < bestBytePos) {
			bestBytePos = pos
		}
	}

	if bestBytePos < 0 {
		// No term found — deterministic fallback.
		return string(runes[:bound])
	}

	// Convert byte position to rune index so we can slice cleanly.
	bestRuneIdx := utf8.RuneCountInString(content[:bestBytePos])

	// Select a window around the match.
	half := bound / 2
	start := bestRuneIdx - half
	if start < 0 {
		start = 0
	}
	end := start + bound
	if end > runeLen {
		end = runeLen
		start = end - bound
		if start < 0 {
			start = 0
		}
	}

	return string(runes[start:end])
}

// cleanupTerms extracts individual, non-empty words from a set of query terms
// and removes duplicates.
func cleanupTerms(queryTerms []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, raw := range queryTerms {
		for _, word := range strings.Fields(raw) {
			word = strings.TrimSpace(word)
			if word != "" && !seen[word] {
				seen[word] = true
				out = append(out, word)
			}
		}
	}
	return out
}
