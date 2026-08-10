package service

import (
	"fmt"

	"github.com/danieljustus/symaira-desktop/internal/notebook"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

// NotebookNew creates a new notebook note and indexes it.
func (s *Service) NotebookNew(title, description string) (*notebook.Notebook, error) {
	nb, err := notebook.New(s.VaultRoot, title, description)
	if err != nil {
		return nil, err
	}
	if err := s.reindexNotebook(nb); err != nil {
		return nb, err
	}
	return nb, nil
}

// NotebookList lists every notebook in the vault.
func (s *Service) NotebookList() ([]*notebook.Notebook, error) {
	return notebook.List(s.VaultRoot)
}

// NotebookGet resolves a notebook by ID or path (see notebook.Resolve).
func (s *Service) NotebookGet(ref string) (*notebook.Notebook, error) {
	return notebook.Resolve(s.VaultRoot, ref)
}

// NotebookAddSource adds a source to a notebook, snapshotting the note
// before the write and re-indexing it after.
func (s *Service) NotebookAddSource(ref, sourcePath string) (*notebook.Notebook, error) {
	nb, err := notebook.Resolve(s.VaultRoot, ref)
	if err != nil {
		return nil, err
	}
	absPath, err := vault.SecurePath(s.VaultRoot, nb.Path)
	if err != nil {
		return nil, err
	}
	s.snapshotBefore(absPath)
	if err := notebook.AddSource(s.VaultRoot, nb, sourcePath); err != nil {
		return nil, err
	}
	if err := s.reindexNotebook(nb); err != nil {
		return nb, err
	}
	return nb, nil
}

// NotebookRemoveSource removes a source from a notebook. The referenced
// file itself is never touched (VAULT.md section 10).
func (s *Service) NotebookRemoveSource(ref, sourcePath string) (*notebook.Notebook, error) {
	nb, err := notebook.Resolve(s.VaultRoot, ref)
	if err != nil {
		return nil, err
	}
	absPath, err := vault.SecurePath(s.VaultRoot, nb.Path)
	if err != nil {
		return nil, err
	}
	s.snapshotBefore(absPath)
	if err := notebook.RemoveSource(s.VaultRoot, nb, sourcePath); err != nil {
		return nil, err
	}
	if err := s.reindexNotebook(nb); err != nil {
		return nb, err
	}
	return nb, nil
}

// NotebookDelete moves a notebook note to the trash, same as any other
// vault note (VAULT.md section 7) — a notebook has no separate lifecycle.
func (s *Service) NotebookDelete(ref string) error {
	nb, err := notebook.Resolve(s.VaultRoot, ref)
	if err != nil {
		return err
	}
	_, err = s.NoteDelete(nb.Path)
	return err
}

// reindexNotebook re-parses the notebook's note from disk and re-indexes
// it, so the sidecar (search, backlinks, doctor) reflects the write.
func (s *Service) reindexNotebook(nb *notebook.Notebook) error {
	absPath, err := vault.SecurePath(s.VaultRoot, nb.Path)
	if err != nil {
		return err
	}
	doc, err := vault.ParseFile(absPath)
	if err != nil {
		return fmt.Errorf("wrote notebook but failed to parse for indexing: %w", err)
	}
	if err := s.IndexDocument(doc); err != nil {
		return fmt.Errorf("wrote notebook but failed to index: %w", err)
	}
	return nil
}
