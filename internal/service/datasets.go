package service

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/dataset"
	"github.com/danieljustus/symaira-desktop/internal/dbviews"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

const (
	DatasetDefaultRowCap = 10
	DatasetMaxRowCap     = 1000
)

type DatasetSummary struct {
	Slug          string                            `json:"slug"`
	Title         string                            `json:"title"`
	Path          string                            `json:"path"`
	Source        string                            `json:"source"`
	Rows          int                               `json:"rows"`
	Columns       map[string]dbviews.PropertyConfig `json:"columns"`
	IdentityField string                            `json:"identity_field,omitempty"`
	Sensitivity   string                            `json:"sensitivity"`
	RetentionRule string                            `json:"retention_rule"`
	Provenance    dataset.Provenance                `json:"provenance"`
}

type DatasetDescription struct {
	DatasetSummary
	Coverage       dataset.Coverage `json:"coverage"`
	RefreshCommand string           `json:"refresh_command,omitempty"`
	Sensitivity    string           `json:"sensitivity"`
	RetentionRule  string           `json:"retention_rule"`
}

type DatasetAggregate struct {
	Column   string `json:"column"`
	Function string `json:"function"`
	As       string `json:"as,omitempty"`
}

type DatasetQueryOptions struct {
	Columns     []string
	Filters     []dbviews.Filter
	FilterGroup *dbviews.FilterGroup
	Sorts       []dbviews.Sort
	GroupBy     string
	Aggregates  []DatasetAggregate
	Limit       int
}

type DatasetQueryResult struct {
	Dataset      string                   `json:"dataset"`
	Columns      []string                 `json:"columns"`
	Rows         []map[string]interface{} `json:"rows"`
	TotalRows    int                      `json:"total_rows"`
	ReturnedRows int                      `json:"returned_rows"`
	Limit        int                      `json:"limit"`
	Capped       bool                     `json:"capped"`
}

type DatasetSyncRow struct {
	Identity string                 `json:"identity"`
	Values   map[string]interface{} `json:"values"`
}

type DatasetSyncOptions struct {
	Title         string
	Slug          string
	IdentityField string
	Schema        map[string]dbviews.PropertyConfig
	Provenance    dataset.Provenance
	Sensitivity   string
	RetentionRule string
	Rows          []DatasetSyncRow
}

type DatasetSyncResult struct {
	Slug         string `json:"slug"`
	Rows         int    `json:"rows"`
	ImportedRows int    `json:"imported_rows"`
	RawPath      string `json:"raw_path"`
	HandlePath   string `json:"handle_path"`
	Idempotent   bool   `json:"idempotent"`
}

// DatasetList returns dataset handles from the authoritative Markdown vault.
func (s *Service) DatasetList() ([]DatasetSummary, error) {
	if s == nil || strings.TrimSpace(s.VaultRoot) == "" {
		return nil, errors.New("dataset list requires a vault")
	}
	dir, err := vault.SecurePath(s.VaultRoot, dataset.RawDir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []DatasetSummary{}, nil
		}
		return nil, err
	}
	result := make([]DatasetSummary, 0)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		rel := filepath.ToSlash(filepath.Join(dataset.RawDir, entry.Name()))
		abs, err := vault.SecurePath(s.VaultRoot, rel)
		if err != nil {
			continue
		}
		data, err := os.ReadFile(abs) //nolint:gosec // path is confined by SecurePath
		if err != nil {
			continue
		}
		handle, err := dataset.ParseHandle(rel, data)
		if err != nil {
			continue
		}
		rows := 0
		if s.DB != nil {
			if count, rowErr := s.DB.DatasetRowCount(handle.Slug); rowErr == nil {
				rows = count
			}
		}
		result = append(result, datasetSummary(*handle, rows))
	}
	sort.Slice(result, func(i, j int) bool { return strings.ToLower(result[i].Title) < strings.ToLower(result[j].Title) })
	return result, nil
}

// DatasetsList is the plural compatibility spelling used by some callers.
func (s *Service) DatasetsList() ([]DatasetSummary, error) { return s.DatasetList() }

