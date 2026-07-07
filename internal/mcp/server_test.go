package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestSearchToolValidation(t *testing.T) {
	tool := newSearchTool(nil) // factory not needed for param validation
	_, err := tool.Handler(context.Background(), json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "query is required") {
		t.Errorf("expected 'query is required' error, got %v", err)
	}
}

func TestPropsToolValidation(t *testing.T) {
	tool := newPropsTool(nil)
	_, err := tool.Handler(context.Background(), json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "file is required") {
		t.Errorf("expected 'file is required' error, got %v", err)
	}
}

func TestBacklinksToolValidation(t *testing.T) {
	tool := newBacklinksTool(nil)
	_, err := tool.Handler(context.Background(), json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "file is required") {
		t.Errorf("expected 'file is required' error, got %v", err)
	}
}

func TestNoteNewToolValidation(t *testing.T) {
	tool := newNoteNewTool(nil)
	_, err := tool.Handler(context.Background(), json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "title is required") {
		t.Errorf("expected 'title is required' error, got %v", err)
	}
}

func TestLsToolValidation(t *testing.T) {
	tool := newLsTool(nil)
	_, err := tool.Handler(context.Background(), json.RawMessage(`{invalid`))
	if err == nil {
		t.Error("expected json unmarshal error")
	}
}
