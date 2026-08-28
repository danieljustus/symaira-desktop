// Package simhash computes a 64-bit SimHash fingerprint for near-duplicate
// detection.  The algorithm tokenizes text on whitespace, hashes each token
// with FNV-1a 64-bit, updates a 64-element vector of +1/-1 counters, then
// folds the vector into a 64-bit fingerprint.
package simhash

import (
	"encoding/hex"
	"fmt"
	"math/bits"
	"strings"
)

// MinimumBodyLength is the minimum amount of meaningful body text for a
// similarity score to be considered reliable. SimHash is intentionally kept
// unchanged; this guard prevents a handful of tokens (or only whitespace)
// from producing an overconfident duplicate score.
const MinimumBodyLength = 64

// ShortBodySimilarityCap is the highest similarity reported when either
// document is shorter than MinimumBodyLength. The duplicate scan's default
// threshold is higher than this cap, so short notes cannot form a duplicate
// group without changing the established threshold policy.
const ShortBodySimilarityCap = 50

// ContentLength returns the number of non-whitespace Unicode characters in a
// document body.
func ContentLength(text string) int {
	return len([]rune(strings.TrimSpace(text)))
}

// SimilarityForContent applies the conservative short-body policy to the
// regular SimHash similarity calculation.
func SimilarityForContent(a, b uint64, aBody, bBody string) int {
	similarity := Similarity(a, b)
	if ContentLength(aBody) < MinimumBodyLength || ContentLength(bBody) < MinimumBodyLength {
		if similarity > ShortBodySimilarityCap {
			return ShortBodySimilarityCap
		}
	}
	return similarity
}

// Compute returns the 64-bit SimHash fingerprint of text.
// The computation is deterministic: identical input always yields the same hash.
func Compute(text string) uint64 {
	tokens := tokenize(text)
	var vector [64]int

	for _, tok := range tokens {
		h := fnv1a64(tok)
		for i := 0; i < 64; i++ {
			if h&(1<<uint(i)) != 0 {
				vector[i]++
			} else {
				vector[i]--
			}
		}
	}

	var fingerprint uint64
	for i := 0; i < 64; i++ {
		if vector[i] > 0 {
			fingerprint |= 1 << uint(i)
		}
	}
	return fingerprint
}

// ComputeHex returns the SimHash fingerprint as a 16-character hex string.
func ComputeHex(text string) string {
	return fmt.Sprintf("%016x", Compute(text))
}

// HammingDistance returns the number of bit positions where a and b differ.
func HammingDistance(a, b uint64) int {
	return bits.OnesCount64(a ^ b)
}

// Similarity maps a Hamming distance (0–64) to a user-facing 0–100 percentage.
// 0 distance = 100% similar; 64 distance = 0% similar.
func Similarity(a, b uint64) int {
	return (64 - HammingDistance(a, b)) * 100 / 64
}

// ParseHex decodes a hex string back to a uint64 fingerprint.
// Returns 0 and an error if the input is invalid.
func ParseHex(s string) (uint64, error) {
	if len(s) != 16 {
		return 0, fmt.Errorf("simhash hex must be 16 characters, got %d", len(s))
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return 0, fmt.Errorf("invalid simhash hex: %w", err)
	}
	var v uint64
	for i := 0; i < 8; i++ {
		v = v<<8 | uint64(b[i])
	}
	return v, nil
}

// fnv1a64 computes FNV-1a 64-bit hash of a string.
func fnv1a64(s string) uint64 {
	h := uint64(14695981039346656037) // FNV offset basis
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211 // FNV prime
	}
	return h
}

// tokenize splits text into lowercase whitespace-separated tokens.
func tokenize(text string) []string {
	words := strings.Fields(strings.ToLower(text))
	return words
}
