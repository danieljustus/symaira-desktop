package simhash

import (
	"strings"
	"testing"
)

func TestComputeDeterministic(t *testing.T) {
	text := "The quick brown fox jumps over the lazy dog"
	h1 := Compute(text)
	h2 := Compute(text)
	if h1 != h2 {
		t.Errorf("expected deterministic hash, got %x != %x", h1, h2)
	}
}

func TestComputeDifferentText(t *testing.T) {
	a := Compute("hello world foo bar")
	b := Compute("completely different text here")
	if a == b {
		t.Errorf("expected different hashes for different text")
	}
}

func TestComputeHex(t *testing.T) {
	hex := ComputeHex("test document")
	if len(hex) != 16 {
		t.Errorf("expected 16-char hex, got %d chars: %s", len(hex), hex)
	}
}

func TestSimilarTextHighSimilarity(t *testing.T) {
	// Two very similar texts should have high similarity
	a := Compute("Monthly utility bill for Alice from Power Co. Amount due: $150.00")
	b := Compute("Monthly utility bill for Alice from Power Co. Amount due: $155.00")
	sim := Similarity(a, b)
	if sim < 70 {
		t.Errorf("expected high similarity (>70%%) for near-duplicate texts, got %d%%", sim)
	}
}

func TestDissimilarTextLowSimilarity(t *testing.T) {
	a := Compute("Monthly utility bill for Alice from Power Co.")
	b := Compute("Car insurance renewal notice for Bob from SafeDrive Inc.")
	sim := Similarity(a, b)
	if sim > 70 {
		t.Errorf("expected low similarity (<70%%) for dissimilar texts, got %d%%", sim)
	}
}

func TestHammingDistanceSelf(t *testing.T) {
	h := Compute("same thing")
	d := HammingDistance(h, h)
	if d != 0 {
		t.Errorf("expected distance 0 from self, got %d", d)
	}
}

func TestParseHex(t *testing.T) {
	original := Compute("hello")
	hex := ComputeHex("hello")
	parsed, err := ParseHex(hex)
	if err != nil {
		t.Fatalf("ParseHex failed: %v", err)
	}
	if parsed != original {
		t.Errorf("expected round-trip, got %x != %x", parsed, original)
	}
}

func TestParseHexInvalid(t *testing.T) {
	_, err := ParseHex("short")
	if err == nil {
		t.Error("expected error for short hex")
	}
	_, err = ParseHex("zzzzzzzzzzzzzzzz")
	if err == nil {
		t.Error("expected error for invalid hex")
	}
}

func TestSimilaritySymmetric(t *testing.T) {
	a := Compute("foo bar baz")
	b := Compute("foo bar qux")
	s1 := Similarity(a, b)
	s2 := Similarity(b, a)
	if s1 != s2 {
		t.Errorf("similarity not symmetric: %d != %d", s1, s2)
	}
}

func TestEmptyText(t *testing.T) {
	h := Compute("")
	if h != 0 {
		t.Errorf("expected 0 hash for empty text, got %x", h)
	}
}

func TestSimilarityForContentCapsShortBodies(t *testing.T) {
	body := "same short note"
	got := SimilarityForContent(Compute(body), Compute(body), body, body)
	if got != ShortBodySimilarityCap {
		t.Fatalf("expected short-body similarity cap %d, got %d", ShortBodySimilarityCap, got)
	}
}

func TestSimilarityForContentKeepsLongBodiesUnchanged(t *testing.T) {
	a := strings.Repeat("monthly utility bill amount due ", 8)
	b := strings.Repeat("monthly utility bill amount paid ", 8)
	got := SimilarityForContent(Compute(a), Compute(b), a, b)
	want := Similarity(Compute(a), Compute(b))
	if got != want {
		t.Fatalf("expected long-body similarity %d, got %d", want, got)
	}
}
