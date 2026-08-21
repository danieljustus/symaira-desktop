package engine

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestExpandPrompt_ContainsQuery(t *testing.T) {
	prompt := expandPrompt("renewable energy optimization")
	if !strings.Contains(prompt, "renewable energy optimization") {
		t.Error("expandPrompt should contain the query")
	}
	if !strings.Contains(prompt, "passage") {
		t.Error("expandPrompt should mention passage")
	}
}

func TestTrimPassage_ShortText(t *testing.T) {
	got := trimPassage("hello", 10)
	if got != "hello" {
		t.Errorf("expected unchanged text, got %q", got)
	}
}

func TestTrimPassage_LongText(t *testing.T) {
	got := trimPassage("one two three four five", 11)
	if got != "one two" {
		t.Errorf("expected trimmed at word boundary, got %q", got)
	}
}

func TestTrimPassage_NoSpaces(t *testing.T) {
	got := trimPassage("abcdefghij", 5)
	if got != "abcde" {
		t.Errorf("expected hard truncate, got %q", got)
	}
}

func TestTrimPassage_ExactLength(t *testing.T) {
	got := trimPassage("abcde", 5)
	if got != "abcde" {
		t.Errorf("expected exact text, got %q", got)
	}
}

func TestAverageVecs_EqualLength(t *testing.T) {
	a := []float32{1.0, 2.0, 3.0}
	b := []float32{3.0, 4.0, 5.0}
	got := averageVecs(a, b)
	expected := []float32{2.0, 3.0, 4.0}
	if len(got) != len(expected) {
		t.Fatalf("len: got %d, want %d", len(got), len(expected))
	}
	for i := range got {
		if diff := got[i] - expected[i]; diff > 0.001 || diff < -0.001 {
			t.Errorf("[%d]: got %v, want %v", i, got[i], expected[i])
		}
	}
}

func TestAverageVecs_DifferentLengths(t *testing.T) {
	a := []float32{1.0, 2.0}
	b := []float32{1.0, 2.0, 3.0}
	got := averageVecs(a, b)
	if len(got) != 2 || got[0] != 1.0 || got[1] != 2.0 {
		t.Error("different lengths should return first vector unchanged")
	}
}

func TestAverageVecs_NilB(t *testing.T) {
	a := []float32{1.0, 2.0}
	got := averageVecs(a, nil)
	if len(got) != 2 || got[0] != 1.0 || got[1] != 2.0 {
		t.Error("nil b should return a unchanged")
	}
}

func TestAverageVecs_EmptyA(t *testing.T) {
	got := averageVecs(nil, []float32{1.0})
	if got != nil {
		t.Error("empty a should return nil")
	}
}

func TestExpandQuery_Success(t *testing.T) {
	cfg := ExpandConfig{
		Enabled: true,
		URL:     "http://test/api",
		Model:   "test-model",
		Timeout: 10 * time.Second,
	}
	chatFn := func(url, model, prompt string, timeout time.Duration) (string, error) {
		if url != "http://test/api" {
			t.Errorf("expected URL http://test/api, got %q", url)
		}
		if model != "test-model" {
			t.Errorf("expected model test-model, got %q", model)
		}
		return "Renewable energy optimization involves using advanced algorithms to maximize the efficiency of solar and wind power systems.", nil
	}
	got, err := expandQuery(chatFn, cfg, "renewable energy")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "Renewable energy") {
		t.Error("expected passage to contain the generated text")
	}
}

func TestExpandQuery_ChatError(t *testing.T) {
	cfg := ExpandConfig{
		Enabled: true,
		URL:     "http://test/api",
		Model:   "test-model",
		Timeout: 10 * time.Second,
	}
	chatFn := func(url, model, prompt string, timeout time.Duration) (string, error) {
		return "", fmt.Errorf("ollama offline")
	}
	got, err := expandQuery(chatFn, cfg, "test query")
	if err == nil {
		t.Error("expected error for chat failure")
	}
	if got != "" {
		t.Error("expected empty string on failure")
	}
}

func TestExpandQuery_EmptyResponse(t *testing.T) {
	cfg := ExpandConfig{
		Enabled: true,
		URL:     "http://test/api",
		Model:   "test-model",
		Timeout: 10 * time.Second,
	}
	chatFn := func(url, model, prompt string, timeout time.Duration) (string, error) {
		return "   ", nil
	}
	_, err := expandQuery(chatFn, cfg, "test query")
	if err == nil {
		t.Error("expected error for empty response")
	}
}

func TestExpander_Expand_Success(t *testing.T) {
	cfg := ExpandConfig{
		Enabled: true,
		URL:     "http://test/api",
		Model:   "test-model",
		Timeout: 10 * time.Second,
	}
	e := NewExpander(cfg)
	e.chatFn = func(url, model, prompt string, timeout time.Duration) (string, error) {
		return "This is a hypothetical passage about the query topic.", nil
	}
	got, err := e.Expand("test query")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "hypothetical passage") {
		t.Error("expected passage content")
	}
}

func TestExpander_Expand_Failure(t *testing.T) {
	cfg := ExpandConfig{
		Enabled: true,
		URL:     "http://test/api",
		Model:   "test-model",
		Timeout: 10 * time.Second,
	}
	e := NewExpander(cfg)
	e.chatFn = func(url, model, prompt string, timeout time.Duration) (string, error) {
		return "", fmt.Errorf("connection refused")
	}
	_, err := e.Expand("test query")
	if err == nil {
		t.Error("expected error for chat failure")
	}
}

func TestComputeExpandedVec_ExpandedTextEmpty(t *testing.T) {
	embedder := NewEmbeddingsGenerator()
	queryVec := []float32{1.0, 0.0, 0.0}
	got := computeExpandedVec(embedder, queryVec, "")
	if len(got) != 3 || got[0] != 1.0 {
		t.Error("empty expanded text should return original query vector")
	}
}

func TestComputeExpandedVec_WithExpandedText(t *testing.T) {
	embedder := NewEmbeddingsGenerator()
	queryVec := make([]float32, defaultEmbeddingDim)
	queryVec[0] = 1.0
	expandedVec := computeExpandedVec(embedder, queryVec, "renewable energy optimization")
	if len(expandedVec) != len(queryVec) {
		t.Fatalf("expected vector of length %d, got %d", len(queryVec), len(expandedVec))
	}
	sameAsQuery := true
	for i := range expandedVec {
		if expandedVec[i] != queryVec[i] {
			sameAsQuery = false
			break
		}
	}
	if sameAsQuery {
		t.Error("expanded vector should differ from original query vector")
	}
}
