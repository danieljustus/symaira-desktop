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

type AskChunk struct {
	Chunk string `json:"chunk"`
}

const defaultModel = "llama3.2"

// Ask streams an answer for query to out and always closes out.
// contextDocs are FTS results from the sidecar ({path,title,snippet}).
func Ask(query string, contextDocs []map[string]interface{}, out chan<- AskChunk) {
	defer close(out)

	cfg, _ := config.Load()
	provider := cfg.LLMProvider
	if provider == "" {
		provider = "ollama"
	}

	if provider == "anthropic" {
		apiKey := secrets.ResolveKey(cfg.LLMAPIKey)
		if apiKey == "" {
			out <- AskChunk{Chunk: "⚠️ **AI-Feature nicht konfiguriert.**\n\n" +
				"Anthropic API-Key konnte nicht aufgelöst werden (Fehlendes Secret via symvault oder Umgebungsvariable).\n"}
			return
		}
		model := os.Getenv("SYMDESK_LLM_MODEL")
		if err := streamAnthropic(apiKey, model, buildPrompt(query, contextDocs), out); err != nil {
			out <- AskChunk{Chunk: fmt.Sprintf("⚠️ Anthropic-Anfrage fehlgeschlagen: %v\n", err)}
		}
		return
	}

	// fallback to ollama
	ollamaURL := strings.TrimRight(os.Getenv("SYMDESK_OLLAMA_URL"), "/")
	if ollamaURL == "" {
		out <- AskChunk{Chunk: "⚠️ **AI-Feature nicht konfiguriert.**\n\n" +
			"`SYMDESK_OLLAMA_URL` ist nicht gesetzt (z. B. `http://localhost:11434`).\n\n" +
			"Hier sind dennoch die relevantesten Suchergebnisse aus deinem Vault:\n\n"}
		for i, doc := range contextDocs {
			if i >= 3 {
				break
			}
			path, _ := doc["path"].(string)
			out <- AskChunk{Chunk: fmt.Sprintf("- [[%s]]\n", path)}
		}
		return
	}

	model := os.Getenv("SYMDESK_OLLAMA_MODEL")
	if model == "" {
		model = defaultModel
	}

	if err := streamOllama(ollamaURL, model, buildPrompt(query, contextDocs), out); err != nil {
		out <- AskChunk{Chunk: fmt.Sprintf("⚠️ Ollama-Anfrage fehlgeschlagen: %v\n", err)}
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
func Transform(text, intent string, out chan<- AskChunk) {
	defer close(out)

	text = strings.TrimSpace(text)
	if text == "" {
		out <- AskChunk{Chunk: "⚠️ Kein Text übergeben – bitte zuerst Text auswählen.\n"}
		return
	}

	cfg, _ := config.Load()
	provider := cfg.LLMProvider
	if provider == "" {
		provider = "ollama"
	}

	if provider == "anthropic" {
		apiKey := secrets.ResolveKey(cfg.LLMAPIKey)
		if apiKey == "" {
			out <- AskChunk{Chunk: "⚠️ **AI-Feature nicht konfiguriert.**\n\n" +
				"Anthropic API-Key konnte nicht aufgelöst werden (Fehlendes Secret via symvault oder Umgebungsvariable).\n"}
			return
		}
		model := os.Getenv("SYMDESK_LLM_MODEL")
		if err := streamAnthropic(apiKey, model, buildTransformPrompt(text, intent), out); err != nil {
			out <- AskChunk{Chunk: fmt.Sprintf("⚠️ Anthropic-Anfrage fehlgeschlagen: %v\n", err)}
		}
		return
	}

	// fallback to ollama
	ollamaURL := strings.TrimRight(os.Getenv("SYMDESK_OLLAMA_URL"), "/")
	if ollamaURL == "" {
		out <- AskChunk{Chunk: "⚠️ **AI-Feature nicht konfiguriert.**\n\n" +
			"`SYMDESK_OLLAMA_URL` ist nicht gesetzt (z. B. `http://localhost:11434`).\n"}
		return
	}

	model := os.Getenv("SYMDESK_OLLAMA_MODEL")
	if model == "" {
		model = defaultModel
	}

	if err := streamOllama(ollamaURL, model, buildTransformPrompt(text, intent), out); err != nil {
		out <- AskChunk{Chunk: fmt.Sprintf("⚠️ Ollama-Anfrage fehlgeschlagen: %v\n", err)}
	}
}

// buildTransformPrompt renders an intent-specific instruction around the text.
func buildTransformPrompt(text, intent string) string {
	var instruction string
	switch intent {
	case IntentSummarize:
		instruction = "Fasse den folgenden Text prägnant zusammen. " +
			"Gib nur die Zusammenfassung zurück, ohne Vorrede."
	case IntentContinue:
		instruction = "Schreibe den folgenden Text im gleichen Stil und Ton sinnvoll weiter. " +
			"Gib nur die Fortsetzung zurück, nicht den ursprünglichen Text."
	case IntentRewrite:
		fallthrough
	default:
		instruction = "Formuliere den folgenden Text klarer und flüssiger um, " +
			"ohne die Bedeutung zu verändern. Gib nur den überarbeiteten Text zurück."
	}

	var b strings.Builder
	b.WriteString(instruction)
	b.WriteString(" Antworte auf Deutsch als reiner Markdown-Text.\n\n---\n")
	b.WriteString(text)
	b.WriteString("\n---\n")
	return b.String()
}

// buildPrompt grounds the model in the vault search results.
func buildPrompt(query string, contextDocs []map[string]interface{}) string {
	var b strings.Builder
	b.WriteString("Du bist der Assistent eines lokalen Markdown-Vaults. " +
		"Beantworte die Frage ausschließlich anhand der folgenden Notiz-Auszüge. " +
		"Wenn die Auszüge die Antwort nicht hergeben, sage das ehrlich. " +
		"Verweise auf Notizen als [[pfad]].\n\n")
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
		fmt.Fprintf(&b, "--- Notiz [[%s]] (%s) ---\n%s\n\n", path, title, snippet)
	}
	fmt.Fprintf(&b, "Frage: %s\n", query)
	return b.String()
}

func streamOllama(baseURL, model, prompt string, out chan<- AskChunk) error {
	client := ollamakit.New(ollamakit.Config{
		BaseURL: baseURL,
		Model:   model,
		Timeout: 5 * time.Minute,
	})
	err := client.Generate(context.Background(), model, prompt, nil, func(chunk ollamakit.GenerateResponse) error {
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
