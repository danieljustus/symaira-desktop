package ingest

import (
	"fmt"
	"strings"
	"testing"
)

func TestInspectTextHealthyProse(t *testing.T) {
	var sentences []string
	for i := 0; i < 30; i++ {
		sentences = append(sentences, fmt.Sprintf("The invoice records customer%d reference%d payment%d date%d and approval%d for account%d.", i, i, i, i, i, i))
	}
	verdict := InspectText(strings.Join(sentences, " "))
	if !verdict.OK {
		t.Fatalf("healthy prose rejected: %+v", verdict)
	}
	if verdict.WordCount < minRatioWords {
		t.Fatalf("WordCount = %d, want at least %d", verdict.WordCount, minRatioWords)
	}
}

func TestInspectTextShortOrEmptyIsNotRejected(t *testing.T) {
	for _, text := range []string{"", "loading loading loading", "one two three four"} {
		verdict := InspectText(text)
		if !verdict.OK {
			t.Errorf("text %q rejected: %+v", text, verdict)
		}
	}
}

func TestInspectTextDenseTablePasses(t *testing.T) {
	var rows []string
	for i := 1; i <= 40; i++ {
		rows = append(rows, "Date Amount Tax Reference", "2026-08-01", "$123.45", "19%", "INV-2026-"+string(rune('A'+i)))
	}
	verdict := InspectText(strings.Join(rows, " "))
	if !verdict.OK {
		t.Fatalf("dense table rejected: %+v", verdict)
	}
}

func TestInspectTextRepetitionLoopFails(t *testing.T) {
	verdict := InspectText(strings.Repeat("loading ", 250))
	if verdict.OK {
		t.Fatalf("repetition loop accepted: %+v", verdict)
	}
	if verdict.Reason != "unique-word ratio is below plausibility limit" {
		t.Fatalf("Reason = %q", verdict.Reason)
	}
	if verdict.UniqueRatio >= minUniqueWordRatio {
		t.Fatalf("UniqueRatio = %f, want below %f", verdict.UniqueRatio, minUniqueWordRatio)
	}
}

func TestInspectTextLengthLimitFails(t *testing.T) {
	verdict := InspectText(strings.Repeat("word ", maxPlausibleWords+1))
	if verdict.OK {
		t.Fatalf("overlong output accepted: %+v", verdict)
	}
	if verdict.Reason != "word count exceeds plausibility limit" {
		t.Fatalf("Reason = %q", verdict.Reason)
	}
}

func TestTruncateText(t *testing.T) {
	if got := TruncateText("one two three", 3); got != "one two three" {
		t.Fatalf("unchanged text = %q", got)
	}
	if got := TruncateText("one two three four", 2); got != "one two" {
		t.Fatalf("truncated text = %q", got)
	}
}
