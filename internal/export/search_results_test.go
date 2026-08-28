package export

import (
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/retrieval"
)

func TestSearchResultsMarkdownPreservesAttributionAndEscapesFormatSensitiveText(t *testing.T) {
	anchor := &retrieval.LocationAnchor{Kind: "heading", Value: "Costs & [2026]"}
	out, err := SearchResults(
		`tag:"R&D" query | with ]`,
		"Results: invoices [Q1]",
		"2026-08-28T12:00:00Z",
		[]SearchHit{{
			Path:            "research/[source].md",
			Title:           "Invoice | North",
			Snippet:         "Amount: 10 | status: open\n# not a new heading",
			Score:           0.875,
			Anchor:          anchor,
			MetadataMatches: []string{"title", "tags"},
		}},
		"markdown",
	)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	for _, want := range []string{
		"type: search_result_set",
		"query: ",
		"result_count: 1",
		"**Source:** [[research/%5Bsource%5D.md#heading:Costs%20%26%20%5B2026%5D]] (`research/[source].md`)",
		"**Location:** heading: Costs & [2026]",
		"**Metadata matches:** title, tags",
		"> Amount: 10 | status: open\n> # not a new heading",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\n# not a new heading") {
		t.Error("snippet escaped its blockquote and became a heading")
	}
}

func TestSearchResultsPDFUsesSameMetadataButOmitsBodyTitle(t *testing.T) {
	out, err := SearchResults("budget", "Budget hits", "2026-08-28T12:00:00Z", []SearchHit{{
		Path: "budget.md", Title: "Budget", Snippet: "42 EUR",
	}}, "pdf")
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if !strings.HasPrefix(got, "---\n") || !strings.Contains(got, "title: Budget hits") {
		t.Fatalf("PDF source missing frontmatter: %s", got)
	}
	if strings.Contains(got, "# Budget hits") {
		t.Fatalf("PDF source repeats title as body heading: %s", got)
	}
	if !strings.Contains(got, "**Source:** [[budget.md]]") {
		t.Fatalf("PDF source lost attribution: %s", got)
	}
}

func TestSearchResultsRejectsUnknownFormat(t *testing.T) {
	if _, err := SearchResults("q", "title", "", nil, "html"); err == nil {
		t.Fatal("expected unsupported format error")
	}
}
