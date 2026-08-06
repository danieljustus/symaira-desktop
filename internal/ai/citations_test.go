package ai

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/config"
)

func TestCheckCitationWarningsFlagsUnreadDocumentLinks(t *testing.T) {
	content := "---\nSources:\n  - [[read.md]]\n---\n## Sources\n- [[unread.pdf|the invoice]]"
	warnings := CheckCitationWarnings(content, []string{"read.md"})
	if len(warnings) != 1 {
		t.Fatalf("expected one warning, got %#v", warnings)
	}
	if warnings[0].Path != "unread.pdf" || warnings[0].Line != 6 {
		t.Fatalf("unexpected warning: %#v", warnings[0])
	}
}

func TestCheckCitationWarningsIgnoresPeopleAndReadPaths(t *testing.T) {
	content := "## Sources\n- [[Jane Doe]]\n- [[notes/read.md]]\n- [[notes/other.md]]"
	warnings := CheckCitationWarnings(content, []string{"notes/read.md"})
	if len(warnings) != 1 || warnings[0].Path != "notes/other.md" {
		t.Fatalf("unexpected warnings: %#v", warnings)
	}
}

func TestCheckCitationWarningsDoesNotCollapseDifferentDirectories(t *testing.T) {
	content := "## Sources\n- [[archive/read.md]]\n- [[inbox/read.md]]"
	warnings := CheckCitationWarnings(content, []string{"archive/read.md"})
	if len(warnings) != 1 || warnings[0].Path != "inbox/read.md" {
		t.Fatalf("unexpected warnings: %#v", warnings)
	}
}

func TestCheckCitationWarningsScansAtMost4000BodyLines(t *testing.T) {
	content := "## Sources\n" + strings.Repeat("- ordinary text\n", citationBodyScanLineLimit-1)
	content += "- [[too-late.md]]\n"
	warnings := CheckCitationWarnings(content, nil)
	if len(warnings) != 0 {
		t.Fatalf("citation after body scan cap should be ignored, got %#v", warnings)
	}
}

func TestCheckCitationWarningsIgnoresOrdinaryBodyLinks(t *testing.T) {
	warnings := CheckCitationWarnings("A normal link [[unread.md]] is not a source claim.", nil)
	if len(warnings) != 0 {
		t.Fatalf("ordinary body link should not warn, got %#v", warnings)
	}
}

func TestRunAgentSurfacesUnreadCitationWarningsWithoutBlocking(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.AgentMaxIterations = 1
	tools := []AgentTool{{
		Name:     "desk_search",
		ReadOnly: true,
		Handler: func(context.Context, json.RawMessage) (any, error) {
			return []map[string]string{{"path": "read.md"}}, nil
		},
	}}
	turn := func(_ context.Context, _ *config.Config, _ []AgentMessage, _ []AgentTool, onText func(string)) (AgentTurnResult, error) {
		text := "## Sources\n- [[unread.md]]"
		onText(text)
		return AgentTurnResult{Text: text}, nil
	}
	events := collectAgentEvents(t, cfg, "q", tools, turn)
	var done AIEvent
	for _, event := range events {
		if event.Type == AIEventDone {
			done = event
		}
	}
	if len(done.CitationWarnings) != 1 || done.CitationWarnings[0].Path != "unread.md" {
		t.Fatalf("expected surfaced citation warning, got %#v", done.CitationWarnings)
	}
}

func TestRunAgentTracksReadPathsAcrossTurns(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.AgentMaxIterations = 2
	tools := []AgentTool{{
		Name:     "desk_search",
		ReadOnly: true,
		Handler: func(context.Context, json.RawMessage) (any, error) {
			return []map[string]string{{"path": "notes/read.md"}}, nil
		},
	}}
	turns := 0
	turn := func(_ context.Context, _ *config.Config, _ []AgentMessage, _ []AgentTool, onText func(string)) (AgentTurnResult, error) {
		turns++
		if turns == 1 {
			return AgentTurnResult{ToolCalls: []ToolCall{{Name: "desk_search", Input: json.RawMessage(`{"query":"read"}`)}}}, nil
		}
		text := "## Sources\n- [[notes/read.md]]"
		onText(text)
		return AgentTurnResult{Text: text}, nil
	}
	events := collectAgentEvents(t, cfg, "q", tools, turn)
	var done AIEvent
	for _, event := range events {
		if event.Type == AIEventDone {
			done = event
		}
	}
	if len(done.ReadPaths) != 1 || done.ReadPaths[0] != "notes/read.md" {
		t.Fatalf("expected tracked read path, got %#v", done.ReadPaths)
	}
	if len(done.CitationWarnings) != 0 {
		t.Fatalf("read citation should not warn, got %#v", done.CitationWarnings)
	}
}
