package ai

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Tool-result externalization (issue #406)
//
// Large tool results are written to a per-vault results area instead of being
// pushed verbatim through the conversation history. The model receives a
// compact summary plus a deterministic handle it can pass to the
// desk_read_result tool to fetch the full output on demand.
// ---------------------------------------------------------------------------

// externalizeThresholds maps tool names to the inline threshold in chars.
// Results at or below the threshold stay inline; larger results are
// externalized. Tools not listed use DefaultExternalizeThreshold.
var externalizeThresholds = map[string]int{
	// Search output is already compact (path/title/snippet list) and a
	// lossy preview forces a second read — keep it inline a bit longer
	// than other tools (upstream refinement: web-shaped results stay
	// inline up to 10k).
	"desk_search": 10000,
}

// externalizeSkip lists tools whose output must never be externalized:
// their output is itself the answer, or it is already a vault file.
var externalizeSkip = map[string]bool{
	"desk_status":      true, // tiny status object, itself the answer
	"desk_ask":         true, // output is the answer text
	"desk_transform":   true, // output is the transformed text (the answer)
	"desk_read_result": true, // output is itself the externalized content
	// Write-shaped tools produce a vault file; their result is a short
	// path confirmation, never a large blob to externalize.
	"desk_note_new":       true,
	"desk_export":         true,
	"desk_clip":           true,
	"desk_autofill":       true,
	"desk_ingest":         true,
	"desk_ingest_retry":   true,
	"desk_doc_set_status": true,
	"meeting_import":      true,
	"desk_meeting_import": true,
}

// DefaultExternalizeThreshold is the inline cap for tools without a
// per-tool threshold. It matches the loop's existing truncation cap so
// nothing that would have been truncated silently is lost: over the cap it
// is externalized instead of cut.
const DefaultExternalizeThreshold = 8000

// Externalizer writes over-threshold tool results to a per-vault results
// area with deterministic paths: <root>/<taskID>/<toolName>-<iteration>.txt
type Externalizer struct {
	// Root is the per-vault results directory (derived by ResultsRoot).
	Root string
	// TaskID scopes one agent run; deterministic handles are built from it.
	TaskID string
	// Thresholds overrides the default inline cap per tool.
	Thresholds map[string]int
	// Skip lists tools that are never externalized.
	Skip map[string]bool
}

// NewExternalizer builds an externalizer rooted at the per-vault results
// directory for taskID, with the canonical thresholds and skip-set.
func NewExternalizer(vaultRoot, taskID string) *Externalizer {
	return &Externalizer{
		Root:       ResultsRoot(vaultRoot),
		TaskID:     taskID,
		Thresholds: externalizeThresholds,
		Skip:       externalizeSkip,
	}
}

// newTaskID returns a unique, time-ordered task id for one agent run. It
// scopes externalized artifacts under the results root; handles stay
// deterministic within the run (taskID + toolName + iteration).
func newTaskID() string {
	return fmt.Sprintf("task-%d", time.Now().UnixNano())
}

// threshold returns the inline cap for toolName.
func (e *Externalizer) threshold(toolName string) int {
	if e.Thresholds != nil {
		if t, ok := e.Thresholds[toolName]; ok && t > 0 {
			return t
		}
	}
	return DefaultExternalizeThreshold
}

// ShouldExternalize reports whether the output of toolName with the given
// size would be externalized rather than kept inline.
func (e *Externalizer) ShouldExternalize(toolName string, size int) bool {
	if e.Skip != nil && e.Skip[toolName] {
		return false
	}
	return size > e.threshold(toolName)
}

// Handle returns the deterministic handle for one tool invocation. The
// path layout is <taskID>/<toolName>-<iteration>.txt, mirroring the
// upstream taskId+toolName+iteration scheme (no timestamps).
func (e *Externalizer) Handle(toolName string, iteration int) string {
	return fmt.Sprintf("%s/%s-%03d.txt", e.TaskID, toolName, iteration)
}

// Externalize writes output to the deterministic handle location and
// returns the handle. It is only called after ShouldExternalize.
func (e *Externalizer) Externalize(toolName string, iteration int, output string) (string, error) {
	handle := e.Handle(toolName, iteration)
	path := filepath.Join(e.Root, filepath.FromSlash(handle))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(output), 0o600); err != nil {
		return "", err
	}
	return handle, nil
}

// ResultsRoot returns the per-vault results directory for externalized
// tool output. It lives next to the sidecar database (outside the vault
// tree), so huge results never stall vault sync — the same failure mode
// the upstream project documented for their in-vault shadow repo.
func ResultsRoot(vaultRoot string) string {
	canonical := vaultRoot
	if resolved, err := filepath.EvalSymlinks(vaultRoot); err == nil {
		canonical = resolved
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.TempDir()
	}
	dataRoot := os.Getenv("XDG_DATA_HOME")
	if dataRoot == "" {
		dataRoot = filepath.Join(home, ".local", "share")
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(filepath.Clean(canonical))))
	return filepath.Join(dataRoot, "symdesk", "agent-results", digest[:16])
}

// ReadExternalized resolves a handle against root and returns the stored
// content. Handles are validated against path traversal: only files under
// root/<taskID>/ are readable.
func ReadExternalized(root, handle string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(handle))
	if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return "", fmt.Errorf("invalid externalized-result handle: %q", handle)
	}
	path := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("invalid externalized-result handle: %q", handle)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("externalized result %q not found: %w", handle, err)
	}
	return string(data), nil
}

// summarize truncates a long result for inline delivery, keeping the
// beginning (head) so the model sees structure before the handle.
func summarize(output string, max int) string {
	if len(output) <= max {
		return output
	}
	return output[:max] + "\n…[externalized — full result via desk_read_result]"
}
