// Package ai answers questions about the vault by combining local FTS
// results with a local Ollama instance. Without Ollama configured it
// degrades honestly: it says so and returns the raw search results.
package ai

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/danieljustus/symaira-corekit/ollamakit"
	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/secrets"
)

// AskChunk is the legacy streaming chunk (kept for Transform compatibility).
type AskChunk struct {
	Chunk string `json:"chunk"`
}

// AIEventType enumerates the kinds of events emitted during an ask session.
type AIEventType string

const (
	AIEventAnswer   AIEventType = "answer"
	AIEventCitation AIEventType = "citation"
	AIEventTool     AIEventType = "tool"
	AIEventDone     AIEventType = "done"
)

// AIEvent is the typed envelope for the ask streaming contract.
// Each NDJSON line emitted by "symdesk ask --json" is one AIEvent.
type AIEvent struct {
	Type AIEventType `json:"type"`
	// answer
	Text string `json:"text,omitempty"`
	// citation
	Path    string  `json:"path,omitempty"`
	Title   string  `json:"title,omitempty"`
	Snippet string  `json:"snippet,omitempty"`
	Score   float64 `json:"score,omitempty"`
	// tool
	ToolName string `json:"tool_name,omitempty"`
	Status   string `json:"status,omitempty"`
}

// AnswerEvent creates an answer-chunk event.
func AnswerEvent(text string) AIEvent {
	return AIEvent{Type: AIEventAnswer, Text: text}
}

// CitationEvent creates a citation event for a search result.
func CitationEvent(path, title, snippet string, score float64) AIEvent {
	return AIEvent{Type: AIEventCitation, Path: path, Title: title, Snippet: snippet, Score: score}
}

// ToolEvent creates a tool-status event.
func ToolEvent(toolName, status string) AIEvent {
	return AIEvent{Type: AIEventTool, ToolName: toolName, Status: status}
}

// DoneEvent signals the end of the stream.
func DoneEvent() AIEvent {
	return AIEvent{Type: AIEventDone}
}

var ErrNotConfigured = errors.New("AI feature not configured")

const defaultModel = "llama3.2"

// Ask streams an answer for query to out and always closes out.
// contextDocs are FTS results from the sidecar ({path,title,snippet}).
func Ask(ctx context.Context, cfg *config.Config, query string, contextDocs []map[string]interface{}, out chan<- AskChunk) {
	defer close(out)

	if err := streamLLM(ctx, cfg, buildPrompt(cfg, query, contextDocs), out); err != nil {
		if errors.Is(err, ErrNotConfigured) {
			provider := cfg.LLMProvider
			if provider == "" {
				provider = "ollama"
			}
			if provider == "anthropic" {
				out <- AskChunk{Chunk: "⚠️ **AI feature not configured.**\n\n" +
					"Anthropic API key could not be resolved (missing secret via symvault or environment variable).\n"}
			} else {
				out <- AskChunk{Chunk: "⚠️ **AI feature not configured.**\n\n" +
					"`SYMDESK_OLLAMA_URL` is not set (e.g., `http://localhost:11434`).\n\n" +
					"Here are the most relevant search results from your vault:\n\n"}
				for i, doc := range contextDocs {
					if i >= 3 {
						break
					}
					path, _ := doc["path"].(string)
					out <- AskChunk{Chunk: fmt.Sprintf("- [[%s]]\n", path)}
				}
			}
		} else {
			out <- AskChunk{Chunk: fmt.Sprintf("⚠️ Request failed: %v\n", err)}
		}
	}
}

// Intent values accepted by Transform. Unknown intents fall back to rewrite.
const (
	IntentSummarize = "summarize"
	IntentRewrite   = "rewrite"
	IntentContinue  = "continue"
)

// Transform streams an AI transformation of text according to intent
// (summarize | rewrite | continue) to out and always closes out. Like Ask it
// degrades honestly: without SYMDESK_OLLAMA_URL configured it explains what is
// missing instead of failing. Unlike Ask it operates purely on the provided
// text and never touches the vault.
func Transform(ctx context.Context, cfg *config.Config, text, intent string, out chan<- AskChunk) {
	defer close(out)

	text = strings.TrimSpace(text)
	if text == "" {
		out <- AskChunk{Chunk: "⚠️ No text provided – please select text first.\n"}
		return
	}

	if err := streamLLM(ctx, cfg, buildTransformPrompt(cfg, text, intent), out); err != nil {
		if errors.Is(err, ErrNotConfigured) {
			provider := cfg.LLMProvider
			if provider == "" {
				provider = "ollama"
			}
			if provider == "anthropic" {
				out <- AskChunk{Chunk: "⚠️ **AI feature not configured.**\n\n" +
					"Anthropic API key could not be resolved (missing secret via symvault or environment variable).\n"}
			} else {
				out <- AskChunk{Chunk: "⚠️ **AI feature not configured.**\n\n" +
					"`SYMDESK_OLLAMA_URL` is not set (e.g., `http://localhost:11434`).\n"}
			}
		} else {
			out <- AskChunk{Chunk: fmt.Sprintf("⚠️ Request failed: %v\n", err)}
		}
	}
}

