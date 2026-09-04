package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/danieljustus/symaira-desktop/internal/dataset"
	"github.com/danieljustus/symaira-desktop/internal/dbviews"
)

// DatasetPurge permanently removes one dataset after a caller has reviewed
// and accepted the named retention rule. It is never invoked by evaluation or
// ordinary note deletion.
func (s *Service) DatasetPurge(slug, acceptedRule string) error {
	if s == nil || strings.TrimSpace(s.VaultRoot) == "" || s.DB == nil {
		return errors.New("dataset purge requires a vault and sidecar")
	}
	slug = strings.TrimSpace(slug)
	if slug == "" || slug != filepath.Base(slug) || slug != dbviews.Slugify(slug) {
		return fmt.Errorf("dataset slug %q is not filesystem-safe", slug)
	}
	handleRel := filepath.ToSlash(filepath.Join(dataset.RawDir, slug+".md"))
	if err := rejectDatasetPurgeSymlinks(s.VaultRoot, handleRel, false); err != nil {
		return err
	}
	handleAbs := filepath.Join(s.VaultRoot, filepath.FromSlash(handleRel))
	handleInfo, err := os.Lstat(handleAbs)
	if err != nil {
		return fmt.Errorf("dataset purge %q: %w", slug, err)
	}
	if handleInfo.Mode()&os.ModeSymlink != 0 || !handleInfo.Mode().IsRegular() {
		return fmt.Errorf("dataset handle %s must be a regular file, not a symlink", handleRel)
	}
	handle, err := readDatasetHandle(s.VaultRoot, handleRel)
	if err != nil {
		return fmt.Errorf("dataset purge %q: %w", slug, err)
	}
	if handle.Slug != slug {
		return fmt.Errorf("dataset handle %s identifies %q, not %q", handleRel, handle.Slug, slug)
	}
	acceptedRule = strings.TrimSpace(acceptedRule)
	if acceptedRule == "" || handle.RetentionRule != acceptedRule {
		return fmt.Errorf("dataset %q declares retention rule %q, not accepted rule %q", slug, handle.RetentionRule, acceptedRule)
	}
	rawDirRel := filepath.Join(dataset.RawDir, slug)
	if err := rejectDatasetPurgeSymlinks(s.VaultRoot, rawDirRel, true); err != nil {
		return err
	}
	rawDir := filepath.Join(s.VaultRoot, rawDirRel)
	rawInfo, err := os.Lstat(rawDir)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil && (rawInfo.Mode()&os.ModeSymlink != 0 || !rawInfo.IsDir()) {
		return fmt.Errorf("dataset raw path %s must be a directory, not a symlink", rawDirRel)
	}
	entries, err := os.ReadDir(rawDir)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	paths := []string{handleRel}
	pathSet := map[string]bool{handleRel: true}
	if err == nil {
		for _, entry := range entries {
			info, infoErr := entry.Info()
			if infoErr != nil {
				return infoErr
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				return fmt.Errorf("dataset raw directory contains non-regular entry %q", entry.Name())
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
		// Every active path is derived from the validated slug plus a ReadDir
		// entry name. Remove the lexical path so a symlink can never redirect
		// deletion to its canonical target.
		abs := filepath.Join(s.VaultRoot, filepath.FromSlash(rel))
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
	if err := s.DeleteDocument(handleAbs); err != nil {
		return fmt.Errorf("deindex dataset handle: %w", err)
	}
	return nil
}

func rejectDatasetPurgeSymlinks(root, rel string, allowMissingFinal bool) error {
	parts := strings.Split(filepath.Clean(filepath.FromSlash(rel)), string(filepath.Separator))
	current := root
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) && allowMissingFinal && i == len(parts)-1 {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("dataset purge path %s contains symlink component %q", rel, part)
		}
		if i < len(parts)-1 && !info.IsDir() {
			return fmt.Errorf("dataset purge path %s has non-directory component %q", rel, part)
		}
	}
	return nil
}
