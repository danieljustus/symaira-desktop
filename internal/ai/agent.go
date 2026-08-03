package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/danieljustus/symaira-desktop/internal/config"
)

// AgentTool is the loop's view of one invocable capability. It mirrors the
// canonical shape from internal/tools (name, description, JSON schema,
// handler) without importing that package — internal/tools already imports
// this one for its ask tool, so the registry's entries are adapted at the
// call site (see cmd/symdesk/ask.go).
type AgentTool struct {
	Name        string
	Description string
	InputSchema json.RawMessage
	// ReadOnly marks tools that must not mutate vault state. The loop only
	// ever executes tools with ReadOnly == true (issue #317).
	ReadOnly bool
	// Handler executes the tool with the raw JSON arguments object.
	Handler func(ctx context.Context, input json.RawMessage) (any, error)
}

// ToolCall is one tool invocation requested by the model.
type ToolCall struct {
	// ID is the provider-specific invocation id (Anthropic tool_use id,
	// Ollama/OpenAI tool_call id). Empty for providers that do not issue ids.
	ID    string
	Name  string
	Input json.RawMessage
}

// AgentTurnResult is the outcome of one provider round trip.
type AgentTurnResult struct {
	// Text is the generated text for this turn, already streamed to the
	// output channel by the turn function.
	Text string
	// ToolCalls are the tool invocations the model requested this turn.
	ToolCalls []ToolCall
	// StopReason is the provider's stop reason ("" when unknown).
	StopReason string
	// TokenUsage is the total input+output tokens for the whole session so
	// far (cumulative), or 0 when the provider does not report usage.
	TokenUsage int
	// ContextWindow is the model's context window in tokens, or 0 when
	// unknown.
	ContextWindow int
}

// agentTurnFunc performs one provider round trip: sends the message history
// plus tool list, streams answer text through onText as it arrives, and
// returns the accumulated turn result. Injectable for tests.
type agentTurnFunc func(
	ctx context.Context,
	cfg *config.Config,
	messages []AgentMessage,
	tools []AgentTool,
	onText func(string),
) (AgentTurnResult, error)

// AgentMessage is one entry in the conversation history, in a
// provider-agnostic shape. The adapters translate to Anthropic content
// blocks or Ollama/OpenAI chat messages.
type AgentMessage struct {
	Role      string // "user" | "assistant" | "tool"
	Text      string
	ToolCalls []ToolCall
	// ToolResult carries the outcome of one tool call (role "tool").
	ToolResult *AgentToolResult
}

// AgentToolResult is the outcome of one executed tool call.
type AgentToolResult struct {
	ToolCallID string
	Name       string
	Output     string
	// Truncated reports whether Output was cut to fit the context window.
	Truncated bool
}

// maxToolOutputChars caps how much of a tool's output is echoed back into
// the conversation; the full output is still delivered to the UI in the
// tool-result event.
const maxToolOutputChars = 8000

// defaultMaxIterations caps the agentic loop so a misbehaving model cannot
// spin forever. The loop always yields a partial answer instead of hanging
// (issue #317).
const defaultMaxIterations = 5

// providerTurnFunc builds the turn function for the configured provider.
// It is a variable so tests can inject a deterministic fake turn and keep
// the loop tests free of network calls (issue #317).
var providerTurnFunc = providerTurn

