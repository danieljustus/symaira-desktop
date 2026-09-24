package history

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestPortHistoryLifecycleContract records the Go history lifecycle — task
// checkpoints and the trash lifecycle — as an exact oracle for the Rust port
// (contract row VAULT-006). The fixture is checked in and must be reproduced
// byte for byte; regenerate it deliberately with PORT_GENERATE=1.
//
// The fixture shape follows testdata/port/vault/frontmatter-write.json: a
// pinned oracle block, source hashes for the covered Go files and one entry per
// case with the recorded side effects (file set, payload hashes, manifest
// bytes with wall-clock stamps replaced by placeholders) plus the exact Go
// error text and its class.
func TestPortHistoryLifecycleContract(t *testing.T) {
	document := buildHistoryLifecycleFixture(t)
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')

	path := filepath.Clean(historyLifecycleFixtureRel)
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	//nolint:gosec // fixture path is fixed relative to the repository
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v (run PORT_GENERATE=1 go test ./internal/history -run TestPortHistoryLifecycleContract)", err)
	}
	normalizedCurrent := filterHistoryPlatform(current, runtime.GOOS)
	normalizedEncoded := filterHistoryPlatform(encoded, runtime.GOOS)
	if !bytes.Equal(normalizedCurrent, normalizedEncoded) {
		t.Fatalf("history lifecycle fixture is stale; regenerate deliberately from the pinned Go oracle\n%s",
			historyCaseDifference(normalizedCurrent, normalizedEncoded))
	}
}

const historyLifecycleFixtureRel = "../../testdata/port/vault/history-lifecycle.json"

const (
	historyOracleCommit  = "6a91639f4f6ef8201cf4cbe7eed6ccc77a3874f1"
	historyOracleRelease = "post-v0.13.0-dependency-refresh"
)

type historyLifecycleFixture struct {
	SchemaVersion int                `json:"schema_version"`
	Oracle        historyOracleBlock `json:"oracle"`
	SourceHashes  map[string]string  `json:"source_hashes"`
	Cases         []historyCase      `json:"cases"`
}

type historyOracleBlock struct {
	Commit  string `json:"commit"`
	Release string `json:"release"`
}

type historyCase struct {
	ID string `json:"id"`
	// Description states the contract the case pins.
	Description string `json:"description"`
	// Operation names the ported entry point under test.
	Operation string `json:"operation"`
	// Platform is "any" or "unix". A Unix-only case either needs Unix
	// permission bits or exercises a Go path that the os.Root fs.FS rejects on
	// Windows; WindowsGap records which, so the gap is visible instead of
	// silently missing.
	Platform string `json:"platform"`
	// WindowsGap explains why Windows does not run the case.
	WindowsGap string `json:"windows_gap,omitempty"`
	// Files lists the vault files that exist before the operation.
	Files []historyFileSpec `json:"files"`
	// Call carries the sanitised operation arguments.
	Call historyCall `json:"call"`
	// Result is the operation's rendered return value (empty for failures).
	Result string `json:"result,omitempty"`
	// Error is the exact Go error text (empty on success).
	Error string `json:"error,omitempty"`
	// ErrorClass groups the error for platform-independent comparison.
	ErrorClass string `json:"error_class,omitempty"`
	// After lists the vault files (and history/trash artifacts) after the
	// operation, sorted by path.
	After []historyFileRecord `json:"after"`
}

type historyFileSpec struct {
	Path string `json:"path"`
	// Content is the file's UTF-8 content.
	Content string `json:"content,omitempty"`
	// ContentBase64 is used for non-UTF-8 payloads.
	ContentBase64 string `json:"content_base64,omitempty"`
	// Mode overrides the default 0644 file mode.
	Mode *uint32 `json:"mode,omitempty"`
}

type historyCall struct {
	// TaskID is the checkpoint task id.
	TaskID string `json:"task_id,omitempty"`
	// Path is the vault-relative path argument.
	Path string `json:"path,omitempty"`
	// SecondPath is a path argument applied before path (checkpoint order).
	SecondPath string `json:"second_path,omitempty"`
	// Name is the trash item name argument.
	Name string `json:"name,omitempty"`
	// MaxAgeSeconds is the trash purge age; negative means "purge all".
	MaxAgeSeconds int64 `json:"max_age_seconds,omitempty"`
	// WriteContent is written to Path after the checkpoint was taken.
	WriteContent string `json:"write_content,omitempty"`
	// CreatePath is created (with CreateContent) after the checkpoint.
	CreatePath    string `json:"create_path,omitempty"`
	CreateContent string `json:"create_content,omitempty"`
	// ExtraTaskID is a second checkpoint task, for list ordering.
	ExtraTaskID string `json:"extra_task_id,omitempty"`
	// ExtraTrashPath is a second trashed file, for list ordering.
	ExtraTrashPath string `json:"extra_trash_path,omitempty"`
	// PurgeMetaAgeSeconds sets how old the purged trash metadata claims to be.
	PurgeMetaAgeSeconds int64 `json:"purge_meta_age_seconds,omitempty"`
}

