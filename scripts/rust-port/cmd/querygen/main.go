// Command querygen freezes the Go search-query and date parser contracts.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/searchquery"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

type document struct {
	SchemaVersion int              `json:"schema_version"`
	Oracle        inventory.Oracle `json:"oracle"`
	Cases         cases            `json:"cases"`
}

type cases struct {
	Queries []queryCase `json:"queries"`
	Dates   []dateCase  `json:"dates"`
}

type queryCase struct {
	ID              string      `json:"id"`
	Input           string      `json:"input"`
	Filters         []filterDTO `json:"filters,omitempty"`
	Terms           []termDTO   `json:"terms,omitempty"`
	Regexes         []regexDTO  `json:"regexes,omitempty"`
	RequiresSidecar bool        `json:"requires_sidecar,omitempty"`
	Error           string      `json:"error,omitempty"`
	ErrorClass      string      `json:"error_class,omitempty"`
}

type filterDTO struct {
	Field   string `json:"field"`
	Value   string `json:"value"`
	Negated bool   `json:"negated,omitempty"`
}

type termDTO struct {
	Value   string `json:"value"`
	Phrase  bool   `json:"phrase,omitempty"`
	Negated bool   `json:"negated,omitempty"`
}

type regexDTO struct {
	Pattern      string `json:"pattern"`
	Negated      bool   `json:"negated,omitempty"`
	Probe        string `json:"probe"`
	ProbeMatches bool   `json:"probe_matches"`
}

type dateCase struct {
	ID            string `json:"id"`
	Value         string `json:"value"`
	Reference     string `json:"reference"`
	FromUnixNanos int64  `json:"from_unix_nanos,omitempty"`
	ToUnixNanos   int64  `json:"to_unix_nanos,omitempty"`
	Error         string `json:"error,omitempty"`
	ValidateError string `json:"validate_error,omitempty"`
}

func main() {
	output := flag.String("output", "testdata/port/core/search-query.json", "fixture path")
	check := flag.Bool("check", false, "fail if fixture differs")
	commit := flag.String("oracle-commit", "ae86331930fdfa2b128b68ae5af7437091b9949a", "Go oracle commit")
	release := flag.String("oracle-release", "v0.12.2", "Go oracle release")
	flag.Parse()

	value := document{
		SchemaVersion: 1,
		Oracle:        inventory.Oracle{Commit: *commit, Release: *release},
		Cases:         cases{Queries: buildQueries(), Dates: buildDates()},
	}
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		fatal("marshal: %v", err)
	}
	content = append(content, '\n')
	if *check {
		existing, err := os.ReadFile(*output)
		if err != nil {
			fatal("read fixture: %v", err)
		}
		if !bytes.Equal(existing, content) {
			fatal("search-query fixture drift; regenerate deliberately")
		}
		fmt.Println("PASS search-query fixture")
		return
	}
	if err := os.MkdirAll(filepath.Dir(*output), 0o750); err != nil {
		fatal("create fixture directory: %v", err)
	}
	if err := os.WriteFile(*output, content, 0o600); err != nil {
		fatal("write fixture: %v", err)
	}
	fmt.Println("PASS search-query fixture generated")
}

func buildQueries() []queryCase {
	probe := "Invoice invoice-2026 draft void annual report"
	inputs := []struct{ id, input string }{
		{"empty", ""},
		{"plain", "tax invoice"},
		{"combined", "tag:invoice path:finance -status:paid steuer"},
		{"phrase-regex", `"annual report" -draft /invoice-[0-9]+/ -/void/`},
		{"formats", "filetype:pdf,epub filename:invoice"},
		{"index-alias", "index_status:stale type:document"},
		{"relative-date", "modified:last week"},
		{"date-range", "created:2026-08-01..2026-08-31"},
		{"timestamp", "modified:2026-09-06T12:34:56.123456789Z"},
		{"escaped-phrase", `"annual \"report\""`},
		{"escaped-regex", `/invoice\/[0-9]+/`},
		{"negation-alone", "-"},
		{"negation-space", "- tax"},
		{"empty-phrase", `""`},
		{"unterminated-phrase", `"tax invoice`},
		{"empty-regex", "//"},
		{"unterminated-regex", "/invoice"},
		{"invalid-regex", "/[/"},
		{"unknown-operator", "owner:daniel"},
		{"empty-operator", "tag:"},
		{"invalid-date", "modified:tomorrow"},
		{"unknown-colon-term", "https://example.test"},
	}
	result := make([]queryCase, 0, len(inputs))
	for _, item := range inputs {
		parsed, err := searchquery.Parse(item.input)
		out := queryCase{ID: item.id, Input: item.input}
		if err != nil {
			out.Error = err.Error()
			out.ErrorClass = classifyError(err.Error())
			result = append(result, out)
			continue
		}
		for _, filter := range parsed.Filters {
			out.Filters = append(out.Filters, filterDTO{Field: string(filter.Field), Value: filter.Value, Negated: filter.Negated})
		}
		for _, term := range parsed.Terms {
			out.Terms = append(out.Terms, termDTO{Value: term.Value, Phrase: term.Phrase, Negated: term.Negated})
		}
		for _, expression := range parsed.Regexes {
			out.Regexes = append(out.Regexes, regexDTO{Pattern: expression.Pattern, Negated: expression.Negated, Probe: probe, ProbeMatches: expression.Matches(probe)})
		}
		out.RequiresSidecar = parsed.RequiresSidecar()
		result = append(result, out)
	}
	return result
}

func classifyError(value string) string {
	for _, class := range []string{"negation", "empty quoted phrase", "unterminated quoted phrase", "empty regular expression", "unterminated regular expression", "invalid regular expression", "unknown search operator", "requires a value", "invalid date", "unexpected character", "expected search term"} {
		if strings.Contains(value, class) {
			return class
		}
	}
	return "other"
}

func buildDates() []dateCase {
	reference := time.Date(2026, 9, 6, 12, 34, 56, 123456789, time.UTC)
	inputs := []struct{ id, value string }{
		{"date", "2026-09-06"},
		{"date-range", "2026-08-01..2026-08-31"},
		{"rfc3339", "2026-09-06T10:34:56+02:00"},
		{"rfc3339-nano-range", "2026-09-06T10:00:00.123456789Z..2026-09-06T11:00:00.987654321Z"},
		{"last-day", "last day"},
		{"last-week", "last week"},
		{"last-month", "last month"},
		{"last-year", "last year"},
		{"empty", ""},
		{"invalid-relative", "last decade"},
		{"open-start", "..2026-09-06"},
		{"open-end", "2026-09-06.."},
		{"too-many", "2026-01-01..2026-02-01..2026-03-01"},
		{"reverse", "2026-09-07..2026-09-06"},
		{"invalid", "tomorrow"},
		{"comma-valid", "2026-09-01,2026-09-02"},
		{"comma-empty", "2026-09-01,"},
	}
	result := make([]dateCase, 0, len(inputs))
	for _, item := range inputs {
		out := dateCase{ID: item.id, Value: item.value, Reference: reference.Format(time.RFC3339Nano)}
		dateRange, err := searchquery.ParseDateValue(item.value, reference)
		if err != nil {
			out.Error = err.Error()
		} else {
			out.FromUnixNanos = dateRange.From.UnixNano()
			out.ToUnixNanos = dateRange.To.UnixNano()
		}
		if err := searchquery.ValidateDateValue(item.value); err != nil {
			out.ValidateError = err.Error()
		}
		result = append(result, out)
	}
	return result
}

func fatal(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, "FAIL "+format+"\n", args...)
	os.Exit(1)
}
