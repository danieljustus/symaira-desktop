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

	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/secrets"
)

// providerTurn returns the agentic turn function for the configured LLM
// provider ("anthropic" or anything else → Ollama/OpenAI-compatible).
func providerTurn(cfg *config.Config) agentTurnFunc {
	provider := cfg.LLMProvider
	if provider == "" {
		provider = "ollama"
	}
	if provider == "anthropic" {
		return anthropicAgentTurn
	}
	return ollamaAgentTurn
}

// ---------------------------------------------------------------------------
// Anthropic adapter (tool_use blocks)
// ---------------------------------------------------------------------------

// anthropicAgentTurn sends the message history plus tools to the Anthropic
// Messages API, streams text via onText and returns the parsed turn result.
func anthropicAgentTurn(
	ctx context.Context,
	cfg *config.Config,
	messages []AgentMessage,
	tools []AgentTool,
	onText func(string),
) (AgentTurnResult, error) {
	apiKey := secrets.ResolveKey(cfg.LLMAPIKey)
	if apiKey == "" {
		return AgentTurnResult{}, ErrNotConfigured
	}
	model := cfg.LLMModel
	if model == "" {
		model = config.DefaultAnthropicModel
	}
	maxTokens := cfg.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 8192
	}

	payload := map[string]any{
		"model":      model,
		"max_tokens": maxTokens,
		"stream":     true,
		"messages":   anthropicMessages(messages),
	}
	if len(tools) > 0 {
		payload["tools"] = anthropicToolDefs(tools)
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return AgentTurnResult{}, err
	}

	apiURL := os.Getenv("SYMDESK_ANTHROPIC_URL")
	if apiURL == "" {
		apiURL = "https://api.anthropic.com/v1/messages"
	}

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(body)) //nolint:gosec // endpoint is user-configured via env (same as the one-shot ask path)
	if err != nil {
		return AgentTurnResult{}, err
	}
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")

	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req) //nolint:gosec // endpoint is user-configured via env (same as the one-shot ask path)
	if err != nil {
		return AgentTurnResult{}, err
	}
	defer resp.Body.Close() //nolint:errcheck // response body is fully drained by the scanner below

	if resp.StatusCode != http.StatusOK {
		var errResp map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&errResp)
		return AgentTurnResult{}, fmt.Errorf("anthropic api error (HTTP %d): %v", resp.StatusCode, errResp)
	}

	result := AgentTurnResult{}
	var inputTokens, outputTokens int
	// Tool inputs arrive incrementally as input_json_delta fragments.
	type partialTool struct {
		id    string
		name  string
		input strings.Builder
	}
	partials := map[int]*partialTool{}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		dataStr := strings.TrimPrefix(line, "data: ")
		if dataStr == "[DONE]" {
			break
		}

		var event map[string]any
		if err := json.Unmarshal([]byte(dataStr), &event); err != nil {
			continue
		}
		eventType, _ := event["type"].(string)

		switch eventType {
		case "message_start":
			if usage, ok := event["usage"].(map[string]any); ok {
				if v, ok := usage["input_tokens"].(float64); ok {
					inputTokens = int(v)
				}
			}
		case "content_block_start":
			idx := int(event["index"].(float64))
			block, _ := event["content_block"].(map[string]any)
			blockType, _ := block["type"].(string)
			if blockType == "tool_use" {
				id, _ := block["id"].(string)
				name, _ := block["name"].(string)
				partials[idx] = &partialTool{id: id, name: name}
			}
		case "content_block_delta":
			delta, _ := event["delta"].(map[string]any)
			deltaType, _ := delta["type"].(string)
			switch deltaType {
			case "text_delta":
				if text, ok := delta["text"].(string); ok && text != "" {
					result.Text += text
					onText(text)
				}
			case "input_json_delta":
				idx := int(event["index"].(float64))
				if p, ok := partials[idx]; ok {
					if frag, ok := delta["partial_json"].(string); ok {
						p.input.WriteString(frag)
					}
				}
			}
		case "content_block_stop":
			idx := int(event["index"].(float64))
			if p, ok := partials[idx]; ok && p.input.Len() > 0 {
				result.ToolCalls = append(result.ToolCalls, ToolCall{
					ID:    p.id,
					Name:  p.name,
					Input: json.RawMessage(p.input.String()),
				})
				delete(partials, idx)
			}
		case "message_delta":
			delta, _ := event["delta"].(map[string]any)
			if stop, ok := delta["stop_reason"].(string); ok {
				result.StopReason = stop
			}
			if usage, ok := event["usage"].(map[string]any); ok {
				if v, ok := usage["output_tokens"].(float64); ok {
					outputTokens = int(v)
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return AgentTurnResult{}, err
	}

	if inputTokens+outputTokens > 0 {
		result.TokenUsage = inputTokens + outputTokens
	}
	// Claude models report their context window in model metadata; keep it
	// conservative and only report when we know it.
	result.ContextWindow = anthropicContextWindow(model)
	return result, nil
}

// anthropicMessages converts the canonical history to Anthropic message
// objects (content blocks with tool_use / tool_result). Consecutive tool
// messages are grouped into one user message carrying tool_result blocks,
// as required by the Anthropic Messages API.
func anthropicMessages(messages []AgentMessage) []map[string]any {
	var out []map[string]any
	i := 0
	for i < len(messages) {
		m := messages[i]
		switch m.Role {
		case "user":
			content := []map[string]any{}
			if m.Text != "" {
				content = append(content, map[string]any{"type": "text", "text": m.Text})
			}
			if len(content) == 0 {
				content = append(content, map[string]any{"type": "text", "text": "…"})
			}
			out = append(out, map[string]any{"role": "user", "content": content})
			i++
		case "assistant":
			content := []map[string]any{}
			if m.Text != "" {
				content = append(content, map[string]any{"type": "text", "text": m.Text})
			}
			for _, call := range m.ToolCalls {
				content = append(content, map[string]any{
					"type":  "tool_use",
					"id":    call.ID,
					"name":  call.Name,
					"input": json.RawMessage(call.Input),
				})
			}
			if len(content) == 0 {
				content = append(content, map[string]any{"type": "text", "text": "…"})
			}
			out = append(out, map[string]any{"role": "assistant", "content": content})
			i++
		case "tool":
			// Group consecutive tool results into one user message.
			content := []map[string]any{}
			for i < len(messages) && messages[i].Role == "tool" && messages[i].ToolResult != nil {
				tr := messages[i].ToolResult
				content = append(content, map[string]any{
					"type":        "tool_result",
					"tool_use_id": tr.ToolCallID,
					"content":     tr.Output,
				})
				i++
			}
			if len(content) == 0 {
				content = append(content, map[string]any{"type": "text", "text": "…"})
			}
			out = append(out, map[string]any{"role": "user", "content": content})
		}
	}
	return out
}

// anthropicToolDefs renders the tool list in Anthropic's schema format.
func anthropicToolDefs(tools []AgentTool) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		schema := t.InputSchema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		out = append(out, map[string]any{
			"name":         t.Name,
			"description":  t.Description,
			"input_schema": json.RawMessage(schema),
		})
	}
	return out
}

