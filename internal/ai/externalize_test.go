package ai

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/danieljustus/symaira-desktop/internal/config"
)

// ---------------------------------------------------------------------------
// Tool-result externalization (issue #406)
// ---------------------------------------------------------------------------

func TestExternalizerThresholdsAndSkipSet(t *testing.T) {
	ext := NewExternalizer(t.TempDir(), "task-1")

	// Default threshold: anything over 8000 chars externalizes.
	if !ext.ShouldExternalize("desk_ls", DefaultExternalizeThreshold+1) {
		t.Error("desk_ls over default threshold should externalize")
	}
	if ext.ShouldExternalize("desk_ls", DefaultExternalizeThreshold) {
		t.Error("desk_ls at default threshold should stay inline")
	}
	// Per-tool threshold: desk_search stays inline up to 10000.
	if ext.ShouldExternalize("desk_search", 9000) {
		t.Error("desk_search under its per-tool threshold must stay inline")
	}
	if !ext.ShouldExternalize("desk_search", 10001) {
		t.Error("desk_search over its per-tool threshold must externalize")
	}
	// Skip-set: answer-shaped and write-shaped tools never externalize.
	for _, name := range []string{"desk_status", "desk_ask", "desk_transform", "desk_note_new", "desk_export"} {
		if ext.ShouldExternalize(name, 1<<20) {
			t.Errorf("%s must never be externalized (skip-set)", name)
		}
	}
}

func TestExternalizerWritesDeterministicHandles(t *testing.T) {
	ext := NewExternalizer(t.TempDir(), "task-42")

	handle, err := ext.Externalize("desk_ls", 1, strings.Repeat("x", 9000))
	if err != nil {
		t.Fatal(err)
	}
	want := "task-42/desk_ls-001.txt"
	if handle != want {
		t.Fatalf("handle = %q, want %q (deterministic taskId+tool+iteration path)", handle, want)
	}
	data, err := os.ReadFile(filepath.Join(ext.Root, filepath.FromSlash(want)))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 9000 {
		t.Fatalf("stored %d bytes, want 9000", len(data))
	}

	// Round-trip through the reader used by desk_read_result.
	got, err := ReadExternalized(ext.Root, handle)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(data) {
		t.Error("ReadExternalized round-trip mismatch")
	}
}

func TestReadExternalizedRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	for _, handle := range []string{
		"../secret.txt",
		"../../etc/passwd",
		"/etc/passwd",
		"task-1/../../etc/passwd",
		"..",
		"./../up.txt",
	} {
		if _, err := ReadExternalized(root, handle); err == nil {
			t.Errorf("handle %q must be rejected, got nil error", handle)
		}
	}
	// A missing file inside the root is a clean not-found, not traversal.
	if _, err := ReadExternalized(root, "task-1/desk_ls-001.txt"); err == nil {
		t.Error("missing handle must return an error")
	}
}

func TestResultsRootLivesOutsideVault(t *testing.T) {
	vault := t.TempDir()
	root := ResultsRoot(vault)
	if strings.HasPrefix(root, vault) {
		t.Errorf("results root must live outside the vault tree, got %q under vault %q", root, vault)
	}
	if !strings.Contains(root, "agent-results") {
		t.Errorf("results root should be under agent-results, got %q", root)
	}
}

func TestSummarizeCapsInlinePreview(t *testing.T) {
	big := strings.Repeat("y", 10000)
	s := summarize(big, externalizePreviewChars)
	if len(s) > externalizePreviewChars+len("\n…[externalized — full result via desk_read_result]")+1 {
		t.Errorf("summary too long: %d chars", len(s))
	}
	if !strings.Contains(s, "desk_read_result") {
		t.Error("summary must mention the read tool")
	}
}

func TestSummarizeNeverSplitsRune(t *testing.T) {
	// Multi-byte runes (umlauts, emoji) must never be cut mid-sequence:
	// the inline summary stays valid UTF-8 even when the byte cut lands
	// inside a rune, and an exact rune-boundary cut loses no bytes.
	big := strings.Repeat("äöüß", 3000) // 4 runes × 2 bytes each
	note := "\n…[externalized — full result via desk_read_result]"
	for _, max := range []int{5000, 5001, 5002, 4999} {
		s := summarize(big, max)
		if !utf8.ValidString(s) {
			t.Fatalf("summary (max=%d) contains invalid UTF-8", max)
		}
		if !strings.HasSuffix(s, note) {
			t.Errorf("summary (max=%d) must still end with the read-tool note", max)
		}
		cut := strings.TrimSuffix(s, note)
		if !strings.HasPrefix(big, cut) || len(cut) > max {
			t.Errorf("summary (max=%d) must be a prefix of the result (cut=%d bytes)", max, len(cut))
		}
	}
}

// configForTest returns a config rooted at a temp vault so the loop's
// externalizer writes into the test sandbox rather than a real results area.
func configForTest(t *testing.T) *config.Config {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Vault = t.TempDir()
	return cfg
}

// The loop externalizes oversized results and hands the model a summary
// plus the handle instead of the raw blob, while small results stay inline.
func TestRunAgentExternalizesOversizedResults(t *testing.T) {
	cfg := configForTest(t)
	cfg.AgentMaxIterations = 1

	// The blob's unique tail marker must never reach the model when the
	// result is externalized (the inline preview is capped).
	marker := "UNIQUE-TAIL-MARKER-" + strings.Repeat("t", 500)
	big := strings.Repeat("z", 20000) + marker
	tools := []AgentTool{
		{
			Name:        "desk_ls",
			Description: "list",
			ReadOnly:    true,
			Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
				return big, nil
			},
		},
	}
	turn := fakeTurn(AgentTurnResult{
		ToolCalls: []ToolCall{{ID: "c1", Name: "desk_ls", Input: json.RawMessage(`{}`)}},
	})
	events := collectAgentEvents(t, cfg, "q", tools, turn)

	var output string
	for _, e := range events {
		if e.Type == AIEventTool && e.ToolOutput != "" {
			output = e.ToolOutput
		}
	}
	if !strings.Contains(output, "desk_read_result") || !strings.Contains(output, "externalized") {
		t.Fatalf("expected externalization marker in model-visible output, got %q", output)
	}
	if strings.Contains(output, marker) {
		t.Error("model-visible output must not contain the raw oversized blob tail")
	}
}