// RunAgent runs the bounded agentic loop for query: repeatedly asks the
// provider, executes any requested read-only tools, appends the results and
// repeats until the provider answers without tool calls, the iteration cap
// is reached, or the context is cancelled. Every AIEvent is emitted to out;
// the channel is closed when the run finishes.
//
// The legacy one-shot Ask path stays available as the fallback when no
// tools are enabled (see cmd/symdesk/ask.go).
func RunAgent(
	ctx context.Context,
	cfg *config.Config,
	query string,
	tools []AgentTool,
	out chan<- AIEvent,
) {
	defer close(out)

	// Never expose mutating tools to the model.
	readOnly := make([]AgentTool, 0, len(tools))
	for _, tool := range tools {
		if tool.ReadOnly {
			readOnly = append(readOnly, tool)
		}
	}

	if len(readOnly) == 0 {
		// Fallback contract: without tools the loop degrades to the classic
		// one-shot Ask so the surface stays useful.
		chunkChan := make(chan AskChunk)
		go func() {
			Ask(ctx, cfg, query, nil, chunkChan)
		}()
		for chunk := range chunkChan {
			out <- AnswerEvent(chunk.Chunk)
		}
		out <- DoneEvent()
		return
	}

	turn := providerTurnFunc(cfg)
	maxIterations := cfg.AgentMaxIterations
	if maxIterations <= 0 {
		maxIterations = defaultMaxIterations
	}

	messages := []AgentMessage{
		{Role: "user", Text: buildAgentSystemPrompt(cfg) + "\n\nQuestion: " + query},
	}

	var totalUsage, contextWindow int
	iterations := 0
	hitCap := false

	for iterations < maxIterations {
		iterations++
		result, err := turn(ctx, cfg, messages, readOnly, func(text string) {
			if text != "" {
				out <- AnswerEvent(text)
			}
		})
		if err != nil {
			out <- AnswerEvent(fmt.Sprintf("⚠️ Agent request failed: %v\n", err))
			out <- TerminalEvent(totalUsage, contextWindow)
			return
		}

		if result.TokenUsage > 0 {
			totalUsage = result.TokenUsage
		}
		if result.ContextWindow > 0 {
			contextWindow = result.ContextWindow
		}

		if len(result.ToolCalls) == 0 {
			// end_turn (or any stop without tool calls): the accumulated
			// text was already streamed — never swallow it.
			out <- TerminalEvent(totalUsage, contextWindow)
			return
		}

		// Execute the requested tools and append results.
		assistantMsg := AgentMessage{Role: "assistant", Text: result.Text, ToolCalls: result.ToolCalls}
		messages = append(messages, assistantMsg)
		for _, call := range result.ToolCalls {
			out <- ToolCallEvent(iterations, call.Name, call.Input)
			output, truncated, err := executeTool(ctx, readOnly, call)
			if err != nil {
				output = fmt.Sprintf("tool error: %v", err)
				truncated = false
			}
			out <- ToolResultEvent(iterations, call.Name, output, truncated)
			messages = append(messages, AgentMessage{
				Role: "tool",
				ToolResult: &AgentToolResult{
					ToolCallID: call.ID,
					Name:       call.Name,
					Output:     output,
					Truncated:  truncated,
				},
			})
		}
	}

	hitCap = true
	if hitCap {
		out <- AnswerEvent("\n\n⚠️ **Reached the iteration limit — answer may be partial.**\n")
	}
	out <- TerminalEvent(totalUsage, contextWindow)
}

// executeTool runs one tool call, truncating its output for the next turn.
func executeTool(ctx context.Context, tools []AgentTool, call ToolCall) (string, bool, error) {
	var target *AgentTool
	for i := range tools {
		if tools[i].Name == call.Name {
			target = &tools[i]
			break
		}
	}
	if target == nil {
		return "", false, fmt.Errorf("unknown tool %q", call.Name)
	}
	result, err := target.Handler(ctx, call.Input)
	if err != nil {
		return "", false, err
	}
	var output string
	switch v := result.(type) {
	case string:
		output = v
	case []byte:
		output = string(v)
	case fmt.Stringer:
		output = v.String()
	default:
		if v == nil {
			output = "ok"
		} else if data, err := json.Marshal(v); err == nil {
			output = string(data)
		} else {
			output = fmt.Sprintf("%v", v)
		}
	}
	truncated := false
	if len(output) > maxToolOutputChars {
		output = output[:maxToolOutputChars] + "\n…[truncated]"
		truncated = true
	}
	return output, truncated, nil
}

// buildAgentSystemPrompt describes the loop's operating rules to the model.
func buildAgentSystemPrompt(cfg *config.Config) string {
	var b strings.Builder
	b.WriteString("You are the assistant of a local Markdown vault. " +
		"You can call the provided tools to search, list and read vault content. " +
		"Call tools whenever you need information; after gathering it, answer the question " +
		"directly, citing notes as [[path]]. " +
		"If the vault does not contain the answer, say so honestly.")
	if cfg.Language != "" {
		fmt.Fprintf(&b, " Answer in %s.\n", cfg.Language)
	} else {
		b.WriteString(" Answer in the language of the question.\n")
	}
	return b.String()
}