// anthropicContextWindow reports the known context window for a model, or 0
// when unknown. Keeping this conservative means the UI never claims a window
// the provider may not honour.
func anthropicContextWindow(model string) int {
	switch {
	case strings.Contains(model, "sonnet"):
		return 200000
	case strings.Contains(model, "opus"):
		return 200000
	case strings.Contains(model, "haiku"):
		return 200000
	default:
		return 0
	}
}

// ---------------------------------------------------------------------------
// Ollama / OpenAI-compatible adapter (tool_calls)
// ---------------------------------------------------------------------------

// ollamaAgentTurn sends the message history plus tools to Ollama's /api/chat
// endpoint (or any OpenAI-compatible /chat/completions proxy) and streams
// text via onText.
func ollamaAgentTurn(
	ctx context.Context,
	cfg *config.Config,
	messages []AgentMessage,
	tools []AgentTool,
	onText func(string),
) (AgentTurnResult, error) {
	baseURL := strings.TrimRight(os.Getenv("SYMDESK_OLLAMA_URL"), "/")
	if baseURL == "" {
		baseURL = strings.TrimRight(cfg.OllamaURL, "/")
	}
	if baseURL == "" {
		return AgentTurnResult{}, ErrNotConfigured
	}
	model := os.Getenv("SYMDESK_OLLAMA_MODEL")
	if model == "" {
		model = defaultModel
	}

	payload := map[string]any{
		"model":    model,
		"stream":   true,
		"messages": ollamaMessages(messages),
	}
	if len(tools) > 0 {
		payload["tools"] = ollamaToolDefs(tools)
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return AgentTurnResult{}, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/api/chat", bytes.NewReader(body)) //nolint:gosec // endpoint is user-configured via env/config (same as the one-shot ask path)
	if err != nil {
		return AgentTurnResult{}, err
	}
	req.Header.Set("content-type", "application/json")

	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req) //nolint:gosec // endpoint is user-configured via env/config (same as the one-shot ask path)
	if err != nil {
		return AgentTurnResult{}, err
	}
	defer resp.Body.Close() //nolint:errcheck // response body is fully drained by the scanner below

	if resp.StatusCode != http.StatusOK {
		var errResp map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&errResp)
		return AgentTurnResult{}, fmt.Errorf("ollama api error (HTTP %d): %v", resp.StatusCode, errResp)
	}

	result := AgentTurnResult{}
	var promptEval, evalCount int

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		var chunk struct {
			Message struct {
				Role      string `json:"role"`
				Content   string `json:"content"`
				ToolCalls []struct {
					Function struct {
						Name      string          `json:"name"`
						Arguments json.RawMessage `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			Done            bool `json:"done"`
			PromptEvalCount int  `json:"prompt_eval_count"`
			EvalCount       int  `json:"eval_count"`
		}
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			continue
		}
		if chunk.Message.Content != "" {
			result.Text += chunk.Message.Content
			onText(chunk.Message.Content)
		}
		for _, tc := range chunk.Message.ToolCalls {
			args := tc.Function.Arguments
			if len(args) == 0 {
				args = json.RawMessage(`{}`)
			}
			// Ollama does not issue tool_call ids; synthesize stable ones.
			id := fmt.Sprintf("call_%d", len(result.ToolCalls))
			result.ToolCalls = append(result.ToolCalls, ToolCall{
				ID:    id,
				Name:  tc.Function.Name,
				Input: args,
			})
		}
		if chunk.PromptEvalCount > 0 {
			promptEval = chunk.PromptEvalCount
		}
		if chunk.EvalCount > 0 {
			evalCount = chunk.EvalCount
		}
		if chunk.Done {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return AgentTurnResult{}, err
	}

	if promptEval+evalCount > 0 {
		result.TokenUsage = promptEval + evalCount
	}
	return result, nil
}

// ollamaMessages converts the canonical history to Ollama/OpenAI chat
// messages (assistant tool_calls + tool role entries).
func ollamaMessages(messages []AgentMessage) []map[string]any {
	var out []map[string]any
	for _, m := range messages {
		switch m.Role {
		case "user":
			out = append(out, map[string]any{"role": "user", "content": m.Text})
		case "assistant":
			msg := map[string]any{"role": "assistant", "content": m.Text}
			if len(m.ToolCalls) > 0 {
				calls := make([]map[string]any, 0, len(m.ToolCalls))
				for _, call := range m.ToolCalls {
					// Ollama's /api/chat requires the arguments as a JSON
					// object; the OpenAI string form is rejected with 400
					// ("Value looks like object, but can't find closing '}'").
					// Inputs originate from the model's own response chunks,
					// so they are valid JSON in practice; guard anyway.
					args := call.Input
					if len(args) == 0 || !json.Valid(args) {
						args = json.RawMessage(`{}`)
					}
					calls = append(calls, map[string]any{
						"id":   call.ID,
						"type": "function",
						"function": map[string]any{
							"name":      call.Name,
							"arguments": json.RawMessage(args),
						},
					})
				}
				msg["tool_calls"] = calls
			}
			out = append(out, msg)
		case "tool":
			if m.ToolResult != nil {
				out = append(out, map[string]any{
					"role":         "tool",
					"tool_call_id": m.ToolResult.ToolCallID,
					"content":      m.ToolResult.Output,
				})
			}
		}
	}
	return out
}

// ollamaToolDefs renders the tool list in OpenAI-compatible format that
// Ollama's /api/chat endpoint accepts.
func ollamaToolDefs(tools []AgentTool) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		schema := t.InputSchema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  json.RawMessage(schema),
			},
		})
	}
	return out
}
