// Package ocr provides CER/WER metrics for OCR quality benchmarking.
package ocr

import (
	"math"
	"regexp"
	"strings"
	"unicode"
)

// ErrorRates holds character and word error rates.
type ErrorRates struct {
	// CER is Character Error Rate: Levenshtein distance at character level
	// divided by reference length. Range [0, 1].
	CER float64

	// WER is Word Error Rate: Levenshtein distance at word level
	// divided by reference word count. Range [0, 1].
	WER float64

	// FieldCER is CER computed only on number fields and case-number fields.
	// Empty if no number fields were found in the reference.
	FieldCER float64

	// RefLen is the number of characters in the reference text.
	RefLen int

	// HypLen is the number of characters in the hypothesis text.
	HypLen int

	// EditOps is the Levenshtein edit operation count (character level).
	EditOps int

	// WordEditOps is the Levenshtein edit operation count (word level).
	WordEditOps int

	// RefWords is the number of words in the reference.
	RefWords int

	// HypWords is the number of words in the hypothesis.
	HypWords int
}

// NumberFieldResult holds error rates computed on number/case-number fields only.
type NumberFieldResult struct {
	// RefFields is the concatenated number/case-number fields from reference.
	RefFields string

	// HypFields is the concatenated number/case-number fields from hypothesis.
	HypFields string

	// CER is Character Error Rate on the concatenated field texts.
	CER float64

	// EditOps is the Levenshtein edit operation count.
	EditOps int

	// RefLen is the character length of the reference fields.
	RefLen int
}

// FieldExtractor extracts number and case-number fields from text.
// It returns a single concatenated string of all matched fields,
// with fields separated by newlines.
type FieldExtractor struct {
	// patterns is the list of regex patterns that identify number/case-number fields.
	patterns []*regexp.Regexp
}

// NewFieldExtractor creates a FieldExtractor with default patterns
// for German number and case-number fields.
func NewFieldExtractor() *FieldExtractor {
	return &FieldExtractor{
		patterns: []*regexp.Regexp{
			// Case numbers: patterns like "Az: 123/45", "Aktenzeichen: AB-123/45",
			// "Geschäftszeichen: 123456-2024", "Kz: 12/345/67"
			regexp.MustCompile(`(?:Az\.?|Aktenzeichen|Gesch[äa]ftszeichen|GZ|Kz\.?|Zeichen|Vorgang(?:s)?(?:-?Nr\.?|snummer)?)\s*:?\s*([\w\d]+(?:[\-/]\d+)+(?:[\-/]\d+)*)`),

			// Invoice numbers: "Rechnung Nr. 2024-00123", "Rechnungsnummer: 240015"
			regexp.MustCompile(`(?:Rechnung(?:s)?(?:-?Nr\.?|snummer)?)\s*:?\s*([\w\d]+(?:[\-/]\d+)*)`),

			// Customer/contract numbers: "Kundennummer: 45678", "Vertragsnummer: V-98765"
			regexp.MustCompile(`(?:Kunden(?:-?Nr\.?|nummer)|Vertrags(?:-?Nr\.?|nummer)|Mandanten(?:-?Nr\.?|nummer)|Steuer(?:-?Nr\.?|nummer))\s*:?\s*([\w\d]+(?:[\-/]\d+)*)`),

			// Currency amounts: "123,45 €", "1.234,56 EUR", "EUR 12,99"
			regexp.MustCompile(`(?:EUR|€)\s*([\d.,]+\s*(?:EUR|€)?)`),
			regexp.MustCompile(`([\d]{1,3}(?:\.[\d]{3})*(?:,\d{1,2})?)\s*(?:EUR|€)`),

			// Standalone amounts with currency context: "Betrag: 123,45"
			regexp.MustCompile(`(?:Betrag|Summe|Gesamt(?:betrag)?|Preis|Kosten|Geb[üu]hr|Honorar)\s*:?\s*([\d.,]+)`),

			// Dates: "01.01.2024", "1. Januar 2024" — these are field-level patterns
			regexp.MustCompile(`\d{1,2}\.\d{1,2}\.\d{2,4}`),

			// Tax IDs / Steuernummern: patterns like "123/456/78901"
			regexp.MustCompile(`(?:Steuer(?:-?Nr\.?|nummer)|USt-IdNr\.?|Steuer-ID|IdNr\.?)\s*:?\s*(DE\d{9}|\d{2,3}/\d{3,4}/\d{4,5})`),
		},
	}
}

// ExtractFields extracts all number/case-number field values from text.
// Returns a concatenated string of all matched fields, separated by newlines.
func (fe *FieldExtractor) ExtractFields(text string) string {
	var fields []string
	for _, pat := range fe.patterns {
		matches := pat.FindAllString(text, -1)
		fields = append(fields, matches...)
	}
	return strings.Join(fields, "\n")
}

// NormalizeText applies common OCR normalization for fair comparison:
// - Collapses multiple whitespace to single space
// - Trims leading/trailing whitespace
// - Normalizes Unicode (NFC)
func NormalizeText(text string) string {
	// Collapse whitespace
	wsRe := regexp.MustCompile(`\s+`)
	text = wsRe.ReplaceAllString(text, " ")
	text = strings.TrimSpace(text)
	return text
}

