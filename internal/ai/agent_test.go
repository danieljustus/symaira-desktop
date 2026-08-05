package ai

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/config"
)

// ---------------------------------------------------------------------------
// Loop behaviour (issue #317): termination at the cap, partial answers never
// swallowed, read-only enforcement, tool output truncation.
// ---------------------------------------------------------------------------

// fakeTurn returns a turn function that emits the given result on every call.
func fakeTurn(result AgentTurnResult) agentTurnFunc {
	return func(_ context.Context, _ *config.Config, _ []AgentMessage, _ []AgentTool, onText func(string)) (AgentTurnResult, error) {
		if result.Text != "" {
			onText(result.Text)
		}
		return result, nil
	}
}

func collectAgentEvents(t *testing.T, cfg *config.Config, query string, tools []AgentTool, turn agentTurnFunc) []AIEvent {
	t.Helper()
	old := providerTurnFunc
	providerTurnFunc = func(*config.Config) agentTurnFunc { return turn }
	defer func() { providerTurnFunc = old }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	out := make(chan AIEvent)
	done := make(chan struct{})
	var events []AIEvent
	var mu sync.Mutex
	go func() {
		for event := range out {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
		}
		close(done)
	}()
	RunAgent(ctx, cfg, query, tools, out)
	<-done
	mu.Lock()
	defer mu.Unlock()
	return events
}

// A model that never stops requesting tools must not spin forever: the loop
// terminates at the iteration cap and still yields a partial answer plus a
// terminal event with usage.
func TestRunAgentTerminatesAtCapWithPartialAnswer(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.AgentMaxIterations = 2

	tools := []AgentTool{
		{
			Name:        "desk_search",
			Description: "search",
			ReadOnly:    true,
			Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
				return "hit", nil
			},
		},
	}
	// Always ask for a tool again — the loop must hit the cap.
	turn := fakeTurn(AgentTurnResult{
		Text:          "collecting…",
		ToolCalls:     []ToolCall{{ID: "c1", Name: "desk_search", Input: json.RawMessage(`{"q":"x"}`)}},
		TokenUsage:    120,
		ContextWindow: 8000,
	})

	events := collectAgentEvents(t, cfg, "question", tools, turn)

	var answer strings.Builder
	toolCalls, toolResults, terminals := 0, 0, 0
	for _, e := range events {
		switch {
		case e.Type == AIEventAnswer:
			answer.WriteString(e.Text)
		case e.Type == AIEventTool && e.ToolInputs != nil:
			toolCalls++
		case e.Type == AIEventTool && e.ToolOutput != "":
			toolResults++
		case e.Type == AIEventDone:
			terminals++
		}
	}
	if toolCalls != 2 || toolResults != 2 {
		t.Errorf("expected 2 tool calls and 2 results, got %d calls / %d results", toolCalls, toolResults)
	}
	if !strings.Contains(answer.String(), "collecting…") {
		t.Errorf("partial answer text was swallowed: %q", answer.String())
	}
	if !strings.Contains(answer.String(), "iteration limit") {
		t.Errorf("expected explicit cap notice, got: %q", answer.String())
	}
	if terminals != 1 {
		t.Errorf("expected exactly one terminal event, got %d", terminals)
	}
	var final AIEvent
	for _, e := range events {
		if e.Type == AIEventDone {
			final = e
		}
	}
	if final.TokenUsage != 120 || final.ContextWindow != 8000 {
		t.Errorf("terminal event usage wrong: %+v", final)
	}
}

// Text emitted before a tool call must never be lost: it is streamed as
// answer events and kept for the next turn.
func TestRunAgentPreservesTextBeforeToolCalls(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.AgentMaxIterations = 3

	tools := []AgentTool{{
		Name:        "desk_status",
		Description: "status",
		ReadOnly:    true,
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			return "ok", nil
		},
	}}
	calls := 0
	turn := func(_ context.Context, _ *config.Config, _ []AgentMessage, _ []AgentTool, onText func(string)) (AgentTurnResult, error) {
		calls++
		if calls == 1 {
			onText("thinking out loud… ")
			return AgentTurnResult{
				Text:      "thinking out loud… ",
				ToolCalls: []ToolCall{{ID: "t1", Name: "desk_status", Input: json.RawMessage(`{}`)}},
			}, nil
		}
		onText("final answer")
		return AgentTurnResult{Text: "final answer"}, nil
	}

	events := collectAgentEvents(t, cfg, "q", tools, turn)
	var answer strings.Builder
	for _, e := range events {
		if e.Type == AIEventAnswer {
			answer.WriteString(e.Text)
		}
	}
	if !strings.Contains(answer.String(), "thinking out loud…") || !strings.Contains(answer.String(), "final answer") {
		t.Errorf("text before/around tool calls was swallowed: %q", answer.String())
	}
}

