package textnorm

import (
	"strings"
	"unicode"
)

// Detection language codes follow Tesseract's traineddata names so callers
// can pass the result straight through to the OCR layer.
const (
	LangGerman  = "deu"
	LangEnglish = "eng"
)

// germanSignalWords are frequent German function words matched as whole
// words. German-specific characters (ä ö ü ß) contribute additional weight.
var germanSignalWords = []string{
	"der", "die", "das", "und", "ist", "nicht", "mit", "für", "auf",
	"eine", "einer", "einem", "einen", "werden", "wurde", "sie", "wir",
	"ich", "dem", "den", "des", "im", "am", "zu", "von", "sich", "auch",
	"oder", "aber", "wenn", "dann", "noch", "nur", "schon", "kann",
	"muss", "soll", "werden", "zwischen", "gegen", "ohne", "über",
}

// englishSignalWords are frequent English function words matched as whole
// words, used to keep an English document from being misrouted when it
// happens to contain a German-looking token.
var englishSignalWords = []string{
	"the", "and", "is", "are", "was", "were", "of", "to", "in", "for",
	"with", "not", "this", "that", "these", "those", "you", "your", "we",
	"they", "their", "from", "have", "has", "had", "will", "would", "can",
	"could", "should", "between", "without", "about", "which", "when",
}

// minDetectWords is the minimum number of words a text must contain before
// detection is attempted at all; below that the signal is noise.
const minDetectWords = 20

// minGermanMargin is the lead the German score must have over the English
// score before a document is reported as German.
const minGermanMargin = 3

// DetectLanguage reports the likely language of a document's text as a
// Tesseract language code. It returns LangGerman when the German signal is
// clear, LangEnglish when the English signal clearly dominates, and "" when
// the evidence is too thin or ambiguous to act on — callers must keep their
// configured default in that case. The check is deterministic and linear in
// the input size.
func DetectLanguage(text string) string {
	words := wordsOf(text)
	if len(words) < minDetectWords {
		return ""
	}
	german, english := 0, 0
	for _, w := range words {
		if containsAny(w, germanSignalWords) {
			german++
		}
		if containsAny(w, englishSignalWords) {
			english++
		}
	}
	// Umlauts and ß are strong evidence: they essentially do not occur in
	// ordinary English prose.
	for _, r := range text {
		switch r {
		case 'ä', 'ö', 'ü', 'Ä', 'Ö', 'Ü', 'ß':
			german += 3
		}
	}
	if german >= english+minGermanMargin && german >= 5 {
		return LangGerman
	}
	if english > german+minGermanMargin {
		return LangEnglish
	}
	return ""
}

func wordsOf(text string) []string {
	words := make([]string, 0, 256)
	var b strings.Builder
	flush := func() {
		if b.Len() > 0 {
			words = append(words, b.String())
			b.Reset()
		}
	}
	for _, r := range text {
		if unicode.IsLetter(r) {
			b.WriteRune(unicode.ToLower(r))
		} else {
			flush()
		}
	}
	flush()
	return words
}

func containsAny(word string, set []string) bool {
	for _, candidate := range set {
		if word == candidate {
			return true
		}
	}
	return false
}
