// Package dataset implements the file-backed dataset primitive. Markdown handle
// notes and raw CSV files are authoritative; sidecar rows are derived state.
package dataset

import (
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/dbviews"
	"github.com/danieljustus/symaira-desktop/internal/vault"
	"gopkg.in/yaml.v3"
)

const (
	Type   = "dataset"
	RawDir = "datasets"

	DefaultSensitivity   = "restricted"
	DefaultRetentionRule = "default"
)

const (
	SensitivityPublic       = "public"
	SensitivityInternal     = "internal"
	SensitivityConfidential = "confidential"
	SensitivityRestricted   = "restricted"
)

var validSensitivities = map[string]struct{}{
	SensitivityPublic: {}, SensitivityInternal: {}, SensitivityConfidential: {}, SensitivityRestricted: {},
}

// NormalizeSensitivity validates the closed sensitivity vocabulary. Empty input
// is intentionally defaulted only at service/CLI/MCP boundaries, never while
// rendering or parsing a persisted handle.
func NormalizeSensitivity(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return DefaultSensitivity, nil
	}
	if _, ok := validSensitivities[value]; !ok {
		return "", fmt.Errorf("invalid dataset sensitivity %q (valid: public, internal, confidential, restricted)", value)
	}
	return value, nil
}

func ValidateSensitivity(value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New("dataset handle requires sensitivity")
	}
	if _, ok := validSensitivities[value]; !ok {
		return fmt.Errorf("invalid dataset sensitivity %q (valid: public, internal, confidential, restricted)", value)
	}
	return nil
}

func ValidateRetentionRuleReference(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("dataset handle requires retention_rule")
	}
	for _, r := range value {
		if r == '/' || r == '\\' || r == '\r' || r == '\n' || r == '	' {
			return fmt.Errorf("invalid dataset retention_rule %q", value)
		}
	}
	return nil
}

type Coverage struct {
	From string `json:"from,omitempty" yaml:"from,omitempty"`
	To   string `json:"to,omitempty" yaml:"to,omitempty"`
}

type Provenance struct {
	ImportedAt   string `json:"imported_at" yaml:"imported_at"`
	SourceName   string `json:"source_name,omitempty" yaml:"source_name,omitempty"`
	SourceSHA256 string `json:"source_sha256" yaml:"source_sha256"`
}

// Handle is the Markdown-backed dataset descriptor. Schema deliberately uses
// the existing dbviews.PropertyConfig type rather than introducing a second
// property type system.
type Handle struct {
	Path           string                            `json:"path" yaml:"-"`
	Slug           string                            `json:"slug" yaml:"dataset_id"`
	Title          string                            `json:"title" yaml:"title"`
	Created        string                            `json:"created" yaml:"created"`
	Source         string                            `json:"source" yaml:"source"`
	Schema         map[string]dbviews.PropertyConfig `json:"schema" yaml:"schema"`
	Coverage       Coverage                          `json:"coverage" yaml:"coverage"`
	Provenance     Provenance                        `json:"provenance" yaml:"provenance"`
	IdentityField  string                            `json:"identity_field,omitempty" yaml:"identity_field,omitempty"`
	RefreshCommand string                            `json:"refresh_command,omitempty" yaml:"refresh_command,omitempty"`
	Sensitivity    string                            `json:"sensitivity" yaml:"sensitivity"`
	RetentionRule  string                            `json:"retention_rule" yaml:"retention_rule"`
}

type Row struct {
	Key        string                 `json:"key"`
	Identity   string                 `json:"identity,omitempty"`
	Values     map[string]interface{} `json:"values"`
	SourcePath string                 `json:"source_path"`
	RowNumber  int                    `json:"row_number"`
}

