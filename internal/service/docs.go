package service

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

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
	ASN           int    `json:"asn,omitempty"`
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
			ASN:           r.ASN,
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
	s.snapshotBefore(absPath)
	if err := vault.SetFrontmatterKey(absPath, "status", status); err != nil {
		return err
	}
	doc, err := vault.ParseFile(absPath)
	if err != nil {
		return err
	}
	return s.IndexDocument(doc)
}

// DocDue sets the due_date frontmatter key of a document and re-indexes.
func (s *Service) DocDue(relPath, date string) error {
	absPath, err := vault.SecurePath(s.VaultRoot, relPath)
	if err != nil {
		return err
	}
	s.snapshotBefore(absPath)
	if err := vault.SetFrontmatterKey(absPath, "due_date", date); err != nil {
		return err
	}
	doc, err := vault.ParseFile(absPath)
	if err != nil {
		return err
	}
	return s.IndexDocument(doc)
}

// DocASN assigns an archive serial number and reindexes the document. "next"
// allocates the lowest unused positive number. Allocation is guarded by a
// vault-local lock and scans the source files rather than trusting a possibly
// stale sidecar index, so concurrent CLI invocations cannot collide.
func (s *Service) DocASN(relPath, value string) (int, error) {
	absPath, err := vault.SecurePath(s.VaultRoot, relPath)
	if err != nil {
		return 0, err
	}
	relTarget, err := filepath.Rel(s.VaultRoot, absPath)
	if err != nil {
		return 0, fmt.Errorf("make document path relative: %w", err)
	}

	var assigned int
	err = vault.WithASNLock(s.VaultRoot, func() error {
		report, err := vault.ScanASNs(s.VaultRoot)
		if err != nil {
			return fmt.Errorf("scan vault ASN assignments: %w", err)
		}
		for _, malformed := range report.Malformed {
			if malformed.Path != relTarget {
				return fmt.Errorf("cannot assign ASN while %s has malformed ASN: %s", malformed.Path, malformed.Message)
			}
		}
		if len(report.ParseErrors) > 0 {
			return fmt.Errorf("cannot assign ASN while %s cannot be parsed: %s", report.ParseErrors[0].Path, report.ParseErrors[0].Message)
		}

		if strings.EqualFold(value, "next") {
			assigned = report.LowestFree()
		} else {
			assigned, err = strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("ASN must be a positive integer or \"next\"")
			}
			if err := vault.ValidateASN(assigned); err != nil {
				return fmt.Errorf("invalid ASN: %w", err)
			}
		}

		for _, existingPath := range report.AssignedPaths(assigned) {
			if existingPath != relTarget {
				return fmt.Errorf("ASN %d is already assigned to %s", assigned, existingPath)
			}
		}

		s.snapshotBefore(absPath)
		if err := vault.SetFrontmatterValue(absPath, "asn", assigned); err != nil {
			return fmt.Errorf("set ASN: %w", err)
		}
		doc, err := vault.ParseFile(absPath)
		if err != nil {
			return fmt.Errorf("parse ASN-updated document: %w", err)
		}
		if err := s.IndexDocument(doc); err != nil {
			return fmt.Errorf("index ASN-updated document: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return assigned, nil
}

// DocType sets the document_type frontmatter key of a document and re-indexes.
func (s *Service) DocType(relPath, docType string) error {
	if strings.TrimSpace(docType) == "" {
		return fmt.Errorf("document type must not be empty")
	}
	return s.setKeyAndReindex(relPath, "document_type", docType)
}

// DocCorrespondent sets the correspondent frontmatter key of a document and re-indexes.
func (s *Service) DocCorrespondent(relPath, name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("correspondent must not be empty")
	}
	return s.setKeyAndReindex(relPath, "correspondent", name)
}

// DocTagAdd appends a tag to the document's tags list (no-op when already
// present) and re-indexes.
func (s *Service) DocTagAdd(relPath, tag string) error {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return fmt.Errorf("tag must not be empty")
	}
	return s.mutateTags(relPath, func(tags []string) []string {
		for _, t := range tags {
			if t == tag {
				return tags
			}
		}
		return append(tags, tag)
	})
}

// DocTagRemove removes a tag from the document's tags list (no-op when absent)
// and re-indexes.
func (s *Service) DocTagRemove(relPath, tag string) error {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return fmt.Errorf("tag must not be empty")
	}
	return s.mutateTags(relPath, func(tags []string) []string {
		out := tags[:0]
		for _, t := range tags {
			if t != tag {
				out = append(out, t)
			}
		}
		return out
	})
}

func (s *Service) mutateTags(relPath string, mutate func([]string) []string) error {
	absPath, err := vault.SecurePath(s.VaultRoot, relPath)
	if err != nil {
		return err
	}
	doc, err := vault.ParseFile(absPath)
	if err != nil {
		return err
	}
	tags := mutate(append([]string(nil), doc.Tags...))
	if tags == nil {
		tags = []string{}
	}
	if err := vault.SetFrontmatterValue(absPath, "tags", tags); err != nil {
		return err
	}
	doc, err = vault.ParseFile(absPath)
	if err != nil {
		return err
	}
	return s.IndexDocument(doc)
}

func (s *Service) setKeyAndReindex(relPath, key, value string) error {
	absPath, err := vault.SecurePath(s.VaultRoot, relPath)
	if err != nil {
		return err
	}
	if err := vault.SetFrontmatterKey(absPath, key, value); err != nil {
		return err
	}
	doc, err := vault.ParseFile(absPath)
	if err != nil {
		return err
	}
	return s.IndexDocument(doc)
}

// BatchResult reports the per-file outcome of a batch document mutation.
type BatchResult struct {
	File   string `json:"file"`
	Status string `json:"status"` // "updated" or "error"
	Error  string `json:"error,omitempty"`
}

// DocBatch applies fn to every file and collects per-file results instead of
// aborting on the first failure. It returns the results plus updated/failed
// counters.
func (s *Service) DocBatch(files []string, fn func(relPath string) error) ([]BatchResult, int, int) {
	results := make([]BatchResult, 0, len(files))
	updated, failed := 0, 0
	for _, f := range files {
		if err := fn(f); err != nil {
			results = append(results, BatchResult{File: f, Status: "error", Error: err.Error()})
			failed++
		} else {
			results = append(results, BatchResult{File: f, Status: "updated"})
			updated++
		}
	}
	return results, updated, failed
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
