package ai

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// ---------------------------------------------------------------------------
// Retention of externalized results (issue #418)
// ---------------------------------------------------------------------------

func TestPruneResultsAge(t *testing.T) {
	root := t.TempDir()
	taskDir := filepath.Join(root, "task-1")
	if err := os.MkdirAll(taskDir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(taskDir, "desk_ls-001.txt")
	fresh := filepath.Join(taskDir, "desk_ls-002.txt")
	for _, p := range []string{old, fresh} {
		if err := os.WriteFile(p, []byte("result"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Age one result 48h into the past; the other stays fresh.
	aged := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(old, aged, aged); err != nil {
		t.Fatal(err)
	}

	deleted, err := PruneResults(root, 24*time.Hour, 0)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("deleted %d files, want 1", deleted)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("result older than maxAge must be pruned, stat err = %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh result must survive, stat err = %v", err)
	}
	// The task dir survives because it still holds a result.
	if _, err := os.Stat(taskDir); err != nil {
		t.Errorf("task dir with remaining results must survive, stat err = %v", err)
	}
}

func TestPruneResultsMaxPerTask(t *testing.T) {
	root := t.TempDir()
	taskDir := filepath.Join(root, "task-1")
	if err := os.MkdirAll(taskDir, 0o700); err != nil {
		t.Fatal(err)
	}
	names := []string{"a-001.txt", "a-002.txt", "a-003.txt"}
	for i, name := range names {
		p := filepath.Join(taskDir, name)
		if err := os.WriteFile(p, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
		// Distinct mtimes: file i is i hours old, so a-001.txt is the
		// newest and a-003.txt the oldest.
		age := time.Duration(i) * time.Hour
		ts := time.Now().Add(-age)
		if err := os.Chtimes(p, ts, ts); err != nil {
			t.Fatal(err)
		}
	}

	deleted, err := PruneResults(root, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("deleted %d files, want 1", deleted)
	}
	for _, name := range names[:2] {
		if _, err := os.Stat(filepath.Join(taskDir, name)); err != nil {
			t.Errorf("newest result %s must be kept, stat err = %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(taskDir, names[2])); !os.IsNotExist(err) {
		t.Errorf("oldest result %s must be pruned, stat err = %v", names[2], err)
	}
}

func TestPruneResultsEmptyDirRemoved(t *testing.T) {
	root := t.TempDir()
	taskDir := filepath.Join(root, "task-1")
	if err := os.MkdirAll(taskDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(taskDir, "desk_ls-001.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Age the result beyond the retention window so pruning removes it,
	// leaving the task dir empty.
	aged := time.Now().Add(-48 * time.Hour)
	p := filepath.Join(taskDir, "desk_ls-001.txt")
	if err := os.Chtimes(p, aged, aged); err != nil {
		t.Fatal(err)
	}

	deleted, err := PruneResults(root, 24*time.Hour, 0)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("deleted %d files, want 1", deleted)
	}
	if _, err := os.Stat(taskDir); !os.IsNotExist(err) {
		t.Errorf("task dir emptied by pruning must be removed, stat err = %v", err)
	}
}

func TestResultsSize(t *testing.T) {
	root := t.TempDir()
	taskDir := filepath.Join(root, "task-1")
	if err := os.MkdirAll(taskDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(taskDir, "a.txt"), []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(taskDir, "b.txt"), []byte("1234567890"), 0o600); err != nil {
		t.Fatal(err)
	}

	bytes, files, err := ResultsSize(root)
	if err != nil {
		t.Fatal(err)
	}
	if bytes != 15 || files != 2 {
		t.Fatalf("ResultsSize = %d bytes / %d files, want 15 / 2", bytes, files)
	}

	// A missing root is a clean zero report, not an error.
	bytes, files, err = ResultsSize(filepath.Join(root, "missing"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes != 0 || files != 0 {
		t.Fatalf("missing root: ResultsSize = %d bytes / %d files, want 0 / 0", bytes, files)
	}
}

func TestReadExternalizedAfterPrune(t *testing.T) {
	ext := NewExternalizer(t.TempDir(), "task-42")
	handle, err := ext.Externalize("desk_ls", 1, strings.Repeat("x", 9000))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadExternalized(ext.Root, handle); err != nil {
		t.Fatalf("result must be readable before pruning: %v", err)
	}

	// Age the result beyond the retention window, prune by age, and prove
	// the handle resolves to a clear not-found error afterwards.
	aged := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(filepath.Join(ext.Root, filepath.FromSlash(handle)), aged, aged); err != nil {
		t.Fatal(err)
	}
	deleted, err := PruneResults(ext.Root, 24*time.Hour, 0)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("pruned %d files, want 1", deleted)
	}
	if _, err := ReadExternalized(ext.Root, handle); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("read after prune must fail with a not-found error, got %v", err)
	}
}