// ParseCSV parses a tabular CSV while preserving every header as a column. A
// supplied schema controls conversion; missing types are inferred from values.
func ParseCSV(r io.Reader, declared map[string]dbviews.PropertyConfig, identityField string) ([]Row, map[string]dbviews.PropertyConfig, error) {
	if r == nil {
		return nil, nil, errors.New("csv reader is required")
	}
	reader := csv.NewReader(r)
	records, err := reader.ReadAll()
	if err != nil {
		return nil, nil, fmt.Errorf("read csv: %w", err)
	}
	if len(records) == 0 {
		return nil, nil, errors.New("csv is empty")
	}
	headers := make([]string, len(records[0]))
	seen := make(map[string]bool, len(headers))
	for i, raw := range records[0] {
		headers[i] = strings.TrimSpace(raw)
		if headers[i] == "" {
			return nil, nil, fmt.Errorf("csv column %d has an empty name", i+1)
		}
		if seen[strings.ToLower(headers[i])] {
			return nil, nil, fmt.Errorf("csv has duplicate column %q", headers[i])
		}
		seen[strings.ToLower(headers[i])] = true
	}
	if identityField != "" && !hasHeader(headers, identityField) {
		return nil, nil, fmt.Errorf("identity field %q is not a CSV column", identityField)
	}

	valuesByColumn := make(map[string][]string, len(headers))
	for i := 1; i < len(records); i++ {
		if len(records[i]) != len(headers) {
			return nil, nil, fmt.Errorf("row %d has %d columns, want %d", i+1, len(records[i]), len(headers))
		}
		for j, value := range records[i] {
			valuesByColumn[headers[j]] = append(valuesByColumn[headers[j]], strings.TrimSpace(value))
		}
	}
	schema := make(map[string]dbviews.PropertyConfig, len(headers))
	for _, header := range headers {
		property := declared[header]
		if property.Type == "" {
			property.Type = inferType(valuesByColumn[header])
		}
		if property.Label == "" {
			property.Label = header
		}
		schema[header] = property
	}

	rows := make([]Row, 0, len(records)-1)
	for i := 1; i < len(records); i++ {
		values := make(map[string]interface{}, len(headers))
		rawValues := make(map[string]string, len(headers))
		for j, header := range headers {
			rawValues[header] = strings.TrimSpace(records[i][j])
			value, err := convertValue(rawValues[header], schema[header].Type)
			if err != nil {
				return nil, nil, fmt.Errorf("row %d column %q: %w", i+1, header, err)
			}
			values[header] = value
		}
		identity := ""
		if identityField != "" {
			for _, header := range headers {
				if strings.EqualFold(header, identityField) {
					identity = rawValues[header]
					break
				}
			}
		}
		key := "hash:" + CanonicalRowHash(headers, rawValues)
		if identity != "" {
			key = "identity:" + identity
		}
		rows = append(rows, Row{Key: key, Identity: identity, Values: values, RowNumber: i + 1})
	}
	return rows, schema, nil
}

func hasHeader(headers []string, wanted string) bool {
	for _, header := range headers {
		if strings.EqualFold(header, wanted) {
			return true
		}
	}
	return false
}

func inferType(values []string) string {
	hasValue := false
	allNumber, allDate, allBool := true, true, true
	for _, value := range values {
		if value == "" {
			continue
		}
		hasValue = true
		if _, err := strconv.ParseFloat(value, 64); err != nil {
			allNumber = false
		}
		if _, ok := parseDate(value); !ok {
			allDate = false
		}
		if _, err := strconv.ParseBool(strings.ToLower(value)); err != nil {
			allBool = false
		}
	}
	if !hasValue {
		return "text"
	}
	if allBool {
		return "checkbox"
	}
	if allNumber {
		return "number"
	}
	if allDate {
		return "date"
	}
	return "text"
}

func convertValue(value, typ string) (interface{}, error) {
	if value == "" {
		return "", nil
	}
	switch strings.ToLower(strings.TrimSpace(typ)) {
	case "number":
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid number %q", value)
		}
		return parsed, nil
	case "checkbox", "boolean", "bool":
		parsed, err := strconv.ParseBool(strings.ToLower(value))
		if err != nil {
			return nil, fmt.Errorf("invalid boolean %q", value)
		}
		return parsed, nil
	case "date":
		if _, ok := parseDate(value); !ok {
			return nil, fmt.Errorf("invalid date %q", value)
		}
		return value, nil
	default:
		return value, nil
	}
}

