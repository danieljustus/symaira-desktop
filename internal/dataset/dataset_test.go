package dataset

import (
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/dbviews"
)

func TestParseCSVInfersTypedColumnsAndCanonicalIdentity(t *testing.T) {
	rows, schema, err := ParseCSV(strings.NewReader("id,date,amount,paid,note\nA,2026-01-02,12.50,true,hello\nB,2026-01-03,8,false,world\n"), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || schema["amount"].Type != "number" || schema["date"].Type != "date" || schema["paid"].Type != "checkbox" || schema["note"].Type != "text" {
		t.Fatalf("unexpected schema or row count: %#v %d", schema, len(rows))
	}
	if rows[0].Key != "hash:"+CanonicalRowHash([]string{"id", "date", "amount", "paid", "note"}, map[string]string{"id": "A", "date": "2026-01-02", "amount": "12.50", "paid": "true", "note": "hello"}) {
		t.Fatalf("unexpected canonical row key: %q", rows[0].Key)
	}
	if amount, ok := rows[0].Values["amount"].(float64); !ok || amount != 12.5 {
		t.Fatalf("amount was not typed: %#v", rows[0].Values["amount"])
	}
}

func TestParseCSVUsesExplicitIdentityFieldAndDeclaredTypes(t *testing.T) {
	declared := map[string]dbviews.PropertyConfig{"id": {Type: "text"}, "amount": {Type: "number"}}
	rows, schema, err := ParseCSV(strings.NewReader("id,amount\ntransaction-1,12\ntransaction-1,13\n"), declared, "id")
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Key != "identity:transaction-1" || rows[1].Key != rows[0].Key {
		t.Fatalf("identity field was not used: %#v", rows)
	}
	if schema["id"].Type != "text" || schema["amount"].Type != "number" {
		t.Fatalf("declared schema changed unexpectedly: %#v", schema)
	}
}

func TestHandleRoundTrip(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	handle := &Handle{
		Slug: "orders", Title: "Orders", Created: now.Format(time.RFC3339), Source: "datasets/orders/source.csv",
		Schema:        map[string]dbviews.PropertyConfig{"amount": {Type: "number", Label: "Amount"}},
		Coverage:      Coverage{From: "2026-01-01", To: "2026-01-31"},
		Provenance:    Provenance{ImportedAt: now.Format(time.RFC3339), SourceName: "source.csv", SourceSHA256: "abc"},
		IdentityField: "id", Sensitivity: "sensitive",
	}
	encoded, err := handle.Render()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseHandle("datasets/orders.md", encoded)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Slug != handle.Slug || parsed.Source != handle.Source || parsed.Schema["amount"].Type != "number" || parsed.IdentityField != "id" || parsed.Sensitivity != "sensitive" {
		t.Fatalf("handle did not round-trip: %#v", parsed)
	}
}
