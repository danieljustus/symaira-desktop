package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

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
	// CitationWarnings are advisory links in Text that were not read during
	// this run. They never prevent a caller from writing generated content.
	CitationWarnings []CitationWarning
	// ReadPaths is the cumulative set of document paths read in this run.
	ReadPaths []string
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
	ToolResult       *AgentToolResult
	CitationWarnings []CitationWarning
	ReadPaths        []string
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

// externalizePreviewChars is how much of an externalized result stays
// inline as a summary for the model (the full output is on disk behind the
// handle).
const externalizePreviewChars = 4000

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

	// Externalization (issue #406): over-threshold tool results are written
	// to the per-vault results area and the model receives a compact summary
	// plus a handle it can re-read via desk_read_result. Deterministic
	// handles need a per-run task id; the loop generates one so every run's
	// artifacts live under their own task directory. Without a vault root
	// there is no results area — the loop degrades to inline truncation.
	var externalizer *Externalizer
	if cfg.Vault != "" {
		externalizer = NewExternalizer(cfg.Vault, newTaskID())
		// Retention (issue #418): task ids are unique per run, so the
		// results area grows without bound unless pruned. Prune at run
		// start on a best-effort basis; a failure must never abort the
		// agent loop.
		if cfg.ResultsMaxAgeDays > 0 || cfg.ResultsMaxPerTask > 0 {
			if _, err := PruneResults(externalizer.Root, time.Duration(cfg.ResultsMaxAgeDays)*24*time.Hour, cfg.ResultsMaxPerTask); err != nil {
				log.Printf("agent: prune externalized results: %v", err)
			}
		}
	}
	messages := []AgentMessage{
		{Role: "user", Text: buildAgentSystemPrompt(cfg) + "\n\nQuestion: " + query},
	}

	var totalUsage, contextWindow int
	readPaths := make(map[string]struct{})
	iterations := 0
	hitCap := false
	var lastWarnings []CitationWarning

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
		result.ReadPaths = sortedReadPaths(readPaths)
		result.CitationWarnings = CheckCitationWarningsSafe(result.Text, result.ReadPaths)
		lastWarnings = result.CitationWarnings

		if len(result.ToolCalls) == 0 {
			// end_turn (or any stop without tool calls): the accumulated
			// text was already streamed — never swallow it. The final
			// assistant message carries the citation metadata so the UI
			// can render warnings for the finished answer.
			done := TerminalEvent(totalUsage, contextWindow)
			done.CitationWarnings = result.CitationWarnings
			done.ReadPaths = result.ReadPaths
			out <- done
			return
		}

		// Execute the requested tools and append results.
		assistantMsg := AgentMessage{Role: "assistant", Text: result.Text, ToolCalls: result.ToolCalls, CitationWarnings: result.CitationWarnings, ReadPaths: result.ReadPaths}
		messages = append(messages, assistantMsg)
		for _, call := range result.ToolCalls {
			out <- ToolCallEvent(iterations, call.Name, call.Input)
			output, truncated, err := executeTool(ctx, readOnly, call)
			if err != nil {
				output = fmt.Sprintf("tool error: %v", err)
				truncated = false
			}
			for _, path := range readPathsForTool(call, output) {
				readPaths[path] = struct{}{}
			}
			// Externalize over-threshold results (issue #406): the full
			// output goes to the results area, the model gets a summary
			// plus the handle so it can re-read on demand.
			if err == nil && externalizer != nil && externalizer.ShouldExternalize(call.Name, len(output)) {
				if handle, xerr := externalizer.Externalize(call.Name, iterations, output); xerr == nil {
					output = summarize(output, externalizePreviewChars) +
						"\n\n[Full result externalized to " + handle + " — call desk_read_result with handle=\"" + handle + "\" to read it.]"
					truncated = false
				}
				// On write failure keep the inline (possibly truncated)
				// output — the model still gets a usable answer.
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
	done := TerminalEvent(totalUsage, contextWindow)
	done.CitationWarnings = lastWarnings
	done.ReadPaths = sortedReadPaths(readPaths)
	out <- done
}

func sortedReadPaths(paths map[string]struct{}) []string {
	result := make([]string, 0, len(paths))
	for path := range paths {
		result = append(result, path)
	}
	sort.Strings(result)
	return result
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
