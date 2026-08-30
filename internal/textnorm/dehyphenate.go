// Package textnorm provides deterministic, language-aware text
// normalisation shared by the ingest extraction pipeline and the retrieval
// parser. It lives outside both facades so neither has to import the other
// module's internals.
package textnorm

import (
	"strings"
	"unicode"
)

// continuationWords are lowercase words that legitimately follow a genuine
// hyphen at a line break (German "Vor- und Nachname" style constructions).
// A trailing hyphen before them is never a hyphenation artifact.
var continuationWords = map[string]struct{}{
	"und": {}, "oder": {}, "bis": {}, "wie": {}, "als": {},
}

// Dehyphenate rejoins words that were broken by hyphenation at a line end,
// while preserving genuine hyphens in compounds and names.
//
// A line-final hyphen is treated as a hyphenation artifact only when all of
// the following hold:
//   - the character before the hyphen is a letter (not a dash or digit),
//   - the next line carries no leading whitespace (a real list item or
//     "Vor- und Nachname"-style continuation is indented or spaced),
//   - the next line starts with a lowercase letter (German hyphenation
//     continues mid-word, hence lowercase; names and compounds such as
//     "Baden-\nWürttemberg" or "Arbeitgeber-\nAnteil" continue uppercase),
//   - the first word of the continuation is not a coordinator such as
//     "und"/"oder" (the "Vor- und Nachname" exception).
func Dehyphenate(s string) string {
	if !strings.ContainsRune(s, '\n') {
		return s
	}
	lines := strings.Split(s, "\n")
	for i := 0; i+1 < len(lines); {
		left := strings.TrimRight(lines[i], " \t")
		right := lines[i+1]
		if strings.HasSuffix(left, "-") && right == strings.TrimLeft(right, " \t") && startsLowercaseLetter(right) && !startsWithContinuationWord(right) && letterBeforeHyphen(left) {
			lines[i] = strings.TrimSuffix(left, "-") + right
			lines = append(lines[:i+1], lines[i+2:]...)
			continue
		}
		i++
	}
	return strings.Join(lines, "\n")
}

func startsLowercaseLetter(s string) bool {
	for _, r := range s {
		return unicode.IsLower(r)
	}
	return false
}

func letterBeforeHyphen(s string) bool {
	runes := []rune(strings.TrimSuffix(s, "-"))
	if len(runes) == 0 {
		return false
	}
	return unicode.IsLetter(runes[len(runes)-1])
}

func startsWithContinuationWord(s string) bool {
	word := strings.ToLower(firstWord(s))
	_, ok := continuationWords[word]
	return ok
}

func firstWord(s string) string {
	for i, r := range s {
		if !unicode.IsLetter(r) {
			return s[:i]
		}
	}
	return s
}
