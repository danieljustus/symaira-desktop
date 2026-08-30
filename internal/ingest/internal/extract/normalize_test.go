package extract

import "testing"

func TestNormalizeTextRejoinsLineEndHyphenation(t *testing.T) {
	got := NormalizeText("Die Kranken-\nversicherung zahlt. Der Miet-\nvertrag läuft.")
	want := "Die Krankenversicherung zahlt. Der Mietvertrag läuft."
	if got != want {
		t.Fatalf("NormalizeText() = %q, want %q", got, want)
	}
}

func TestNormalizeTextPreservesGenuineHyphens(t *testing.T) {
	got := NormalizeText("E-Mail\nBaden-\nWürttemberg\nVor-\n und Nachname")
	want := "E-Mail\nBaden-\nWürttemberg\nVor-\nund Nachname"
	if got != want {
		t.Fatalf("NormalizeText() = %q, want %q", got, want)
	}
}
