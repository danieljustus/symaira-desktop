package service

import (
	"fmt"
	"path/filepath"

	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/simhash"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

type DocsListResult struct {
	Path          string `json:"path"`
	Title         string `json:"title"`
	DocumentDate  string `json:"document_date,omitempty"`
	Person        string `json:"person,omitempty"`
	Status        string `json:"status,omitempty"`
	DueDate       string `json:"due_date,omitempty"`
	Confidence    int    `json:"confidence,omitempty"`
	Correspondent string `json:"correspondent,omitempty"`
	DocumentType  string `json:"document_type,omitempty"`
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
			Path:          relPath,
			Title:         r.Title,
			DocumentDate:  r.DocumentDate,
			Person:        r.Person,
			Status:        r.Status,
			DueDate:       r.DueDate,
			Confidence:    r.Confidence,
			Correspondent: r.Correspondent,
			DocumentType:  r.DocumentType,
		})
	}
	return results, nil
}

// DocStatus sets the status frontmatter key of a document and re-indexes.
func (s *Service) DocStatus(relPath, status string) error {
	if !vault.ValidStatuses[status] {
		return fmt.Errorf("invalid status %q (valid: open, paid, submitted, done, needs_review, waiting_for_reply)", status)
	}
	absPath, err := vault.SecurePath(s.VaultRoot, relPath)
	if err != nil {
		return err
	}
	if err := vault.SetFrontmatterKey(absPath, "status", status); err != nil {
		return err
	}
	doc, err := vault.ParseFile(absPath)
	if err != nil {
		return err
	}
	return s.DB.IndexDocument(doc)
}

// DocDue sets the due_date frontmatter key of a document and re-indexes.
func (s *Service) DocDue(relPath, date string) error {
	absPath, err := vault.SecurePath(s.VaultRoot, relPath)
	if err != nil {
		return err
	}
	if err := vault.SetFrontmatterKey(absPath, "due_date", date); err != nil {
		return err
	}
	doc, err := vault.ParseFile(absPath)
	if err != nil {
		return err
	}
	return s.DB.IndexDocument(doc)
}

// DocsReview returns documents that need human review based on the threshold.
func (s *Service) DocsReview(threshold int) ([]sidecar.ReviewResult, error) {
	return s.DB.ReviewQueue(threshold)
}

// SimilarDocs returns documents similar to the given file above the threshold.
func (s *Service) SimilarDocs(relPath string, threshold int) ([]sidecar.SimilarResult, error) {
	absPath, err := vault.SecurePath(s.VaultRoot, relPath)
	if err != nil {
		return nil, err
	}
	doc, err := vault.ParseFile(absPath)
	if err != nil {
		return nil, err
	}
	if doc.Simhash == "" {
		doc.Simhash = simhash.ComputeHex(doc.Body)
	}
	return s.DB.SimilarDocs(doc.Simhash, threshold)
}
