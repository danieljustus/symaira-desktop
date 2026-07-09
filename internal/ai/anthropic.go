package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

var jsonMarshal = json.Marshal

func streamAnthropic(ctx context.Context, apiKey, model, prompt string, out chan<- AskChunk) error {
	if model == "" {
		model = "claude-3-5-sonnet-20240620"
	}

	payload := map[string]interface{}{
		"model":      model,
		"max_tokens": 4096,
		"stream":     true,
		"messages": []map[string]string{
			{
				"role":    "user",
				"content": prompt,
			},
		},
	}

	body, err := jsonMarshal(payload)
	if err != nil {
		return err
	}

	apiURL := os.Getenv("SYMDESK_ANTHROPIC_URL")
	if apiURL == "" {
		apiURL = "https://api.anthropic.com/v1/messages"
	}

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")

	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errResp map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&errResp)
		return fmt.Errorf("anthropic api error (HTTP %d): %v", resp.StatusCode, errResp)
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		dataStr := strings.TrimPrefix(line, "data: ")
		if dataStr == "[DONE]" {
			break
		}

		var event map[string]interface{}
		if err := json.Unmarshal([]byte(dataStr), &event); err != nil {
			continue
		}

		eventType, _ := event["type"].(string)
		if eventType == "content_block_delta" {
			delta, _ := event["delta"].(map[string]interface{})
			if text, ok := delta["text"].(string); ok {
				out <- AskChunk{Chunk: text}
			}
		}
	}

	return scanner.Err()
}
