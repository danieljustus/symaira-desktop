package history

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// CheckpointFile is one file grouped into a task checkpoint. It references
// the content-addressed blob captured before the task's first write.
type CheckpointFile struct {
	// RelPath is the vault-relative path of the file.
	RelPath string `json:"rel_path"`
	// Entry is the snapshot (blob id, timestamp, size) taken before the
	// task's first write. The blob lives in the same object store as every
	// other history snapshot.
	Entry Entry `json:"entry"`
}

// Checkpoint groups content-addressed blobs under one task id so an entire
// agent run can be rejected as a unit ("undo what the agent just did").
// It reuses the existing blob store: the diff is grouping + restore
// semantics, not a second storage engine.
type Checkpoint struct {
	// TaskID identifies the agent run (or any caller-chosen task).
	TaskID string `json:"task_id"`
	// Timestamp is when the checkpoint was created (UTC).
	Timestamp time.Time `json:"timestamp"`
	// Files lists files that existed before the task and were snapshotted.
	// Undo restores each from its blob.
	Files []CheckpointFile `json:"files"`
	// NewFiles lists vault-relative paths that did not exist when the
	// checkpoint was taken. If the task creates them, undo deletes them.
	NewFiles []string `json:"new_files"`
	// Skipped lists paths that could not be snapshotted. A checkpoint with
	// a non-empty Skipped is a *partial* checkpoint and is reported as
	// such instead of implying completeness.
	Skipped []string `json:"skipped"`
}

// checkpointsDir is where task checkpoint manifests live.
func (s *Store) checkpointsDir() string {
	return filepath.Join(s.historyDir(), "checkpoints")
}

func (s *Store) checkpointPath(taskID string) (string, error) {
	if err := validateTaskID(taskID); err != nil {
		return "", err
	}
	return filepath.Join(s.checkpointsDir(), taskID+".json"), nil
}

// validateTaskID rejects task ids that could escape the checkpoints
// directory (path traversal) or collide with dot-files.
func validateTaskID(taskID string) error {
	if taskID == "" {
		return fmt.Errorf("task id is required")
	}
	if strings.ContainsAny(taskID, "/\\") || taskID == "." || taskID == ".." ||
		strings.HasPrefix(taskID, ".") || strings.Contains(taskID, ":") {
		return fmt.Errorf("invalid task id: %q", taskID)
	}
	return nil
}

// BeginCheckpoint starts (or resumes) a task checkpoint. It is idempotent:
// calling it twice for the same task keeps the first checkpoint's files and
// timestamp, so lazy callers can invoke it before every write.
func (s *Store) BeginCheckpoint(taskID string) (*Checkpoint, error) {
	cp, err := s.loadCheckpoint(taskID)
	if err == nil {
		return cp, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	cp = &Checkpoint{TaskID: taskID, Timestamp: time.Now().UTC()}
	if err := s.saveCheckpoint(cp); err != nil {
		return nil, err
	}
	return cp, nil
}

// CheckpointFile records the current content of relPath under taskID,
// lazily, before the file's first write of the task. It is a no-op when
// the file is already recorded (the pre-task state must never be
// overwritten by a later call in the same task).
//
//   - file exists: its current content is snapshotted (deduplicated via the
//     blob store) and added to Files;
//   - file does not exist: the path is recorded in NewFiles, so undo can
//     delete the file if the task creates it;
//   - snapshot failure: the path is recorded in Skipped — the checkpoint
//     becomes partial and is reported as such.
func (s *Store) CheckpointFile(taskID, relPath string) (*Checkpoint, error) {
	cp, err := s.BeginCheckpoint(taskID)
	if err != nil {
		return nil, err
	}
	rel, err := cleanRel(relPath)
	if err != nil {
		return nil, err
	}
	rel = filepath.ToSlash(rel)

	if cp.has(rel) {
		return cp, nil
	}

	entry, err := s.Snapshot(rel)
	if err != nil {
		cp.Skipped = append(cp.Skipped, rel)
	} else if entry == nil {
		// File does not exist (or is unchanged-nil): mark as new so undo
		// deletes it if the task creates it.
		cp.NewFiles = append(cp.NewFiles, rel)
	} else {
		cp.Files = append(cp.Files, CheckpointFile{RelPath: rel, Entry: *entry})
	}
	if err := s.saveCheckpoint(cp); err != nil {
		return nil, err
	}
	return cp, nil
}

// has reports whether relPath is already tracked by the checkpoint.
func (cp *Checkpoint) has(rel string) bool {
	for _, f := range cp.Files {
		if f.RelPath == rel {
			return true
		}
	}
	for _, n := range cp.NewFiles {
		if n == rel {
			return true
		}
	}
	for _, sk := range cp.Skipped {
		if sk == rel {
			return true
		}
	}
	return false
}

// UndoCheckpoint restores the pre-task state as one unit: every recorded
// file is restored from its blob and every new file created by the task is
// deleted. It returns the checkpoint with any restore failures listed in
// Skipped — a partial undo is reported, never silently claimed complete.
func (s *Store) UndoCheckpoint(taskID string) (*Checkpoint, error) {
	cp, err := s.loadCheckpoint(taskID)
	if err != nil {
		return nil, err
	}
	if len(cp.Files) == 0 && len(cp.NewFiles) == 0 {
		return cp, nil
	}

	restored, deleted, failed := 0, 0, []string{}
	for _, f := range cp.Files {
		if _, err := s.Restore(f.RelPath, f.Entry.ID); err != nil {
			failed = append(failed, f.RelPath)
			continue
		}
		restored++
	}
	for _, rel := range cp.NewFiles {
		target := filepath.Join(s.vaultRoot, filepath.FromSlash(rel))
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			failed = append(failed, rel)
			continue
		}
		deleted++
	}

	cp.Skipped = append(cp.Skipped, failed...)
	if err := s.saveCheckpoint(cp); err != nil {
		return nil, err
	}
	return cp, nil
}

// ListCheckpoints returns all task checkpoints, newest first.
func (s *Store) ListCheckpoints() ([]Checkpoint, error) {
	items, err := os.ReadDir(s.checkpointsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Checkpoint
	for _, item := range items {
		if item.IsDir() || !strings.HasSuffix(item.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.checkpointsDir(), item.Name()))
		if err != nil {
			return nil, err
		}
		var cp Checkpoint
		if err := json.Unmarshal(data, &cp); err != nil {
			continue // skip corrupt manifests rather than failing the listing
		}
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Timestamp.After(out[j].Timestamp) })
	return out, nil
}

// Partial reports whether the checkpoint has skipped entries (incomplete
// capture or partial undo).
func (cp *Checkpoint) Partial() bool {
	return len(cp.Skipped) > 0
}

func (s *Store) loadCheckpoint(taskID string) (*Checkpoint, error) {
	path, err := s.checkpointPath(taskID)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, fmt.Errorf("corrupt checkpoint manifest for %s: %w", taskID, err)
	}
	return &cp, nil
}

func (s *Store) saveCheckpoint(cp *Checkpoint) error {
	if err := os.MkdirAll(s.checkpointsDir(), 0755); err != nil {
		return err
	}
	path, err := s.checkpointPath(cp.TaskID)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data, 0644)
}
