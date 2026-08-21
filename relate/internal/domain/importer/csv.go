package importer

import (
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
)

// ColumnMapping names which CSV header maps to which logical field. An
// empty value means the column is not present in the source file.
type ColumnMapping struct {
	DisplayName  string
	GivenName    string
	FamilyName   string
	Organization string
	Title        string
	Email        string
	Phone        string
	URL          string
}

// knownHeaderAliases lists, per logical field, the header spellings
// DetectColumnMapping recognizes (case-insensitive).
var knownHeaderAliases = map[string][]string{
	"DisplayName":  {"name", "full name", "display name"},
	"GivenName":    {"given name", "first name", "firstname"},
	"FamilyName":   {"family name", "last name", "lastname", "surname"},
	"Organization": {"organization", "org", "company"},
	"Title":        {"title", "job title"},
	"Email":        {"email", "e-mail", "email address"},
	"Phone":        {"phone", "telephone", "phone number", "mobile"},
	"URL":          {"url", "website", "web"},
}

// DetectColumnMapping builds a ColumnMapping from a CSV header row using
// knownHeaderAliases. Callers that need a different mapping (renamed
// columns, a language other than English) build a ColumnMapping directly
// instead of relying on detection — this is a convenience, not the only
// path.
func DetectColumnMapping(header []string) ColumnMapping {
	lower := make(map[string]string, len(header)) // lowercased header -> original header
	for _, h := range header {
		lower[strings.ToLower(strings.TrimSpace(h))] = h
	}

	find := func(field string) string {
		for _, alias := range knownHeaderAliases[field] {
			if orig, ok := lower[alias]; ok {
				return orig
			}
		}
		return ""
	}

	return ColumnMapping{
		DisplayName:  find("DisplayName"),
		GivenName:    find("GivenName"),
		FamilyName:   find("FamilyName"),
		Organization: find("Organization"),
		Title:        find("Title"),
		Email:        find("Email"),
		Phone:        find("Phone"),
		URL:          find("URL"),
	}
}

// ParseCSV reads a CSV file with a header row and returns one ImportRow
// per data row, using mapping to find each logical field's column. A row
// with no display name (and no given/family name to derive one from) is
// reported as a ValidationIssue and excluded from the returned rows —
// never silently dropped.
func ParseCSV(r io.Reader, mapping ColumnMapping) ([]ImportRow, []ValidationIssue, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1 // report ragged rows as issues, not hard errors

	header, err := cr.Read()
	if err != nil {
		if err == io.EOF {
			return nil, nil, fmt.Errorf("csv file is empty")
		}
		return nil, nil, fmt.Errorf("failed to read CSV header: %w", err)
	}
	col := columnIndex(header)

	var rows []ImportRow
	var issues []ValidationIssue
	rowNum := 0
	for {
		record, err := cr.Read()
		if err == io.EOF {
			break
		}
		rowNum++
		if err != nil {
			issues = append(issues, ValidationIssue{RowNumber: rowNum, Field: "", Message: err.Error()})
			continue
		}

		row := ImportRow{RowNumber: rowNum}
		row.DisplayName = field(record, col, mapping.DisplayName)
		row.GivenName = field(record, col, mapping.GivenName)
		row.FamilyName = field(record, col, mapping.FamilyName)
		row.OrganizationName = field(record, col, mapping.Organization)
		row.Title = field(record, col, mapping.Title)
		if v := field(record, col, mapping.Email); v != "" {
			row.Emails = splitMulti(v)
		}
		if v := field(record, col, mapping.Phone); v != "" {
			row.Phones = splitMulti(v)
		}
		if v := field(record, col, mapping.URL); v != "" {
			row.URLs = splitMulti(v)
		}

		if row.DisplayName == "" {
			row.DisplayName = strings.TrimSpace(row.GivenName + " " + row.FamilyName)
		}
		if row.DisplayName == "" {
			issues = append(issues, ValidationIssue{RowNumber: rowNum, Field: "name", Message: "row has no display name, given name or family name"})
			continue
		}

		row.SourceRef = csvRowHash(row)
		rows = append(rows, row)
	}

	return rows, issues, nil
}

func columnIndex(header []string) map[string]int {
	idx := make(map[string]int, len(header))
	for i, h := range header {
		idx[h] = i
	}
	return idx
}

func field(record []string, col map[string]int, header string) string {
	if header == "" {
		return ""
	}
	i, ok := col[header]
	if !ok || i >= len(record) {
		return ""
	}
	return strings.TrimSpace(record[i])
}

// splitMulti allows a single cell to carry more than one email/phone/url
// separated by ";" — a common convention in address-book exports.
func splitMulti(v string) []string {
	parts := strings.Split(v, ";")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// csvRowHash derives a stable source_ref for idempotent re-import from
// the row's mapped identity fields, so re-importing the same file
// (unchanged) is recognized as the same source on every run.
func csvRowHash(row ImportRow) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|%s|%s", row.DisplayName, row.GivenName, row.FamilyName, strings.Join(row.Emails, ","))
	return "csv:" + hex.EncodeToString(h.Sum(nil))[:16]
}
