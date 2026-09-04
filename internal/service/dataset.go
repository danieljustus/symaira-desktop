package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/dataset"
	"github.com/danieljustus/symaira-desktop/internal/dbviews"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

// DatasetImportOptions controls the explicit CSV-to-dataset path. Ordinary
// ingest and CSVImport remain document-oriented and are not changed by this API.
type DatasetImportOptions struct {
	Title          string
	Slug           string
	IdentityField  string
	Schema         map[string]dbviews.PropertyConfig
	RefreshCommand string
	Sensitivity    string
	RetentionRule  string
	Now            time.Time
}

type DatasetImportResult struct {
	HandlePath    string                            `json:"handle_path"`
	RawPath       string                            `json:"raw_path"`
	Slug          string                            `json:"slug"`
	Rows          int                               `json:"rows"`
	Columns       map[string]dbviews.PropertyConfig `json:"columns"`
	SourceSHA256  string                            `json:"source_sha256"`
	Sensitivity   string                            `json:"sensitivity"`
	RetentionRule string                            `json:"retention_rule"`
}

// DatasetImport explicitly stores a CSV as a raw vault asset, writes its
// Markdown dataset handle, and materializes deduplicated typed rows in the
// rebuildable sidecar.
func (s *Service) DatasetImport(source string, opts DatasetImportOptions) (*DatasetImportResult, error) {
	if s == nil || strings.TrimSpace(s.VaultRoot) == "" {
		return nil, errors.New("dataset import requires a vault")
	}
	if s.DB == nil {
		return nil, errors.New("dataset import requires a sidecar")
	}
	sensitivity, retentionRule, err := datasetPolicy(opts.Sensitivity, opts.RetentionRule)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(source)
	if err != nil {
		return nil, fmt.Errorf("stat dataset source: %w", err)
	}
	if info.IsDir() || strings.ToLower(filepath.Ext(source)) != ".csv" {
		return nil, fmt.Errorf("dataset import supports CSV files only")
	}
	data, err := os.ReadFile(source) //nolint:gosec // source is explicitly selected by the caller
	if err != nil {
		return nil, fmt.Errorf("read dataset source: %w", err)
	}

	rows, schema, err := dataset.ParseCSV(strings.NewReader(string(data)), opts.Schema, opts.IdentityField)
	if err != nil {
		return nil, err
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	title := strings.TrimSpace(opts.Title)
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(source), filepath.Ext(source))
	}
	slug := strings.TrimSpace(opts.Slug)
	if slug == "" {
		slug = dbviews.Slugify(title)
	}
	if slug != dbviews.Slugify(slug) {
		return nil, fmt.Errorf("dataset slug %q is not filesystem-safe", slug)
	}

	// The vault contract names raw snapshots by import date; StoreAsset adds a
	// collision suffix when several exports for the same dataset arrive that day.
	rawName := now.UTC().Format(time.DateOnly) + ".csv"
	rawPath, err := dataset.StoreRaw(s.VaultRoot, slug, rawName, data, now)
	if err != nil {
		return nil, fmt.Errorf("store dataset source: %w", err)
	}

	handleRel := filepath.ToSlash(filepath.Join(dataset.RawDir, slug+".md"))
	handle := &dataset.Handle{
		Path:           handleRel,
		Slug:           slug,
		Title:          title,
		Created:        now.Format(time.RFC3339),
		Source:         rawPath,
		Schema:         schema,
		Coverage:       coverageForRows(rows, schema),
		Provenance:     dataset.Provenance{ImportedAt: now.Format(time.RFC3339), SourceName: filepath.Base(source), SourceSHA256: sha256Hex(data)},
		IdentityField:  opts.IdentityField,
		RefreshCommand: opts.RefreshCommand,
		Sensitivity:    sensitivity,
		RetentionRule:  retentionRule,
	}
	if existing, readErr := readDatasetHandle(s.VaultRoot, handleRel); readErr == nil {
		handle.Created = existing.Created
	}
	encoded, err := handle.Render()
	if err != nil {
		return nil, err
	}
	handleAbs, err := vault.SecurePath(s.VaultRoot, handleRel)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(handleAbs), 0750); err != nil {
		return nil, fmt.Errorf("create dataset handle directory: %w", err)
	}
	if err := writeDatasetFileAtomic(handleAbs, encoded); err != nil {
		return nil, fmt.Errorf("write dataset handle: %w", err)
	}

	materialized, err := dataset.ReadRawFiles(s.VaultRoot, slug, schema, opts.IdentityField)
	if err != nil {
		return nil, fmt.Errorf("rebuild dataset rows: %w", err)
	}
	if err := s.replaceDatasetRows(slug, materialized); err != nil {
		return nil, fmt.Errorf("store dataset rows: %w", err)
	}
	if doc, parseErr := vault.ParseFile(handleAbs); parseErr == nil {
		if err := s.DB.IndexDocument(doc); err != nil {
			return nil, fmt.Errorf("index dataset handle: %w", err)
		}
	} else {
		return nil, fmt.Errorf("parse dataset handle: %w", parseErr)
	}
	return &DatasetImportResult{HandlePath: handleRel, RawPath: rawPath, Slug: slug, Rows: uniqueDatasetRowCount(materialized), Columns: schema, SourceSHA256: sha256Hex(data), Sensitivity: sensitivity, RetentionRule: retentionRule}, nil
}

