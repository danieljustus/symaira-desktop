package ingest

import (
	"strings"
	"unicode"
)

const (
	// maxPlausibleWords is deliberately generous. Calibration on the repository's
	// OCR fixture (testdata/vault/symingest-sample.md) found 72 words with a
	// unique-word ratio of 0.736. The 10,000-word ceiling leaves more than two
	// orders of magnitude for legitimate long pages while sitting well below the
	// 119,165-word repetition incident documented in issue #316.
	maxPlausibleWords = 10_000

	// minRatioWords prevents short receipts and headings from being judged by a
	// statistic that is not meaningful at that size.
	minRatioWords = 100

	// minUniqueWordRatio is low enough for repeated labels in dense tables while
	// still rejecting a model that loops over the same token.
	minUniqueWordRatio = 0.10

	// guardFallbackWordLimit bounds the persisted result after a failed retry.
	guardFallbackWordLimit = 100
)

// Verdict describes the plausibility inspection of an OCR result.
type Verdict struct {
	OK          bool
	Reason      string
	WordCount   int
	UniqueRatio float64
}

// InspectText checks OCR output for implausibly large or degenerate text.
// It is pure and intentionally does not attempt to judge OCR accuracy.
func InspectText(text string) Verdict {
	words := normalizedWords(text)
	verdict := Verdict{OK: true, WordCount: len(words)}
	if len(words) == 0 {
		verdict.Reason = "empty text is not rejected"
		return verdict
	}
	if len(words) > maxPlausibleWords {
		verdict.OK = false
		verdict.Reason = "word count exceeds plausibility limit"
		return verdict
	}
	verdict.UniqueRatio = float64(uniqueCount(words)) / float64(len(words))
	if len(words) >= minRatioWords && verdict.UniqueRatio < minUniqueWordRatio {
		verdict.OK = false
		verdict.Reason = "unique-word ratio is below plausibility limit"
	}
	return verdict
}

// TruncateText keeps a bounded prefix for a result that failed both checks.
// The function preserves the original text when it is already short enough.
func TruncateText(text string, maxWords int) string {
	if maxWords <= 0 {
		return ""
	}
	words := strings.Fields(text)
	if len(words) <= maxWords {
		return text
	}
	return strings.Join(words[:maxWords], " ")
}

// GuardFallbackWordLimit is the maximum number of words persisted after both
// plausibility attempts fail.
const GuardFallbackWordLimit = guardFallbackWordLimit

func normalizedWords(text string) []string {
	fields := strings.Fields(text)
	words := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimFunc(field, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsNumber(r)
		})
		if field != "" {
			words = append(words, strings.ToLower(field))
		}
	}
	return words
}

func uniqueCount(words []string) int {
	seen := make(map[string]struct{}, len(words))
	for _, word := range words {
		seen[word] = struct{}{}
	}
	return len(seen)
}
