package service

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

// Service encapsulates the core operations of symdesk.
type Service struct {
	VaultRoot string
	DB        *sidecar.DB
}

// New creates a new Service instance.
func New(vaultRoot string, db *sidecar.DB) *Service {
	return &Service{
		VaultRoot: vaultRoot,
		DB:        db,
	}
}

// Ls returns a list of files in the vault.
func (s *Service) Ls(dirPrefix string) ([]map[string]interface{}, error) {
	docs, err := s.DB.ListFiles(dirPrefix)
	if err != nil {
		return nil, err
	}

	var results []map[string]interface{}
	for _, d := range docs {
		relPath, _ := filepath.Rel(s.VaultRoot, d.Path)
		if relPath == "" {
			relPath = d.Path
		}
		results = append(results, map[string]interface{}{
			"path":     relPath,
			"title":    d.Title,
			"modified": d.Created, // Re-using Created field for modified_at in docs
		})
	}
	return results, nil
}

// Search performs a full-text search.
func (s *Service) Search(query string) ([]map[string]interface{}, error) {
	docs, err := s.DB.Search(query)
	if err != nil {
		return nil, err
	}

	var results []map[string]interface{}
	for _, d := range docs {
		relPath, _ := filepath.Rel(s.VaultRoot, d.Path)
		results = append(results, map[string]interface{}{
			"path":    relPath,
			"title":   d.Title,
			"snippet": d.Body,
			"score":   0.0, // FTS doesn't return score trivially here without further query modifications
		})
	}
	return results, nil
}

// Props returns the properties for a given file.
func (s *Service) Props(file string) (map[string]interface{}, error) {
	absPath := filepath.Join(s.VaultRoot, file)
	return s.DB.GetProperties(absPath)
}

// Backlinks returns the files linking to the given file.
func (s *Service) Backlinks(file string) ([]string, error) {
	absPath := filepath.Join(s.VaultRoot, file)
	links, err := s.DB.GetBacklinks(absPath)
	if err != nil {
		return nil, err
	}

	var relLinks []string
	for _, p := range links {
		rel, _ := filepath.Rel(s.VaultRoot, p)
		relLinks = append(relLinks, rel)
	}
	return relLinks, nil
}

// NoteNew creates a new note in the vault and indexes it.
func (s *Service) NoteNew(title, content string) (string, error) {
	if title == "" {
		return "", fmt.Errorf("title is required")
	}

	fileName := strings.ReplaceAll(title, " ", "_") + ".md"
	absPath := filepath.Join(s.VaultRoot, fileName)

	// Create content with frontmatter
	now := time.Now().UTC().Format(time.RFC3339)
	fullContent := fmt.Sprintf("---\ntitle: \"%s\"\ncreated: \"%s\"\ntags: []\n---\n\n%s", title, now, content)

	if err := os.WriteFile(absPath, []byte(fullContent), 0644); err != nil {
		return "", fmt.Errorf("failed to write file: %w", err)
	}

	// Index immediately
	doc, err := vault.ParseFile(absPath)
	if err != nil {
		return absPath, err
	}

	hash := sha256.Sum256([]byte(fullContent))
	doc.SHA256 = hex.EncodeToString(hash[:])
	
	if err := s.DB.IndexDocument(doc); err != nil {
		return absPath, fmt.Errorf("failed to index new file: %w", err)
	}

	return fileName, nil
}

// NoteMove renames a note and updates the index.
func (s *Service) NoteMove(oldPath, newPath string) error {
	absOld := filepath.Join(s.VaultRoot, oldPath)
	absNew := filepath.Join(s.VaultRoot, newPath)

	if err := os.Rename(absOld, absNew); err != nil {
		return fmt.Errorf("failed to move file: %w", err)
	}

	// Re-index new file
	doc, err := vault.ParseFile(absNew)
	if err != nil {
		return err
	}
	if err := s.DB.IndexDocument(doc); err != nil {
		return err
	}

	// NOTE: In a complete implementation, we should also delete the old entry from the DB.
	return nil
}