func (s *Service) DatasetDescribe(slug string) (*DatasetDescription, error) {
	if s == nil || strings.TrimSpace(s.VaultRoot) == "" {
		return nil, errors.New("dataset describe requires a vault")
	}
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return nil, errors.New("dataset slug is required")
	}
	rel := filepath.ToSlash(filepath.Join(dataset.RawDir, slug+".md"))
	handle, err := readDatasetHandle(s.VaultRoot, rel)
	if err != nil {
		return nil, fmt.Errorf("dataset %q not found: %w", slug, err)
	}
	rows := 0
	if s.DB != nil {
		count, rowErr := s.DB.DatasetRowCount(handle.Slug)
		if rowErr != nil {
			return nil, rowErr
		}
		rows = count
	}
	summary := datasetSummary(*handle, rows)
	return &DatasetDescription{DatasetSummary: summary, Coverage: handle.Coverage, RefreshCommand: handle.RefreshCommand, Sensitivity: handle.Sensitivity, RetentionRule: handle.RetentionRule}, nil
}

func datasetSummary(handle dataset.Handle, rows int) DatasetSummary {
	return DatasetSummary{Slug: handle.Slug, Title: handle.Title, Path: handle.Path, Source: handle.Source, Rows: rows, Columns: handle.Schema, IdentityField: handle.IdentityField, Sensitivity: handle.Sensitivity, RetentionRule: handle.RetentionRule, Provenance: handle.Provenance}
}

func (s *Service) DatasetQuery(slug string, opts DatasetQueryOptions) (*DatasetQueryResult, error) {
	if s == nil || s.DB == nil {
		return nil, errors.New("dataset query requires a sidecar")
	}
	handle, err := s.DatasetDescribe(slug)
	if err != nil {
		return nil, err
	}
	queryOpts, err := datasetSidecarQueryOptions(handle, opts)
	if err != nil {
		return nil, err
	}
	queried, err := s.DB.QueryDataset(handle.Slug, queryOpts)
	if err != nil {
		return nil, err
	}
	result := &DatasetQueryResult{
		Dataset:   handle.Slug,
		Columns:   queryOpts.Columns,
		TotalRows: queried.TotalRows,
		Limit:     queried.Limit,
		Rows:      make([]map[string]interface{}, 0, len(queried.Rows)),
	}
	if opts.GroupBy != "" || len(opts.Aggregates) > 0 {
		result.Columns = aggregateColumns(opts.GroupBy, opts.Aggregates)
	}
	for _, row := range queried.Rows {
		values := row.Values
		values["identity"] = row.Identity
		values["_identity"] = row.Identity
		values["_key"] = row.RowKey
		if opts.GroupBy == "" && len(opts.Aggregates) == 0 {
			selected := make(map[string]interface{}, len(queryOpts.Columns))
			for _, column := range queryOpts.Columns {
				selected[column] = values[column]
			}
			values = selected
		}
		for _, aggregate := range opts.Aggregates {
			if strings.EqualFold(aggregate.Function, "count") {
				name := aggregate.As
				if name == "" {
					name = "count_" + aggregate.Column
					if aggregate.Column == "" {
						name = "count"
					}
				}
				if count, ok := values[name].(float64); ok {
					values[name] = int(count)
				}
			}
		}
		result.Rows = append(result.Rows, values)
	}
	result.ReturnedRows = len(result.Rows)
	result.Capped = result.ReturnedRows < result.TotalRows-resultLimitOffset(queryOpts.Offset)
	return result, nil
}

func resultLimitOffset(offset int) int {
	if offset < 0 {
		return 0
	}
	return offset
}

func datasetSidecarQueryOptions(handle *DatasetDescription, opts DatasetQueryOptions) (sidecar.DatasetQueryOptions, error) {
	schema := make(map[string]string, len(handle.Columns))
	for column, property := range handle.Columns {
		typ := property.Type
		if typ == "" {
			typ = "text"
		}
		schema[column] = typ
	}
	columns := queryColumns(opts.Columns, handle.Columns, nil)
	if len(opts.Columns) == 0 {
		columns = nil
		for column := range schema {
			columns = append(columns, column)
		}
		sort.Strings(columns)
	}
	filters := make([]sidecar.DatasetFilter, len(opts.Filters))
	for i, filter := range opts.Filters {
		filters[i] = sidecar.DatasetFilter{Key: filter.Key, Operator: filter.Operator, Value: filter.Value}
	}
	var group *sidecar.DatasetFilterGroup
	if opts.FilterGroup != nil {
		converted := convertDatasetFilterGroup(*opts.FilterGroup)
		group = &converted
	}
	sorts := make([]sidecar.DatasetSort, len(opts.Sorts))
	for i, sortSpec := range opts.Sorts {
		sorts[i] = sidecar.DatasetSort{Key: sortSpec.Key, Ascending: sortSpec.Ascending}
	}
	aggregates := make([]sidecar.DatasetAggregate, len(opts.Aggregates))
	for i, aggregate := range opts.Aggregates {
		aggregates[i] = sidecar.DatasetAggregate{Column: aggregate.Column, Function: aggregate.Function, As: aggregate.As}
	}
	return sidecar.DatasetQueryOptions{Schema: schema, Columns: columns, Filters: filters, FilterGroup: group, Sorts: sorts, GroupBy: opts.GroupBy, Aggregates: aggregates, Limit: opts.Limit}, nil
}

