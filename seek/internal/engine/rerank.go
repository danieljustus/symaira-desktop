package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/danieljustus/symaira-seek/internal/db"
)

const (
	rerankTopN        = 20
	rerankMaxContent  = 500
	strongBM25Thresh = 0.85
)

// RerankConfig holds the user-tunable knobs for LLM re-ranking.
type RerankConfig struct {
	Enabled bool
	URL     string
	Model   string
	Timeout time.Duration
}

// chatFunc is the signature used for Ollama chat completion requests.
// It is injectable for deterministic testing.
type chatFunc func(url, model, prompt string, timeout time.Duration) (string, error)

// Reranker scores search results against a query using an Ollama chat model.
type Reranker struct {
	cfg    RerankConfig
	chatFn chatFunc
}

// NewReranker creates a Reranker from the given config.
func NewReranker(cfg RerankConfig) *Reranker {
	return &Reranker{
		cfg:    cfg,
		chatFn: ollamaChatCompletion,
	}
}

// rerankPrompt builds a deterministic prompt that asks the LLM to score each
// candidate's relevance to the query on a 0-100 integer scale.
func rerankPrompt(query string, results []*db.SearchResult) string {
	var b strings.Builder
	b.WriteString("You are a search result re-ranker. Score each candidate's relevance to the query on a scale of 0-100.\n\n")
	b.WriteString("Query: ")
	b.WriteString(query)
	b.WriteString("\n\nCandidates:\n")
	for i, r := range results {
		content := r.Chunk.Content
		if len(content) > rerankMaxContent {
			content = content[:rerankMaxContent] + "..."
		}
		b.WriteString(fmt.Sprintf("[%d] %s\n", i, content))
	}
	b.WriteString("\nReturn ONLY a JSON array of integers, one per candidate, in order. Example: [95, 42, 80]")
	return b.String()
}

// parseScores extracts a JSON array of integers from the LLM response text.
// It tolerates surrounding text and markdown fences.
func parseScores(text string) ([]int, error) {
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)

	start := strings.IndexByte(text, '[')
	end := strings.LastIndexByte(text, ']')
	if start < 0 || end < 0 || end <= start {
		return nil, fmt.Errorf("reranker: no JSON array found in response")
	}
	text = text[start : end+1]

	var scores []int
	if err := json.Unmarshal([]byte(text), &scores); err != nil {
		return nil, fmt.Errorf("reranker: failed to parse scores: %w", err)
	}
	return scores, nil
}

// blendScore computes a position-aware blend of normalized RRF and reranker
// scores. Top-3 results weight RRF more (0.7→0.6); deeper ranks weight the
// reranker more (down to 0.3 RRF weight).
func blendScore(rrfNorm, rerankerNorm float32, position int) float32 {
	rrfW := float32(math.Max(0.3, 0.7-float64(position)*0.05))
	return rrfW*rrfNorm + (1-rrfW)*rerankerNorm
}

// shouldSkipRerank reports true when the top result is a strong enough signal
// that reranking is unnecessary: BM25 rank 1 and cosine similarity >= 0.85.
func shouldSkipRerank(results []*db.SearchResult) bool {
	if len(results) == 0 {
		return false
	}
	top := results[0]
	return top.BM25Rank == 1 && top.CosineScore >= strongBM25Thresh
}

// RerankResults scores the top-N candidates via Ollama chat completion and
// re-sorts them by a blended RRF+reranker score. On any failure it returns
// the original results unchanged.
func (r *Reranker) RerankResults(query string, results []*db.SearchResult) []*db.SearchResult {
	if !r.cfg.Enabled || len(results) == 0 {
		return results
	}

	if shouldSkipRerank(results) {
		fmt.Fprintf(os.Stderr, "engine: reranking skipped (strong BM25+cosine signal)\n")
		return results
	}

	candidates := results
	if len(candidates) > rerankTopN {
		candidates = candidates[:rerankTopN]
	}

	prompt := rerankPrompt(query, candidates)
	text, err := r.chatFn(r.cfg.URL, r.cfg.Model, prompt, r.cfg.Timeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "engine: reranking failed (%v), falling back to RRF\n", err)
		return results
	}

	scores, err := parseScores(text)
	if err != nil {
		fmt.Fprintf(os.Stderr, "engine: %v, falling back to RRF\n", err)
		return results
	}
	if len(scores) < len(candidates) {
		fmt.Fprintf(os.Stderr, "engine: reranker returned %d scores for %d candidates, falling back to RRF\n", len(scores), len(candidates))
		return results
	}

	// Find max RRF score for normalization.
	var maxRRF float32
	for _, c := range candidates {
		if c.RRFScore > maxRRF {
			maxRRF = c.RRFScore
		}
	}

	type scored struct {
		result    *db.SearchResult
		finalScore float32
	}
	scoredResults := make([]scored, len(candidates))
	for i, c := range candidates {
		rrfNorm := float32(0)
		if maxRRF > 0 {
			rrfNorm = c.RRFScore / maxRRF
		}
		rerankerNorm := float32(math.Min(1.0, float64(max(0, min(scores[i], 100)))/100.0))
		scoredResults[i] = scored{
			result:     c,
			finalScore: blendScore(rrfNorm, rerankerNorm, i),
		}
	}

	// Sort descending by blended score.
	for i := 1; i < len(scoredResults); i++ {
		for j := i; j > 0 && scoredResults[j].finalScore > scoredResults[j-1].finalScore; j-- {
			scoredResults[j], scoredResults[j-1] = scoredResults[j-1], scoredResults[j]
		}
	}

	rerankedUUIDs := make(map[string]float32, len(scoredResults))
	for _, s := range scoredResults {
		s.result.RRFScore = s.finalScore
		rerankedUUIDs[s.result.Chunk.UUID] = s.finalScore
	}

	out := make([]*db.SearchResult, 0, len(results))
	for _, s := range scoredResults {
		out = append(out, s.result)
	}
	for _, r := range results {
		if _, ok := rerankedUUIDs[r.Chunk.UUID]; !ok {
			out = append(out, r)
		}
	}

	return out
}

// ollamaChatCompletion sends a chat completion request to Ollama's /api/chat
// endpoint and returns the assistant's response text.
func ollamaChatCompletion(url, model, prompt string, timeout time.Duration) (string, error) {
	chatURL := strings.Replace(url, "/api/embeddings", "/api/chat", 1)
	chatURL = strings.Replace(chatURL, "/api/embed", "/api/chat", 1)

	reqBody, err := json.Marshal(map[string]interface{}{
		"model": model,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"stream": false,
	})
	if err != nil {
		return "", err
	}

	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequest(http.MethodPost, chatURL, bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("ollama chat request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("ollama returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to decode ollama response: %w", err)
	}
	return result.Message.Content, nil
}


