package health

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/history"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

const AdoptReportSchemaVersion = 1

// AdoptOptions configures the vault adopt operation.
type AdoptOptions struct {
	VaultRoot string
	DryRun    bool
	DB        *sidecar.DB
	History   *history.Store
}

// AdoptFileResult records the concrete change details for a single vault file.
type AdoptFileResult struct {
	Path        string   `json:"path"`
	Status      string   `json:"status"` // "adopted", "skipped", "failed"
	Title       string   `json:"title,omitempty"`
	Created     string   `json:"created,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	AddedFields []string `json:"added_fields,omitempty"`
	Error       string   `json:"error,omitempty"`
}

// AdoptReport is the machine-readable summary of a vault adopt operation.
type AdoptReport struct {
	SchemaVersion   int               `json:"schema_version"`
	Vault           string            `json:"vault"`
	StartedAt       time.Time         `json:"started_at"`
	FinishedAt      time.Time         `json:"finished_at"`
	DurationSeconds float64           `json:"duration_seconds"`
	DryRun          bool              `json:"dry_run"`
	Total           int               `json:"total"`
	Adopted         int               `json:"adopted"`
	Skipped         int               `json:"skipped"`
	Failed          int               `json:"failed"`
	Documents       []AdoptFileResult `json:"documents"`
	Warnings        []string          `json:"warnings,omitempty"`
}

// Adopt brings an existing Markdown vault into compliance with the contract in place.
func Adopt(opts AdoptOptions) (*AdoptReport, error) {
	startedAt := time.Now().UTC()
	vaultRoot, err := filepath.Abs(opts.VaultRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve vault root: %w", err)
	}

	report := &AdoptReport{
		SchemaVersion: AdoptReportSchemaVersion,
		Vault:         vaultRoot,
		StartedAt:     startedAt,
		DryRun:        opts.DryRun,
		Documents:     []AdoptFileResult{},
		Warnings:      []string{},
	}

	var histStore *history.Store
	if opts.History != nil {
		histStore = opts.History
	} else if !opts.DryRun {
		histStore = history.NewStore(vaultRoot)
	}

	var filePaths []string
	walkErr := vault.Walk(vaultRoot, func(p string) error {
		filePaths = append(filePaths, p)
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("walk vault: %w", walkErr)
	}

	sort.Strings(filePaths)
	report.Total = len(filePaths)

	for _, p := range filePaths {
		rel, err := filepath.Rel(vaultRoot, p)
		if err != nil {
			rel = p
		}
		rel = filepath.ToSlash(rel)

		fileInfo, statErr := os.Stat(p)
		if statErr != nil {
			report.Failed++
			report.Documents = append(report.Documents, AdoptFileResult{
				Path:   rel,
				Status: "failed",
				Error:  statErr.Error(),
			})
			continue
		}

		fileBytes, readErr := os.ReadFile(p) //nolint:gosec // p is produced by vault.Walk under vaultRoot
		if readErr != nil {
			report.Failed++
			report.Documents = append(report.Documents, AdoptFileResult{
				Path:   rel,
				Status: "failed",
				Error:  readErr.Error(),
			})
			continue
		}

		doc, parseErr := vault.ParseBytes(p, fileBytes)
		if parseErr != nil {
			report.Failed++
			report.Documents = append(report.Documents, AdoptFileResult{
				Path:   rel,
				Status: "failed",
				Error:  parseErr.Error(),
			})
			continue
		}

		// Check missing required fields
		hasTitle := false
		if t, ok := doc.Frontmatter["title"].(string); ok && strings.TrimSpace(t) != "" {
			hasTitle = true
		}
		hasCreated := false
		if c, ok := doc.Frontmatter["created"].(string); ok && strings.TrimSpace(c) != "" {
			hasCreated = true
		}
		_, hasTags := doc.Frontmatter["tags"]

		if hasTitle && hasCreated && hasTags {
			report.Skipped++
			report.Documents = append(report.Documents, AdoptFileResult{
				Path:   rel,
				Status: "skipped",
			})
			continue
		}

		missing := make(map[string]interface{})
		var addedFields []string

		var derivedTitle string
		if !hasTitle {
			derivedTitle = extractH1Title(doc.Body)
			if derivedTitle == "" {
				base := filepath.Base(p)
				derivedTitle = strings.TrimSuffix(base, filepath.Ext(base))
			}
			missing["title"] = derivedTitle
			addedFields = append(addedFields, "title")
		} else {
			derivedTitle = doc.Title
		}

		var derivedCreated string
		if !hasCreated {
			derivedCreated = deriveCreatedTime(p, fileInfo)
			missing["created"] = derivedCreated
			addedFields = append(addedFields, "created")
		} else {
			derivedCreated = doc.Created
		}

		var derivedTags []string
		if !hasTags {
			derivedTags = []string{}
			missing["tags"] = derivedTags
			addedFields = append(addedFields, "tags")
		} else {
			derivedTags = doc.Tags
		}

		if opts.DryRun {
			report.Adopted++
			report.Documents = append(report.Documents, AdoptFileResult{
				Path:        rel,
				Status:      "adopted",
				Title:       derivedTitle,
				Created:     derivedCreated,
				Tags:        derivedTags,
				AddedFields: addedFields,
			})
			continue
		}

		// Apply mode: snapshot pre-adoption content into history safety net
		if histStore != nil {
			if _, snapErr := histStore.Snapshot(rel); snapErr != nil {
				report.Warnings = append(report.Warnings, fmt.Sprintf("history snapshot failed for %s: %v", rel, snapErr))
			}
		}

		if writeErr := vault.BackfillFrontmatter(p, missing); writeErr != nil {
			report.Failed++
			report.Documents = append(report.Documents, AdoptFileResult{
				Path:   rel,
				Status: "failed",
				Error:  writeErr.Error(),
			})
			continue
		}

		// Re-index updated note in sidecar DB if available
		if opts.DB != nil {
			newDoc, parseNewErr := vault.ParseFile(p)
			if parseNewErr == nil {
				_ = opts.DB.IndexDocument(newDoc)
			}
		}

		report.Adopted++
		report.Documents = append(report.Documents, AdoptFileResult{
			Path:        rel,
			Status:      "adopted",
			Title:       derivedTitle,
			Created:     derivedCreated,
			Tags:        derivedTags,
			AddedFields: addedFields,
		})
	}

	report.FinishedAt = time.Now().UTC()
	report.DurationSeconds = report.FinishedAt.Sub(report.StartedAt).Seconds()
	return report, nil
}

func extractH1Title(body string) string {
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "# ") {
			title := strings.TrimSpace(line[2:])
			if title != "" {
				return title
			}
		}
	}
	return ""
}

func parseDailyNoteDate(path string) (time.Time, bool) {
	base := strings.TrimSuffix(filepath.Base(path), ".md")
	t, err := time.Parse("2006-01-02", base)
	if err == nil {
		return t.UTC(), true
	}
	return time.Time{}, false
}

func deriveCreatedTime(path string, info os.FileInfo) string {
	if t, ok := parseDailyNoteDate(path); ok {
		return t.Format(time.RFC3339)
	}
	if info != nil {
		if bt, ok := fileBirthTime(info); ok && !bt.IsZero() {
			return bt.UTC().Truncate(time.Second).Format(time.RFC3339)
		}
		return info.ModTime().UTC().Truncate(time.Second).Format(time.RFC3339)
	}
	return time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)
}

// WriteAdoptReport writes report to path as indented JSON.
func WriteAdoptReport(path string, report *AdoptReport) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal adopt report: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write adopt report: %w", err)
	}
	return nil
}
