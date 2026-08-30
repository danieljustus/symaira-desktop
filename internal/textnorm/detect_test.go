package textnorm

import "testing"

func TestDetectLanguageGerman(t *testing.T) {
	text := "Der Vertrag wurde zwischen den Parteien geschlossen und ist gültig. " +
		"Die Beiträge für die Krankenversicherung werden monatlich gezahlt. " +
		"Wir können die Änderung nicht übernehmen, wenn die Frist schon abgelaufen ist."
	if got := DetectLanguage(text); got != LangGerman {
		t.Fatalf("DetectLanguage() = %q, want %q", got, LangGerman)
	}
}

func TestDetectLanguageEnglish(t *testing.T) {
	text := "The contract was signed between the parties and is valid. " +
		"The contributions for the insurance are paid monthly and this will not change. " +
		"We cannot accept the modification when the deadline has already passed."
	if got := DetectLanguage(text); got != LangEnglish {
		t.Fatalf("DetectLanguage() = %q, want %q", got, LangEnglish)
	}
}

func TestDetectLanguageThinEvidence(t *testing.T) {
	if got := DetectLanguage("kurz"); got != "" {
		t.Fatalf("DetectLanguage(short) = %q, want empty", got)
	}
}

func TestDetectLanguageAmbiguous(t *testing.T) {
	// Mixed text without a clear majority must not trigger a re-route.
	text := "der the und and die is das are und the der is und the der is und the der is"
	if got := DetectLanguage(text); got != "" {
		t.Fatalf("DetectLanguage(ambiguous) = %q, want empty", got)
	}
}
