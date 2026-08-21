package importer

import (
	"strings"
	"testing"
)

const sampleVCard3 = "BEGIN:VCARD\r\n" +
	"VERSION:3.0\r\n" +
	"FN:Jordan Example\r\n" +
	"N:Example;Jordan;;;\r\n" +
	"ORG:Example Co\r\n" +
	"TITLE:Analyst\r\n" +
	"EMAIL:jordan@example.com\r\n" +
	"TEL:+15550100001\r\n" +
	"UID:sample-vcard-uid-001\r\n" +
	"END:VCARD\r\n"

const sampleVCard4 = "BEGIN:VCARD\r\n" +
	"VERSION:4.0\r\n" +
	"FN:Taylor Sample\r\n" +
	"N:Sample;Taylor;;;\r\n" +
	"EMAIL:taylor@example.org\r\n" +
	"TEL:+15550100002\r\n" +
	"URL:https://taylor.example.org\r\n" +
	"UID:sample-vcard-uid-002\r\n" +
	"END:VCARD\r\n"

func TestParseVCard_Version3(t *testing.T) {
	rows, issues, err := ParseVCard(strings.NewReader(sampleVCard3))
	if err != nil {
		t.Fatalf("ParseVCard() error = %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("issues = %+v, want none", issues)
	}
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.DisplayName != "Jordan Example" || r.GivenName != "Jordan" || r.FamilyName != "Example" {
		t.Errorf("names = %+v", r)
	}
	if r.OrganizationName != "Example Co" || r.Title != "Analyst" {
		t.Errorf("org/title = %q/%q", r.OrganizationName, r.Title)
	}
	if len(r.Emails) != 1 || r.Emails[0] != "jordan@example.com" {
		t.Errorf("Emails = %v", r.Emails)
	}
	if r.SourceRef != "vcard-uid:sample-vcard-uid-001" {
		t.Errorf("SourceRef = %q", r.SourceRef)
	}
}

func TestParseVCard_Version4(t *testing.T) {
	rows, issues, err := ParseVCard(strings.NewReader(sampleVCard4))
	if err != nil {
		t.Fatalf("ParseVCard() error = %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("issues = %+v, want none", issues)
	}
	if len(rows) != 1 || rows[0].DisplayName != "Taylor Sample" {
		t.Fatalf("rows = %+v", rows)
	}
	if len(rows[0].URLs) != 1 || rows[0].URLs[0] != "https://taylor.example.org" {
		t.Errorf("URLs = %v", rows[0].URLs)
	}
}

func TestParseVCard_MultipleCards(t *testing.T) {
	rows, issues, err := ParseVCard(strings.NewReader(sampleVCard3 + sampleVCard4))
	if err != nil {
		t.Fatalf("ParseVCard() error = %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("issues = %+v, want none", issues)
	}
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2", len(rows))
	}
	if rows[0].RowNumber != 1 || rows[1].RowNumber != 2 {
		t.Errorf("row numbers = %d, %d, want 1, 2", rows[0].RowNumber, rows[1].RowNumber)
	}
}

func TestParseVCard_NoNameReportedAsIssue(t *testing.T) {
	card := "BEGIN:VCARD\r\nVERSION:4.0\r\nEMAIL:nobody@example.com\r\nEND:VCARD\r\n"
	rows, issues, err := ParseVCard(strings.NewReader(card))
	if err != nil {
		t.Fatalf("ParseVCard() error = %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("len(rows) = %d, want 0 (nameless card must be excluded)", len(rows))
	}
	if len(issues) != 1 || issues[0].RowNumber != 1 {
		t.Fatalf("issues = %+v, want one issue on card 1", issues)
	}
}

func TestParseVCard_WithoutUIDGetsStableHashRef(t *testing.T) {
	card := "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:No UID Person\r\nEMAIL:nouid@example.com\r\nEND:VCARD\r\n"
	rows1, _, err := ParseVCard(strings.NewReader(card))
	if err != nil {
		t.Fatalf("first ParseVCard() error = %v", err)
	}
	rows2, _, err := ParseVCard(strings.NewReader(card))
	if err != nil {
		t.Fatalf("second ParseVCard() error = %v", err)
	}
	if rows1[0].SourceRef == "" || rows1[0].SourceRef != rows2[0].SourceRef {
		t.Errorf("SourceRef not stable: %q vs %q", rows1[0].SourceRef, rows2[0].SourceRef)
	}
}
