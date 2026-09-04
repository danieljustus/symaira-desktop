package dataset

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/dbviews"
)

func TestPolicyValidationNormalizesAndRejectsUnsafeReferences(t *testing.T) {
	for input, want := range map[string]string{
		"":             DefaultSensitivity,
		"  INTERNAL ":  SensitivityInternal,
		"Confidential": SensitivityConfidential,
		"restricted":   SensitivityRestricted,
		"public":       SensitivityPublic,
	} {
		got, err := NormalizeSensitivity(input)
		if err != nil || got != want {
			t.Errorf("NormalizeSensitivity(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	if _, err := NormalizeSensitivity("secret"); err == nil || !strings.Contains(err.Error(), "invalid dataset sensitivity") {
		t.Fatalf("invalid sensitivity was accepted: %v", err)
	}
	for _, input := range []string{"", "foo/bar", "foo\\bar", "foo\nbar", "foo	bar"} {
		if err := ValidateRetentionRuleReference(input); err == nil {
			t.Errorf("ValidateRetentionRuleReference(%q) accepted unsafe value", input)
		}
	}
	if err := ValidateSensitivity(""); err == nil {
		t.Fatal("empty persisted sensitivity was accepted")
	}
	if err := ValidateSensitivity("INTERNAL"); err == nil {
		t.Fatal("case-variant persisted sensitivity was accepted")
	}
}

func TestParseCSVRejectsMalformedStructureAndTypedValues(t *testing.T) {
	tests := []struct {
		name string
		csv  string
		decl map[string]dbviews.PropertyConfig
		id   string
		want string
	}{
		{"empty", "", nil, "", "csv is empty"},
		{"empty header", ",value\n1,2\n", nil, "", "empty name"},
		{"duplicate header", "ID,id\na,b\n", nil, "", "duplicate"},
		{"missing identity", "id,value\na,b\n", nil, "account", "identity field"},
		{"ragged row", "id,value\na\n", nil, "", "read csv"},
		{"invalid number", "id,value\na,not-a-number\n", map[string]dbviews.PropertyConfig{"value": {Type: "number"}}, "", "invalid number"},
		{"invalid bool", "id,enabled\na,perhaps\n", map[string]dbviews.PropertyConfig{"enabled": {Type: "checkbox"}}, "", "invalid boolean"},
		{"invalid date", "id,when\na,2026/01/02\n", map[string]dbviews.PropertyConfig{"when": {Type: "date"}}, "", "invalid date"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := ParseCSV(strings.NewReader(tt.csv), tt.decl, tt.id)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ParseCSV error = %v, want substring %q", err, tt.want)
			}
		})
	}
	if _, _, err := ParseCSV(nil, nil, ""); err == nil || !strings.Contains(err.Error(), "reader") {
		t.Fatalf("nil reader error = %v", err)
	}
	if _, _, err := ParseCSV(strings.NewReader("id\n\"unterminated\n"), nil, ""); err == nil || !strings.Contains(err.Error(), "read csv") {
		t.Fatalf("malformed CSV error = %v", err)
	}
}

