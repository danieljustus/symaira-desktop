package service

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/danieljustus/symaira-desktop/internal/dbviews"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

// CSVImportOptions configures one-way CSV import into vault notes.
type CSVImportOptions struct {
	CSVData       io.Reader         // CSV content reader (required)
	Apply         bool              // false = dry-run (default), true = execute writes
	Folder        string            // target relative directory in vault (e.g. "invoices")
	ColumnMapping map[string]string // CSV Column Header -> Note Property Key
	TitleColumn   string            // specific CSV column to use for note title
	BaseName      string            // optional base note to create or update
	OnCollision   string            // "suffix" (default), "skip", "error"
}

// CSVImportRowResult records the outcome of importing a single CSV row.
type CSVImportRowResult struct {
	LineNumber int                    `json:"line_number"`
	Title      string                 `json:"title"`
	Path       string                 `json:"path"`
	Properties map[string]interface{} `json:"properties"`
	Status     string                 `json:"status"` // "created", "preview", "skipped", "error"
	Error      string                 `json:"error,omitempty"`
}

// MalformedRow reports a CSV row that could not be parsed.
type MalformedRow struct {
	LineNumber int    `json:"line_number"`
	Raw        string `json:"raw"`
	Reason     string `json:"reason"`
}

// CSVImportReport summarizes the complete results of a CSV import operation.
type CSVImportReport struct {
	DryRun         bool                 `json:"dry_run"`
	TotalRows      int                  `json:"total_rows"`
	ImportedCount  int                  `json:"imported_count"`
	SkippedCount   int                  `json:"skipped_count"`
	CollisionCount int                  `json:"collision_count"`
	ErrorCount     int                  `json:"error_count"`
	Rows           []CSVImportRowResult `json:"rows"`
	MalformedRows  []MalformedRow       `json:"malformed_rows,omitempty"`
	CreatedBase    string               `json:"created_base,omitempty"`
}

// ViewsExportCSV exports visible and computed view rows to standard CSV bytes.
// It never mutates vault notes or uses CSV as the vault source of truth.
func (s *Service) ViewsExportCSV(viewID string) ([]byte, error) {
	view, err := s.ViewsGet(viewID)
	if err != nil {
		return nil, fmt.Errorf("views export csv: %w", err)
	}

	rows, err := s.ViewsExec(viewID)
	if err != nil {
		return nil, fmt.Errorf("views exec %q: %w", viewID, err)
	}

	// Determine visible columns
	columns := view.Columns
	if len(columns) == 0 {
		colSet := make(map[string]bool)
		for _, r := range rows {
			for k := range r {
				if k == "_path" {
					continue
				}
				colSet[k] = true
			}
		}
		var sortedCols []string
		for k := range colSet {
			sortedCols = append(sortedCols, k)
		}
		sort.Strings(sortedCols)
		if colSet["_title"] {
			columns = append([]string{"_title"}, removeCol(sortedCols, "_title")...)
		} else if colSet["title"] {
			columns = append([]string{"title"}, removeCol(sortedCols, "title")...)
		} else {
			columns = sortedCols
		}
	}

	var cleanCols []string
	for _, c := range columns {
		c = strings.TrimSpace(c)
		if c != "" {
			cleanCols = append(cleanCols, c)
		}
	}
	if len(cleanCols) == 0 {
		cleanCols = []string{"title"}
	}

	var buf bytes.Buffer
	w := csv.NewWriter(&buf)

	// Write header row
	headers := make([]string, len(cleanCols))
	for i, c := range cleanCols {
		switch c {
		case "_title":
			headers[i] = "title"
		case "_path":
			headers[i] = "path"
		default:
			headers[i] = c
		}
	}
	if err := w.Write(headers); err != nil {
		return nil, fmt.Errorf("write csv headers: %w", err)
	}

	// Write data rows
	for _, row := range rows {
		record := make([]string, len(cleanCols))
		for i, c := range cleanCols {
			val, exists := row[c]
			if !exists && c == "title" {
				val = row["_title"]
			}
			if val == nil {
				record[i] = ""
				continue
			}
			switch v := val.(type) {
			case []string:
				record[i] = strings.Join(v, ", ")
			case []interface{}:
				var parts []string
				for _, item := range v {
					parts = append(parts, fmt.Sprintf("%v", item))
				}
				record[i] = strings.Join(parts, ", ")
			default:
				record[i] = fmt.Sprintf("%v", val)
			}
		}
		if err := w.Write(record); err != nil {
			return nil, fmt.Errorf("write csv record: %w", err)
		}
	}

	w.Flush()
	if err := w.Error(); err != nil {
		return nil, fmt.Errorf("flush csv: %w", err)
	}

	return buf.Bytes(), nil
}

