package importer

import (
	"strings"
	"testing"
)

func TestDetectColumnMapping(t *testing.T) {
	m := DetectColumnMapping([]string{"Full Name", "Email Address", "Phone", "Company"})
	if m.DisplayName != "Full Name" {
		t.Errorf("DisplayName = %q, want \"Full Name\"", m.DisplayName)
	}
	if m.Email != "Email Address" {
		t.Errorf("Email = %q, want \"Email Address\"", m.Email)
	}
	if m.Phone != "Phone" {
		t.Errorf("Phone = %q, want Phone", m.Phone)
	}
	if m.Organization != "Company" {
		t.Errorf("Organization = %q, want Company", m.Organization)
	}
}

func TestParseCSV_ValidRows(t *testing.T) {
	csv := "Full Name,Email,Phone,Company\n" +
		"Jordan Example,jordan@example.com,+1 555 010 0001,Example Co\n" +
		"Taylor Sample,taylor@example.org,,\n"
	mapping := DetectColumnMapping([]string{"Full Name", "Email", "Phone", "Company"})

	rows, issues, err := ParseCSV(strings.NewReader(csv), mapping)
	if err != nil {
		t.Fatalf("ParseCSV() error = %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("issues = %+v, want none", issues)
	}
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2", len(rows))
	}
	if rows[0].DisplayName != "Jordan Example" || rows[0].OrganizationName != "Example Co" {
		t.Errorf("rows[0] = %+v", rows[0])
	}
	if len(rows[0].Emails) != 1 || rows[0].Emails[0] != "jordan@example.com" {
		t.Errorf("rows[0].Emails = %v", rows[0].Emails)
	}
	if rows[0].RowNumber != 1 || rows[1].RowNumber != 2 {
		t.Errorf("row numbers = %d, %d, want 1, 2", rows[0].RowNumber, rows[1].RowNumber)
	}
}

func TestParseCSV_MissingNameReportedAsIssue(t *testing.T) {
	csv := "Full Name,Email\n,jordan@example.com\n"
	mapping := DetectColumnMapping([]string{"Full Name", "Email"})

	rows, issues, err := ParseCSV(strings.NewReader(csv), mapping)
	if err != nil {
		t.Fatalf("ParseCSV() error = %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("len(rows) = %d, want 0 (invalid row must be excluded)", len(rows))
	}
	if len(issues) != 1 || issues[0].RowNumber != 1 {
		t.Fatalf("issues = %+v, want one issue on row 1", issues)
	}
}

func TestParseCSV_DerivesDisplayNameFromGivenAndFamily(t *testing.T) {
	csv := "First Name,Last Name,Email\nJordan,Example,jordan@example.com\n"
	mapping := DetectColumnMapping([]string{"First Name", "Last Name", "Email"})

	rows, issues, err := ParseCSV(strings.NewReader(csv), mapping)
	if err != nil {
		t.Fatalf("ParseCSV() error = %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("issues = %+v, want none", issues)
	}
	if len(rows) != 1 || rows[0].DisplayName != "Jordan Example" {
		t.Fatalf("rows = %+v, want derived display name \"Jordan Example\"", rows)
	}
}

func TestParseCSV_MultiValueCellSplitsOnSemicolon(t *testing.T) {
	csv := "Full Name,Email\nJordan Example,jordan@example.com;jordan@work.example\n"
	mapping := DetectColumnMapping([]string{"Full Name", "Email"})

	rows, _, err := ParseCSV(strings.NewReader(csv), mapping)
	if err != nil {
		t.Fatalf("ParseCSV() error = %v", err)
	}
	if len(rows) != 1 || len(rows[0].Emails) != 2 {
		t.Fatalf("rows = %+v, want 2 emails", rows)
	}
}

func TestParseCSV_SourceRefIsStableForIdenticalRow(t *testing.T) {
	csv := "Full Name,Email\nJordan Example,jordan@example.com\n"
	mapping := DetectColumnMapping([]string{"Full Name", "Email"})

	rows1, _, err := ParseCSV(strings.NewReader(csv), mapping)
	if err != nil {
		t.Fatalf("first ParseCSV() error = %v", err)
	}
	rows2, _, err := ParseCSV(strings.NewReader(csv), mapping)
	if err != nil {
		t.Fatalf("second ParseCSV() error = %v", err)
	}
	if rows1[0].SourceRef != rows2[0].SourceRef {
		t.Errorf("SourceRef not stable: %q vs %q", rows1[0].SourceRef, rows2[0].SourceRef)
	}
	if rows1[0].SourceRef == "" {
		t.Error("SourceRef is empty")
	}
}

func TestParseCSV_EmptyFileIsAnError(t *testing.T) {
	_, _, err := ParseCSV(strings.NewReader(""), ColumnMapping{})
	if err == nil {
		t.Fatal("ParseCSV() on empty input: want error, got nil")
	}
}
