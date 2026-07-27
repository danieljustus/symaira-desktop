// Package templatepath evaluates a configurable storage path template against
// contract frontmatter fields to determine where an ingested document's note
// and original are written. The template syntax uses {field} for plain
// substitution and {field:%format} for strftime formatting of date fields.
//
// Supported fields: correspondent, document_type, document_date, person,
// category, asn, title, tags.
//
// Missing fields use the empty string as a fallback. Characters that are not
// filesystem-safe are replaced with underscores. Path-traversal attempts
// (../) are rejected at validation time.
package templatepath

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// Template is a compiled storage-path template.
type Template struct {
	raw      string
	segments []segment
}

// segment is one piece of a compiled template: either a literal string or a
// field reference with an optional strftime format string.
type segment struct {
	literal string // non-empty for literal segments
	field   string // non-empty for field-reference segments
	format  string // strftime format string (only meaningful for date fields)
}

var fieldPattern = regexp.MustCompile(`\{(\w+)(?::([^}]*))?\}`)

// Compile parses a template string and returns a compiled Template or an error
// if the template is syntactically invalid or attempts path traversal.
func Compile(raw string) (*Template, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return &Template{raw: raw, segments: nil}, nil
	}

	// Reject path-traversal escapes in the raw template before expansion.
	cleaned := filepath.ToSlash(filepath.Clean(raw))
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return nil, fmt.Errorf("template escapes vault root: %q", raw)
	}
	for _, part := range strings.Split(cleaned, "/") {
		if part == ".." {
			return nil, fmt.Errorf("template escapes vault root: %q", raw)
		}
	}

	var segments []segment
	remaining := raw
	for remaining != "" {
		loc := fieldPattern.FindStringIndex(remaining)
		if loc == nil {
			segments = append(segments, segment{literal: remaining})
			break
		}
		if loc[0] > 0 {
			segments = append(segments, segment{literal: remaining[:loc[0]]})
		}
		match := fieldPattern.FindStringSubmatch(remaining[loc[0]:loc[1]])
		if len(match) < 2 {
			segments = append(segments, segment{literal: remaining[loc[0]:loc[1]]})
		} else {
			field := match[1]
			format := ""
			if len(match) >= 3 {
				format = match[2]
			}
			if !validField(field) {
				return nil, fmt.Errorf("unknown template field %q", field)
			}
			segments = append(segments, segment{field: field, format: format})
		}
		remaining = remaining[loc[1]:]
	}

	return &Template{raw: raw, segments: segments}, nil
}

var validFields = map[string]bool{
	"correspondent": true,
	"document_type": true,
	"document_date": true,
	"person":        true,
	"category":      true,
	"asn":           true,
	"title":         true,
	"created":       true,
}

func validField(name string) bool {
	return validFields[name]
}

// Eval resolves the template against the given field values and returns the
// resulting relative path. Field values is a map from frontmatter keys to
// string representations. Missing fields become empty segments.
func (t *Template) Eval(fields map[string]string) string {
	if len(t.segments) == 0 {
		return ""
	}
	var b strings.Builder
	for _, seg := range t.segments {
		if seg.literal != "" {
			b.WriteString(seg.literal)
			continue
		}
		val := fields[seg.field]
		if seg.field == "document_date" || seg.field == "created" {
			if formatted := formatDateField(val, seg.format); formatted != "" {
				b.WriteString(formatted)
				continue
			}
		}
		if val == "" {
			val = "unknown"
		}
		b.WriteString(sanitizePathSegment(val))
	}
	result := b.String()
	if result == "" {
		return ""
	}
	// Normalize slashes and clean
	result = filepath.ToSlash(filepath.Clean(result))
	return result
}

// Raw returns the original uncompiled template string.
func (t *Template) Raw() string { return t.raw }

// IsEmpty reports whether the template is the empty string (no template configured).
func (t *Template) IsEmpty() bool { return t.raw == "" }

// formatDateField attempts to parse val as an ISO-8601 date or datetime and
// reformats it with the given strftime pattern. If parsing or formatting fails,
// val is returned unchanged.
func formatDateField(val, format string) string {
	if val == "" {
		return ""
	}
	t, err := parseDate(val)
	if err != nil {
		return ""
	}
	if format == "" {
		format = "%Y-%m-%d"
	}
	return strftime(t, format)
}

// parseDate tries common date formats.
func parseDate(s string) (time.Time, error) {
	formats := []string{
		time.RFC3339,
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05",
		"2006-01-02",
		"2006-01",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse date: %s", s)
}

// strftime formats a time using a limited set of strftime-style directives.
func strftime(t time.Time, format string) string {
	replacements := map[string]string{
		"%Y": t.Format("2006"),
		"%y": t.Format("06"),
		"%m": t.Format("01"),
		"%d": t.Format("02"),
		"%H": t.Format("15"),
		"%M": t.Format("04"),
		"%S": t.Format("05"),
		"%b": t.Format("Jan"),
		"%B": t.Format("January"),
		"%a": t.Format("Mon"),
		"%A": t.Format("Monday"),
	}
	result := format
	for pat, val := range replacements {
		result = strings.ReplaceAll(result, pat, val)
	}
	return result
}

// sanitizePathSegment replaces characters that are unsafe in filesystem paths.
func sanitizePathSegment(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == '/' || r == '\\' {
			b.WriteRune('-')
		} else if r == 0 || unicode.IsControl(r) {
			b.WriteRune('_')
		} else if r == ':' && b.Len() == 0 {
			// Leading colon is problematic on some systems
			b.WriteRune('_')
		} else {
			b.WriteRune(r)
		}
	}
	result := b.String()
	// Trim leading/trailing dots and spaces
	result = strings.Trim(result, ". ")
	if result == "" {
		result = "unknown"
	}
	return result
}

// Disambiguate returns a unique path by appending a numeric suffix when the
// target path already exists in knownPaths. It is the caller's responsibility
// to track paths allocated so far.
func Disambiguate(target string, knownPaths map[string]bool) string {
	candidate := target
	for counter := 1; knownPaths[candidate]; counter++ {
		ext := filepath.Ext(target)
		base := strings.TrimSuffix(target, ext)
		candidate = fmt.Sprintf("%s_%d%s", base, counter, ext)
	}
	return candidate
}
