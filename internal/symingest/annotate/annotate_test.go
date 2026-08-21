package annotate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGetProfile_Unknown(t *testing.T) {
	_, err := GetProfile("nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown profile")
	}
}

func TestGetProfile_AllBuiltin(t *testing.T) {
	for _, name := range ProfileNames() {
		p, err := GetProfile(name)
		if err != nil {
			t.Errorf("GetProfile(%q) error: %v", name, err)
		}
		if p.Name() != name {
			t.Errorf("profile.Name() = %q, want %q", p.Name(), name)
		}
		if len(p.Fields()) == 0 {
			t.Errorf("profile %q has no fields", name)
		}
	}
}

func TestExtract_GenericProfile(t *testing.T) {
	p, err := GetProfile("generic")
	if err != nil {
		t.Fatal(err)
	}
	text := `Invoice from Acme Corp
Date: 2026-03-12
Total Due: $284.50
Contact: info@example.com
Payment: https://pay.acme.com/invoice/123
IBAN: DE89370400440532013000
`
	extractions := Extract(p, text)
	if len(extractions) == 0 {
		t.Fatal("expected at least one extraction")
	}
	for _, e := range extractions {
		if !e.Matched {
			t.Errorf("extraction %q is not matched", e.Field)
		}
		if e.Span == nil {
			t.Errorf("extraction %q has no span", e.Field)
		} else if e.Span.Start < 0 || e.Span.End <= e.Span.Start {
			t.Errorf("extraction %q has invalid span [%d, %d]", e.Field, e.Span.Start, e.Span.End)
		}
		if e.Span != nil && e.Span.Snippet == "" {
			t.Errorf("extraction %q has empty snippet", e.Field)
		}
		if e.Value == "" {
			t.Errorf("extraction %q has empty value", e.Field)
		}
	}

	// Verify we found specific types
	found := make(map[string]bool)
	for _, e := range extractions {
		found[e.Field] = true
	}
	if !found["date"] {
		t.Error("expected to find date extraction")
	}
	if !found["email"] {
		t.Error("expected to find email extraction")
	}
	if !found["url"] {
		t.Error("expected to find URL extraction")
	}
	if !found["iban"] {
		t.Error("expected to find IBAN extraction")
	}
}

func TestExtract_InvoiceProfile(t *testing.T) {
	p, err := GetProfile("invoice")
	if err != nil {
		t.Fatal(err)
	}
	text := `Rechnung Nr. 4471
Acme Hardware Supply
Date: 2026-03-12
Total Due: $284.50
Fälligkeit: 15.04.2026
`
	extractions := Extract(p, text)
	if len(extractions) == 0 {
		t.Fatal("expected at least one extraction")
	}
	found := make(map[string]bool)
	for _, e := range extractions {
		found[e.Field] = true
	}
	if !found["invoice_number"] {
		t.Error("expected to find invoice_number extraction")
	}
}

func TestExtract_ContractProfile(t *testing.T) {
	p, err := GetProfile("contract")
	if err != nil {
		t.Fatal(err)
	}
	text := `Service Agreement between Acme Corp and Widget LLC
Deadline: 31.12.2026
Contact: legal@widget.com
`
	extractions := Extract(p, text)
	if len(extractions) == 0 {
		t.Fatal("expected at least one extraction")
	}
	found := make(map[string]bool)
	for _, e := range extractions {
		found[e.Field] = true
	}
	if !found["deadline"] {
		t.Error("expected to find deadline extraction")
	}
	if !found["email"] {
		t.Error("expected to find email extraction")
	}
}

func TestExtract_JobcenterProfile(t *testing.T) {
	p, err := GetProfile("jobcenter")
	if err != nil {
		t.Fatal(err)
	}
	text := `Fallnummer 2024-0815
Acme Staffing Organization GmbH
Termin: 2026-06-15
Frist: 30.06.2026
`
	extractions := Extract(p, text)
	if len(extractions) == 0 {
		t.Fatal("expected at least one extraction")
	}
	found := make(map[string]bool)
	for _, e := range extractions {
		found[e.Field] = true
	}
	if !found["job_id"] {
		t.Error("expected to find job_id extraction")
	}
}

func TestExtract_NoMatchesReturnsEmpty(t *testing.T) {
	p, err := GetProfile("generic")
	if err != nil {
		t.Fatal(err)
	}
	text := "This document contains only plain words without any structured data."
	extractions := Extract(p, text)
	if len(extractions) != 0 {
		t.Errorf("expected 0 extractions, got %d: %v", len(extractions), extractions)
	}
}

func TestExtract_Deduplicates(t *testing.T) {
	p, err := GetProfile("generic")
	if err != nil {
		t.Fatal(err)
	}
	text := "Date: 2026-01-01. Another date: 2026-01-01."
	extractions := Extract(p, text)
	// The same date appears twice but should be deduplicated
	dateCount := 0
	for _, e := range extractions {
		if e.Field == "date" {
			dateCount++
		}
	}
	if dateCount != 1 {
		t.Errorf("expected 1 date extraction (deduped), got %d", dateCount)
	}
}

