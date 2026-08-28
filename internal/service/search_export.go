package service

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/export"
	"github.com/danieljustus/symaira-desktop/internal/notebook"
	"github.com/danieljustus/symaira-desktop/internal/pdf"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

// SearchSetExportResult describes a rendered search result set. Path is a
// vault-relative Markdown path for a Markdown export and an absolute output
// path for PDF, matching the existing export service contract.
type SearchSetExportResult struct {
	Format string `json:"format"`
	Path   string `json:"path"`
	Query  string `json:"query"`
	Title  string `json:"title"`
	Count  int    `json:"count"`
}

// SearchNotebook promotes the current search result set into the existing
// Markdown-backed notebook primitive. The notebook stores the query for
// revisiting/refining, while Sources remains the bounded grounding set.
func (s *Service) SearchNotebook(query, title string) (*notebook.Notebook, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("search query is required")
	}
	response, err := s.SearchWithMeta(query)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(title) == "" {
		title = "Search: " + query
	}
	nb, err := notebook.NewWithQuery(s.VaultRoot, title, "Promoted from search query: "+query, query)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool, len(response.Results))
	for _, result := range response.Results {
		path := filepath.ToSlash(strings.TrimSpace(result.Path))
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		if err := notebook.AddSource(s.VaultRoot, nb, path); err != nil {
			return nb, err
		}
	}
	if err := s.reindexNotebook(nb); err != nil {
		return nb, err
	}
	return nb, nil
}

// ExportSearch searches query once and renders the complete result set to
// Markdown or PDF. Markdown exports are ordinary vault notes; PDF uses the
// existing in-process renderer and the same generated Markdown source.
func (s *Service) ExportSearch(query, title, outputPath, format string) (*SearchSetExportResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("search query is required")
	}
	response, err := s.SearchWithMeta(query)
	if err != nil {
		return nil, err
	}
	return s.ExportSearchResults(query, title, outputPath, format, response.Results)
}

// ExportSearchResults renders an already-resolved result set. It is public so
// callers that already own a result set (for example a native UI selection)
// can use the exact same formatter without inventing a second Markdown shape.
func (s *Service) ExportSearchResults(query, title, outputPath, format string, results []SearchResult) (*SearchSetExportResult, error) {
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "md" {
		format = "markdown"
	}
	if format != "markdown" && format != "pdf" {
		return nil, fmt.Errorf("unsupported search export format %q: use markdown or pdf", format)
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("search query is required")
	}
	if strings.TrimSpace(title) == "" {
		title = "Search results: " + query
	}

	hits := make([]export.SearchHit, 0, len(results))
	for _, result := range results {
		hits = append(hits, export.SearchHit{
			Path:            result.Path,
			Title:           result.Title,
			Snippet:         result.Snippet,
			Score:           result.Score,
			Anchor:          result.Anchor,
			MetadataMatches: result.MetadataMatches,
		})
	}
	created := time.Now().UTC().Format(time.RFC3339)
	content, err := export.SearchResults(query, title, created, hits, format)
	if err != nil {
		return nil, err
	}

	if format == "markdown" {
		rel, abs, err := s.searchMarkdownOutput(outputPath, title)
		if err != nil {
			return nil, err
		}
		s.snapshotBefore(abs)
		if err := os.WriteFile(abs, content, 0600); err != nil {
			return nil, fmt.Errorf("write search result note: %w", err)
		}
		if err := s.indexWrittenSearchNote(rel, abs, content); err != nil {
			return nil, err
		}
		return &SearchSetExportResult{Format: format, Path: rel, Query: query, Title: title, Count: len(results)}, nil
	}

	if outputPath == "" {
		_, outputPath, err = s.searchPDFOutput(title)
		if err != nil {
			return nil, err
		}
	} else {
		outputPath, err = absoluteOutputPath(outputPath)
		if err != nil {
			return nil, err
		}
	}
	if ok, hint := pdf.EngineAvailable(); !ok {
		return nil, fmt.Errorf("PDF export requires a typesetting engine: %s", hint)
	}
	if _, err := pdf.Render(content, outputPath, "", s.VaultRoot); err != nil {
		return nil, err
	}
	return &SearchSetExportResult{Format: format, Path: outputPath, Query: query, Title: title, Count: len(results)}, nil
}

func (s *Service) searchMarkdownOutput(outputPath, title string) (string, string, error) {
	if outputPath == "" {
		return s.searchNotePath(title, ".md")
	}
	rel, abs, err := s.vaultOutputPath(outputPath, ".md")
	return rel, abs, err
}

func (s *Service) searchPDFOutput(title string) (string, string, error) {
	return s.searchNotePath(title, ".pdf")
}

func (s *Service) searchNotePath(title, extension string) (string, string, error) {
	dir := filepath.Join("search-results")
	slug := notebook.Slugify(title)
	for i := 1; ; i++ {
		name := slug + extension
		if i > 1 {
			name = fmt.Sprintf("%s-%d%s", slug, i, extension)
		}
		rel, abs, err := s.vaultOutputPath(filepath.Join(dir, name), extension)
		if err != nil {
			return "", "", err
		}
		if _, err := os.Stat(abs); os.IsNotExist(err) {
			return rel, abs, nil
		}
	}
}

func (s *Service) vaultOutputPath(outputPath, extension string) (string, string, error) {
	if outputPath == "" {
		return "", "", fmt.Errorf("output path is required")
	}
	if filepath.Ext(outputPath) == "" {
		outputPath += extension
	}
	if filepath.IsAbs(outputPath) {
		rel, err := filepath.Rel(s.VaultRoot, filepath.Clean(outputPath))
		if err != nil {
			return "", "", err
		}
		outputPath = rel
	}
	rel := filepath.ToSlash(filepath.Clean(outputPath))
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || rel == ".symdesk" || strings.HasPrefix(rel, ".symdesk/") {
		return "", "", fmt.Errorf("search export path must stay inside the vault")
	}
	abs, err := vault.SecurePath(s.VaultRoot, rel)
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0750); err != nil {
		return "", "", fmt.Errorf("create search export directory: %w", err)
	}
	return rel, abs, nil
}

func absoluteOutputPath(outputPath string) (string, error) {
	outputPath = filepath.Clean(outputPath)
	if filepath.IsAbs(outputPath) {
		return outputPath, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("cannot resolve cwd: %w", err)
	}
	return filepath.Join(cwd, outputPath), nil
}

func (s *Service) indexWrittenSearchNote(rel, abs string, content []byte) error {
	doc, err := vault.ParseFile(abs)
	if err != nil {
		return fmt.Errorf("wrote search result note but failed to parse for indexing: %w", err)
	}
	hash := sha256.Sum256(content)
	doc.SHA256 = hex.EncodeToString(hash[:])
	if err := s.IndexDocument(doc); err != nil {
		return fmt.Errorf("wrote search result note but failed to index: %w", err)
	}
	return nil
}
