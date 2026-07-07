package service

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/danieljustus/symaira-desktop/internal/ai"
	"github.com/danieljustus/symaira-desktop/internal/dbviews"
	"github.com/danieljustus/symaira-desktop/internal/ingest"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

// Service encapsulates the core operations of symdesk.
type Service struct {
	VaultRoot string
	DB        *sidecar.DB
	ViewsMgr  *dbviews.Manager
}

// New creates a new Service instance.
func New(vaultRoot string, db *sidecar.DB) *Service {
	return &Service{
		VaultRoot: vaultRoot,
		DB:        db,
		ViewsMgr:  dbviews.NewManager(vaultRoot),
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
	absPath, err := vault.SecurePath(s.VaultRoot, file)
	if err != nil {
		return nil, err
	}
	return s.DB.GetProperties(absPath)
}

// Backlinks returns the files linking to the given file.
func (s *Service) Backlinks(file string) ([]string, error) {
	absPath, err := vault.SecurePath(s.VaultRoot, file)
	if err != nil {
		return nil, err
	}
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
	absPath, err := vault.SecurePath(s.VaultRoot, fileName)
	if err != nil {
		return "", err
	}

	// Create content with frontmatter
	now := time.Now().UTC().Format(time.RFC3339)
	fullContent := fmt.Sprintf("---\ntitle: \"%s\"\ncreated: \"%s\"\ntags: []\n---\n\n%s", title, now, content)

	if err := os.WriteFile(absPath, []byte(fullContent), 0644); err != nil {
		return "", fmt.Errorf("failed to write file: %w", err)
	}

	// Index immediately
	doc, err := vault.ParseFile(absPath)
	if err != nil {
		return fileName, err
	}

	hash := sha256.Sum256([]byte(fullContent))
	doc.SHA256 = hex.EncodeToString(hash[:])

	if err := s.DB.IndexDocument(doc); err != nil {
		return fileName, fmt.Errorf("failed to index new file: %w", err)
	}

	return fileName, nil
}

// NoteMove renames a note and updates the index.
func (s *Service) NoteMove(oldPath, newPath string) error {
	absOld, err := vault.SecurePath(s.VaultRoot, oldPath)
	if err != nil {
		return err
	}
	absNew, err := vault.SecurePath(s.VaultRoot, newPath)
	if err != nil {
		return err
	}

	if err := os.Rename(absOld, absNew); err != nil {
		return fmt.Errorf("failed to move file: %w", err)
	}

	if err := s.DB.DeleteDocument(absOld); err != nil {
		return err
	}

	doc, err := vault.ParseFile(absNew)
	if err != nil {
		return err
	}
	return s.DB.IndexDocument(doc)
}

// PropsEdit updates a frontmatter property in the file and re-indexes.
func (s *Service) PropsEdit(relPath, key, value string) error {
	absPath, err := vault.SecurePath(s.VaultRoot, relPath)
	if err != nil {
		return err
	}
	doc, err := vault.ParseFile(absPath)
	if err != nil {
		return err
	}

	// Update frontmatter
	if doc.Frontmatter == nil {
		doc.Frontmatter = make(map[string]interface{})
	}
	doc.Frontmatter[key] = value

	// Write back to file.
	// For MVP, we reconstruct the frontmatter simply.
	fmBytes, err := yaml.Marshal(doc.Frontmatter)
	if err != nil {
		return err
	}

	newContent := fmt.Sprintf("---\n%s---\n%s", string(fmBytes), doc.Body)
	if err := os.WriteFile(absPath, []byte(newContent), 0644); err != nil {
		return err
	}

	// Re-parse and index
	newDoc, err := vault.ParseFile(absPath)
	if err != nil {
		return err
	}
	return s.DB.IndexDocument(newDoc)
}

// Ask performs a semantic/RAG search and streams the answer using the AI package.
func (s *Service) Ask(query string, out chan<- interface{}) {
	// 1. Search the vault
	results, err := s.Search(query)
	if err != nil {
		out <- map[string]string{"error": err.Error()}
		close(out)
		return
	}

	// 2. Stream AI chunks
	chunkChan := make(chan ai.AskChunk)
	go func() {
		ai.Ask(query, results, chunkChan)
	}()

	for chunk := range chunkChan {
		out <- chunk
	}
	close(out)
}

// Ingest copies a file into the inbox and indexes the new note.
func (s *Service) Ingest(sourcePath string) (map[string]string, error) {
	relPath, err := ingest.IngestFile(s.VaultRoot, sourcePath)
	if err != nil {
		return nil, err
	}

	// Index the new note
	absPath, err := vault.SecurePath(s.VaultRoot, relPath)
	if err != nil {
		return nil, err
	}
	doc, err := vault.ParseFile(absPath)
	if err == nil {
		_ = s.DB.IndexDocument(doc)
	}

	return map[string]string{"path": relPath}, nil
}
