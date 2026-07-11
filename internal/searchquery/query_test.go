package searchquery

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name             string
		query            string
		wantFilters      []Filter
		wantTerms        []Term
		wantRegex        []string
		wantSidecar      bool
		wantErrSubstring string
	}{
		{
			name:  "combined operators",
			query: "tag:invoice path:finance -status:paid steuer",
			wantFilters: []Filter{
				{Field: FieldTag, Value: "invoice"},
				{Field: FieldPath, Value: "finance"},
				{Field: FieldStatus, Value: "paid", Negated: true},
			},
			wantTerms:   []Term{{Value: "steuer"}},
			wantSidecar: true,
		},
		{
			name:  "phrase negation and regular expression",
			query: `"annual report" -draft /invoice-[0-9]+/ -/void/`,
			wantTerms: []Term{
				{Value: "annual report", Phrase: true},
				{Value: "draft", Negated: true},
			},
			wantRegex:   []string{"invoice-[0-9]+", "void"},
			wantSidecar: true,
		},
		{
			name:        "plain terms retain sibling search eligibility",
			query:       "tax invoice",
			wantTerms:   []Term{{Value: "tax"}, {Value: "invoice"}},
			wantSidecar: false,
		},
		{
			name:             "unterminated quote",
			query:            `"tax invoice`,
			wantErrSubstring: "unterminated quoted phrase",
		},
		{
			name:             "bad regular expression",
			query:            "/[/",
			wantErrSubstring: "invalid regular expression",
		},
		{
			name:             "unknown operator",
			query:            "owner:daniel",
			wantErrSubstring: "unknown search operator",
		},
		{
			name:             "operator without value",
			query:            "tag:",
			wantErrSubstring: "requires a value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.query)
			if tt.wantErrSubstring != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErrSubstring) {
					t.Fatalf("Parse(%q) error = %v, want substring %q", tt.query, err, tt.wantErrSubstring)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%q): %v", tt.query, err)
			}
			if !equalFilters(got.Filters, tt.wantFilters) {
				t.Errorf("Filters = %#v, want %#v", got.Filters, tt.wantFilters)
			}
			if !equalTerms(got.Terms, tt.wantTerms) {
				t.Errorf("Terms = %#v, want %#v", got.Terms, tt.wantTerms)
			}
			if len(got.Regexes) != len(tt.wantRegex) {
				t.Fatalf("Regexes = %#v, want %v", got.Regexes, tt.wantRegex)
			}
			for i, pattern := range tt.wantRegex {
				if got.Regexes[i].Pattern != pattern {
					t.Errorf("Regexes[%d].Pattern = %q, want %q", i, got.Regexes[i].Pattern, pattern)
				}
			}
			if got.RequiresSidecar() != tt.wantSidecar {
				t.Errorf("RequiresSidecar() = %v, want %v", got.RequiresSidecar(), tt.wantSidecar)
			}
		})
	}
}

func TestRegexMatches(t *testing.T) {
	plan, err := Parse(`/invoice-[0-9]+/`)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Regexes[0].Matches("Invoice invoice-2026") {
		t.Fatal("expected regular expression to match")
	}
}

func equalFilters(a, b []Filter) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalTerms(a, b []Term) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
