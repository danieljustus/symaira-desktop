package engine

import (
	"fmt"
	"os"
	"strings"
	"time"
)

const hydeMaxPassageLen = 512

// ExpandConfig holds the user-tunable knobs for HyDE query expansion.
type ExpandConfig struct {
	Enabled bool
	URL     string
	Model   string
	Timeout time.Duration
}

// Expander generates hypothetical document passages for query expansion.
type Expander struct {
	cfg    ExpandConfig
	chatFn chatFunc
}

// NewExpander creates an Expander from the given config.
func NewExpander(cfg ExpandConfig) *Expander {
	return &Expander{
		cfg:    cfg,
		chatFn: ollamaChatCompletion,
	}
}

// Expand generates a hypothetical document passage for the query.
func (e *Expander) Expand(query string) (string, error) {
	return expandQuery(e.chatFn, e.cfg, query)
}

// expandPrompt builds a prompt that asks the LLM to generate a short
// hypothetical document passage that would be a strong match for the query.
func expandPrompt(query string) string {
	return "Write a short, factual passage (2-4 sentences) that directly answers or describes the following topic. " +
		"This passage will be used to improve document search relevance.\n\n" +
		"Topic: " + query
}

// expandQuery uses Ollama chat to generate a hypothetical document passage
// for the given query. On success it returns the generated text; on failure
// it returns ("", err) so callers can fall back to the original query vector.
func expandQuery(chatFn chatFunc, cfg ExpandConfig, query string) (string, error) {
	prompt := expandPrompt(query)
	text, err := chatFn(cfg.URL, cfg.Model, prompt, cfg.Timeout)
	if err != nil {
		return "", fmt.Errorf("hyde expansion failed: %w", err)
	}
	text = trimPassage(text, hydeMaxPassageLen)
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("hyde expansion returned empty passage")
	}
	return text, nil
}

// trimPassage truncates text to maxLen runes, trimming at a word boundary
// when possible.
func trimPassage(text string, maxLen int) string {
	runes := []rune(text)
	if len(runes) <= maxLen {
		return text
	}
	truncated := string(runes[:maxLen])
	for i := len(truncated) - 1; i >= 0; i-- {
		if truncated[i] == ' ' {
			return truncated[:i]
		}
	}
	return truncated
}

// averageVecs returns the element-wise average of two equal-length vectors.
// If either vector is nil or lengths differ, it returns a.
func averageVecs(a, b []float32) []float32 {
	if len(a) != len(b) || len(a) == 0 {
		return a
	}
	out := make([]float32, len(a))
	for i := range a {
		out[i] = (a[i] + b[i]) / 2
	}
	return out
}

// computeExpandedVec produces the combined vector for a query when HyDE
// expansion is enabled. It returns the original query vector unchanged
// when expansion fails, logging the failure to stderr.
func computeExpandedVec(embedder Embedder, queryVec []float32, expandedText string) []float32 {
	if expandedText == "" {
		return queryVec
	}
	expansionVec := embedder.GenerateVectorNoRetry(expandedText)
	if len(expansionVec) == 0 {
		fmt.Fprintf(os.Stderr, "engine: HyDE expansion produced empty vector, using original query vector\n")
		return queryVec
	}
	combined := averageVecs(queryVec, expansionVec)
	if len(combined) == 0 {
		return queryVec
	}
	return combined
}