// Mutating tools are never executed by the loop, and requests for unknown
// tools produce an honest error instead of panicking.
func TestRunAgentNeverExecutesMutatingTools(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.AgentMaxIterations = 1

	mutatingCalled := false
	tools := []AgentTool{
		{
			Name:        "desk_status",
			Description: "status",
			ReadOnly:    true,
			Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
				return "ok", nil
			},
		},
		{
			Name:        "note_new",
			Description: "creates notes",
			ReadOnly:    false,
			Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
				mutatingCalled = true
				return "created", nil
			},
		},
	}
	// The model requests BOTH tools; the loop must only execute the
	// read-only one and report the mutating one as unknown.
	turn := fakeTurn(AgentTurnResult{
		ToolCalls: []ToolCall{
			{ID: "a", Name: "desk_status", Input: json.RawMessage(`{}`)},
			{ID: "b", Name: "note_new", Input: json.RawMessage(`{"title":"x"}`)},
		},
	})

	events := collectAgentEvents(t, cfg, "q", tools, turn)
	if mutatingCalled {
		t.Fatal("mutating tool handler was executed — the loop must never run non-read-only tools")
	}
	results := 0
	var outputs []string
	for _, e := range events {
		if e.Type == AIEventTool && e.ToolOutput != "" {
			results++
			outputs = append(outputs, e.ToolOutput)
		}
	}
	if results != 2 {
		t.Fatalf("expected 2 tool results, got %d", results)
	}
	joined := strings.Join(outputs, "|")
	if !strings.Contains(joined, "ok") {
		t.Errorf("read-only tool result missing: %q", joined)
	}
	if !strings.Contains(joined, "unknown tool") {
		t.Errorf("expected honest error for the mutating tool, got: %q", joined)
	}
}

// Tool output longer than the echo cap is truncated for the model with the
// flag set on the result event.
func TestRunAgentTruncatesToolOutput(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.AgentMaxIterations = 1

	tools := []AgentTool{{
		Name:        "desk_search",
		Description: "search",
		ReadOnly:    true,
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			return strings.Repeat("x", maxToolOutputChars+500), nil
		},
	}}
	turn := fakeTurn(AgentTurnResult{
		ToolCalls: []ToolCall{{ID: "c1", Name: "desk_search", Input: json.RawMessage(`{"q":"x"}`)}},
	})
	events := collectAgentEvents(t, cfg, "q", tools, turn)

	var truncated bool
	var output string
	for _, e := range events {
		if e.Type == AIEventTool && e.ToolOutput != "" {
			output = e.ToolOutput
			truncated = e.ToolOutputTruncated
		}
	}
	if !truncated {
		t.Error("expected truncation flag on oversized tool output")
	}
	if len(output) != maxToolOutputChars+len("\n…[truncated]") {
		t.Errorf("expected output capped at maxToolOutputChars, got %d chars", len(output))
	}
}

// Without any read-only tools the loop degrades to the classic one-shot Ask
// path (which honestly reports when AI is not configured).
func TestRunAgentFallsBackToOneShotWithoutTools(t *testing.T) {
	t.Setenv("SYMDESK_OLLAMA_URL", "")
	cfg := config.DefaultConfig()

	events := collectAgentEvents(t, cfg, "q", nil, fakeTurn(AgentTurnResult{}))
	var answer strings.Builder
	terminals := 0
	for _, e := range events {
		if e.Type == AIEventAnswer {
			answer.WriteString(e.Text)
		}
		if e.Type == AIEventDone {
			terminals++
		}
	}
	if !strings.Contains(answer.String(), "not configured") {
		t.Errorf("expected honest fallback message, got: %q", answer.String())
	}
	if terminals != 1 {
		t.Errorf("expected one terminal event, got %d", terminals)
	}
}
