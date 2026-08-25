package annotate

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const maxSnippetLen = 120

// regexPatterns maps field types to compiled regular expressions.
var regexPatterns = map[string]*regexp.Regexp{
	// ISO date (YYYY-MM-DD) and common date formats
	"date": regexp.MustCompile(`\b\d{4}[-/\.]\d{1,2}[-/\.]\d{1,2}\b|\b\d{1,2}[-/\.]\d{1,2}[-/\.]\d{4}\b|\b\d{1,2}\s+(?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)[a-z]*\s+\d{4}\b`),

	// Monetary amounts: $1,234.56 / €1.234,56 / 1234.56 USD / EUR 1234
	"amount": regexp.MustCompile(`(?:[$€£¥]\s*[\d.,]+|[\d.,]+\s*(?:USD|EUR|GBP|JPY|CAD|AUD|CHF))\b`),

	// Email
	"email": regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`),

	// URL
	"url": regexp.MustCompile(`\bhttps?://[^\s<>")\]]+`),

	// IBAN (simplified pattern for common formats)
	"iban": regexp.MustCompile(`\b[A-Z]{2}\d{2}[A-Z0-9]{4,30}\b`),

	// Phone (international and US formats)
	"phone": regexp.MustCompile(`(?:\+\d{1,3}[-.\s]?)?\(?\d{2,4}\)?[-.\s]?\d{3,4}[-.\s]?\d{3,4}\b`),

	// Invoice number patterns: INV-123, Invoice #123, Rechnung Nr. 123, etc.
	"invoice_number": regexp.MustCompile(`(?i)\b(?:INV(?:OICE)?[\s#:\-]*\d+|Rechnung\s+(?:Nr\.?|No\.?|Nummer)\s*\d+|Faktura\s+#?\d+)\b`),

	// Job/case ID: Case #12345, Akte Nr. 12345, Fallnummer 12345, etc.
	"job_id": regexp.MustCompile(`(?i)\b(?:Case\s*#?\d+|Akte\s+(?:Nr\.?|Nummer)\s*\d+|Fallnummer\s*\d+|Aktenzeichen\s*\d+|Job\s*#?\d+|Vorgang\s*#?\d+)\b`),

	// Organization (heuristic: "GmbH", "Inc", "LLC", "AG", "S.A.", "Ltd", etc.)
	"organization": regexp.MustCompile(`\b[A-Z][A-Za-z\s&.-]{2,50}\s+(?:GmbH|Inc\.?|LLC|AG|S\.A\.|Ltd\.?|Corp\.?|Company|Co\.|e\.V\.|Stiftung|Organisation|Organization)\b`),

	// Party (name-like patterns near "Party", "between", "Contractor", "Client")
	"party": regexp.MustCompile(`(?i)\b(?:Party|Between|Contractor|Client|Lieferant|Auftraggeber|Auftragnehmer)\s*[:;]?\s+[A-Z][A-Za-z\s&.,-]{2,60}`),

	// Deadline
	"deadline": regexp.MustCompile(`(?i)\b(?:deadline|due\s+date|Frist|Termin|fälligkeit)\s*[:;]?\s*\d{1,2}[-/\.]\d{1,2}[-/\.]\d{4}\b`),
}

// Extract applies the given profile's regex rules to text and returns a slice
// of Extractions with spans and snippets. Unmatched candidates are omitted.
func Extract(profile Profile, text string) []Extraction {
	fields := profile.Fields()
	fieldSet := make(map[string]bool, len(fields))
	for _, f := range fields {
		fieldSet[f.Name] = true
	}

	seen := make(map[string]bool) // deduplicate per field+value
	var result []Extraction

	for _, f := range fields {
		pattern, ok := regexPatterns[f.Name]
		if !ok {
			continue
		}
		matches := pattern.FindAllStringIndex(text, -1)
		for _, loc := range matches {
			start, end := loc[0], loc[1]
			value := strings.TrimSpace(text[start:end])

			// Deduplicate: same field + same normalized value
			key := f.Name + "|" + strings.ToLower(value)
			if seen[key] {
				continue
			}
			seen[key] = true

			snippet := extractSnippet(text, start, end)
			result = append(result, Extraction{
				Field:   f.Name,
				Type:    f.Type,
				Value:   value,
				Span:    &Span{Start: start, End: end, Snippet: snippet},
				Matched: true,
			})
		}
	}

	// Sort by span start offset for deterministic output
	sort.Slice(result, func(i, j int) bool {
		if result[i].Span == nil {
			return true
		}
		if result[j].Span == nil {
			return false
		}
		return result[i].Span.Start < result[j].Span.Start
	})

	return result
}

// extractSnippet returns a bounded snippet of the original text around a match.
// The snippet includes up to maxSnippetLen characters centered on the match.
func extractSnippet(text string, start, end int) string {
	runeText := []rune(text)
	byteToRune := make([]int, len(runeText)+1)
	runeToByte := make([]int, len(runeText)+1)
	byteIdx := 0
	for i, r := range runeText {
		byteToRune[i] = byteIdx
		runeToByte[i] = byteIdx
		byteIdx += len(string(r))
	}
	runeToByte[len(runeText)] = byteIdx
	byteToRune[len(runeText)] = byteIdx

	// Convert byte offsets to rune offsets
	runeStart := 0
	for i := range runeText {
		if runeToByte[i] >= start {
			runeStart = i
			break
		}
	}
	runeEnd := len(runeText)
	for i := range runeText {
		if runeToByte[i] >= end {
			runeEnd = i + 1
			break
		}
	}

	matchLen := runeEnd - runeStart
	snippetRunes := maxSnippetLen
	if matchLen > snippetRunes {
		snippetRunes = matchLen
	}

	// Center the snippet around the match
	halfWindow := (snippetRunes - matchLen) / 2
	snippetStart := runeStart - halfWindow
	if snippetStart < 0 {
		snippetStart = 0
	}
	snippetEnd := snippetStart + snippetRunes
	if snippetEnd > len(runeText) {
		snippetEnd = len(runeText)
		snippetStart = snippetEnd - snippetRunes
		if snippetStart < 0 {
			snippetStart = 0
		}
	}

	snippet := string(runeText[snippetStart:snippetEnd])

	// Add ellipsis indicators
	if snippetStart > 0 {
		snippet = "..." + snippet
	}
	if snippetEnd < len(runeText) {
		snippet = snippet + "..."
	}

	return fmt.Sprintf("%q", snippet)
}