// CSVImport performs one-way import of CSV records into frontmatter Markdown notes.
// By default, it runs in dry-run mode (Apply=false) to preview generated notes and
// report collisions and malformed rows. CSV never becomes the vault source of truth.
func (s *Service) CSVImport(opts CSVImportOptions) (*CSVImportReport, error) {
	if opts.CSVData == nil {
		return nil, errors.New("csv data reader is required")
	}

	collisionMode := strings.ToLower(strings.TrimSpace(opts.OnCollision))
	if collisionMode == "" {
		collisionMode = "suffix"
	}

	r := csv.NewReader(opts.CSVData)
	r.LazyQuotes = true
	r.FieldsPerRecord = -1

	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parse csv: %w", err)
	}
	if len(records) == 0 {
		return nil, errors.New("csv is empty")
	}

	rawHeaders := records[0]
	headers := make([]string, len(rawHeaders))
	for i, h := range rawHeaders {
		headers[i] = strings.TrimSpace(h)
	}

	report := &CSVImportReport{
		DryRun:        !opts.Apply,
		TotalRows:     len(records) - 1,
		Rows:          make([]CSVImportRowResult, 0),
		MalformedRows: make([]MalformedRow, 0),
	}

	// Track imported properties for base inference
	propertyValues := make(map[string][]string)

	folderRel := strings.Trim(opts.Folder, "/\\")

	for lineIdx := 1; lineIdx < len(records); lineIdx++ {
		lineNum := lineIdx + 1
		row := records[lineIdx]

		if len(row) == 0 || (len(row) == 1 && strings.TrimSpace(row[0]) == "") {
			continue // skip empty lines
		}

		if len(row) != len(headers) {
			report.MalformedRows = append(report.MalformedRows, MalformedRow{
				LineNumber: lineNum,
				Raw:        strings.Join(row, ","),
				Reason:     fmt.Sprintf("column count mismatch: expected %d, got %d", len(headers), len(row)),
			})
			report.ErrorCount++
			continue
		}

		// Extract properties
		props := make(map[string]interface{})
		var title string

		for colIdx, colName := range headers {
			val := strings.TrimSpace(row[colIdx])
			propKey := sanitizePropKey(colName)
			if mapped, ok := opts.ColumnMapping[colName]; ok && mapped != "" {
				propKey = mapped
			} else if mapped, ok := opts.ColumnMapping[propKey]; ok && mapped != "" {
				propKey = mapped
			}

			// Check title column
			if opts.TitleColumn != "" && (strings.EqualFold(colName, opts.TitleColumn) || strings.EqualFold(propKey, opts.TitleColumn)) {
				title = val
			} else if title == "" && (propKey == "title" || strings.EqualFold(colName, "title") || strings.EqualFold(colName, "name") || strings.EqualFold(colName, "subject")) {
				title = val
			}

			if val != "" {
				props[propKey] = val
				propertyValues[propKey] = append(propertyValues[propKey], val)
			}
		}

		if title == "" {
			if firstVal, ok := props[sanitizePropKey(headers[0])].(string); ok && firstVal != "" {
				title = firstVal
			} else {
				title = fmt.Sprintf("Row %d", lineIdx)
			}
		}

		slug := dbviews.Slugify(title)
		if slug == "" {
			slug = fmt.Sprintf("row-%d", lineIdx)
		}

		noteFilename := slug + ".md"
		var targetRelPath string
		if folderRel != "" {
			targetRelPath = filepath.Join(folderRel, noteFilename)
		} else {
			targetRelPath = noteFilename
		}

		// Collision check
		absPath, err := vault.SecurePath(s.VaultRoot, targetRelPath)
		if err != nil {
			report.Rows = append(report.Rows, CSVImportRowResult{
				LineNumber: lineNum,
				Title:      title,
				Path:       targetRelPath,
				Properties: props,
				Status:     "error",
				Error:      fmt.Sprintf("invalid path: %v", err),
			})
			report.ErrorCount++
			continue
		}

		collided := false
		if _, statErr := os.Stat(absPath); statErr == nil {
			collided = true
			report.CollisionCount++

			if collisionMode == "skip" {
				report.Rows = append(report.Rows, CSVImportRowResult{
					LineNumber: lineNum,
					Title:      title,
					Path:       targetRelPath,
					Properties: props,
					Status:     "skipped",
					Error:      "file already exists (collision skipped)",
				})
				report.SkippedCount++
				continue
			} else if collisionMode == "error" {
				report.Rows = append(report.Rows, CSVImportRowResult{
					LineNumber: lineNum,
					Title:      title,
					Path:       targetRelPath,
					Properties: props,
					Status:     "error",
					Error:      "file already exists (collision error)",
				})
				report.ErrorCount++
				continue
			} else {
				// Suffix mode: find next available suffix
				suffixIdx := 2
				for {
					suffixedFilename := fmt.Sprintf("%s-%d.md", slug, suffixIdx)
					if folderRel != "" {
						targetRelPath = filepath.Join(folderRel, suffixedFilename)
					} else {
						targetRelPath = suffixedFilename
					}
					nextAbs, err := vault.SecurePath(s.VaultRoot, targetRelPath)
					if err != nil {
						break
					}
					if _, err := os.Stat(nextAbs); os.IsNotExist(err) {
						absPath = nextAbs
						break
					}
					suffixIdx++
				}
			}
		}

		// Build frontmatter map
		fm := make(map[string]interface{})
		fm["title"] = title
		fm["created"] = time.Now().UTC().Format(time.RFC3339)

		isDoc := false
		for k, v := range props {
			if k == "title" {
				continue
			}
			if k == "tags" {
				if strVal, ok := v.(string); ok {
					var tagList []string
					for _, t := range strings.Split(strVal, ",") {
						t = strings.TrimSpace(t)
						if t != "" {
							tagList = append(tagList, t)
						}
					}
					fm["tags"] = tagList
					continue
				}
			}
			if k == "document_date" || k == "correspondent" || k == "status" || k == "amount" || k == "due_date" {
				isDoc = true
			}
			fm[k] = v
		}

		if isDoc {
			fm["type"] = "document"
		} else {
			fm["type"] = "note"
		}

		fmBytes, err := yaml.Marshal(fm)
		if err != nil {
			report.Rows = append(report.Rows, CSVImportRowResult{
				LineNumber: lineNum,
				Title:      title,
				Path:       targetRelPath,
				Properties: props,
				Status:     "error",
				Error:      fmt.Sprintf("marshal frontmatter: %v", err),
			})
			report.ErrorCount++
			continue
		}

		fullContent := fmt.Sprintf("---\n%s---\n\n# %s\n", string(fmBytes), title)

		if opts.Apply {
			if err := os.MkdirAll(filepath.Dir(absPath), 0750); err != nil {
				report.Rows = append(report.Rows, CSVImportRowResult{
					LineNumber: lineNum,
					Title:      title,
					Path:       targetRelPath,
					Properties: props,
					Status:     "error",
					Error:      fmt.Sprintf("create parent directory: %v", err),
				})
				report.ErrorCount++
				continue
			}

			if err := writeFileAtomic(absPath, []byte(fullContent)); err != nil {
				report.Rows = append(report.Rows, CSVImportRowResult{
					LineNumber: lineNum,
					Title:      title,
					Path:       targetRelPath,
					Properties: props,
					Status:     "error",
					Error:      fmt.Sprintf("write file: %v", err),
				})
				report.ErrorCount++
				continue
			}

			// Index into sidecar DB
			doc, parseErr := vault.ParseFile(absPath)
			if parseErr == nil && s.DB != nil {
				_ = s.IndexDocument(doc)
			}

			status := "created"
			if collided {
				status = "created (collision suffixed)"
			}
			report.Rows = append(report.Rows, CSVImportRowResult{
				LineNumber: lineNum,
				Title:      title,
				Path:       targetRelPath,
				Properties: props,
				Status:     status,
			})
			report.ImportedCount++
		} else {
			status := "preview"
			if collided {
				status = "preview (collision suffix needed)"
			}
			report.Rows = append(report.Rows, CSVImportRowResult{
				LineNumber: lineNum,
				Title:      title,
				Path:       targetRelPath,
				Properties: props,
				Status:     status,
			})
			report.ImportedCount++
		}
	}

	// Create or update optional Base note
	if opts.BaseName != "" {
		baseTitle := strings.TrimSpace(opts.BaseName)
		baseSlug := dbviews.Slugify(baseTitle)
		basePath := filepath.Join(dbviews.Dir, baseSlug+".md")

		inferredProperties := make(map[string]dbviews.PropertyConfig)
		var columnOrder []string

		for _, h := range headers {
			propKey := sanitizePropKey(h)
			if mapped, ok := opts.ColumnMapping[h]; ok && mapped != "" {
				propKey = mapped
			} else if mapped, ok := opts.ColumnMapping[propKey]; ok && mapped != "" {
				propKey = mapped
			}
			if propKey == "title" {
				continue
			}

			columnOrder = append(columnOrder, propKey)
			values := propertyValues[propKey]
			propType := inferPropertyType(values)
			var options []string
			if propType == "select" {
				options = collectDistinctOptions(values)
			}

			inferredProperties[propKey] = dbviews.PropertyConfig{
				Type:    propType,
				Label:   h,
				Options: options,
			}
		}

		sourceScope := ""
		if folderRel != "" {
			sourceScope = folderRel + "/"
		}

		defaultCols := append([]string{"title"}, columnOrder...)

		base := &dbviews.Base{
			ID:          baseSlug,
			Path:        basePath,
			Title:       baseTitle,
			Description: fmt.Sprintf("Imported from CSV on %s", time.Now().UTC().Format("2006-01-02")),
			Created:     time.Now().UTC().Format(time.RFC3339),
			Tags:        []string{"base"},
			Properties:  inferredProperties,
			Views: []dbviews.View{
				{
					ID:      "all_" + baseSlug,
					Name:    "All " + baseTitle,
					Type:    "table",
					Source:  sourceScope,
					Columns: defaultCols,
				},
			},
		}

		if opts.Apply {
			if err := s.BaseSave(base); err != nil {
				return report, fmt.Errorf("create base %q: %w", baseTitle, err)
			}
		}
		report.CreatedBase = basePath
	}

	return report, nil
}

