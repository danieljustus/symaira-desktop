package textnorm

import "testing"

func TestDehyphenateRejoinsLineEndHyphenation(t *testing.T) {
	got := Dehyphenate("Die Kranken-\nversicherung zahlt. Der Miet-\nvertrag läuft.")
	want := "Die Krankenversicherung zahlt. Der Mietvertrag läuft."
	if got != want {
		t.Fatalf("Dehyphenate() = %q, want %q", got, want)
	}
}

func TestDehyphenatePreservesGenuineHyphens(t *testing.T) {
	cases := map[string]string{
		// Capitalised continuations are names/compounds, not hyphenation.
		"Baden-\nWürttemberg":    "Baden-\nWürttemberg",
		"Arbeitgeber-\nAnteil":   "Arbeitgeber-\nAnteil",
		"E-Mail-Adresse":         "E-Mail-Adresse",
		// Spaced continuation: "Vor- und Nachname" construction.
		"Vor-\n und Nachname":    "Vor-\n und Nachname",
		"Vor-\nund Nachname":     "Vor-\nund Nachname",
		"Ein-\noder Ausgang":     "Ein-\noder Ausgang",
		// No newline at all: untouched fast path.
		"Kranken-versicherung":   "Kranken-versicherung",
		// Hyphen after a non-letter (enumeration dash) is not hyphenation.
		"1-\n2 Punkte":           "1-\n2 Punkte",
	}
	for in, want := range cases {
		if got := Dehyphenate(in); got != want {
			t.Errorf("Dehyphenate(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDehyphenateAcrossManyLines(t *testing.T) {
	got := Dehyphenate("Ver-\nsi-\nche-\nrung")
	if got != "Versicherung" {
		t.Fatalf("Dehyphenate() = %q, want %q", got, "Versicherung")
	}
}
