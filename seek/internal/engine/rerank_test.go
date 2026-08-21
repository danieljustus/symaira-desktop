package engine

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-seek/internal/db"
)

func TestParseScores_Valid(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []int
	}{
		{"plain JSON", "[95, 42, 80]", []int{95, 42, 80}},
		{"with markdown fence", "```json\n[95, 42, 80]\n```", []int{95, 42, 80}},
		{"with surrounding text", "Here are the scores: [95, 42, 80]", []int{95, 42, 80}},
		{"single element", "[100]", []int{100}},
		{"zeros", "[0, 0, 0]", []int{0, 0, 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseScores(tt.input)
			if err != nil {
				t.Fatalf("parseScores(%q) error: %v", tt.input, err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("len: got %d, want %d", len(got), len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("[%d]: got %d, want %d", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestParseScores_Invalid(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"empty string", ""},
		{"no array", "no scores here"},
		{"broken JSON", "[1, 2,]"},
		{"missing close bracket", "[1, 2, 3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseScores(tt.input)
			if err == nil {
				t.Errorf("parseScores(%q) expected error, got nil", tt.input)
			}
		})
	}
}

func TestBlendScore(t *testing.T) {
	tests := []struct {
		name     string
		rrf      float32
		reranker float32
		pos      int
		want     float32
	}{
		{"top result favors RRF", 1.0, 0.0, 0, 0.7},
		{"top result zero reranker", 1.0, 0.0, 0, 0.7},
		{"deep result favors reranker", 0.0, 1.0, 10, 0.7},
		{"position 0 equal blend", 0.5, 0.5, 0, 0.5},
		{"position 5 transition", 1.0, 0.0, 5, 0.45},
		{"position 9 floor", 1.0, 0.0, 9, 0.3},
		{"position 20 floor", 1.0, 0.0, 20, 0.3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := blendScore(tt.rrf, tt.reranker, tt.pos)
			if diff := got - tt.want; diff > 0.001 || diff < -0.001 {
				t.Errorf("blendScore(%v, %v, %d) = %v, want %v", tt.rrf, tt.reranker, tt.pos, got, tt.want)
			}
		})
	}
}

func TestShouldSkipRerank(t *testing.T) {
	tests := []struct {
		name    string
		results []*db.SearchResult
		want    bool
	}{
		{"empty", nil, false},
		{"top BM25 rank 1 high cosine", []*db.SearchResult{{BM25Rank: 1, CosineScore: 0.9}}, true},
		{"top BM25 rank 1 low cosine", []*db.SearchResult{{BM25Rank: 1, CosineScore: 0.5}}, false},
		{"top BM25 rank 2 high cosine", []*db.SearchResult{{BM25Rank: 2, CosineScore: 0.9}}, false},
		{"top exactly at threshold", []*db.SearchResult{{BM25Rank: 1, CosineScore: 0.85}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldSkipRerank(tt.results)
			if got != tt.want {
				t.Errorf("shouldSkipRerank() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRerankResults_DisabledReturnsOriginal(t *testing.T) {
	results := []*db.SearchResult{
		{Chunk: &db.Chunk{UUID: "a"}, RRFScore: 0.5},
		{Chunk: &db.Chunk{UUID: "b"}, RRFScore: 0.3},
	}
	r := NewReranker(RerankConfig{Enabled: false})
	got := r.RerankResults("query", results)
	if len(got) != 2 || got[0].Chunk.UUID != "a" {
		t.Error("disabled reranker should return original results unchanged")
	}
}

func TestRerankResults_EmptyReturnsOriginal(t *testing.T) {
	r := NewReranker(RerankConfig{Enabled: true})
	got := r.RerankResults("query", nil)
	if got != nil {
		t.Error("empty results should return nil")
	}
}

func TestRerankResults_FallbackOnChatError(t *testing.T) {
	results := []*db.SearchResult{
		{Chunk: &db.Chunk{UUID: "a"}, RRFScore: 0.5, BM25Rank: 5, CosineScore: 0.3},
	}
	r := NewReranker(RerankConfig{Enabled: true})
	r.chatFn = func(url, model, prompt string, timeout time.Duration) (string, error) {
		return "", fmt.Errorf("ollama offline")
	}
	got := r.RerankResults("query", results)
	if len(got) != 1 || got[0].Chunk.UUID != "a" {
		t.Error("chat error should fall back to original results")
	}
}

func TestRerankResults_FallbackOnBadResponse(t *testing.T) {
	results := []*db.SearchResult{
		{Chunk: &db.Chunk{UUID: "a"}, RRFScore: 0.5, BM25Rank: 5, CosineScore: 0.3},
	}
	r := NewReranker(RerankConfig{Enabled: true})
	r.chatFn = func(url, model, prompt string, timeout time.Duration) (string, error) {
		return "I cannot do that", nil
	}
	got := r.RerankResults("query", results)
	if len(got) != 1 || got[0].Chunk.UUID != "a" {
		t.Error("bad response should fall back to original results")
	}
}

func TestRerankResults_FallbackOnTooFewScores(t *testing.T) {
	results := []*db.SearchResult{
		{Chunk: &db.Chunk{UUID: "a"}, RRFScore: 0.5, BM25Rank: 5, CosineScore: 0.3},
		{Chunk: &db.Chunk{UUID: "b"}, RRFScore: 0.3, BM25Rank: 10, CosineScore: 0.2},
	}
	r := NewReranker(RerankConfig{Enabled: true})
	r.chatFn = func(url, model, prompt string, timeout time.Duration) (string, error) {
		return "[90]", nil
	}
	got := r.RerankResults("query", results)
	if len(got) != 2 || got[0].Chunk.UUID != "a" {
		t.Error("too few scores should fall back to original results")
	}
}

func TestRerankResults_SkipsOnStrongSignal(t *testing.T) {
	results := []*db.SearchResult{
		{Chunk: &db.Chunk{UUID: "a"}, RRFScore: 0.5, BM25Rank: 1, CosineScore: 0.9},
	}
	r := NewReranker(RerankConfig{Enabled: true})
	chatCalled := false
	r.chatFn = func(url, model, prompt string, timeout time.Duration) (string, error) {
		chatCalled = true
		return "[50]", nil
	}
	got := r.RerankResults("query", results)
	if chatCalled {
		t.Error("should skip reranking for strong signal")
	}
	if len(got) != 1 || got[0].Chunk.UUID != "a" {
		t.Error("should return original results")
	}
}

func TestRerankResults_ReranksAndReorders(t *testing.T) {
	results := []*db.SearchResult{
		{Chunk: &db.Chunk{UUID: "a"}, RRFScore: 0.5, BM25Rank: 3, CosineScore: 0.3},
		{Chunk: &db.Chunk{UUID: "b"}, RRFScore: 0.45, BM25Rank: 5, CosineScore: 0.2},
		{Chunk: &db.Chunk{UUID: "c"}, RRFScore: 0.1, BM25Rank: 10, CosineScore: 0.1},
	}
	r := NewReranker(RerankConfig{Enabled: true})
	r.chatFn = func(url, model, prompt string, timeout time.Duration) (string, error) {
		if !strings.Contains(prompt, "query") {
			t.Error("prompt should contain the query")
		}
		if !strings.Contains(prompt, "[0]") || !strings.Contains(prompt, "[1]") || !strings.Contains(prompt, "[2]") {
			t.Error("prompt should contain candidate indices")
		}
		return "[10, 95, 50]", nil
	}
	got := r.RerankResults("query", results)
	if len(got) != 3 {
		t.Fatalf("expected 3 results, got %d", len(got))
	}
	if got[0].Chunk.UUID != "b" {
		t.Errorf("expected top result to be 'b' (highest reranker score), got %s", got[0].Chunk.UUID)
	}
	if got[1].Chunk.UUID != "a" {
		t.Errorf("expected second result to be 'a', got %s", got[1].Chunk.UUID)
	}
}

func TestRerankResults_TruncatesToTop20(t *testing.T) {
	results := make([]*db.SearchResult, 25)
	for i := range results {
		results[i] = &db.SearchResult{
			Chunk:    &db.Chunk{UUID: fmt.Sprintf("u%d", i)},
			RRFScore: float32(25-i) / 25.0,
			BM25Rank: i + 5,
		}
	}
	r := NewReranker(RerankConfig{Enabled: true})
	promptLen := 0
	r.chatFn = func(url, model, prompt string, timeout time.Duration) (string, error) {
		promptLen = len(prompt)
		scores := make([]int, 20)
		for i := range scores {
			scores[i] = 50
		}
		b := "["
		for i, s := range scores {
			if i > 0 {
				b += ", "
			}
			b += fmt.Sprintf("%d", s)
		}
		b += "]"
		return b, nil
	}
	got := r.RerankResults("query", results)
	if len(got) != 25 {
		t.Fatalf("expected 25 results, got %d", len(got))
	}
	if promptLen == 0 {
		t.Error("chat should have been called")
	}
}

func TestRerankerPromptContainsAllCandidates(t *testing.T) {
	results := []*db.SearchResult{
		{Chunk: &db.Chunk{UUID: "a", Content: "alpha content"}},
		{Chunk: &db.Chunk{UUID: "b", Content: "beta content"}},
	}
	prompt := rerankPrompt("test query", results)
	if !strings.Contains(prompt, "test query") {
		t.Error("prompt should contain the query")
	}
	if !strings.Contains(prompt, "[0]") || !strings.Contains(prompt, "[1]") {
		t.Error("prompt should contain indexed candidates")
	}
	if !strings.Contains(prompt, "alpha content") || !strings.Contains(prompt, "beta content") {
		t.Error("prompt should contain candidate content")
	}
}

func TestRerankerPromptTruncatesLongContent(t *testing.T) {
	longContent := strings.Repeat("x", rerankMaxContent+100)
	results := []*db.SearchResult{
		{Chunk: &db.Chunk{UUID: "a", Content: longContent}},
	}
	prompt := rerankPrompt("query", results)
	if strings.Contains(prompt, longContent) {
		t.Error("prompt should truncate long content")
	}
	if !strings.Contains(prompt, "...") {
		t.Error("prompt should append ellipsis for truncated content")
	}
}

func TestOllamaChatCompletion_Success(t *testing.T) {
	var receivedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/api/chat" {
			t.Errorf("expected /api/chat, got %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		receivedBody = body

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"message":{"content":"[95, 42, 80]"}}`)
	}))
	defer srv.Close()

	got, err := ollamaChatCompletion(srv.URL+"/api/embeddings", "test-model", "score these", 5*time.Second)
	if err != nil {
		t.Fatalf("ollamaChatCompletion: %v", err)
	}
	if got != "[95, 42, 80]" {
		t.Errorf("expected score array, got %q", got)
	}
	if !strings.Contains(string(receivedBody), `"model":"test-model"`) {
		t.Error("request body should contain model name")
	}
	if !strings.Contains(string(receivedBody), `"stream":false`) {
		t.Error("request body should disable streaming")
	}
	if !strings.Contains(string(receivedBody), "score these") {
		t.Error("request body should contain the prompt")
	}
}

func TestOllamaChatCompletion_EmbedURLRewrite(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"message":{"content":"ok"}}`)
	}))
	defer srv.Close()

	_, err := ollamaChatCompletion(srv.URL+"/api/embed", "m", "p", 5*time.Second)
	if err != nil {
		t.Fatalf("ollamaChatCompletion: %v", err)
	}
	if path != "/api/chat" {
		t.Errorf("expected /api/chat path after rewrite, got %s", path)
	}
}

func TestOllamaChatCompletion_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "model not found")
	}))
	defer srv.Close()

	_, err := ollamaChatCompletion(srv.URL+"/api/embeddings", "m", "p", 5*time.Second)
	if err == nil {
		t.Fatal("expected error on HTTP 500")
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("expected HTTP 500 in error, got: %v", err)
	}
}

func TestOllamaChatCompletion_BadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"message":{"content":123}}`)
	}))
	defer srv.Close()

	_, err := ollamaChatCompletion(srv.URL+"/api/embeddings", "m", "p", 5*time.Second)
	if err == nil {
		t.Fatal("expected error on malformed response")
	}
}

func TestOllamaChatCompletion_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"message":{"content":"late"}}`)
	}))
	defer srv.Close()

	_, err := ollamaChatCompletion(srv.URL+"/api/embeddings", "m", "p", 50*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestOllamaChatCompletion_ConnectionRefused(t *testing.T) {
	_, err := ollamaChatCompletion("http://127.0.0.1:1/api/embeddings", "m", "p", 1*time.Second)
	if err == nil {
		t.Fatal("expected connection error")
	}
}