type historyFileRecord struct {
	Path   string  `json:"path"`
	Mode   *uint32 `json:"mode,omitempty"`
	Size   int64   `json:"size"`
	SHA256 string  `json:"sha256"`
	// Content is the normalised UTF-8 content (empty for binary payloads).
	Content string `json:"content,omitempty"`
}

// historyTimestampPattern replaces wall-clock stamps so the document is
// reproducible. The Rust replay applies the same substitution.
var historyTimestampPattern = regexp.MustCompile(`"(timestamp|deleted_at)": "[^"]*"`)

func buildHistoryLifecycleFixture(t *testing.T) historyLifecycleFixture {
	t.Helper()
	hashes, err := historySourceHashes()
	if err != nil {
		t.Fatal(err)
	}
	gated := []historyCase{
		historyCaseCheckpointBegin(t),
		historyCaseCheckpointIdempotent(t),
		historyCaseCheckpointExistingFile(t),
		historyCaseCheckpointNewFile(t),
		historyCaseCheckpointUndo(t),
		historyCaseCheckpointInvalidTaskID(t),
		historyCaseCheckpointList(t),
		historyCaseTrashListEmpty(t),
		historyCaseTrashListOrder(t),
		historyCaseTrashListStrictCorrupt(t),
		historyCaseTrashListStrictOrphan(t),
		historyCaseTrashRestore(t),
		historyCaseTrashRestoreConflict(t),
		historyCaseTrashRestoreMissing(t),
		historyCaseTrashRestoreInvalidName(t),
		historyCaseTrashPurgeAll(t),
		historyCaseTrashPurgeByAge(t),
		historyCaseTrashPurgeRefusesCorrupt(t),
	}
	cases := gated
	if runtime.GOOS == "windows" {
		cases = make([]historyCase, 0, len(gated))
		for _, item := range gated {
			if item.Platform == "unix" {
				continue
			}
			cases = append(cases, item)
		}
	}
	return historyLifecycleFixture{
		SchemaVersion: 1,
		Oracle: historyOracleBlock{
			Commit:  historyOracleCommit,
			Release: historyOracleRelease,
		},
		SourceHashes: hashes,
		Cases:        cases,
	}
}

func historySourceHashes() (map[string]string, error) {
	paths := []string{
		"internal/history/history.go",
		"internal/history/checkpoint.go",
		"internal/history/trash.go",
	}
	out := make(map[string]string, len(paths))
	for _, rel := range paths {
		//nolint:gosec // repository-relative source path
		data, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(data)
		out[rel] = hex.EncodeToString(sum[:])
	}
	return out, nil
}

// scenario describes one isolated vault used by a single case.
type scenario struct {
	root  string
	store *Store
}

func newScenario(t *testing.T, files []historyFileSpec) *scenario {
	t.Helper()
	// A self-managed directory instead of t.TempDir(): the history Store caches
	// an os.Root, which Go does not close, so on Windows the directory handle is
	// still open when t.TempDir's cleanup runs and the removal would fail the
	// test after every assertion passed (see #964).
	root, err := os.MkdirTemp("", "symdesk-port-history-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	for _, spec := range files {
		writeScenarioFile(t, root, spec, 0o644)
	}
	return &scenario{root: root, store: NewStore(root)}
}

func writeScenarioFile(t *testing.T, root string, spec historyFileSpec, fallback os.FileMode) {
	t.Helper()
	target := filepath.Join(root, filepath.FromSlash(spec.Path))
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		t.Fatal(err)
	}
	data := []byte(spec.Content)
	if spec.ContentBase64 != "" {
		decoded, err := hex.DecodeString(spec.ContentBase64)
		if err != nil {
			t.Fatal(err)
		}
		data = decoded
	}
	mode := fallback
	if spec.Mode != nil {
		mode = os.FileMode(*spec.Mode) //nolint:gosec // explicit fixture mode
	}
	//nolint:gosec // fixture payload written with the case's declared mode
	if err := os.WriteFile(target, data, mode); err != nil {
		t.Fatal(err)
	}
	if spec.Mode != nil {
		if err := os.Chmod(target, mode); err != nil { //nolint:gosec // restoring the case mode past umask
			t.Fatal(err)
		}
	}
}

