package service

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/history"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

// HistoryList returns the recorded snapshots for a vault file, newest first.
func (s *Service) HistoryList(relPath string) ([]history.Entry, error) {
	absPath, err := vault.SecurePath(s.VaultRoot, relPath)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(s.VaultRoot, absPath)
	if err != nil {
		return nil, err
	}
	return s.History.List(rel)
}

// HistoryContent returns the stored content of a specific snapshot.
func (s *Service) HistoryContent(id string) ([]byte, error) {
	return s.History.Content(id)
}

// HistoryRestore restores a snapshot (empty id = most recent) and re-indexes
// the file so the sidecar stays consistent.
func (s *Service) HistoryRestore(relPath, id string) (*history.Entry, error) {
	absPath, err := vault.SecurePath(s.VaultRoot, relPath)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(s.VaultRoot, absPath)
	if err != nil {
		return nil, err
	}
	entry, err := s.History.Restore(rel, id)
	if err != nil {
		return nil, err
	}
	doc, err := vault.ParseFile(absPath)
	if err != nil {
		return entry, fmt.Errorf("restored file but failed to parse for indexing: %w", err)
	}
	if err := s.IndexDocument(doc); err != nil {
		return entry, fmt.Errorf("restored file but failed to re-index: %w", err)
	}
	_ = s.RecordActivity("file_changed", rel, "", "restored snapshot "+id)
	return entry, nil
}

// HistoryPrune applies a retention policy to the snapshot store.
func (s *Service) HistoryPrune(policy history.RetentionPolicy) (int, error) {
	return s.History.Prune(policy)
}

// CheckpointBegin starts (or resumes) a task-scoped checkpoint so a whole
// agent run can be rejected as one unit (issue #405).
func (s *Service) CheckpointBegin(taskID string) (*history.Checkpoint, error) {
	return s.History.BeginCheckpoint(taskID)
}

// CheckpointFiles records the pre-write state of relPaths under taskID,
// lazily, before the task's first write (issue #405).
func (s *Service) CheckpointFiles(taskID string, relPaths []string) (*history.Checkpoint, error) {
	cp, err := s.History.BeginCheckpoint(taskID)
	if err != nil {
		return nil, err
	}
	for _, relPath := range relPaths {
		absPath, err := vault.SecurePath(s.VaultRoot, relPath)
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(s.VaultRoot, absPath)
		if err != nil {
			return nil, err
		}
		cp, err = s.History.CheckpointFile(taskID, rel)
		if err != nil {
			return nil, err
		}
	}
	return cp, nil
}

// CheckpointUndo restores the pre-task state of a task as one unit:
// recorded files are restored from their blobs, files created by the task
// are deleted, and partial undos are reported (issue #405).
func (s *Service) CheckpointUndo(taskID string) (*history.Checkpoint, error) {
	cp, err := s.History.UndoCheckpoint(taskID)
	if err != nil {
		return nil, err
	}
	// Re-index every restored file so the sidecar matches the vault.
	for _, f := range cp.Files {
		absPath, err := vault.SecurePath(s.VaultRoot, f.RelPath)
		if err != nil {
			continue
		}
		if filepath.Ext(absPath) == ".md" {
			if doc, err := vault.ParseFile(absPath); err == nil {
				_ = s.IndexDocument(doc)
			}
		}
	}
	// Files the task created were deleted; drop them from the index too.
	for _, rel := range cp.NewFiles {
		absPath, err := vault.SecurePath(s.VaultRoot, rel)
		if err != nil {
			continue
		}
		_ = s.DeleteDocument(absPath)
	}
	return cp, nil
}

// CheckpointList lists all task checkpoints, newest first (issue #405).
func (s *Service) CheckpointList() ([]history.Checkpoint, error) {
	return s.History.ListCheckpoints()
}

// NoteDelete soft-deletes a vault file: the file moves to the vault-local
// trash and is removed from the sidecar index.
func (s *Service) NoteDelete(relPath string) (*history.TrashEntry, error) {
	absPath, err := vault.SecurePath(s.VaultRoot, relPath)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(s.VaultRoot, absPath)
	if err != nil {
		return nil, err
	}
	entry, err := s.History.Trash(rel)
	if err != nil {
		return nil, err
	}
	if err := s.DeleteDocument(absPath); err != nil {
		return entry, fmt.Errorf("moved to trash but failed to deindex: %w", err)
	}
	_ = s.RecordActivity("file_removed", rel, entry.Name, "moved to trash")
	return entry, nil
}

// TrashList lists the soft-deleted files, newest deletion first.
func (s *Service) TrashList() ([]history.TrashEntry, error) {
	return s.History.TrashList()
}

// TrashRestore moves a trash item back to its original location and
// re-indexes it so the sidecar stays consistent.
func (s *Service) TrashRestore(name string) (*history.TrashEntry, error) {
	entry, err := s.History.TrashRestore(name)
	if err != nil {
		return nil, err
	}
	absPath, err := vault.SecurePath(s.VaultRoot, entry.OriginalPath)
	if err != nil {
		return entry, err
	}
	if filepath.Ext(absPath) == ".md" {
		doc, err := vault.ParseFile(absPath)
		if err != nil {
			return entry, fmt.Errorf("restored file but failed to parse for indexing: %w", err)
		}
		if err := s.IndexDocument(doc); err != nil {
			return entry, fmt.Errorf("restored file but failed to re-index: %w", err)
		}
	}
	_ = s.RecordActivity("file_added", entry.OriginalPath, "", "restored from trash")
	return entry, nil
}

// TrashPurge permanently removes trash items older than maxAge
// (maxAge <= 0 purges everything).
func (s *Service) TrashPurge(maxAge time.Duration) (int, error) {
	return s.History.TrashPurge(maxAge)
}