func convertDatasetFilterGroup(group dbviews.FilterGroup) sidecar.DatasetFilterGroup {
	converted := sidecar.DatasetFilterGroup{Operator: group.Operator, Filters: make([]sidecar.DatasetFilter, len(group.Filters)), Groups: make([]sidecar.DatasetFilterGroup, len(group.Groups))}
	for i, filter := range group.Filters {
		converted.Filters[i] = sidecar.DatasetFilter{Key: filter.Key, Operator: filter.Operator, Value: filter.Value}
	}
	for i, child := range group.Groups {
		converted.Groups[i] = convertDatasetFilterGroup(child)
	}
	return converted
}

func queryColumns(requested []string, schema map[string]dbviews.PropertyConfig, rows []sidecar.DatasetRow) []string {
	if len(requested) > 0 {
		result := make([]string, 0, len(requested))
		seen := make(map[string]bool)
		for _, column := range requested {
			column = strings.TrimSpace(column)
			if column != "" && !seen[column] {
				result, seen[column] = append(result, column), true
			}
		}
		return result
	}
	result := make([]string, 0, len(schema))
	for column := range schema {
		result = append(result, column)
	}
	if len(result) == 0 && len(rows) > 0 {
		var values map[string]interface{}
		_ = json.Unmarshal([]byte(rows[0].ValuesJSON), &values)
		for column := range values {
			result = append(result, column)
		}
	}
	sort.Strings(result)
	return result
}

// DatasetSync persists producer rows as a raw CSV snapshot and rebuilds the
// derived sidecar. Provenance and row identity are mandatory by design.
func (s *Service) DatasetSync(opts DatasetSyncOptions) (*DatasetSyncResult, error) {
	if s == nil || strings.TrimSpace(s.VaultRoot) == "" || s.DB == nil {
		return nil, errors.New("dataset sync requires a vault and sidecar")
	}
	sensitivity, retentionRule, err := datasetPolicy(opts.Sensitivity, opts.RetentionRule)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(opts.IdentityField) == "" {
		return nil, errors.New("dataset sync requires an identity field")
	}
	if strings.TrimSpace(opts.Provenance.SourceName) == "" || strings.TrimSpace(opts.Provenance.SourceSHA256) == "" || strings.TrimSpace(opts.Provenance.ImportedAt) == "" {
		return nil, errors.New("dataset sync requires explicit provenance: source_name, source_sha256, imported_at")
	}
	importedAt, err := time.Parse(time.RFC3339, opts.Provenance.ImportedAt)
	if err != nil {
		return nil, fmt.Errorf("invalid imported_at: %w", err)
	}
	slug := strings.TrimSpace(opts.Slug)
	if slug == "" {
		slug = dbviews.Slugify(opts.Title)
	}
	if slug == "" || slug != dbviews.Slugify(slug) {
		return nil, fmt.Errorf("dataset slug %q is not filesystem-safe", slug)
	}
	if len(opts.Rows) == 0 {
		return nil, errors.New("dataset sync requires at least one row")
	}
	seen := make(map[string]bool, len(opts.Rows))
	for _, row := range opts.Rows {
		if strings.TrimSpace(row.Identity) == "" {
			return nil, errors.New("dataset sync requires an identity for every row")
		}
		if seen[row.Identity] {
			return nil, fmt.Errorf("duplicate dataset row identity %q", row.Identity)
		}
		seen[row.Identity] = true
	}
	handleRel := filepath.ToSlash(filepath.Join(dataset.RawDir, slug+".md"))
	var existing *dataset.Handle
	if current, readErr := readDatasetHandle(s.VaultRoot, handleRel); readErr == nil {
		existing = current
	}
	if existing != nil && existing.Provenance.SourceSHA256 == opts.Provenance.SourceSHA256 && existing.Provenance.SourceName == opts.Provenance.SourceName {
		rows, rowErr := s.DB.DatasetRows(slug)
		if rowErr != nil {
			return nil, rowErr
		}
		// A matching handle can survive a deleted/recreated sidecar. Restore
		// the derived rows instead of treating the empty sidecar as complete.
		if len(rows) == 0 {
			materialized, rebuildErr := dataset.ReadRawFiles(s.VaultRoot, slug, existing.Schema, existing.IdentityField)
			if rebuildErr != nil {
				return nil, rebuildErr
			}
			if rebuildErr := s.replaceDatasetRows(slug, materialized); rebuildErr != nil {
				return nil, rebuildErr
			}
			rows, rowErr = s.DB.DatasetRows(slug)
			if rowErr != nil {
				return nil, rowErr
			}
		}
		return &DatasetSyncResult{Slug: slug, Rows: len(rows), ImportedRows: len(opts.Rows), RawPath: existing.Source, HandlePath: handleRel, Idempotent: true}, nil
	}

	data, schema, err := syncCSV(opts.Rows, opts.IdentityField, opts.Schema)
	if err != nil {
		return nil, err
	}
	rawName := importedAt.UTC().Format(time.DateOnly) + ".csv"
	rawPath, err := dataset.StoreRaw(s.VaultRoot, slug, rawName, data, importedAt)
	if err != nil {
		return nil, fmt.Errorf("store dataset sync: %w", err)
	}
	title := strings.TrimSpace(opts.Title)
	if title == "" && existing != nil {
		title = existing.Title
	}
	if title == "" {
		title = slug
	}
	created := opts.Provenance.ImportedAt
	if existing != nil {
		created = existing.Created
	}
	handle := &dataset.Handle{Slug: slug, Title: title, Created: created, Source: rawPath, Schema: schema, Provenance: opts.Provenance, IdentityField: opts.IdentityField, Sensitivity: sensitivity, RetentionRule: retentionRule}
	if existing != nil {
		handle.Coverage = existing.Coverage
		handle.RefreshCommand = existing.RefreshCommand
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
		return nil, err
	}
	if err := writeDatasetFileAtomic(handleAbs, encoded); err != nil {
		return nil, err
	}
	materialized, err := dataset.ReadRawFiles(s.VaultRoot, slug, schema, opts.IdentityField)
	if err != nil {
		return nil, err
	}
	if err := s.replaceDatasetRows(slug, materialized); err != nil {
		return nil, err
	}
	if doc, parseErr := vault.ParseFile(handleAbs); parseErr == nil {
		if err := s.DB.IndexDocument(doc); err != nil {
			return nil, err
		}
	}
	return &DatasetSyncResult{Slug: slug, Rows: uniqueDatasetRowCount(materialized), ImportedRows: len(opts.Rows), RawPath: rawPath, HandlePath: handleRel}, nil
}