// recordHistoryCase runs one operation and records its observable effects.
func recordHistoryCase(t *testing.T, document historyCase, run func(*scenario) (string, error)) historyCase {
	t.Helper()
	if document.Platform == "unix" && runtime.GOOS == "windows" {
		// Windows does not run this case: it needs Unix permission bits or a Go
		// path that #962 records as broken there. Nothing executes, so the case
		// cannot fail the Windows lane, and the comparison drops it on both
		// sides of the drift check.
		document.After = []historyFileRecord{}
		return document
	}
	s := newScenario(t, document.Files)
	result, err := run(s)
	if err != nil {
		document.Error = historyErrorText(err)
		document.ErrorClass = historyErrorClass(err)
	} else {
		document.Result = result
	}
	document.After = historyStateOf(t, s.root)
	return document
}

// historyStateOf records every file below root with its mode, size, hash and
// normalised content, skipping the object blobs (their names already are their
// hashes) so the document stays readable.
func historyStateOf(t *testing.T, root string) []historyFileRecord {
	t.Helper()
	out := []historyFileRecord{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		if strings.HasPrefix(rel, filepath.ToSlash(objectsRelDir())+"/") {
			return nil
		}
		//nolint:gosec // repository-relative path under the case root
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		sum := sha256.Sum256(data)
		record := historyFileRecord{
			Path:   rel,
			Size:   int64(len(data)),
			SHA256: hex.EncodeToString(sum[:]),
		}
		if mode := historyModeOf(info); mode != nil {
			record.Mode = mode
		}
		if content, ok := normaliseHistoryText(data); ok {
			record.Content = content
			record.SHA256 = ""
			record.Size = int64(len(content))
		}
		out = append(out, record)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// normaliseHistoryText replaces wall-clock stamps inside text artifacts so the
// recorded bytes are reproducible; binary payloads are recorded by hash.
func normaliseHistoryText(data []byte) (string, bool) {
	if bytes.IndexByte(data, 0) >= 0 {
		return "", false
	}
	text := string(data)
	if !utf8Valid(text) {
		return "", false
	}
	return historyTimestampPattern.ReplaceAllString(text, `"$1": "{{$1}}"`), true
}

func utf8Valid(text string) bool {
	for _, r := range text {
		if r == '\uFFFD' {
			return false
		}
	}
	return true
}

func historyModeOf(info os.FileInfo) *uint32 {
	if runtime.GOOS == "windows" {
		return nil
	}
	mode := uint32(info.Mode().Perm())
	return &mode
}

// historyErrorText returns the part of the error message the Rust port must
// reproduce byte for byte. OS-produced tails (syscall text) and third-party
// JSON-decoder wording are dropped there and covered by the class instead.
func historyErrorText(err error) string {
	message := err.Error()
	switch {
	case strings.HasPrefix(message, "trash item ") && strings.Contains(message, " not found: "):
		index := strings.Index(message, " not found: ")
		return message[:index+len(" not found: ")]
	case strings.HasPrefix(message, "corrupt trash metadata for "),
		strings.HasPrefix(message, "corrupt checkpoint manifest for "):
		if index := strings.Index(message, ": "); index >= 0 {
			return message[:index+2]
		}
	}
	return message
}

func historyErrorClass(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	switch {
	case strings.HasPrefix(message, "task id is required"):
		return "task_id_required"
	case strings.HasPrefix(message, "invalid task id:"):
		return "invalid_task_id"
	case strings.HasPrefix(message, "corrupt checkpoint manifest for"):
		return "corrupt_checkpoint"
	case strings.HasPrefix(message, "cannot trash a directory: "):
		return "trash_directory"
	case strings.HasPrefix(message, "invalid trash item name:"):
		return "invalid_trash_name"
	case strings.HasPrefix(message, "trash item ") && strings.Contains(message, "not found"):
		return "trash_not_found"
	case strings.HasPrefix(message, "corrupt trash metadata for"):
		return "corrupt_trash_metadata"
	case strings.HasPrefix(message, "invalid trash inventory: "):
		return "trash_inventory"
	case strings.HasPrefix(message, "trash metadata name mismatch:"),
		strings.HasPrefix(message, "trash metadata path mismatch for "),
		strings.HasPrefix(message, "trash payload size mismatch for "),
		strings.HasPrefix(message, "invalid trash metadata for "):
		return "trash_metadata_mismatch"
	case strings.HasPrefix(message, "cannot restore "):
		return "trash_restore_conflict"
	case strings.HasPrefix(message, "invalid vault-relative path"):
		return "invalid_path"
	default:
		return "other"
	}
}

// filterHistoryPlatform removes the platform-dependent parts of the document
// before comparison so the Go check also runs on Windows.
func filterHistoryPlatform(document []byte, goos string) []byte {
	if goos != "windows" {
		return document
	}
	var value historyLifecycleFixture
	if err := json.Unmarshal(document, &value); err != nil {
		return document
	}
	kept := make([]historyCase, 0, len(value.Cases))
	for _, item := range value.Cases {
		if item.Platform == "unix" {
			continue
		}
		for record := range item.After {
			item.After[record].Mode = nil
		}
		for file := range item.Files {
			item.Files[file].Mode = nil
		}
		kept = append(kept, item)
	}
	value.Cases = kept
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return document
	}
	return append(encoded, '\n')
}

// historyCaseDifference attributes a drift to one case and field; a raw line
// diff misleads as soon as one side omits or reorders a case.
func historyCaseDifference(want, got []byte) string {
	var wantDoc, gotDoc historyLifecycleFixture
	if err := json.Unmarshal(want, &wantDoc); err != nil {
		return err.Error()
	}
	if err := json.Unmarshal(got, &gotDoc); err != nil {
		return err.Error()
	}
	gotByID := make(map[string]string, len(gotDoc.Cases))
	for _, item := range gotDoc.Cases {
		encoded, err := json.Marshal(item)
		if err != nil {
			return err.Error()
		}
		gotByID[item.ID] = string(encoded)
	}
	for _, item := range wantDoc.Cases {
		encoded, err := json.Marshal(item)
		if err != nil {
			return err.Error()
		}
		counterpart, ok := gotByID[item.ID]
		if !ok {
			return fmt.Sprintf("fixture case %q is missing from the generated document", item.ID)
		}
		if counterpart != string(encoded) {
			return fmt.Sprintf("case %q differs:\n  fixture:   %s\n  generated: %s", item.ID, string(encoded), counterpart)
		}
		delete(gotByID, item.ID)
	}
	if len(gotByID) > 0 {
		for id := range gotByID {
			return fmt.Sprintf("generated case %q is missing from the fixture", id)
		}
	}
	return "cases are equal; only the surrounding document differs"
}

func historyCaseCheckpointBegin(t *testing.T) historyCase {
	t.Helper()
	document := historyCase{
		ID:          "checkpoint-begin",
		Description: "begin creates an empty manifest; the timestamp is the only wall-clock stamp",
		Operation:   "begin_checkpoint",
		Platform:    "any",
		Files:       []historyFileSpec{{Path: "notes/a.md", Content: "alpha\n"}},
		Call:        historyCall{TaskID: "task-1"},
	}
	return recordHistoryCase(t, document, func(s *scenario) (string, error) {
		checkpoint, err := s.store.BeginCheckpoint("task-1")
		if err != nil {
			return "", err
		}
		return renderCheckpoint(checkpoint), nil
	})
}

func historyCaseCheckpointIdempotent(t *testing.T) historyCase {
	t.Helper()
	document := historyCase{
		ID:          "checkpoint-begin-idempotent",
		Description: "a second begin keeps the first checkpoint and its timestamp",
		Operation:   "begin_checkpoint_twice",
		Platform:    "any",
		Files:       []historyFileSpec{{Path: "notes/a.md", Content: "alpha\n"}},
		Call:        historyCall{TaskID: "task-1"},
	}
	return recordHistoryCase(t, document, func(s *scenario) (string, error) {
		first, err := s.store.BeginCheckpoint("task-1")
		if err != nil {
			return "", err
		}
		second, err := s.store.BeginCheckpoint("task-1")
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("same_timestamp=%t same_files=%t", first.Timestamp.Equal(second.Timestamp), len(second.Files) == 0), nil
	})
}

func historyCaseCheckpointExistingFile(t *testing.T) historyCase {
	t.Helper()
	document := historyCase{
		ID:          "checkpoint-file-existing",
		Description: "an existing file is snapshotted before the task's first write and never re-snapshotted",
		Operation:   "checkpoint_file",
		Platform:    "any",
		Files:       []historyFileSpec{{Path: "notes/a.md", Content: "alpha\n"}},
		Call:        historyCall{TaskID: "task-1", Path: "notes/a.md"},
	}
	return recordHistoryCase(t, document, func(s *scenario) (string, error) {
		if _, err := s.store.CheckpointFile("task-1", "notes/a.md"); err != nil {
			return "", err
		}
		// The task overwrites the file; a second checkpoint must keep the
		// pre-task content.
		//nolint:gosec // case fixture file written with the mode the case declares
		if err := os.WriteFile(filepath.Join(s.root, "notes", "a.md"), []byte("rewritten\n"), 0o644); err != nil {
			return "", err
		}
		checkpoint, err := s.store.CheckpointFile("task-1", "notes/a.md")
		if err != nil {
			return "", err
		}
		content, err := s.store.Content(checkpoint.Files[0].Entry.ID)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("files=%d new=%d skipped=%d blob=%q", len(checkpoint.Files), len(checkpoint.NewFiles), len(checkpoint.Skipped), string(content)), nil
	})
}

func historyCaseCheckpointNewFile(t *testing.T) historyCase {
	t.Helper()
	document := historyCase{
		ID:          "checkpoint-file-new",
		Description: "a missing file is recorded as new so undo deletes it",
		Operation:   "checkpoint_file",
		Platform:    "any",
		Call:        historyCall{TaskID: "task-1", Path: "notes/new.md"},
	}
	return recordHistoryCase(t, document, func(s *scenario) (string, error) {
		checkpoint, err := s.store.CheckpointFile("task-1", "notes/new.md")
		if err != nil {
			return "", err
		}
		return renderCheckpoint(checkpoint), nil
	})
}

func historyCaseCheckpointUndo(t *testing.T) historyCase {
	t.Helper()
	document := historyCase{
		ID:          "checkpoint-undo",
		Description: "undo restores recorded files and deletes files the task created",
		Operation:   "undo_checkpoint",
		Platform:    "any",
		Files:       []historyFileSpec{{Path: "notes/a.md", Content: "alpha\n"}},
		Call: historyCall{
			TaskID:        "task-1",
			Path:          "notes/a.md",
			WriteContent:  "rewritten by the task\n",
			CreatePath:    "notes/created.md",
			CreateContent: "created by the task\n",
		},
	}
	return recordHistoryCase(t, document, func(s *scenario) (string, error) {
		if _, err := s.store.CheckpointFile("task-1", "notes/a.md"); err != nil {
			return "", err
		}
		if _, err := s.store.CheckpointFile("task-1", "notes/created.md"); err != nil {
			return "", err
		}
		//nolint:gosec // case fixture file written with the mode the case declares
		if err := os.WriteFile(filepath.Join(s.root, "notes", "a.md"), []byte("rewritten by the task\n"), 0o644); err != nil {
			return "", err
		}
		//nolint:gosec // case fixture file written with the mode the case declares
		if err := os.WriteFile(filepath.Join(s.root, "notes", "created.md"), []byte("created by the task\n"), 0o644); err != nil {
			return "", err
		}
		checkpoint, err := s.store.UndoCheckpoint("task-1")
		if err != nil {
			return "", err
		}
		return renderCheckpoint(checkpoint), nil
	})
}

func historyCaseCheckpointInvalidTaskID(t *testing.T) historyCase {
	t.Helper()
	document := historyCase{
		ID:          "checkpoint-invalid-task-id",
		Description: "task ids that escape the checkpoints directory are rejected",
		Operation:   "begin_checkpoint",
		Platform:    "any",
		Call:        historyCall{TaskID: "../escape"},
	}
	return recordHistoryCase(t, document, func(s *scenario) (string, error) {
		var rejected []string
		for _, taskID := range []string{"", "../escape", ".hidden", "a/b", "a:b", ".", ".."} {
			if _, err := s.store.BeginCheckpoint(taskID); err != nil {
				rejected = append(rejected, fmt.Sprintf("%q=%s", taskID, historyErrorClass(err)))
			}
		}
		if len(rejected) != 7 {
			return "", fmt.Errorf("only %d of 7 invalid task ids were rejected", len(rejected))
		}
		return strings.Join(rejected, " "), nil
	})
}

func historyCaseCheckpointList(t *testing.T) historyCase {
	t.Helper()
	document := historyCase{
		ID:          "checkpoint-list",
		Description: "listing returns newest first and skips a corrupt manifest",
		Operation:   "list_checkpoints",
		Platform:    "unix",
		WindowsGap:  "ListCheckpoints reads through os.Root fs.FS, whose path rules reject Windows separators (#962)",
		Files:       []historyFileSpec{{Path: "notes/a.md", Content: "alpha\n"}},
		Call:        historyCall{TaskID: "task-1", ExtraTaskID: "task-2"},
	}
	return recordHistoryCase(t, document, func(s *scenario) (string, error) {
		first, err := s.store.BeginCheckpoint("task-1")
		if err != nil {
			return "", err
		}
		time.Sleep(5 * time.Millisecond)
		second, err := s.store.BeginCheckpoint("task-2")
		if err != nil {
			return "", err
		}
		corrupt := filepath.Join(s.root, checkpointsRelDir(), "broken.json")
		//nolint:gosec // case fixture file written with the mode the case declares
		if err := os.WriteFile(corrupt, []byte("{not json"), 0o644); err != nil {
			return "", err
		}
		checkpoints, err := s.store.ListCheckpoints()
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("count=%d first=%s second=%s newer=%t", len(checkpoints), checkpoints[0].TaskID, checkpoints[1].TaskID, second.Timestamp.After(first.Timestamp)), nil
	})
}

// --- trash cases ------------------------------------------------------------

func historyCaseTrashListEmpty(t *testing.T) historyCase {
	t.Helper()
	document := historyCase{
		ID:          "trash-list-empty",
		Description: "a missing trash directory lists as empty, not as an error",
		Operation:   "trash_list",
		Platform:    "unix",
		WindowsGap:  "TrashList reads through os.Root fs.FS, whose path rules reject Windows separators (#962)",
		Call:        historyCall{},
	}
	return recordHistoryCase(t, document, func(s *scenario) (string, error) {
		entries, err := s.store.TrashList()
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("list=%d strict=%d", len(entries), 0), nil
	})
}

func historyCaseTrashListOrder(t *testing.T) historyCase {
	t.Helper()
	document := historyCase{
		ID:          "trash-list-order",
		Description: "trash entries are listed newest deletion first",
		Operation:   "trash_list",
		Platform:    "unix",
		WindowsGap:  "TrashList/TrashListStrict read through os.Root fs.FS, whose path rules reject Windows separators (#962)",
		Files: []historyFileSpec{
			{Path: "notes/a.md", Content: "alpha\n"},
			{Path: "notes/b.md", Content: "bravo\n"},
		},
		Call: historyCall{Path: "notes/a.md", ExtraTrashPath: "notes/b.md"},
	}
	return recordHistoryCase(t, document, func(s *scenario) (string, error) {
		if _, err := s.store.Trash("notes/a.md"); err != nil {
			return "", err
		}
		time.Sleep(5 * time.Millisecond)
		if _, err := s.store.Trash("notes/b.md"); err != nil {
			return "", err
		}
		entries, err := s.store.TrashList()
		if err != nil {
			return "", err
		}
		strict, err := s.store.TrashListStrict()
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("count=%d strict=%d first=%s second=%s", len(entries), len(strict), entries[0].OriginalPath, entries[1].OriginalPath), nil
	})
}

func historyCaseTrashListStrictCorrupt(t *testing.T) historyCase {
	t.Helper()
	document := historyCase{
		ID:          "trash-list-strict-corrupt-metadata",
		Description: "the strict inventory refuses corrupt metadata instead of skipping it",
		Operation:   "trash_list_strict",
		Platform:    "unix",
		WindowsGap:  "TrashListStrict reads through os.Root fs.FS, whose path rules reject Windows separators (#962)",
		Files:       []historyFileSpec{{Path: "notes/a.md", Content: "alpha\n"}},
		Call:        historyCall{Path: "notes/a.md"},
	}
	return recordHistoryCase(t, document, func(s *scenario) (string, error) {
		entry, err := s.store.Trash("notes/a.md")
		if err != nil {
			return "", err
		}
		if len(entriesLenient(s)) != 1 {
			t.Fatal("lenient listing should still see the corrupt entry")
		}
		meta := filepath.Join(s.root, trashRelDir(), entry.Name+trashMetaSuffix)
		//nolint:gosec // case fixture file written with the mode the case declares
		if err := os.WriteFile(meta, []byte("{not json"), 0o644); err != nil {
			return "", err
		}
		if _, err := s.store.TrashListStrict(); err != nil {
			return "", err
		}
		return "strict accepted corrupt metadata", nil
	})
}

func entriesLenient(s *scenario) []TrashEntry {
	entries, err := s.store.TrashList()
	if err != nil {
		return nil
	}
	return entries
}

func historyCaseTrashListStrictOrphan(t *testing.T) historyCase {
	t.Helper()
	document := historyCase{
		ID:          "trash-list-strict-orphan-payload",
		Description: "the strict inventory refuses a payload without metadata",
		Operation:   "trash_list_strict",
		Platform:    "unix",
		WindowsGap:  "TrashListStrict reads through os.Root fs.FS, whose path rules reject Windows separators (#962)",
		Files:       []historyFileSpec{{Path: "notes/a.md", Content: "alpha\n"}},
		Call:        historyCall{Path: "notes/a.md"},
	}
	return recordHistoryCase(t, document, func(s *scenario) (string, error) {
		if _, err := s.store.Trash("notes/a.md"); err != nil {
			return "", err
		}
		orphan := filepath.Join(s.root, trashRelDir(), "orphan.md")
		//nolint:gosec // case fixture file written with the mode the case declares
		if err := os.WriteFile(orphan, []byte("orphan\n"), 0o644); err != nil {
			return "", err
		}
		if _, err := s.store.TrashListStrict(); err != nil {
			return "", err
		}
		return "strict accepted an orphan payload", nil
	})
}

func historyCaseTrashRestore(t *testing.T) historyCase {
	t.Helper()
	document := historyCase{
		ID:          "trash-restore",
		Description: "restore moves the payload back and removes both trash artifacts",
		Operation:   "trash_restore",
		Platform:    "any",
		Files:       []historyFileSpec{{Path: "notes/deep/a.md", Content: "alpha\n"}},
		Call:        historyCall{Path: "notes/deep/a.md"},
	}
	return recordHistoryCase(t, document, func(s *scenario) (string, error) {
		entry, err := s.store.Trash("notes/deep/a.md")
		if err != nil {
			return "", err
		}
		restored, err := s.store.TrashRestore(entry.Name)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("name=%s original=%s size=%d", restored.Name, restored.OriginalPath, restored.Size), nil
	})
}

func historyCaseTrashRestoreConflict(t *testing.T) historyCase {
	t.Helper()
	document := historyCase{
		ID:          "trash-restore-conflict",
		Description: "restore refuses to overwrite an occupied original path",
		Operation:   "trash_restore",
		Platform:    "any",
		Files:       []historyFileSpec{{Path: "notes/a.md", Content: "alpha\n"}},
		Call:        historyCall{Path: "notes/a.md"},
	}
	return recordHistoryCase(t, document, func(s *scenario) (string, error) {
		entry, err := s.store.Trash("notes/a.md")
		if err != nil {
			return "", err
		}
		//nolint:gosec // case fixture file written with the mode the case declares
		if err := os.WriteFile(filepath.Join(s.root, "notes", "a.md"), []byte("replacement\n"), 0o644); err != nil {
			return "", err
		}
		restored, err := s.store.TrashRestore(entry.Name)
		if err != nil {
			return "", err
		}
		_ = restored
		return "restore overwrote the occupied path", nil
	})
}

func historyCaseTrashRestoreMissing(t *testing.T) historyCase {
	t.Helper()
	document := historyCase{
		ID:          "trash-restore-missing",
		Description: "restoring an unknown item fails without touching the trash",
		Operation:   "trash_restore",
		Platform:    "unix",
		WindowsGap:  "the precondition uses TrashListStrict, which rejects Windows separators (#962)",
		Call:        historyCall{Name: "does-not-exist.md"},
	}
	return recordHistoryCase(t, document, func(s *scenario) (string, error) {
		if _, err := s.store.TrashListStrict(); err != nil {
			t.Fatalf("empty strict inventory failed: %v", err)
		}
		if _, err := s.store.TrashRestore("does-not-exist.md"); err != nil {
			return "", err
		}
		return "restore accepted an unknown item", nil
	})
}

func historyCaseTrashRestoreInvalidName(t *testing.T) historyCase {
	t.Helper()
	document := historyCase{
		ID:          "trash-restore-invalid-name",
		Description: "item names with separators are rejected before any file access",
		Operation:   "trash_restore",
		Platform:    "any",
		Call:        historyCall{Name: "../escape"},
	}
	return recordHistoryCase(t, document, func(s *scenario) (string, error) {
		for _, name := range []string{"../escape", "a/b", ".", ".."} {
			if _, err := s.store.TrashRestore(name); err == nil {
				return "", fmt.Errorf("restore accepted name %q", name)
			}
		}
		_, err := s.store.TrashRestore("../escape")
		if err != nil {
			return "", err
		}
		return "", nil
	})
}

func historyCaseTrashPurgeAll(t *testing.T) historyCase {
	t.Helper()
	document := historyCase{
		ID:          "trash-purge-all",
		Description: "a non-positive age purges every entry and leaves the trash empty",
		Operation:   "trash_purge",
		Platform:    "unix",
		WindowsGap:  "TrashPurge validates through TrashListStrict, which rejects Windows separators (#962)",
		Files: []historyFileSpec{
			{Path: "notes/a.md", Content: "alpha\n"},
			{Path: "notes/b.md", Content: "bravo\n"},
		},
		Call: historyCall{Path: "notes/a.md", ExtraTrashPath: "notes/b.md", MaxAgeSeconds: 0},
	}
	return recordHistoryCase(t, document, func(s *scenario) (string, error) {
		if _, err := s.store.Trash("notes/a.md"); err != nil {
			return "", err
		}
		if _, err := s.store.Trash("notes/b.md"); err != nil {
			return "", err
		}
		purged, err := s.store.TrashPurge(0)
		if err != nil {
			return "", err
		}
		entries, err := s.store.TrashListStrict()
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("purged=%d remaining=%d", purged, len(entries)), nil
	})
}

func historyCaseTrashPurgeByAge(t *testing.T) historyCase {
	t.Helper()
	document := historyCase{
		ID:          "trash-purge-by-age",
		Description: "an entry older than the age is purged, a fresher one is kept",
		Operation:   "trash_purge",
		Platform:    "unix",
		WindowsGap:  "TrashPurge validates through TrashListStrict, which rejects Windows separators (#962)",
		Files: []historyFileSpec{
			{Path: "notes/old.md", Content: "old\n"},
			{Path: "notes/fresh.md", Content: "fresh\n"},
		},
		Call: historyCall{Path: "notes/old.md", PurgeMetaAgeSeconds: 7200, MaxAgeSeconds: 3600},
	}
	return recordHistoryCase(t, document, func(s *scenario) (string, error) {
		old, err := s.store.Trash("notes/old.md")
		if err != nil {
			return "", err
		}
		if _, err := s.store.Trash("notes/fresh.md"); err != nil {
			return "", err
		}
		meta := filepath.Join(s.root, trashRelDir(), old.Name+trashMetaSuffix)
		aged := old
		aged.DeletedAt = time.Now().UTC().Add(-2 * time.Hour)
		data, err := json.MarshalIndent(aged, "", "  ")
		if err != nil {
			return "", err
		}
		//nolint:gosec // case fixture file written with the mode the case declares
		if err := os.WriteFile(meta, data, 0o644); err != nil {
			return "", err
		}
		purged, err := s.store.TrashPurge(time.Hour)
		if err != nil {
			return "", err
		}
		entries, err := s.store.TrashListStrict()
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("purged=%d remaining=%d kept=%s", purged, len(entries), entries[0].OriginalPath), nil
	})
}

func historyCaseTrashPurgeRefusesCorrupt(t *testing.T) historyCase {
	t.Helper()
	document := historyCase{
		ID:          "trash-purge-refuses-corrupt",
		Description: "purge fails closed on a corrupt inventory and removes nothing",
		Operation:   "trash_purge",
		Platform:    "unix",
		WindowsGap:  "TrashPurge validates through TrashListStrict, which rejects Windows separators (#962)",
		Files:       []historyFileSpec{{Path: "notes/a.md", Content: "alpha\n"}},
		Call:        historyCall{Path: "notes/a.md", MaxAgeSeconds: 0},
	}
	return recordHistoryCase(t, document, func(s *scenario) (string, error) {
		entry, err := s.store.Trash("notes/a.md")
		if err != nil {
			return "", err
		}
		meta := filepath.Join(s.root, trashRelDir(), entry.Name+trashMetaSuffix)
		//nolint:gosec // case fixture file written with the mode the case declares
		if err := os.WriteFile(meta, []byte("{}"), 0o644); err != nil {
			return "", err
		}
		if _, err := s.store.TrashPurge(0); err != nil {
			return "", err
		}
		return "purge accepted a corrupt inventory", nil
	})
}

func renderCheckpoint(checkpoint *Checkpoint) string {
	if checkpoint == nil {
		return "none"
	}
	files := make([]string, 0, len(checkpoint.Files))
	for _, file := range checkpoint.Files {
		files = append(files, fmt.Sprintf("%s@%s", file.RelPath, file.Entry.ID))
	}
	return fmt.Sprintf("task=%s files=[%s] new=[%s] skipped=[%s] partial=%t",
		checkpoint.TaskID, strings.Join(files, " "), strings.Join(checkpoint.NewFiles, " "),
		strings.Join(checkpoint.Skipped, " "), checkpoint.Partial())
}
