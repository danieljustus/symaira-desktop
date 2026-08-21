package contact

import (
	"strings"
	"unicode"
)

// Normalize dispatches to the kind-specific normalizer used for
// same-entity duplicate detection (see the beta contact model's unique
// indexes) and, later, cross-entity import dedup.
func Normalize(kind ContactPointKind, raw string) string {
	switch kind {
	case ContactPointEmail:
		return NormalizeEmail(raw)
	case ContactPointPhone:
		return NormalizePhone(raw)
	case ContactPointURL:
		return NormalizeURL(raw)
	case ContactPointHandle:
		return NormalizeHandle(raw)
	case ContactPointAddress:
		return NormalizeAddress(raw)
	default:
		return strings.ToLower(strings.TrimSpace(raw))
	}
}

// NormalizeEmail lowercases and trims an email address. It intentionally
// does not validate RFC 5322 syntax — that is the caller's job.
func NormalizeEmail(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// NormalizePhone keeps a leading '+' and strips everything but digits. It
// is a conservative approximation of E.164 comparison without pulling in
// a full phone-number library; "+1 (555) 123-4567" and "15551234567"
// normalize to the same digit string once the '+' is stripped by the
// caller's own comparison, so this alone is not sufficient for
// cross-format dedup — see the importer's duplicate-candidate matching.
func NormalizePhone(raw string) string {
	raw = strings.TrimSpace(raw)
	var b strings.Builder
	for i, r := range raw {
		if r == '+' && i == 0 {
			b.WriteRune(r)
			continue
		}
		if unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// NormalizeURL lowercases and trims a trailing slash so "example.com" and
// "https://example.com/" compare closer to equal. It is not a full URL
// normalizer (no scheme/host parsing).
func NormalizeURL(raw string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(raw), "/")
	return strings.ToLower(trimmed)
}

// NormalizeHandle lowercases and strips a single leading "@" so "@user"
// and "user" dedup to the same value.
func NormalizeHandle(raw string) string {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	return strings.TrimPrefix(trimmed, "@")
}

// NormalizeAddress collapses internal whitespace and lowercases a postal
// address for loose dedup comparisons; addresses are not otherwise
// structured in the beta.
func NormalizeAddress(raw string) string {
	return strings.ToLower(strings.Join(strings.Fields(raw), " "))
}
