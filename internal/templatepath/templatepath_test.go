package templatepath

import (
	"strings"
	"testing"
)

func TestCompile_ValidFields(t *testing.T) {
	tmpl, err := Compile("documents/{correspondent}/{document_date:%Y}/{title}")
	if err != nil {
		t.Fatal(err)
	}
	if tmpl.Raw() != "documents/{correspondent}/{document_date:%Y}/{title}" {
		t.Errorf("unexpected raw: %s", tmpl.Raw())
	}
	if len(tmpl.segments) != 6 {
		t.Errorf("expected 6 segments, got %d", len(tmpl.segments))
	}
}

func TestCompile_EmptyTemplate(t *testing.T) {
	tmpl, err := Compile("")
	if err != nil {
		t.Fatal(err)
	}
	if !tmpl.IsEmpty() {
		t.Error("expected empty template")
	}
}

func TestCompile_InvalidField(t *testing.T) {
	_, err := Compile("{nonexistent}")
	if err == nil {
		t.Error("expected error for unknown field")
	}
}

func TestCompile_PathTraversalRejected(t *testing.T) {
	_, err := Compile("../escape")
	if err == nil {
		t.Error("expected path traversal to be rejected")
	}
	_, err = Compile("docs/../../outside")
	if err == nil {
		t.Error("expected nested traversal to be rejected")
	}
}

func TestEval_SubstitutesFields(t *testing.T) {
	tmpl, err := Compile("documents/{correspondent}/{title}")
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]string{
		"correspondent": "Acme Corp",
		"title":         "Invoice 2026-001",
	}
	result := tmpl.Eval(fields)
	expected := "documents/Acme Corp/Invoice 2026-001"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestEval_MissingFieldFallback(t *testing.T) {
	tmpl, err := Compile("documents/{correspondent}/{title}")
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]string{
		"title": "Invoice 2026-001",
	}
	result := tmpl.Eval(fields)
	if !strings.Contains(result, "unknown") {
		t.Errorf("expected 'unknown' fallback in result, got %q", result)
	}
}

func TestEval_DateFormatting(t *testing.T) {
	tmpl, err := Compile("docs/{document_date:%Y}/{document_date:%m}")
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]string{
		"document_date": "2026-07-27",
	}
	result := tmpl.Eval(fields)
	expected := "docs/2026/07"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestEval_DateFormattingDefault(t *testing.T) {
	tmpl, err := Compile("docs/{document_date}")
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]string{
		"document_date": "2026-07-27",
	}
	result := tmpl.Eval(fields)
	expected := "docs/2026-07-27"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestEval_SanitizeUnsafeChars(t *testing.T) {
	tmpl, err := Compile("{title}")
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]string{
		"title": "Invoice/Acme: 001",
	}
	result := tmpl.Eval(fields)
	if strings.Contains(result, "/") {
		t.Errorf("expected slashes to be sanitized, got %q", result)
	}
}

func TestDisambiguate(t *testing.T) {
	known := map[string]bool{"test.md": true, "test_1.md": true}
	result := Disambiguate("test.md", known)
	if result != "test_2.md" {
		t.Errorf("expected test_2.md, got %s", result)
	}
	result = Disambiguate("unique.md", known)
	if result != "unique.md" {
		t.Errorf("expected unique.md, got %s", result)
	}
}

func TestSanitizePathSegment(t *testing.T) {
	tests := []struct{ in, want string }{
		{"hello/world", "hello-world"},
		{".dotfile", "dotfile"},
		{"normal", "normal"},
		{"with space", "with space"},
		{"", "unknown"},
	}
	for _, tt := range tests {
		got := sanitizePathSegment(tt.in)
		if got != tt.want {
			t.Errorf("sanitizePathSegment(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
