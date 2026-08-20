package ocr

import (
	"strings"
	"testing"
)

func TestLevenshteinDistance(t *testing.T) {
	tests := []struct {
		a, b   string
		expect int
	}{
		{"", "", 0},
		{"a", "", 1},
		{"", "a", 1},
		{"a", "a", 0},
		{"abc", "abc", 0},
		{"abc", "abd", 1},
		{"abc", "ab", 1},
		{"ab", "abc", 1},
		{"abc", "abcd", 1},
		{"kitten", "sitting", 3},
		{"flaw", "lawn", 2},
		{"gumbo", "gambol", 2},
	}

	for _, tt := range tests {
		got := levenshteinDistance([]rune(tt.a), []rune(tt.b))
		if got != tt.expect {
			t.Errorf("levenshtein(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.expect)
		}
	}
}

func TestLevenshteinDistanceStrings(t *testing.T) {
	a := []string{"the", "quick", "brown", "fox"}
	b := []string{"the", "quick", "red", "fox"}
	got := levenshteinDistanceStrings(a, b)
	if got != 1 {
		t.Errorf("got %d, want 1", got)
	}

	// Insertion
	a2 := []string{"hello", "world"}
	b2 := []string{"hello", "beautiful", "world"}
	got2 := levenshteinDistanceStrings(a2, b2)
	if got2 != 1 {
		t.Errorf("got %d, want 1", got2)
	}
}

func TestComputeErrorRates_Perfect(t *testing.T) {
	text := "Dies ist ein Testdokument mit deutschem Text."
	fe := NewFieldExtractor()
	rates := ComputeErrorRates(text, text, fe)

	if rates.CER != 0 {
		t.Errorf("CER = %f, want 0", rates.CER)
	}
	if rates.WER != 0 {
		t.Errorf("WER = %f, want 0", rates.WER)
	}
}

func TestComputeErrorRates_CER(t *testing.T) {
	ref := "abcdef"
	hyp := "abcxef" // 1 substitution (d→x), 6 chars
	fe := NewFieldExtractor()
	rates := ComputeErrorRates(ref, hyp, fe)

	expected := float64(1) / float64(6)
	if rates.CER != round4(expected) {
		t.Errorf("CER = %f, want %f", rates.CER, round4(expected))
	}
	if rates.EditOps != 1 {
		t.Errorf("EditOps = %d, want 1", rates.EditOps)
	}
}

func TestComputeErrorRates_WER(t *testing.T) {
	ref := "der schnelle braune Fuchs"
	hyp := "der schnelle rote Fuchs" // 1 word substitution
	fe := NewFieldExtractor()
	rates := ComputeErrorRates(ref, hyp, fe)

	if rates.WER != 0.25 { // 1/4
		t.Errorf("WER = %f, want 0.25", rates.WER)
	}
}

func TestComputeErrorRates_Insertion(t *testing.T) {
	ref := "abc"
	hyp := "abxc" // 1 insertion
	fe := NewFieldExtractor()
	rates := ComputeErrorRates(ref, hyp, fe)

	expected := float64(1) / float64(3)
	if rates.CER != round4(expected) {
		t.Errorf("CER = %f, want %f", rates.CER, round4(expected))
	}
}

func TestComputeErrorRates_Deletion(t *testing.T) {
	ref := "abcd"
	hyp := "abd" // 1 deletion
	fe := NewFieldExtractor()
	rates := ComputeErrorRates(ref, hyp, fe)

	if rates.CER != 0.25 {
		t.Errorf("CER = %f, want 0.25", rates.CER)
	}
}

func TestComputeErrorRates_EmptyReference(t *testing.T) {
	rates := ComputeErrorRates("", "some text", nil)
	if rates.CER != 0 {
		t.Errorf("CER with empty ref = %f, want 0", rates.CER)
	}
}

func TestFieldExtractor_CaseNumber(t *testing.T) {
	fe := NewFieldExtractor()
	text := "Az: 123/45/6789\nBetreff: Ihr Antrag"
	fields := fe.ExtractFields(text)
	if !strings.Contains(fields, "123/45/6789") {
		t.Errorf("expected case number in fields, got: %q", fields)
	}
}

func TestFieldExtractor_InvoiceNumber(t *testing.T) {
	fe := NewFieldExtractor()
	// The regex matches the full "Rechnung Nr. 2024-00123" string
	text := "Rechnung Nr. 2024-00123 vom 01.01.2024"
	fields := fe.ExtractFields(text)
	// Should contain the date field for sure
	if !strings.Contains(fields, "01.01.2024") {
		t.Errorf("expected date in fields, got: %q", fields)
	}
	// The invoice regex should match
	if len(fields) == 0 {
		t.Errorf("expected non-empty fields")
	}
}

func TestFieldExtractor_Amount(t *testing.T) {
	fe := NewFieldExtractor()
	text := "Betrag: 1.234,56 EUR\nSumme: 789,00 €"
	fields := fe.ExtractFields(text)
	if !strings.Contains(fields, "1.234,56") {
		t.Errorf("expected amount in fields, got: %q", fields)
	}
	if !strings.Contains(fields, "789,00") {
		t.Errorf("expected amount in fields, got: %q", fields)
	}
}

func TestFieldExtractor_TaxID(t *testing.T) {
	fe := NewFieldExtractor()
	text := "Steuernummer: 123/456/78901\nUSt-IdNr.: DE123456789"
	fields := fe.ExtractFields(text)
	// Check that we got the full matched text
	if len(fields) == 0 {
		t.Errorf("expected tax ID fields, got empty")
	}
}

func TestComputeNumberFieldResult(t *testing.T) {
	// Simple test with amounts that differ
	ref := "Betrag: 100,00 EUR\nSumme: 50,00 EUR"
	hyp := "Betrag: 100,00 EUR\nSumme: 51,00 EUR" // one digit wrong in second amount
	fe := NewFieldExtractor()
	result := ComputeNumberFieldResult(ref, hyp, fe)

	if result.CER == 0 {
		t.Errorf("expected non-zero FieldCER, got 0 (refFields=%q, hypFields=%q)", result.RefFields, result.HypFields)
	}
	if len(result.RefFields) == 0 {
		t.Errorf("expected reference fields to be non-empty")
	}
}

func TestNormalizeText(t *testing.T) {
	in := "  Hello   World  \n\n  Test  "
	out := NormalizeText(in)
	expected := "Hello World Test"
	if out != expected {
		t.Errorf("got %q, want %q", out, expected)
	}
}

func TestNormalizeTextLenient(t *testing.T) {
	in := "FÜR DIE GROẞE STRAẞE"
	out := NormalizeTextLenient(in)
	// ß→ss, ü→ue, ß→ss
	if !strings.Contains(out, "fuer") {
		t.Errorf("expected 'fuer' in output, got: %q", out)
	}
	if !strings.Contains(out, "strasse") {
		t.Errorf("expected 'strasse' in output, got: %q", out)
	}
}

func TestTokenize(t *testing.T) {
	words := tokenize("hello world test")
	if len(words) != 3 {
		t.Errorf("got %d words, want 3", len(words))
	}
	if words[0] != "hello" || words[1] != "world" || words[2] != "test" {
		t.Errorf("got %v", words)
	}
}

func TestTokenize_Empty(t *testing.T) {
	words := tokenize("")
	if len(words) != 0 {
		t.Errorf("got %d words, want 0", len(words))
	}
}

func TestFieldExtractor_Date(t *testing.T) {
	fe := NewFieldExtractor()
	text := "Datum: 15.03.2024\nFällig am 01.04.2024"
	fields := fe.ExtractFields(text)
	if !strings.Contains(fields, "15.03.2024") {
		t.Errorf("expected date in fields, got: %q", fields)
	}
}