func syncCSV(rows []DatasetSyncRow, identityField string, declared map[string]dbviews.PropertyConfig) ([]byte, map[string]dbviews.PropertyConfig, error) {
	columns := make(map[string]bool)
	for column := range declared {
		columns[column] = true
	}
	columns[identityField] = true
	for _, row := range rows {
		for column := range row.Values {
			columns[column] = true
		}
	}
	headers := make([]string, 0, len(columns))
	for column := range columns {
		if strings.TrimSpace(column) != "" {
			headers = append(headers, column)
		}
	}
	sort.Strings(headers)
	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	if err := writer.Write(headers); err != nil {
		return nil, nil, err
	}
	for _, row := range rows {
		record := make([]string, len(headers))
		values := row.Values
		for i, column := range headers {
			value := values[column]
			if column == identityField {
				value = row.Identity
			}
			formatted, err := syncValueString(value)
			if err != nil {
				return nil, nil, fmt.Errorf("row %q column %q: %w", row.Identity, column, err)
			}
			record[i] = formatted
		}
		if err := writer.Write(record); err != nil {
			return nil, nil, err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, nil, err
	}
	parsedRows, schema, err := dataset.ParseCSV(bytes.NewReader(buffer.Bytes()), declared, identityField)
	if err != nil {
		return nil, nil, err
	}
	if len(parsedRows) != len(rows) {
		return nil, nil, errors.New("dataset sync failed to materialize producer rows")
	}
	return buffer.Bytes(), schema, nil
}

func syncValueString(value interface{}) (string, error) {
	if value == nil {
		return "", nil
	}
	switch v := value.(type) {
	case string:
		return v, nil
	case bool:
		return strconv.FormatBool(v), nil
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), nil
	case float32:
		return strconv.FormatFloat(float64(v), 'f', -1, 32), nil
	case int:
		return strconv.Itoa(v), nil
	case int64:
		return strconv.FormatInt(v, 10), nil
	default:
		data, err := json.Marshal(value)
		return string(data), err
	}
}