func sanitizePropKey(header string) string {
	header = strings.TrimSpace(header)
	s := strings.ToLower(header)
	s = strings.ReplaceAll(s, " ", "_")
	s = strings.ReplaceAll(s, "-", "_")
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' {
			b.WriteByte(c)
		}
	}
	res := strings.Trim(b.String(), "_")
	if res == "" {
		res = "prop"
	}
	return res
}

func inferPropertyType(values []string) string {
	if len(values) == 0 {
		return "text"
	}

	allNumeric := true
	allDates := true

	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, ok := parseNumberFlexible(v); !ok {
			allNumeric = false
		}
		if _, ok := parseDateFlexible(v); !ok {
			allDates = false
		}
	}

	if allNumeric {
		return "number"
	}
	if allDates {
		return "date"
	}

	distinct := collectDistinctOptions(values)
	if len(distinct) <= 10 && len(values) >= 3 && len(distinct) < len(values) {
		return "select"
	}

	return "text"
}

func collectDistinctOptions(values []string) []string {
	seen := make(map[string]bool)
	var opts []string
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v != "" && !seen[strings.ToLower(v)] {
			seen[strings.ToLower(v)] = true
			opts = append(opts, v)
		}
	}
	sort.Strings(opts)
	return opts
}

func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".symdesk-csv-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("rename temp file: %w", err)
	}
	return nil
}
