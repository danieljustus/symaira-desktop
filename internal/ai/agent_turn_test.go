package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/config"
)

// ---------------------------------------------------------------------------
// Provider dispatch (issue #317): the loop must speak each provider's wire
// format — Anthropic tool_use blocks, Ollama/OpenAI tool_calls — and never
// lose text streamed before a tool request.
// ---------------------------------------------------------------------------

func testTool() AgentTool {
	return AgentTool{
		Name:        "desk_search",
		Description: "Search the vault",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`),
		ReadOnly:    true,
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			return "result", nil
		},
	}
}

func TestAnthropicAgentTurnStreamsToolUse(t *testing.T) {
	t.Setenv("SYMDESK_LLM_PROVIDER", "anthropic")
	t.Setenv("SYMDESK_LLM_API_KEY", "test-key")

	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("missing api key header")
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("content-type", "text/event-stream")
		// Text before the tool_use block must be streamed.
		_, _ = w.Write([]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Looking \"}}\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"up…\"}}\n\n"))
		_, _ = w.Write([]byte("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"))
		_, _ = w.Write([]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"desk_search\"}}\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"q\\\":\\\"alpha\\\"}\"}}\n\n"))
		_, _ = w.Write([]byte("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n"))
		_, _ = w.Write([]byte("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":25}}\n\n"))
	}))
	defer srv.Close()
	t.Setenv("SYMDESK_ANTHROPIC_URL", srv.URL+"/v1/messages")

	cfg := config.DefaultConfig()
	cfg.LLMProvider = "anthropic"
	cfg.LLMAPIKey = "test-key"

	var streamed strings.Builder
	turn := anthropicAgentTurn
	result, err := turn(context.Background(), cfg,
		[]AgentMessage{{Role: "user", Text: "find alpha"}},
		[]AgentTool{testTool()},
		func(text string) { streamed.WriteString(text) },
	)
	if err != nil {
		t.Fatalf("turn failed: %v", err)
	}

	if streamed.String() != "Looking up…" {
		t.Errorf("text before tool_use was not streamed: %q", streamed.String())
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(result.ToolCalls))
	}
	call := result.ToolCalls[0]
	if call.ID != "toolu_1" || call.Name != "desk_search" || string(call.Input) != `{"q":"alpha"}` {
		t.Errorf("tool call parsed wrong: %+v", call)
	}
	if result.StopReason != "tool_use" {
		t.Errorf("stop reason wrong: %q", result.StopReason)
	}

	// The request body must carry the tools array in Anthropic's shape.
	tools, _ := gotBody["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool definition in request, got %d", len(tools))
	}
	toolDef := tools[0].(map[string]any)
	if toolDef["name"] != "desk_search" {
		t.Errorf("tool name wrong in request: %v", toolDef["name"])
	}
	if _, ok := toolDef["input_schema"]; !ok {
		t.Errorf("expected input_schema in Anthropic tool definition")
	}
}

func TestAnthropicMessagesGroupToolResults(t *testing.T) {
	messages := []AgentMessage{
		{Role: "user", Text: "search for x"},
		{Role: "assistant", Text: "", ToolCalls: []ToolCall{{ID: "toolu_1", Name: "desk_search", Input: json.RawMessage(`{"q":"x"}`)}}},
		{Role: "tool", ToolResult: &AgentToolResult{ToolCallID: "toolu_1", Name: "desk_search", Output: "hit"}},
		{Role: "user", Text: "thanks"},
	}
	out := anthropicMessages(messages)
	if len(out) != 4 {
		t.Fatalf("expected 4 messages (user, assistant, tool-results-user, user), got %d", len(out))
	}
	// Consecutive tool results must be grouped into ONE user message carrying
	// tool_result blocks, as the Messages API requires.
	if out[2]["role"] != "user" {
		t.Errorf("expected tool results grouped into a user message, got role %v", out[2]["role"])
	}
	content, _ := out[2]["content"].([]map[string]any)
	if len(content) != 1 || content[0]["type"] != "tool_result" || content[0]["tool_use_id"] != "toolu_1" {
		t.Errorf("tool_result block malformed: %v", content)
	}
	if out[3]["role"] != "user" {
		t.Errorf("expected trailing user message, got %v", out[3])
	}
	// The assistant message carries the tool_use block, not the text.
	assistantContent, _ := out[1]["content"].([]map[string]any)
	if len(assistantContent) != 1 || assistantContent[0]["type"] != "tool_use" {
		t.Errorf("assistant tool_use block malformed: %v", assistantContent)
	}
}

func TestOllamaAgentTurnStreamsToolCalls(t *testing.T) {
	t.Setenv("SYMDESK_OLLAMA_URL", "") // isolate from any ambient local Ollama
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("content-type", "application/x-ndjson")
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"checking "}}` + "\n"))
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"the vault","tool_calls":[{"function":{"name":"desk_search","arguments":{"q":"alpha"}}}]}}` + "\n"))
		_, _ = w.Write([]byte(`{"done":true,"prompt_eval_count":40,"eval_count":9}` + "\n"))
	}))
	defer srv.Close()

	cfg := config.DefaultConfig()
	cfg.OllamaURL = srv.URL
	cfg.LLMProvider = "ollama"

	var streamed strings.Builder
	turn := ollamaAgentTurn
	result, err := turn(context.Background(), cfg,
		[]AgentMessage{{Role: "user", Text: "find alpha"}},
		[]AgentTool{testTool()},
		func(text string) { streamed.WriteString(text) },
	)
	if err != nil {
		t.Fatalf("turn failed: %v", err)
	}

	if streamed.String() != "checking the vault" {
		t.Errorf("streamed text wrong: %q", streamed.String())
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(result.ToolCalls))
	}
	call := result.ToolCalls[0]
	if call.Name != "desk_search" || string(call.Input) != `{"q":"alpha"}` {
		t.Errorf("tool call parsed wrong: %+v", call)
	}
	if !strings.HasPrefix(call.ID, "call_") {
		t.Errorf("expected synthesized tool call id, got %q", call.ID)
	}
	if result.TokenUsage != 49 {
		t.Errorf("expected token usage 49, got %d", result.TokenUsage)
	}

	// The request body must carry OpenAI-compatible function tools.
	tools, _ := gotBody["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool definition in request, got %d", len(tools))
	}
	toolDef := tools[0].(map[string]any)
	if toolDef["type"] != "function" {
		t.Errorf("expected function tool type, got %v", toolDef["type"])
	}
	fn := toolDef["function"].(map[string]any)
	if fn["name"] != "desk_search" {
		t.Errorf("function name wrong: %v", fn["name"])
	}
	if _, ok := fn["parameters"]; !ok {
		t.Errorf("expected parameters in OpenAI tool definition")
	}
}

func TestOllamaMessagesCarryToolCallsAndResults(t *testing.T) {
	messages := []AgentMessage{
		{Role: "user", Text: "go"},
		{Role: "assistant", Text: "hold on", ToolCalls: []ToolCall{{ID: "call_0", Name: "desk_search", Input: json.RawMessage(`{"q":"x"}`)}}},
		{Role: "tool", ToolResult: &AgentToolResult{ToolCallID: "call_0", Name: "desk_search", Output: "hit"}},
	}
	out := ollamaMessages(messages)
	if len(out) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(out))
	}
	assistant := out[1]
	calls, _ := assistant["tool_calls"].([]map[string]any)
	if len(calls) != 1 {
		t.Fatalf("expected tool_calls on assistant message, got %v", assistant)
	}
	if calls[0]["id"] != "call_0" {
		t.Errorf("tool call id missing: %v", calls[0])
	}
	toolMsg := out[2]
	if toolMsg["role"] != "tool" || toolMsg["tool_call_id"] != "call_0" || toolMsg["content"] != "hit" {
		t.Errorf("tool message malformed: %v", toolMsg)
	}
	// Ollama requires arguments as a raw JSON object, not a string.
	fn := calls[0]["function"].(map[string]any)
	if _, isString := fn["arguments"].(string); isString {
		t.Errorf("arguments must be a JSON object for Ollama, got string: %v", fn["arguments"])
	}
	if string(fn["arguments"].(json.RawMessage)) != `{"q":"x"}` {
		t.Errorf("arguments object wrong: %v", fn["arguments"])
	}
}
