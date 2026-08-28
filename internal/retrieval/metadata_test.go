package retrieval

import (
	"reflect"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/vault"
)

func TestSearchMetadataFromVaultUsesContractOrderAndWeights(t *testing.T) {
	asn := 42
	doc := &vault.Document{
		Title:        "Invoice",
		Tags:         []string{"finance", "urgent"},
		Aliases:      []string{"Bill"},
		Created:      "2026-08-28T10:00:00Z",
		DocumentDate: "2026-08-01",
		Person:       "Alex",
		Status:       "open",
		DueDate:      "2026-09-01",
		ASN:          &asn,
		Type:         "document",
		Frontmatter: map[string]interface{}{
			"document_type": "invoice",
			"correspondent": "Musterfirma",
			"confidence":    float64(95),
		},
	}
	got := SearchMetadataFromVault(doc)
	want := SearchMetadata{Fields: []SearchMetadataField{
		{Name: "title", Value: "Invoice", Weight: 3},
		{Name: "tags", Value: "finance urgent", Weight: 3},
		{Name: "aliases", Value: "Bill", Weight: 2},
		{Name: "created", Value: "2026-08-28T10:00:00Z", Weight: 1},
		{Name: "document_date", Value: "2026-08-01", Weight: 1},
		{Name: "person", Value: "Alex", Weight: 1},
		{Name: "status", Value: "open", Weight: 1},
		{Name: "due_date", Value: "2026-09-01", Weight: 1},
		{Name: "type", Value: "document", Weight: 1},
		{Name: "asn", Value: "42", Weight: 1},
		{Name: "document_type", Value: "invoice", Weight: 1},
		{Name: "correspondent", Value: "Musterfirma", Weight: 1},
		{Name: "confidence", Value: "95", Weight: 1},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("metadata = %#v, want %#v", got, want)
	}
}
