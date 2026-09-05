package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/danieljustus/symaira-desktop/internal/dataset"
	"github.com/danieljustus/symaira-desktop/internal/retention"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

// RetentionState is the authoritative state used both when staging and when
// accepting a retention item. Dataset state includes the handle and all raw
// CSV snapshots; ordinary documents include their current file bytes.
type RetentionState struct {
	Meta        retention.DocMeta
	RuleName    string
	Fingerprint string
	Dataset     bool
}

// RetentionState reads the current authoritative vault state for relPath.
// Callers must compare Fingerprint with the staged value before acting.
func (s *Service) RetentionState(relPath string) (*RetentionState, error) {
	if s == nil || strings.TrimSpace(s.VaultRoot) == "" {
		return nil, errors.New("retention state requires a vault")
	}
	relPath = filepath.ToSlash(strings.TrimSpace(relPath))
	if relPath == "" || filepath.IsAbs(relPath) {
		return nil, fmt.Errorf("invalid retention path %q", relPath)
	}
	absPath, err := vault.SecurePath(s.VaultRoot, relPath)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(absPath) //nolint:gosec // path is confined by SecurePath
	if err != nil {
		return nil, err
	}
	doc, err := vault.ParseBytes(relPath, data)
	if err != nil {
		return nil, err
	}
	if doc.Type == dataset.Type {
		state, err := s.datasetRetentionState(relPath, data)
		if err != nil {
			return nil, err
		}
		state.Meta.Path = relPath
		return state, nil
	}
	return &RetentionState{
		Meta:        retention.DocMetaFromDocument(doc),
		Fingerprint: retention.Fingerprint(nil, []retention.RawSource{{Path: relPath, Data: data}}),
	}, nil
}

func (s *Service) datasetRetentionState(relPath string, handleData []byte) (*RetentionState, error) {
	handle, err := dataset.ParseHandle(relPath, handleData)
	if err != nil {
		return nil, err
	}
	canonicalPath := filepath.ToSlash(filepath.Join(dataset.RawDir, handle.Slug+".md"))
	if relPath != canonicalPath {
		return nil, fmt.Errorf("dataset handle path %q does not match dataset %q", relPath, handle.Slug)
	}
	rawDir, err := vault.SecurePath(s.VaultRoot, filepath.Join(dataset.RawDir, handle.Slug))
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(rawDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	sources := make([]retention.RawSource, 0)
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() || strings.ToLower(filepath.Ext(entry.Name())) != ".csv" {
				continue
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("dataset raw source %s/%s is a symlink", handle.Slug, entry.Name())
			}
			sourceRel := filepath.ToSlash(filepath.Join(dataset.RawDir, handle.Slug, entry.Name()))
			sourcePath := filepath.Join(rawDir, entry.Name())
			sourceData, readErr := os.ReadFile(sourcePath) //nolint:gosec // source is confined by SecurePath
			if readErr != nil {
				return nil, readErr
			}
			sources = append(sources, retention.RawSource{Path: sourceRel, Data: sourceData})
		}
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Path < sources[j].Path })
	return &RetentionState{
		Meta: retention.DocMeta{
			Path:         relPath,
			Title:        handle.Title,
			Created:      handle.Created,
			DocumentDate: handle.Coverage.To,
			DocumentType: dataset.Type,
		},
		RuleName:    handle.RetentionRule,
		Fingerprint: retention.Fingerprint(handleData, sources),
		Dataset:     true,
	}, nil
}