func parseDate(value string) (time.Time, bool) {
	for _, layout := range []string{time.DateOnly, time.RFC3339, "2006-01-02 15:04:05", "2006-01-02 15:04"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

// CanonicalRowHash provides deterministic identity for rows without an
// explicit stable identity field. Column names are sorted before hashing so
// equivalent CSV exports with a different column order still deduplicate.
func CanonicalRowHash(columns []string, values map[string]string) string {
	canonicalColumns := append([]string(nil), columns...)
	sort.Strings(canonicalColumns)
	var builder strings.Builder
	for _, column := range canonicalColumns {
		builder.WriteString(strconv.Itoa(len(column)))
		builder.WriteByte(':')
		builder.WriteString(column)
		builder.WriteByte('=')
		value := strings.TrimSpace(values[column])
		builder.WriteString(strconv.Itoa(len(value)))
		builder.WriteByte(':')
		builder.WriteString(value)
		builder.WriteByte('\n')
	}
	sum := sha256.Sum256([]byte(builder.String()))
	return hex.EncodeToString(sum[:])
}

func (h *Handle) Render() ([]byte, error) {
	if h == nil {
		return nil, errors.New("dataset handle is nil")
	}
	if h.Title == "" || h.Slug == "" || h.Source == "" {
		return nil, errors.New("dataset handle requires title, slug, and source")
	}
	if err := ValidateSensitivity(h.Sensitivity); err != nil {
		return nil, err
	}
	if err := ValidateRetentionRuleReference(h.RetentionRule); err != nil {
		return nil, err
	}
	fm := map[string]interface{}{
		"type":           Type,
		"title":          h.Title,
		"created":        h.Created,
		"tags":           []string{"dataset"},
		"dataset_id":     h.Slug,
		"source":         h.Source,
		"schema":         h.Schema,
		"coverage":       h.Coverage,
		"provenance":     h.Provenance,
		"sensitivity":    h.Sensitivity,
		"retention_rule": h.RetentionRule,
	}
	if h.IdentityField != "" {
		fm["identity_field"] = h.IdentityField
	}
	if h.RefreshCommand != "" {
		fm["refresh_command"] = h.RefreshCommand
	}
	encoded, err := yaml.Marshal(fm)
	if err != nil {
		return nil, fmt.Errorf("encode dataset handle: %w", err)
	}
	return []byte("---\n" + string(encoded) + "---\n\n# " + h.Title + "\n\nSource: `" + h.Source + "`\n"), nil
}

func ParseHandle(relPath string, data []byte) (*Handle, error) {
	doc, err := vault.ParseBytes(relPath, data)
	if err != nil {
		return nil, err
	}
	if doc.Type != Type {
		return nil, fmt.Errorf("%s is not a dataset handle", relPath)
	}
	var fm struct {
		Type          string                            `yaml:"type"`
		Title         string                            `yaml:"title"`
		Created       string                            `yaml:"created"`
		Slug          string                            `yaml:"dataset_id"`
		Source        string                            `yaml:"source"`
		Schema        map[string]dbviews.PropertyConfig `yaml:"schema"`
		Coverage      Coverage                          `yaml:"coverage"`
		Provenance    Provenance                        `yaml:"provenance"`
		IdentityField string                            `yaml:"identity_field"`
		Refresh       string                            `yaml:"refresh_command"`
		Sensitivity   string                            `yaml:"sensitivity"`
		RetentionRule string                            `yaml:"retention_rule"`
	}
	if err := yaml.Unmarshal(frontmatter(data), &fm); err != nil {
		return nil, fmt.Errorf("parse dataset handle: %w", err)
	}
	if fm.Type != Type || fm.Source == "" {
		return nil, fmt.Errorf("dataset handle %s is missing type or source", relPath)
	}
	if fm.Title == "" || fm.Slug == "" {
		return nil, fmt.Errorf("dataset handle %s is missing title or dataset_id", relPath)
	}
	if err := ValidateSensitivity(fm.Sensitivity); err != nil {
		return nil, fmt.Errorf("dataset handle %s: %w", relPath, err)
	}
	if err := ValidateRetentionRuleReference(fm.RetentionRule); err != nil {
		return nil, fmt.Errorf("dataset handle %s: %w", relPath, err)
	}
	return &Handle{Path: relPath, Slug: fm.Slug, Title: fm.Title, Created: fm.Created, Source: fm.Source, Schema: fm.Schema, Coverage: fm.Coverage, Provenance: fm.Provenance, IdentityField: fm.IdentityField, RefreshCommand: fm.Refresh, Sensitivity: fm.Sensitivity, RetentionRule: fm.RetentionRule}, nil
}

func frontmatter(data []byte) []byte {
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if len(lines) < 3 || lines[0] != "---" {
		return nil
	}
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			return []byte(strings.Join(lines[1:i], "\n"))
		}
	}
	return nil
}

// StoreRaw stores a raw source under datasets/<slug>/ using the vault asset
// writer, retaining collision-safe naming and atomic writes.
func StoreRaw(vaultRoot, slug, preferredName string, data []byte, now time.Time) (string, error) {
	if strings.TrimSpace(slug) == "" {
		return "", errors.New("dataset slug is required")
	}
	base := filepath.Base(preferredName)
	if base == "." || base == string(filepath.Separator) || base == "" {
		base = "source.csv"
	}
	ext := filepath.Ext(base)
	if ext == "" {
		ext = ".csv"
	}
	return vault.StoreAsset(vaultRoot, data, base, ext, filepath.Join(RawDir, slug), now)
}

// ReadRawFiles returns all CSVs in datasets/<slug> in stable path order.
func ReadRawFiles(vaultRoot, slug string, schema map[string]dbviews.PropertyConfig, identityField string) ([]Row, error) {
	dir, err := vault.SecurePath(vaultRoot, filepath.Join(RawDir, slug))
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []Row{}, nil
		}
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	all := make([]Row, 0)
	for _, entry := range entries {
		if entry.IsDir() || strings.ToLower(filepath.Ext(entry.Name())) != ".csv" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path) //nolint:gosec // path is confined by SecurePath above
		if err != nil {
			return nil, err
		}
		rows, _, err := ParseCSV(strings.NewReader(string(data)), schema, identityField)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", entry.Name(), err)
		}
		rel, _ := filepath.Rel(vaultRoot, path)
		for i := range rows {
			rows[i].SourcePath = filepath.ToSlash(rel)
		}
		all = append(all, rows...)
	}
	return all, nil
}

func RowsJSON(rows []Row) (string, error) {
	data, err := json.Marshal(rows)
	return string(data), err
}
