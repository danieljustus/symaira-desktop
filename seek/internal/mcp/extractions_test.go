package mcp

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-seek/internal/db"
)

func TestSearchExtractions_ReturnsValidJSONString(t *testing.T) {
	store := &fakeStore{
		searchExtractionsFunc: func(query string, limit int) ([]*db.Extraction, error) {
			return []*db.Extraction{
				{ID: 1, DocumentPath: "/docs/invoice.md", Class: "amount", Value: "$500.00", EvidenceText: "Total: $500.00", Matched: true},
			}, nil
		},
	}
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	got, isError := callTool(t, server, "search_extractions", map[string]interface{}{"query": "invoice"})
	if isError {
		t.Fatalf("search_extractions failed: %s", got)
	}

	var results []*db.Extraction
	if err := json.Unmarshal([]byte(got), &results); err != nil {
		t.Fatalf("expected valid JSON string content, got %q: %v", got, err)
	}
	if len(results) != 1 || results[0].Class != "amount" {
		t.Errorf("unexpected results: %+v", results)
	}
}

func TestSearchExtractions_MissingQuery(t *testing.T) {
	store := &fakeStore{}
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	_, isError := callTool(t, server, "search_extractions", map[string]interface{}{})
	if !isError {
		t.Fatal("expected error for missing query")
	}
}

func TestListExtractions_FilterByClassReturnsValidJSONString(t *testing.T) {
	store := &fakeStore{
		listExtractionsFunc: func(class string, limit int) ([]*db.Extraction, error) {
			if class != "amount" {
				t.Errorf("expected class filter 'amount', got %q", class)
			}
			return []*db.Extraction{
				{ID: 2, DocumentPath: "/docs/a.md", Class: "amount", Value: "$1.00"},
			}, nil
		},
	}
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	got, isError := callTool(t, server, "list_extractions", map[string]interface{}{"class": "amount"})
	if isError {
		t.Fatalf("list_extractions failed: %s", got)
	}

	var results []*db.Extraction
	if err := json.Unmarshal([]byte(got), &results); err != nil {
		t.Fatalf("expected valid JSON string content, got %q: %v", got, err)
	}
	if len(results) != 1 || results[0].Value != "$1.00" {
		t.Errorf("unexpected results: %+v", results)
	}
}

func TestListExtractions_NoClassFilter(t *testing.T) {
	store := &fakeStore{
		listExtractionsFunc: func(class string, limit int) ([]*db.Extraction, error) {
			return []*db.Extraction{}, nil
		},
	}
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	got, isError := callTool(t, server, "list_extractions", map[string]interface{}{})
	if isError {
		t.Fatalf("list_extractions failed: %s", got)
	}
	if strings.TrimSpace(got) != "[]" {
		t.Errorf("expected empty JSON array, got %q", got)
	}
}

func TestGetDocumentExtractions_ReturnsValidJSONString(t *testing.T) {
	store := &fakeStore{
		getDocExtractionsFunc: func(docPath string) ([]*db.Extraction, error) {
			return []*db.Extraction{
				{ID: 3, DocumentPath: docPath, Class: "deadline", Value: "2026-08-01"},
			}, nil
		},
	}
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	homeEnv := t.TempDir()
	t.Setenv("HOME", homeEnv)
	if err := os.MkdirAll(homeEnv+"/notes", 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	docPath := homeEnv + "/notes/invoice.md"
	if err := os.WriteFile(docPath, []byte("content"), 0644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	got, isError := callTool(t, server, "get_document_extractions", map[string]interface{}{"path": docPath})
	if isError {
		t.Fatalf("get_document_extractions failed: %s", got)
	}

	var results []*db.Extraction
	if err := json.Unmarshal([]byte(got), &results); err != nil {
		t.Fatalf("expected valid JSON string content, got %q: %v", got, err)
	}
	if len(results) != 1 || results[0].Class != "deadline" {
		t.Errorf("unexpected results: %+v", results)
	}
}

func TestGetDocumentExtractions_MissingPath(t *testing.T) {
	store := &fakeStore{}
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	_, isError := callTool(t, server, "get_document_extractions", map[string]interface{}{})
	if !isError {
		t.Fatal("expected error for missing path")
	}
}

func TestGetDocumentExtractions_FileNotFound(t *testing.T) {
	store := &fakeStore{}
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	homeEnv := t.TempDir()
	t.Setenv("HOME", homeEnv)

	_, isError := callTool(t, server, "get_document_extractions", map[string]interface{}{"path": homeEnv + "/does-not-exist.md"})
	if !isError {
		t.Fatal("expected error for nonexistent document path")
	}
}
