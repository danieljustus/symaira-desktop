package service

import (
	"fmt"
	"strings"

	"github.com/danieljustus/symaira-desktop/internal/ai"
	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

// AutofillResult is returned by the Autofill service method.
type AutofillResult struct {
	View     string              `json:"view"`
	Property string              `json:"property"`
	Total    int                 `json:"total"`
	Filled   int                 `json:"filled"`
	Skipped  int                 `json:"skipped"`
	Failed   int                 `json:"failed"`
	DryRun   bool                `json:"dry_run"`
	Changes  []AutofillChange    `json:"changes,omitempty"`
	Errors   []map[string]string `json:"errors,omitempty"`
}

// AutofillChange describes one property change that would be or was applied.
type AutofillChange struct {
	Path     string `json:"path"`
	Title    string `json:"title,omitempty"`
	OldValue string `json:"old_value,omitempty"`
	NewValue string `json:"new_value"`
	Status   string `json:"status"` // pending, applied, skipped, failed
}

// Autofill fills an empty frontmatter property on all notes that match a view.
// If property is already present and non-empty, the note is skipped.
func (s *Service) Autofill(viewID, property, prompt string, dryRun bool) (*AutofillResult, error) {
	property = strings.TrimSpace(property)
	if property == "" {
		return nil, fmt.Errorf("property must not be empty")
	}

	view, err := s.ViewsGet(viewID)
	if err != nil {
		return nil, fmt.Errorf("view not found: %w", err)
	}

	rows, err := s.ViewsExec(viewID)
	if err != nil {
		return nil, fmt.Errorf("failed to evaluate view: %w", err)
	}

	res := &AutofillResult{
		View:     view.Name,
		Property: property,
		Total:    len(rows),
		DryRun:   dryRun,
	}

	for _, row := range rows {
		path, _ := row["_path"].(string)
		if path == "" {
			res.Skipped++
			continue
		}
		relPath := path
		if strings.HasPrefix(path, s.VaultRoot+"/") {
			relPath = strings.TrimPrefix(path, s.VaultRoot+"/")
		}

		title, _ := row["_title"].(string)
		existing := ""
		if v, ok := row[property]; ok && v != nil {
			existing = fmt.Sprintf("%v", v)
		}
		if strings.TrimSpace(existing) != "" && existing != "<nil>" {
			res.Skipped++
			res.Changes = append(res.Changes, AutofillChange{
				Path:   relPath,
				Title:  title,
				Status: "skipped",
			})
			continue
		}

		doc, err := vault.ParseFile(path)
		if err != nil {
			res.Failed++
			res.Errors = append(res.Errors, map[string]string{"path": relPath, "error": err.Error()})
			continue
		}

		value, err := runAutofillPrompt(s.Config, doc, property, prompt)
		if err != nil {
			res.Failed++
			res.Errors = append(res.Errors, map[string]string{"path": relPath, "error": err.Error()})
			continue
		}
		if value == "" {
			res.Skipped++
			res.Changes = append(res.Changes, AutofillChange{
				Path:   relPath,
				Title:  title,
				Status: "skipped",
			})
			continue
		}

		change := AutofillChange{
			Path:     relPath,
			Title:    title,
			OldValue: existing,
			NewValue: value,
			Status:   "pending",
		}
		if !dryRun {
			if err := vault.SetFrontmatterValue(path, property, value); err != nil {
				res.Failed++
				res.Errors = append(res.Errors, map[string]string{"path": relPath, "error": err.Error()})
				continue
			}
			change.Status = "applied"
			res.Filled++
		} else {
			res.Filled++
		}
		res.Changes = append(res.Changes, change)
	}

	return res, nil
}

func runAutofillPrompt(cfg *config.Config, doc *vault.Document, property, extraPrompt string) (string, error) {
	property = strings.TrimSpace(property)
	var instruction strings.Builder
	fmt.Fprintf(&instruction, "Extract the value for the property %q from the note below. ", property)
	if extraPrompt != "" {
		fmt.Fprintf(&instruction, "%s ", extraPrompt)
	}
	instruction.WriteString("Return only the value, no markdown, no quotes, no explanation. " +
		"If the note does not contain the information, return an empty line.\n\n")
	if doc.Title != "" {
		fmt.Fprintf(&instruction, "Title: %s\n", doc.Title)
	}
	instruction.WriteString(doc.Body)
	instruction.WriteString("\n")

	return ai.PromptOne(cfg, instruction.String())
}