func TestExtract_DeterministicOrder(t *testing.T) {
	p, err := GetProfile("generic")
	if err != nil {
		t.Fatal(err)
	}
	text := "email: test@example.com and date: 2026-01-01"

	var first, second []Extraction
	for i := 0; i < 10; i++ {
		extractions := Extract(p, text)
		if i == 0 {
			first = extractions
		} else {
			second = extractions
		}
	}

	if len(first) != len(second) {
		t.Fatalf("non-deterministic: first=%d, second=%d", len(first), len(second))
	}
	for i := range first {
		if first[i].Field != second[i].Field || first[i].Value != second[i].Value {
			t.Errorf("non-deterministic at index %d: %v vs %v", i, first[i], second[i])
		}
	}
}

func TestExtractSnippet(t *testing.T) {
	text := strings.Repeat("word ", 50) + "TARGET" + strings.Repeat(" word", 50)
	start := strings.Index(text, "TARGET")
	end := start + len("TARGET")

	snippet := extractSnippet(text, start, end)
	if snippet == "" {
		t.Fatal("expected non-empty snippet")
	}
	// Should contain the match
	if !strings.Contains(snippet, "TARGET") {
		t.Errorf("snippet should contain TARGET, got: %s", snippet)
	}
	// Should be quoted
	if !strings.HasPrefix(snippet, "\"") || !strings.HasSuffix(snippet, "\"") {
		t.Errorf("snippet should be quoted, got: %s", snippet)
	}
}

func TestWriteSidecar(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")

	extractions := []Extraction{
		{Field: "date", Type: "date", Value: "2026-01-01", Span: &Span{Start: 10, End: 20, Snippet: "\"2026-01-01\""}, Matched: true},
		{Field: "email", Type: "email", Value: "test@example.com", Span: &Span{Start: 30, End: 47, Snippet: "\"test@example.com\""}, Matched: true},
	}

	sha := "abc123"
	err := WriteSidecar(vault, sha, extractions)
	if err != nil {
		t.Fatalf("WriteSidecar: %v", err)
	}

	path := SidecarPath(vault, sha)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}

	for _, line := range lines {
		var e Extraction
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Errorf("unmarshal line: %v", err)
		}
		if e.Value == "" {
			t.Error("extraction has empty value")
		}
	}

	// Check permissions
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("file permissions = %o, want 0600", info.Mode().Perm())
	}
}

func TestValidate_GoodSidecar(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")

	lines := []string{
		`{"field":"date","type":"date","value":"2026-01-01","span":{"start":0,"end":10,"snippet":"\"2026-01-01\""},"matched":true}`,
		`{"field":"email","type":"email","value":"a@b.com","span":{"start":20,"end":27,"snippet":"\"a@b.com\""},"matched":true}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := Validate(path)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Valid {
		t.Errorf("expected valid, got errors: %v", result.Errors)
	}
	if result.Lines != 2 {
		t.Errorf("expected 2 lines, got %d", result.Lines)
	}
}

func TestValidate_BadJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")

	if err := os.WriteFile(path, []byte("not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := Validate(path)
	if err != nil {
		t.Fatal(err)
	}
	if result.Valid {
		t.Error("expected invalid for bad JSON")
	}
	if len(result.Errors) == 0 {
		t.Error("expected errors for bad JSON")
	}
}

func TestValidate_MissingFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")

	lines := []string{
		`{"field":"","type":"date","value":"test"}`,
		`{"field":"date","type":"date","value":"","span":null}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := Validate(path)
	if err != nil {
		t.Fatal(err)
	}
	if result.Valid {
		t.Error("expected invalid for missing fields")
	}
	if len(result.Errors) < 2 {
		t.Errorf("expected at least 2 errors, got %d", len(result.Errors))
	}
}

func TestReportFromExtractions(t *testing.T) {
	extractions := []Extraction{
		{Field: "date", Type: "date", Value: "2026-01-01"},
	}
	report := ReportFromExtractions("abc123", "generic", extractions)
	if report.SchemaVersion != 1 {
		t.Errorf("schema_version = %d, want 1", report.SchemaVersion)
	}
	if report.DocSHA256 != "abc123" {
		t.Errorf("doc_sha256 = %q, want abc123", report.DocSHA256)
	}
	if report.Profile != "generic" {
		t.Errorf("profile = %q, want generic", report.Profile)
	}
	if report.TotalFields != 1 {
		t.Errorf("total_fields = %d, want 1", report.TotalFields)
	}
	if len(report.Extractions) != 1 {
		t.Errorf("extractions = %d, want 1", len(report.Extractions))
	}
}

func TestComputeSHA256(t *testing.T) {
	hash := ComputeSHA256([]byte("hello"))
	if len(hash) != 64 {
		t.Errorf("SHA256 length = %d, want 64", len(hash))
	}
	// Deterministic
	hash2 := ComputeSHA256([]byte("hello"))
	if hash != hash2 {
		t.Errorf("non-deterministic SHA256: %q vs %q", hash, hash2)
	}
}

func TestSidecarPath(t *testing.T) {
	path := SidecarPath("/vault", "abc123")
	expected := filepath.Join("/vault", SidecarDirName, "abc123.jsonl")
	if path != expected {
		t.Errorf("SidecarPath = %q, want %q", path, expected)
	}
}

func TestExtract_IgnoringUnmatched(t *testing.T) {
	p, err := GetProfile("generic")
	if err != nil {
		t.Fatal(err)
	}
	// Text that matches some but not all generic fields
	text := "Just an email: test@example.com"
	extractions := Extract(p, text)
	for _, e := range extractions {
		if !e.Matched {
			t.Errorf("unmatched extraction in output: %v", e)
		}
	}
}