// RebuildDatasets recreates every dataset's derived rows from raw vault files.
// It does not delete or rewrite Markdown, and is safe after recreating a sidecar.
func (s *Service) RebuildDatasets() error {
	if s == nil || s.DB == nil {
		return errors.New("dataset rebuild requires a sidecar")
	}
	return vault.Walk(s.VaultRoot, func(path string) error {
		if filepath.Base(filepath.Dir(path)) != dataset.RawDir || filepath.Ext(path) != ".md" {
			return nil
		}
		rel, err := filepath.Rel(s.VaultRoot, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path) //nolint:gosec // path comes from the confined vault walk
		if err != nil {
			return err
		}
		handle, err := dataset.ParseHandle(filepath.ToSlash(rel), data)
		if err != nil {
			return err
		}
		rows, err := dataset.ReadRawFiles(s.VaultRoot, handle.Slug, handle.Schema, handle.IdentityField)
		if err != nil {
			return err
		}
		return s.replaceDatasetRows(handle.Slug, rows)
	})
}

func uniqueDatasetRowCount(rows []dataset.Row) int {
	keys := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		keys[row.Key] = struct{}{}
	}
	return len(keys)
}

func (s *Service) replaceDatasetRows(slug string, rows []dataset.Row) error {
	byKey := make(map[string]dataset.Row, len(rows))
	for _, row := range rows {
		byKey[row.Key] = row
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	materialized := make([]sidecar.DatasetRow, 0, len(keys))
	for _, key := range keys {
		row := byKey[key]
		values, err := json.Marshal(row.Values)
		if err != nil {
			return err
		}
		materialized = append(materialized, sidecar.DatasetRow{DatasetSlug: slug, RowKey: row.Key, Identity: row.Identity, ValuesJSON: string(values), SourcePath: row.SourcePath, RowNumber: row.RowNumber})
	}
	return s.DB.ReplaceDatasetRows(slug, materialized)
}

func readDatasetHandle(root, rel string) (*dataset.Handle, error) {
	path, err := vault.SecurePath(root, rel)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path) //nolint:gosec // path is confined by SecurePath
	if err != nil {
		return nil, err
	}
	return dataset.ParseHandle(rel, data)
}

func coverageForRows(rows []dataset.Row, schema map[string]dbviews.PropertyConfig) dataset.Coverage {
	dateColumns := make([]string, 0)
	for column, property := range schema {
		if property.Type == "date" {
			dateColumns = append(dateColumns, column)
		}
	}
	sort.Strings(dateColumns)
	if len(dateColumns) == 0 {
		return dataset.Coverage{}
	}
	var dates []string
	for _, row := range rows {
		for _, column := range dateColumns {
			if value, ok := row.Values[column].(string); ok && value != "" {
				dates = append(dates, value)
				break
			}
		}
	}
	if len(dates) == 0 {
		return dataset.Coverage{}
	}
	sort.Strings(dates)
	return dataset.Coverage{From: dates[0], To: dates[len(dates)-1]}
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func writeDatasetFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".symdesk-dataset-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
