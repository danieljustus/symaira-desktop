// Package ai answers questions about the vault by combining local FTS
// results with a local Ollama instance. Without Ollama configured it
// degrades honestly: it says so and returns the raw search results.
package ai

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

type AskChunk struct {
	Chunk string `json:"chunk"`
}

const defaultModel = "llama3.2"

// Ask streams an answer for query to out and always closes out.
// contextDocs are FTS results from the sidecar ({path,title,snippet}).
func Ask(query string, contextDocs []map[string]interface{}, out chan<- AskChunk) {
	defer close(out)

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
	reqBody, err := json.Marshal(map[string]interface{}{
		"model":  model,
		"prompt": prompt,
		"stream": true,
	})
	if err != nil {
		return err
	}

	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Post(baseURL+"/api/generate", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var apiErr struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&apiErr)
		if apiErr.Error != "" {
			return fmt.Errorf("ollama: %s (HTTP %d)", apiErr.Error, resp.StatusCode)
		}
		return fmt.Errorf("ollama: HTTP %d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var part struct {
			Response string `json:"response"`
			Done     bool   `json:"done"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &part); err != nil {
			continue
		}
		if part.Response != "" {
			out <- AskChunk{Chunk: part.Response}
		}
		if part.Done {
			return nil
		}
	}
	return scanner.Err()
}
