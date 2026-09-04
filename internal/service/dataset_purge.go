package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/danieljustus/symaira-desktop/internal/dataset"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

// DatasetPurge permanently removes one dataset after a caller has reviewed
// and accepted its retention action. It is never invoked by evaluation or
// ordinary note deletion.
func (s *Service) DatasetPurge(slug string) error {
	if s == nil || strings.TrimSpace(s.VaultRoot) == "" || s.DB == nil {
		return errors.New("dataset purge requires a vault and sidecar")
	}
	slug = strings.TrimSpace(slug)
	if slug == "" || slug != filepath.Base(slug) || slug != sanitizeDatasetSlug(slug) {
		return fmt.Errorf("dataset slug %q is not filesystem-safe", slug)
	}
	handleRel := filepath.ToSlash(filepath.Join(dataset.RawDir, slug+".md"))
	handle, err := readDatasetHandle(s.VaultRoot, handleRel)
	if err != nil {
		return fmt.Errorf("dataset purge %q: %w", slug, err)
	}
	if handle.Slug != slug {
		return fmt.Errorf("dataset handle %s identifies %q, not %q", handleRel, handle.Slug, slug)
	}
	rawDirRel := filepath.Join(dataset.RawDir, slug)
	rawDir, err := vault.SecurePath(s.VaultRoot, rawDirRel)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(rawDir)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	paths := []string{handleRel}
	pathSet := map[string]bool{handleRel: true}
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				return fmt.Errorf("dataset raw directory contains nested directory %q", entry.Name())
			}
			rel := filepath.ToSlash(filepath.Join(rawDirRel, entry.Name()))
			paths = append(paths, rel)
			pathSet[rel] = true
		}
	}
	// Include raw snapshots that were already moved to trash by an earlier
	// explicit operation; they are not visible in the raw directory anymore.
	trashEntries, err := s.History.TrashList()
	if err != nil {
		return fmt.Errorf("list dataset trash: %w", err)
	}
	for _, entry := range trashEntries {
		rel := filepath.ToSlash(entry.OriginalPath)
		if (rel == handleRel || strings.HasPrefix(rel, filepath.ToSlash(rawDirRel)+"/")) && !pathSet[rel] {
			paths = append(paths, rel)
			pathSet[rel] = true
		}
	}
	if err := s.History.PurgePaths(paths...); err != nil {
		return fmt.Errorf("purge dataset history: %w", err)
	}
	if _, err := s.History.PurgeTrashPaths(paths...); err != nil {
		return fmt.Errorf("purge dataset trash: %w", err)
	}
	for _, rel := range paths {
		abs, err := vault.SecurePath(s.VaultRoot, rel)
		if err != nil {
			return err
		}
		if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.Remove(rawDir); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := s.DB.DeleteDataset(slug); err != nil {
		return fmt.Errorf("delete dataset rows: %w", err)
	}
	handleAbs, err := vault.SecurePath(s.VaultRoot, handleRel)
	if err == nil {
		if err := s.DeleteDocument(handleAbs); err != nil {
			return fmt.Errorf("deindex dataset handle: %w", err)
		}
	}
	return nil
}

func sanitizeDatasetSlug(value string) string {
	if value == "" || strings.ContainsAny(value, "/\\") {
		return ""
	}
	return value
}
