package service

import (
	"path/filepath"

	"github.com/danieljustus/symaira-desktop/internal/sidecar"
)

type DocsListResult struct {
	Path         string `json:"path"`
	Title        string `json:"title"`
	DocumentDate string `json:"document_date,omitempty"`
	Person       string `json:"person,omitempty"`
	Status       string `json:"status,omitempty"`
	DueDate      string `json:"due_date,omitempty"`
	Confidence   int    `json:"confidence,omitempty"`
	Correspondent string `json:"correspondent,omitempty"`
	DocumentType string `json:"document_type,omitempty"`
}

func (s *Service) DocsList(f sidecar.DocsFilter) ([]DocsListResult, error) {
	rows, err := s.DB.DocsList(f)
	if err != nil {
		return nil, err
	}

	results := make([]DocsListResult, 0, len(rows))
	for _, r := range rows {
		relPath, _ := filepath.Rel(s.VaultRoot, r.Path)
		if relPath == "" {
			relPath = r.Path
		}
		results = append(results, DocsListResult{
			Path:         relPath,
			Title:        r.Title,
			DocumentDate: r.DocumentDate,
			Person:       r.Person,
			Status:       r.Status,
			DueDate:      r.DueDate,
			Confidence:   r.Confidence,
			Correspondent: r.Correspondent,
			DocumentType: r.DocumentType,
		})
	}
	return results, nil
}
