package engine

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/vault"
)

type portMetadataCase struct {
	Name      string                `json:"name"`
	Document  *vault.Document       `json:"document,omitempty"`
	Fields    []SearchMetadataField `json:"fields,omitempty"`
	Formatted string                `json:"formatted"`
	Queries   map[string][]string   `json:"queries"`
	Stripped  string                `json:"stripped"`
}

type portMetadataFixture struct {
	SchemaVersion int                `json:"schema_version"`
	Cases         []portMetadataCase `json:"cases"`
}

func TestSearchMetadataInventory(t *testing.T) {
	asn := 42
	doc := &vault.Document{
		Title: "  Rechnung   Müller  ", Tags: []string{"invoice", "überfällig"}, Aliases: []string{"Bill", "Faktura"},
		Created: "2026-09-06", DocumentDate: "2026-08-31", Person: "Alice", Status: "open", DueDate: "2026-09-30",
		OcrJSONPath: "ocr/42.json", Simhash: "abc123", Type: "document", ASN: &asn,
		Frontmatter: map[string]interface{}{
			"document_type": "invoice", "correspondent": "Müller GmbH", "source_path": "inbox/scan.pdf",
			"confidence": 91, "meeting_id": "meet-7", "empty": "ignored",
		},
	}
	metadata := SearchMetadataFromVault(doc)
	formatted := formatSearchMetadata(metadata)
	queries := map[string][]string{}
	for _, query := range []string{"rechnung", "überfällig alice", "invoice", "missing", "\"müller\""} {
		queries[query] = MetadataMatches(query, formatted+"\nBODY")
	}
	custom := SearchMetadata{Fields: []SearchMetadataField{
		{Name: "zeta", Value: " many\tspaces ", Weight: 0},
		{Name: "title", Value: "Title", Weight: 9},
		{Name: "aliases", Value: "Alias", Weight: 2},
		{Name: "", Value: "ignored", Weight: 1},
		{Name: "status", Value: "", Weight: 1},
	}}
	customFormatted := formatSearchMetadata(custom)
	fixture := portMetadataFixture{SchemaVersion: 1, Cases: []portMetadataCase{
		{Name: "vault-document", Document: doc, Fields: metadata.Fields, Formatted: formatted, Queries: queries, Stripped: StripSearchMetadata(formatted + "\nBODY")},
		{Name: "weight-normalization-and-order", Fields: custom.Fields, Formatted: customFormatted, Queries: map[string][]string{"alias spaces": MetadataMatches("alias spaces", customFormatted)}, Stripped: StripSearchMetadata(customFormatted + "\nBODY")},
		{Name: "empty", Fields: []SearchMetadataField{}, Formatted: formatSearchMetadata(SearchMetadata{}), Queries: map[string][]string{"x": MetadataMatches("x", "BODY")}, Stripped: StripSearchMetadata("BODY")},
	}}
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	fixturePath := filepath.Join("..", "..", "..", "..", "testdata", "port", "vault", "metadata.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.WriteFile(fixturePath, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	//nolint:gosec // fixturePath is a fixed repo-relative path
	current, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, encoded) {
		t.Fatal("search metadata fixture is stale; run make port-fixtures-generate")
	}
}
