package notionimport

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/danieljustus/symaira-ingest/internal/writer"
)

// convertCSV processes a Notion CSV database export into:
//  1. One note per row with CSV columns as frontmatter properties.
//  2. An index note summarizing the database.
func convertCSV(entry ExportEntry, vault string, runID string, dryRun bool) []PageResult {
	var results []PageResult

	srcData, err := os.ReadFile(entry.SourcePath)
	if err != nil {
		results = append(results, PageResult{
			SourcePath: entry.SourcePath,
			Status:     "failed",
			Error:      fmt.Sprintf("open CSV: %v", err),
		})
		return results
	}
	fileHash := sha256Hex(srcData)

	reader := csv.NewReader(strings.NewReader(string(srcData)))
	records, err := reader.ReadAll()
	if err != nil {
		results = append(results, PageResult{
			SourcePath: entry.SourcePath,
			Status:     "failed",
			Error:      fmt.Sprintf("read CSV: %v", err),
		})
		return results
	}

	if len(records) < 2 {
		results = append(results, PageResult{
			SourcePath: entry.SourcePath,
			Status:     "skipped",
			Reason:     "empty CSV (no data rows)",
		})
		return results
	}

	headers := records[0]
	dbDir := filepath.Join(vault, "databases", sanitizeBaseName(entry.DisplayName))

	for i, row := range records[1:] {
		if len(row) == 0 {
			continue
		}
		rowNum := i + 1
		props := make(map[string]string, len(headers))
		for j, h := range headers {
			if j < len(row) {
				props[h] = row[j]
			}
		}

		rowTitle := row[0]
		if rowTitle == "" {
			rowTitle = fmt.Sprintf("Row %d", rowNum)
		}

		rowPath := filepath.Join(dbDir, sanitizeBaseName(rowTitle)+".md")

		if !dryRun && vault != "" && runID != "" {
			if noteExistsWithRunID(rowPath, runID) {
				results = append(results, PageResult{
					SourcePath: entry.SourcePath,
					VaultPath:  rowPath,
					Status:     "skipped",
					Reason:     "already imported in a previous run",
				})
				continue
			}
		}

		rowHash := sha256Hex([]byte(strings.Join(row, "\x1f")))
		if !dryRun {
			rowFrontmatter := buildCSVRowFrontmatter(entry, props, runID, rowHash)
			if err := writeNoteWithFrontmatter(rowPath, rowFrontmatter, buildCSVRowBody(props)); err != nil {
				results = append(results, PageResult{
					SourcePath: entry.SourcePath,
					Status:     "failed",
					Error:      fmt.Sprintf("write row %d: %v", rowNum, err),
				})
				continue
			}
		}

		results = append(results, PageResult{
			SourcePath: entry.SourcePath,
			VaultPath:  rowPath,
			Status:     mapStatus(dryRun, "imported"),
			SHA256:     rowHash,
		})
	}

	indexPath := filepath.Join(dbDir, "_index.md")
	if !dryRun && vault != "" && runID != "" && noteExistsWithRunID(indexPath, runID) {
		results = append(results, PageResult{
			SourcePath: entry.SourcePath,
			VaultPath:  indexPath,
			Status:     "skipped",
			Reason:     "already imported in a previous run",
		})
	} else {
		if !dryRun {
			indexNote := buildCSVIndexNote(entry, runID, fileHash)
			if err := writeNoteWithFrontmatter(indexPath, indexNote, buildCSVIndexBody(entry, headers, len(records)-1)); err != nil {
				results = append(results, PageResult{
					SourcePath: entry.SourcePath,
					Status:     "failed",
					Error:      fmt.Sprintf("write index: %v", err),
				})
				return results
			}
		}
		results = append(results, PageResult{
			SourcePath: entry.SourcePath,
			VaultPath:  indexPath,
			Status:     mapStatus(dryRun, "imported"),
			SHA256:     fileHash,
		})
	}

	return results
}

func mapStatus(dryRun bool, realStatus string) string {
	if dryRun {
		return "would-import"
	}
	return realStatus
}

// buildCSVRowFrontmatter creates a frontmatter map with the standard contract
// fields plus the CSV columns as top-level properties.
func buildCSVRowFrontmatter(entry ExportEntry, props map[string]string, runID, sha256 string) map[string]any {
	frontmatter := map[string]any{
		"source_path":   entry.SourcePath,
		"imported_from": "notion",
		"import_run_id": runID,
		"ingested_at":   time.Now().UTC(),
		"sha256":        sha256,
		"mime":          "text/csv",
		"tags":          []string{"notion", "csv-row", "database"},
		"category":      entry.Category,
	}

	reserved := map[string]bool{
		"source_path":   true,
		"imported_from": true,
		"import_run_id": true,
		"ingested_at":   true,
		"mime":          true,
		"tags":          true,
		"category":      true,
		"sha256":        true,
		"archive_path":  true,
		"sidecar_path":  true,
		"ocr_engine":    true,
	}
	for k, v := range props {
		key := k
		if reserved[strings.ToLower(key)] {
			key = "csv_" + key
		}
		frontmatter[key] = v
	}
	return frontmatter
}

// buildCSVRowBody generates the Markdown body for a CSV row note.
func buildCSVRowBody(props map[string]string) string {
	var sb strings.Builder
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if v := props[k]; v != "" {
			sb.WriteString(fmt.Sprintf("**%s:** %s\n\n", k, v))
		}
	}
	return sb.String()
}

// buildCSVIndexNote creates frontmatter for a CSV database index note.
func buildCSVIndexNote(entry ExportEntry, runID, sha256 string) writer.Note {
	return writer.Note{
		SourcePath:   entry.SourcePath,
		ImportedFrom: "notion",
		ImportRunID:  runID,
		IngestedAt:   time.Now().UTC(),
		SHA256:       sha256,
		MIME:         "text/csv",
		Tags:         []string{"notion", "csv-index", "database"},
		Category:     entry.Category,
	}
}

// buildCSVIndexBody generates the Markdown body for a CSV database index note.
func buildCSVIndexBody(entry ExportEntry, headers []string, rowCount int) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# %s\n\n", entry.DisplayName))
	sb.WriteString(fmt.Sprintf("Database with %d rows and %d columns.\n\n", rowCount, len(headers)))
	sb.WriteString("## Columns\n\n")
	for _, h := range headers {
		sb.WriteString(fmt.Sprintf("- %s\n", h))
	}
	sb.WriteString("\n")
	return sb.String()
}