// streamLLM is the unified dispatch/stream helper.
func streamLLM(ctx context.Context, cfg *config.Config, prompt string, out chan<- AskChunk) error {
	provider := cfg.LLMProvider
	if provider == "" {
		provider = "ollama"
	}

	if provider == "anthropic" {
		apiKey := secrets.ResolveKey(cfg.LLMAPIKey)
		if apiKey == "" {
			return ErrNotConfigured
		}
		return streamAnthropic(ctx, cfg, apiKey, cfg.LLMModel, prompt, out)
	}

	// fallback to ollama
	ollamaURL := strings.TrimRight(os.Getenv("SYMDESK_OLLAMA_URL"), "/")
	if ollamaURL == "" {
		return ErrNotConfigured
	}

	model := os.Getenv("SYMDESK_OLLAMA_MODEL")
	if model == "" {
		model = defaultModel
	}

	return streamOllama(ctx, ollamaURL, model, prompt, out)
}

// buildTransformPrompt renders an intent-specific instruction around the text.
func buildTransformPrompt(cfg *config.Config, text, intent string) string {
	var instruction string
	switch intent {
	case IntentSummarize:
		instruction = "Summarize the following text concisely. " +
			"Return only the summary, without introductory remarks."
	case IntentContinue:
		instruction = "Continue the following text in a meaningful way, keeping the same style and tone. " +
			"Return only the continuation, not the original text."
	case IntentRewrite:
		fallthrough
	default:
		instruction = "Rewrite the following text more clearly and fluently, " +
			"without changing its meaning. Return only the revised text."
	}

	var b strings.Builder
	b.WriteString(instruction)
	if cfg.Language != "" {
		fmt.Fprintf(&b, " Answer in %s as pure Markdown text.\n\n---\n", cfg.Language)
	} else {
		b.WriteString(" Answer in the language of the input text as pure Markdown text.\n\n---\n")
	}
	b.WriteString(text)
	b.WriteString("\n---\n")
	return b.String()
}

// buildPrompt grounds the model in the vault search results.
func buildPrompt(cfg *config.Config, query string, contextDocs []map[string]interface{}) string {
	var b strings.Builder
	b.WriteString("You are the assistant of a local Markdown vault. " +
		"Answer the question exclusively based on the following note excerpts. " +
		"If the excerpts do not contain the answer, say so honestly. " +
		"Refer to notes as [[path]].")
	if cfg.Language != "" {
		fmt.Fprintf(&b, " Answer in %s.\n\n", cfg.Language)
	} else {
		b.WriteString(" Answer in the language of the query.\n\n")
	}
	for i, doc := range contextDocs {
		if i >= 5 {
			break
		}
		path, _ := doc["path"].(string)
		title, _ := doc["title"].(string)
		snippet, _ := doc["snippet"].(string)
		if len(snippet) > 1500 {
			snippet = snippet[:1500]
		}
		fmt.Fprintf(&b, "--- Note [[%s]] (%s) ---\n%s\n\n", path, title, snippet)
	}
	fmt.Fprintf(&b, "Question: %s\n", query)
	return b.String()
}

// PromptOne sends a single prompt to the configured AI provider and returns the
// complete response as a string. It is intended for short, one-shot extractions
// such as autofill where streaming is not helpful.
var PromptOne = promptOne

// PromptOneReal is the production implementation, preserved for tests that
// temporarily override PromptOne to avoid real LLM calls.
var PromptOneReal = promptOne

func promptOne(cfg *config.Config, prompt string) (string, error) {
	out := make(chan AskChunk, 1)
	var result strings.Builder
	done := make(chan struct{})
	go func() {
		for chunk := range out {
			result.WriteString(chunk.Chunk)
		}
		close(done)
	}()

	err := streamLLM(context.Background(), cfg, prompt, out)
	close(out)
	<-done

	if err != nil {
		if errors.Is(err, ErrNotConfigured) {
			provider := cfg.LLMProvider
			if provider == "" {
				provider = "ollama"
			}
			if provider == "anthropic" {
				return "", errors.New("anthropic API key not resolved")
			}
			return "", errors.New("SYMDESK_OLLAMA_URL not set")
		}
		return "", err
	}
	return strings.TrimSpace(result.String()), nil
}

func streamOllama(ctx context.Context, baseURL, model, prompt string, out chan<- AskChunk) error {
	client := ollamakit.New(ollamakit.Config{
		BaseURL: baseURL,
		Model:   model,
		Timeout: 5 * time.Minute,
	})
	err := client.Generate(ctx, model, prompt, nil, func(chunk ollamakit.GenerateResponse) error {
		if chunk.Response != "" {
			out <- AskChunk{Chunk: chunk.Response}
		}
		return nil
	})
	if err == nil {
		return nil
	}
	var respErr *ollamakit.ResponseError
	if errors.As(err, &respErr) {
		return fmt.Errorf("ollama: %s (HTTP %d)", respErr.Body, respErr.StatusCode)
	}
	return err
}
