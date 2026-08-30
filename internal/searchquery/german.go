package searchquery

import (
	"strings"
	"unicode"
)

// GermanSearchTokens normalizes a whitespace-delimited query for the FTS
// indexes. German stopwords are removed and the remaining words are reduced to
// a conservative stem so inflected forms (for example Rechnung/Rechnungen)
// share a prefix. The result is deterministic and bounded by the input size.
func GermanSearchTokens(value string) []string {
	fields := strings.Fields(strings.ToLower(value))
	out := make([]string, 0, len(fields))
	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		token := trimToken(field)
		if token == "" || isGermanStopword(token) {
			continue
		}
		token = germanStem(token)
		if token == "" {
			continue
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		out = append(out, token)
	}
	return out
}

// GermanNormText builds the normalised token stream stored alongside the
// original text in the FTS norm indexes: German stopwords removed, remaining
// tokens stemmed and umlaut-folded, duplicates collapsed. Query-side
// GermanSearchTokens produces the same normalisation, so a stemmed query
// term prefix-matches the stemmed index form regardless of inflection.
func GermanNormText(text string) string {
	return strings.Join(GermanSearchTokens(text), " ")
}

// GermanFTSQuery converts free-form input into a safe FTS5 query for the
// prefix indexes. Every term is a stemmed prefix query; German stopwords are
// dropped. Callers must still combine this expression with SQL parameters;
// this function only returns FTS syntax.
func GermanFTSQuery(value string) string {
	parts := make([]string, 0)
	for _, field := range strings.Fields(value) {
		if expression := germanTermExpression(field); expression != "" {
			parts = append(parts, expression)
		}
	}
	return strings.Join(parts, " AND ")
}

// GermanFTSTerm returns the FTS expression for one parsed search term.
// Phrases retain their boundary while receiving the same stopword/stemming
// normalization as ordinary terms.
func GermanFTSTerm(value string, phrase bool) string {
	if phrase {
		tokens := GermanSearchTokens(value)
		if len(tokens) == 0 {
			return ""
		}
		return `"` + strings.ReplaceAll(strings.Join(tokens, " "), `"`, `""`) + `"`
	}
	return GermanFTSQuery(value)
}

func germanTermExpression(value string) string {
	token := trimToken(strings.ToLower(value))
	if token == "" || isGermanStopword(token) {
		return ""
	}
	whole := germanStem(token)
	if whole == "" {
		return ""
	}
	return ftsPrefix(whole)
}

func ftsPrefix(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"*`
}

func trimToken(value string) string {
	runes := []rune(value)
	start, end := 0, len(runes)
	for start < end && !unicode.IsLetter(runes[start]) && !unicode.IsDigit(runes[start]) {
		start++
	}
	for end > start && !unicode.IsLetter(runes[end-1]) && !unicode.IsDigit(runes[end-1]) {
		end--
	}
	return string(runes[start:end])
}

// foldUmlauts maps German umlauts and ß onto their ASCII base forms, the
// same folding the norm and trigram indexes apply at write time.
func foldUmlauts(value string) string {
	return strings.NewReplacer("ä", "a", "ö", "o", "ü", "u", "ß", "ss").Replace(value)
}

// germanStem is intentionally conservative. It handles common German plural,
// case and derivational endings without pretending to be a full linguistic
// analyzer; prefix matching remains the final recall mechanism.
func germanStem(value string) string {
	value = foldUmlauts(strings.ToLower(value))
	for _, suffix := range []string{"lichkeit", "igkeit", "heit", "keit", "isch", "lich", "ung", "end", "ern", "em", "er", "en", "es", "e", "s"} {
		if strings.HasSuffix(value, suffix) && len([]rune(value))-len([]rune(suffix)) >= 4 {
			return strings.TrimSuffix(value, suffix)
		}
	}
	return value
}

var germanStopwords = map[string]struct{}{
	"aber": {}, "alle": {}, "allem": {}, "allen": {}, "aller": {}, "alles": {},
	"als": {}, "also": {}, "am": {}, "an": {}, "auch": {}, "auf": {},
	"aus": {}, "bei": {}, "bin": {}, "bis": {}, "bist": {}, "da": {},
	"dabei": {}, "dadurch": {}, "dafür": {}, "daher": {}, "damit": {},
	"danach": {}, "dann": {}, "das": {}, "dass": {}, "davon": {}, "dazu": {},
	"dem": {}, "den": {}, "denn": {}, "der": {}, "des": {}, "die": {},
	"dies": {}, "diese": {}, "diesem": {}, "diesen": {}, "dieser": {}, "dieses": {},
	"doch": {}, "dort": {}, "du": {}, "durch": {}, "ein": {}, "eine": {},
	"einem": {}, "einen": {}, "einer": {}, "eines": {}, "einige": {},
	"er": {}, "es": {}, "für": {}, "gegen": {}, "hat": {}, "haben": {},
	"hier": {}, "ich": {}, "im": {}, "in": {}, "ist": {}, "ja": {},
	"jede": {}, "jedem": {}, "jeden": {}, "jeder": {}, "jedes": {}, "kein": {},
	"keine": {}, "mit": {}, "nach": {}, "nicht": {}, "noch": {}, "nur": {},
	"oder": {}, "ohne": {}, "sehr": {}, "sie": {}, "sind": {}, "so": {},
	"über": {}, "um": {}, "und": {}, "uns": {}, "unter": {}, "vom": {},
	"von": {}, "vor": {}, "war": {}, "waren": {}, "was": {}, "weil": {},
	"weiter": {}, "welche": {}, "wenn": {}, "wer": {}, "wie": {}, "wieder": {},
	"will": {}, "wir": {}, "wird": {}, "wo": {}, "zu": {}, "zum": {}, "zur": {},
}

func isGermanStopword(value string) bool {
	_, ok := germanStopwords[value]
	return ok
}