func TestParseCSVInfersEmptyAndAlternateDateColumns(t *testing.T) {
	rows, schema, err := ParseCSV(strings.NewReader("empty,when\n,2026-01-02 15:04\n,2026-01-02T15:04:05Z\n"), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || schema["empty"].Type != "text" || schema["when"].Type != "date" {
		t.Fatalf("unexpected inferred schema: %#v, rows=%d", schema, len(rows))
	}
	if rows[0].Values["empty"] != "" || rows[0].Values["when"] != "2026-01-02 15:04" {
		t.Fatalf("unexpected converted values: %#v", rows[0].Values)
	}
}

func TestHandleRenderAndParseRejectMalformedPersistedMetadata(t *testing.T) {
	valid := &Handle{Slug: "orders", Title: "Orders", Source: "datasets/orders/source.csv", Sensitivity: SensitivityInternal, RetentionRule: DefaultRetentionRule}
	for name, handle := range map[string]*Handle{
		"nil":         nil,
		"no identity": {Title: "Orders", Source: valid.Source, Sensitivity: valid.Sensitivity, RetentionRule: valid.RetentionRule},
		"no title":    {Slug: valid.Slug, Source: valid.Source, Sensitivity: valid.Sensitivity, RetentionRule: valid.RetentionRule},
		"no source":   {Slug: valid.Slug, Title: valid.Title, Sensitivity: valid.Sensitivity, RetentionRule: valid.RetentionRule},
	} {
		t.Run("render "+name, func(t *testing.T) {
			if _, err := handle.Render(); err == nil {
				t.Fatal("malformed handle rendered successfully")
			}
		})
	}
	encoded, err := valid.Render()
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"wrong type":     strings.Replace(string(encoded), "type: dataset", "type: note", 1),
		"missing type":   strings.Replace(string(encoded), "type: dataset\n", "", 1),
		"missing source": strings.Replace(string(encoded), "source: datasets/orders/source.csv", "source: \"\"", 1),
		"missing title":  strings.Replace(string(encoded), "title: Orders", "title: \"\"", 1),
		"missing slug":   strings.Replace(string(encoded), "dataset_id: orders", "dataset_id: \"\"", 1),
		"partial policy": strings.Replace(string(encoded), "retention_rule: default\n", "", 1),
		"bad retention":  strings.Replace(string(encoded), "retention_rule: default", "retention_rule: bad/rule", 1),
	} {
		t.Run("parse "+name, func(t *testing.T) {
			if _, err := ParseHandle("datasets/orders.md", []byte(data)); err == nil {
				t.Fatal("malformed handle parsed successfully")
			}
		})
	}
	if _, err := ParseHandle("datasets/orders.md", []byte("not frontmatter\n")); err == nil {
		t.Fatal("non-frontmatter document parsed successfully")
	}
}

func TestStoreRawCollisionsAndReadRawFilesAreSafeAndStable(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	if _, err := StoreRaw(root, "", "source.csv", []byte("id\na\n"), now); err == nil {
		t.Fatal("empty slug was accepted")
	}
	first, err := StoreRaw(root, "orders", "", []byte("id,amount\na,1\n"), now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := StoreRaw(root, "orders", "snapshot", []byte("id,amount\nb,2\n"), now)
	if err != nil {
		t.Fatal(err)
	}
	third, err := StoreRaw(root, "orders", filepath.Join("nested", "snapshot.csv"), []byte("id,amount\nc,3\n"), now)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || second == third || first == third {
		t.Fatalf("asset names collided: %q %q %q", first, second, third)
	}
	for _, rel := range []string{first, second, third} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("stored asset %q missing: %v", rel, err)
		}
	}
	junkDir := filepath.Join(root, RawDir, "orders", "nested-dir")
	if err := os.MkdirAll(junkDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, RawDir, "orders", "readme.txt"), []byte("ignore"), 0600); err != nil {
		t.Fatal(err)
	}
	rows, err := ReadRawFiles(root, "orders", map[string]dbviews.PropertyConfig{"id": {Type: "text"}, "amount": {Type: "number"}}, "id")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].SourcePath > rows[1].SourcePath || rows[1].SourcePath > rows[2].SourcePath {
		t.Fatalf("raw files were not read in stable order: %#v", rows)
	}
	if empty, err := ReadRawFiles(root, "missing", nil, ""); err != nil || len(empty) != 0 {
		t.Fatalf("missing raw directory = %#v, %v", empty, err)
	}
	if _, err := ReadRawFiles(root, "../../outside", nil, ""); err == nil {
		t.Fatal("raw path traversal was accepted")
	}
	badPath := filepath.Join(root, RawDir, "bad")
	if err := os.MkdirAll(badPath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badPath, "broken.csv"), []byte("id,amount\na,not-number\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRawFiles(root, "bad", map[string]dbviews.PropertyConfig{"amount": {Type: "number"}}, ""); err == nil || !strings.Contains(err.Error(), "broken.csv") {
		t.Fatalf("broken raw CSV error = %v", err)
	}
}
