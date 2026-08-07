package ai

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
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

// Retention policy for the externalized agent-results area (issue #418):
// task ids are unique per run, so without pruning the results area grows
// without bound on long-lived servers.
const (
	// DefaultResultsMaxAge is the default age after which a result is
	// dropped, regardless of how few results its task holds.
	DefaultResultsMaxAge = 30 * 24 * time.Hour
	// DefaultResultsMaxPerTask is the default cap on results kept per
	// task directory (newest wins).
	DefaultResultsMaxPerTask = 20
)

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
	rel, err := filepath.Rel(root, filepath.Join(root, clean))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid externalized-result handle: %q", handle)
	}
	// The handle is confined to root/<taskID>/ by the checks above; the
	// linter cannot see that invariant, so the read is annotated.
	data, err := os.ReadFile(filepath.Join(root, clean)) //nolint:gosec // G304: handle verified confined to root above
	if err != nil {
		return "", fmt.Errorf("externalized result %q not found: %w", handle, err)
	}
	return string(data), nil
}

// PruneResults enforces the retention policy on the results area (issue
// #418). Within each task directory it keeps the newest maxPerTask results
// and drops any result older than maxAge; task directories left empty are
// removed. It returns the number of files deleted. A missing root is a
// no-op (0, nil). maxAge <= 0 disables the age rule, maxPerTask <= 0 the
// count rule; only regular files are ever deleted.
func PruneResults(root string, maxAge time.Duration, maxPerTask int) (int, error) {
	tasks, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	var cutoff time.Time
	if maxAge > 0 {
		cutoff = time.Now().Add(-maxAge)
	}
	deleted := 0
	for _, task := range tasks {
		if !task.IsDir() {
			continue
		}
		taskDir := filepath.Join(root, task.Name())
		entries, err := os.ReadDir(taskDir)
		if err != nil {
			return deleted, err
		}
		type candidate struct {
			path string
			mod  time.Time
		}
		var files []candidate
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				return deleted, err
			}
			if !info.Mode().IsRegular() {
				continue
			}
			files = append(files, candidate{path: filepath.Join(taskDir, entry.Name()), mod: info.ModTime()})
		}
		// Newest first; both rules keep the newest results.
		sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
		for i, f := range files {
			if (maxAge > 0 && f.mod.Before(cutoff)) || (maxPerTask > 0 && i >= maxPerTask) {
				if err := os.Remove(f.path); err != nil && !os.IsNotExist(err) {
					return deleted, err
				}
				deleted++
			}
		}
		// Remove task directories that pruning emptied.
		if remaining, err := os.ReadDir(taskDir); err != nil {
			if !os.IsNotExist(err) {
				return deleted, err
			}
		} else if len(remaining) == 0 {
			if err := os.Remove(taskDir); err != nil && !os.IsNotExist(err) {
				return deleted, err
			}
		}
	}
	return deleted, nil
}

// ResultsSize reports the total bytes and file count of all regular files
// under root (recursive). A missing root reports 0, 0, nil.
func ResultsSize(root string) (int64, int, error) {
	var total int64
	var count int
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		total += info.Size()
		count++
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	return total, count, nil
}

// summarize truncates a long result for inline delivery, keeping the
// beginning (head) so the model sees structure before the handle. The cut
// backs off to a UTF-8 rune boundary so a multi-byte rune (umlauts, emoji)
// is never split mid-sequence — a truncated summary must stay valid UTF-8
// for the model conversation.
func summarize(output string, max int) string {
	if len(output) <= max {
		return output
	}
	cut := output[:max]
	// A byte cut can land inside a multi-byte rune (or leave a dangling
	// lead byte at the end). While the summary is not valid UTF-8, drop
	// the final rune — an exact rune-boundary cut is kept as-is.
	for !utf8.ValidString(cut) {
		_, size := utf8.DecodeLastRuneInString(cut)
		if size <= 0 {
			break // defensive: cannot happen for a valid-prefix input
		}
		cut = cut[:len(cut)-size]
	}
	return cut + "\n…[externalized — full result via desk_read_result]"
}
