package service

import (
	"errors"
	"fmt"
	"io/fs"
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

	vaultRoot, err := os.OpenRoot(s.VaultRoot)
	if err != nil {
		return fmt.Errorf("open vault root: %w", err)
	}
	defer func() { _ = vaultRoot.Close() }()
	datasetsRoot, err := openVerifiedDatasetDir(vaultRoot, dataset.RawDir)
	if err != nil {
		return err
	}
	defer func() { _ = datasetsRoot.Close() }()

	handleName := slug + ".md"
	handleRel := filepath.ToSlash(filepath.Join(dataset.RawDir, handleName))
	handleInfo, err := datasetsRoot.Lstat(handleName)
	if err != nil {
		return fmt.Errorf("dataset purge %q: %w", slug, err)
	}
	if handleInfo.Mode()&os.ModeSymlink != 0 || !handleInfo.Mode().IsRegular() {
		return fmt.Errorf("dataset handle %s must be a regular file, not a symlink", handleRel)
	}
	handleData, err := datasetsRoot.ReadFile(handleName)
	if err != nil {
		return fmt.Errorf("read dataset handle %s: %w", handleRel, err)
	}
	handle, err := dataset.ParseHandle(handleRel, handleData)
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

	paths := []string{handleRel}
	pathSet := map[string]bool{handleRel: true}
	var rawRoot *os.Root
	rawInfo, rawErr := datasetsRoot.Lstat(slug)
	if rawErr != nil && !os.IsNotExist(rawErr) {
		return rawErr
	}
	if rawErr == nil {
		if rawInfo.Mode()&os.ModeSymlink != 0 || !rawInfo.IsDir() {
			return fmt.Errorf("dataset raw path %s/%s must be a directory, not a symlink", dataset.RawDir, slug)
		}
		rawRoot, err = openVerifiedDatasetDir(datasetsRoot, slug)
		if err != nil {
			return err
		}
		defer func(root *os.Root) { _ = root.Close() }(rawRoot)
		entries, err := fs.ReadDir(rawRoot.FS(), ".")
		if err != nil {
			return err
		}
		for _, entry := range entries {
			info, err := rawRoot.Lstat(entry.Name())
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				return fmt.Errorf("dataset raw directory contains non-regular entry %q", entry.Name())
			}
			rel := filepath.ToSlash(filepath.Join(dataset.RawDir, slug, entry.Name()))
			paths = append(paths, rel)
			pathSet[rel] = true
		}
	}

	// Include snapshots already moved to trash; they are not visible through
	// the open raw-directory handle but still contain dataset content.
	trashEntries, err := s.History.TrashList()
	if err != nil {
		return fmt.Errorf("list dataset trash: %w", err)
	}
	prefix := filepath.ToSlash(filepath.Join(dataset.RawDir, slug)) + "/"
	for _, entry := range trashEntries {
		rel := filepath.ToSlash(entry.OriginalPath)
		if (rel == handleRel || strings.HasPrefix(rel, prefix)) && !pathSet[rel] {
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
	if rawRoot != nil {
		entries, err := fs.ReadDir(rawRoot.FS(), ".")
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := rawRoot.Remove(entry.Name()); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		if err := rawRoot.Close(); err != nil {
			return err
		}
		rawRoot = nil
		currentRaw, err := datasetsRoot.Lstat(slug)
		if err != nil {
			return err
		}
		if currentRaw.Mode()&os.ModeSymlink != 0 || !os.SameFile(rawInfo, currentRaw) {
			return fmt.Errorf("dataset raw path %q changed during purge", slug)
		}
		if err := datasetsRoot.Remove(slug); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	currentHandle, err := datasetsRoot.Lstat(handleName)
	if err != nil {
		return err
	}
	if currentHandle.Mode()&os.ModeSymlink != 0 || !os.SameFile(handleInfo, currentHandle) {
		return fmt.Errorf("dataset handle %q changed during purge", handleRel)
	}
	if err := datasetsRoot.Remove(handleName); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := s.DB.DeleteDataset(slug); err != nil {
		return fmt.Errorf("delete dataset rows: %w", err)
	}
	handleAbs := filepath.Join(s.VaultRoot, filepath.FromSlash(handleRel))
	if err := s.DeleteDocument(handleAbs); err != nil {
		return fmt.Errorf("deindex dataset handle: %w", err)
	}
	return nil
}

func openVerifiedDatasetDir(parent *os.Root, name string) (*os.Root, error) {
	before, err := parent.Lstat(name)
	if err != nil {
		return nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.IsDir() {
		return nil, fmt.Errorf("dataset purge path %q must be a directory, not a symlink", name)
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	after, err := child.Stat(".")
	if err != nil {
		_ = child.Close()
		return nil, err
	}
	if !os.SameFile(before, after) {
		_ = child.Close()
		return nil, fmt.Errorf("dataset purge path %q changed during verification", name)
	}
	return child, nil
}
