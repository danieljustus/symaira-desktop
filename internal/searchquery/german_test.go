package searchquery

import (
	"strings"
	"testing"
)

func TestGermanSearchTokensRemoveStopwordsAndStemInflections(t *testing.T) {
	got := GermanSearchTokens("Die Rechnungen sind geprüft")
	want := []string{"rechnung", "gepruft"}
	if len(got) != len(want) {
		t.Fatalf("tokens = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("tokens[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestGermanFTSQueryUsesSafePrefixAndAndExpressions(t *testing.T) {
	got := GermanFTSQuery("Die Rechnungen")
	if strings.Contains(got, "die") || !strings.Contains(got, `"rechnung"*`) {
		t.Fatalf("unexpected German FTS query: %q", got)
	}
}
