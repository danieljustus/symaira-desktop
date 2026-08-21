package mcp

import (
	"encoding/json"
	"testing"
)

func TestParseMCPDocumentIDs_Empty(t *testing.T) {
	ids, err := parseMCPDocumentIDs(nil)
	if err != nil {
		t.Fatalf("parseMCPDocumentIDs(nil): %v", err)
	}
	if ids != nil {
		t.Errorf("ids = %v, want nil", ids)
	}
}

func TestParseMCPDocumentIDs_Null(t *testing.T) {
	ids, err := parseMCPDocumentIDs(json.RawMessage("null"))
	if err != nil {
		t.Fatalf("parseMCPDocumentIDs(null): %v", err)
	}
	if ids != nil {
		t.Errorf("ids = %v, want nil", ids)
	}
}

func TestParseMCPDocumentIDs_Array(t *testing.T) {
	ids, err := parseMCPDocumentIDs(json.RawMessage("[1, 2, 3]"))
	if err != nil {
		t.Fatalf("parseMCPDocumentIDs: %v", err)
	}
	if len(ids) != 3 {
		t.Fatalf("len(ids) = %d, want 3", len(ids))
	}
	want := []int{1, 2, 3}
	for i, id := range want {
		if ids[i] != id {
			t.Errorf("ids[%d] = %d, want %d", i, ids[i], id)
		}
	}
}

func TestParseMCPDocumentIDs_ArrayWithZero(t *testing.T) {
	_, err := parseMCPDocumentIDs(json.RawMessage("[1, 0, 3]"))
	if err == nil {
		t.Fatal("expected error for zero ID")
	}
	if err.Error() == "" {
		t.Error("error message is empty")
	}
}

func TestParseMCPDocumentIDs_ArrayWithNegative(t *testing.T) {
	_, err := parseMCPDocumentIDs(json.RawMessage("[1, -1, 3]"))
	if err == nil {
		t.Fatal("expected error for negative ID")
	}
}

func TestParseMCPDocumentIDs_String(t *testing.T) {
	ids, err := parseMCPDocumentIDs(json.RawMessage(`"1,2,3"`))
	if err != nil {
		t.Fatalf("parseMCPDocumentIDs: %v", err)
	}
	if len(ids) != 3 {
		t.Fatalf("len(ids) = %d, want 3", len(ids))
	}
	want := []int{1, 2, 3}
	for i, id := range want {
		if ids[i] != id {
			t.Errorf("ids[%d] = %d, want %d", i, ids[i], id)
		}
	}
}

func TestParseMCPDocumentIDs_StringWithSpaces(t *testing.T) {
	ids, err := parseMCPDocumentIDs(json.RawMessage(`" 1 , 2 , 3 "`))
	if err != nil {
		t.Fatalf("parseMCPDocumentIDs: %v", err)
	}
	if len(ids) != 3 {
		t.Fatalf("len(ids) = %d, want 3", len(ids))
	}
}

func TestParseMCPDocumentIDs_EmptyString(t *testing.T) {
	ids, err := parseMCPDocumentIDs(json.RawMessage(`""`))
	if err != nil {
		t.Fatalf("parseMCPDocumentIDs: %v", err)
	}
	if ids != nil {
		t.Errorf("ids = %v, want nil for empty string", ids)
	}
}

func TestParseMCPDocumentIDs_StringWithInvalid(t *testing.T) {
	_, err := parseMCPDocumentIDs(json.RawMessage(`"1,abc,3"`))
	if err == nil {
		t.Fatal("expected error for non-numeric ID")
	}
}

func TestParseMCPDocumentIDs_StringWithZero(t *testing.T) {
	_, err := parseMCPDocumentIDs(json.RawMessage(`"1,0,3"`))
	if err == nil {
		t.Fatal("expected error for zero ID")
	}
}

func TestParseMCPDocumentIDs_InvalidType(t *testing.T) {
	_, err := parseMCPDocumentIDs(json.RawMessage(`{"foo": "bar"}`))
	if err == nil {
		t.Fatal("expected error for invalid type")
	}
}