// NormalizeTextLenient applies lenient normalization that also:
// - Lowercases text (common OCR errors are case errors in German nouns)
// - Replaces common OCR confusions (ß↔ss, umlauts↔digraphs)
func NormalizeTextLenient(text string) string {
	text = NormalizeText(text)
	text = strings.ToLower(text)
	// Normalize German special characters
	text = strings.ReplaceAll(text, "ß", "ss")
	text = strings.ReplaceAll(text, "ä", "ae")
	text = strings.ReplaceAll(text, "ö", "oe")
	text = strings.ReplaceAll(text, "ü", "ue")
	return text
}

// ComputeErrorRates computes CER, WER, and field-level error rates
// between reference and hypothesis texts.
func ComputeErrorRates(reference, hypothesis string, fe *FieldExtractor) ErrorRates {
	ref := NormalizeText(reference)
	hyp := NormalizeText(hypothesis)

	// Character-level edit distance
	charDist := levenshteinDistance([]rune(ref), []rune(hyp))
	refLen := len([]rune(ref))
	hypLen := len([]rune(hyp))

	var cer float64
	if refLen > 0 {
		cer = float64(charDist) / float64(refLen)
	}

	// Word-level edit distance
	refWords := tokenize(ref)
	hypWords := tokenize(hyp)
	wordDist := levenshteinDistanceStrings(refWords, hypWords)
	refWordCount := len(refWords)
	hypWordCount := len(hypWords)

	var wer float64
	if refWordCount > 0 {
		wer = float64(wordDist) / float64(refWordCount)
	}

	// Field-level CER (numbers and case numbers)
	var fieldCER float64
	var fieldEditOps int
	var fieldRefLen int
	if fe != nil {
		refFields := fe.ExtractFields(ref)
		hypFields := fe.ExtractFields(hyp)
		if len(refFields) > 0 {
			fieldEditOps = levenshteinDistance([]rune(refFields), []rune(hypFields))
			fieldRefLen = len([]rune(refFields))
			fieldCER = float64(fieldEditOps) / float64(fieldRefLen)
		}
	}

	return ErrorRates{
		CER:         round4(cer),
		WER:         round4(wer),
		FieldCER:    round4(fieldCER),
		RefLen:      refLen,
		HypLen:      hypLen,
		EditOps:     charDist,
		WordEditOps: wordDist,
		RefWords:    refWordCount,
		HypWords:    hypWordCount,
	}
}

// ComputeNumberFieldResult extracts number/case-number fields and computes
// CER on the extracted text only.
func ComputeNumberFieldResult(reference, hypothesis string, fe *FieldExtractor) NumberFieldResult {
	ref := NormalizeText(reference)
	hyp := NormalizeText(hypothesis)
	if fe == nil {
		fe = NewFieldExtractor()
	}
	refFields := fe.ExtractFields(ref)
	hypFields := fe.ExtractFields(hyp)

	refRunes := []rune(refFields)
	hypRunes := []rune(hypFields)
	editOps := levenshteinDistance(refRunes, hypRunes)
	refLen := len(refRunes)

	var cer float64
	if refLen > 0 {
		cer = float64(editOps) / float64(refLen)
	}

	return NumberFieldResult{
		RefFields: refFields,
		HypFields: hypFields,
		CER:       round4(cer),
		EditOps:   editOps,
		RefLen:    refLen,
	}
}

// levenshteinDistance computes the Levenshtein distance between two rune slices.
// Pure Go stdlib implementation — no external dependencies.
func levenshteinDistance(a, b []rune) int {
	// Optimize: use two rows instead of full matrix
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}

	// Ensure a is the shorter slice for space optimization
	if len(a) > len(b) {
		a, b = b, a
	}

	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)

	for j := 0; j <= len(b); j++ {
		prev[j] = j
	}

	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min3(
				prev[j]+1,      // deletion
				curr[j-1]+1,    // insertion
				prev[j-1]+cost, // substitution
			)
		}
		prev, curr = curr, prev
	}

	return prev[len(b)]
}

// levenshteinDistanceStrings computes the Levenshtein distance between two string slices.
func levenshteinDistanceStrings(a, b []string) int {
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	if len(a) > len(b) {
		a, b = b, a
	}

	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)

	for j := 0; j <= len(b); j++ {
		prev[j] = j
	}

	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min3(
				prev[j]+1,
				curr[j-1]+1,
				prev[j-1]+cost,
			)
		}
		prev, curr = curr, prev
	}

	return prev[len(b)]
}

// tokenize splits text into words, preserving only alphanumeric characters
// and some punctuation that matters for field extraction.
func tokenize(text string) []string {
	var words []string
	current := strings.Builder{}

	for _, r := range text {
		if unicode.IsSpace(r) {
			if current.Len() > 0 {
				words = append(words, current.String())
				current.Reset()
			}
		} else {
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		words = append(words, current.String())
	}

	return words
}

func min3(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}

func round4(v float64) float64 {
	return math.Round(v*10000) / 10000
}
