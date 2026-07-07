package vault

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/danieljustus/symaira-desktop/internal/config"
)

var wikilinkRegex = regexp.MustCompile(`\[\[([^\]]+)\]\]`)

// Document represents a parsed Markdown file from the vault
type Document struct {
	Path        string
	SHA256      string
	Title       string
	Created     string
	Tags        []string
	Frontmatter map[string]interface{}
	Body        string
	Links       []string
}

// ResolveVaultRoot determines the actual vault root directory.
// Priority: explicitly passed path (from flag) > config.Vault (which already prioritizes SYMDESK_VAULT)
func ResolveVaultRoot(flagPath string, cfg *config.Config) (string, error) {
	root := flagPath
	if root == "" {
		if cfg != nil && cfg.Vault != "" {
			root = cfg.Vault
		}
	}
	if root == "" {
		return "", fmt.Errorf("vault path not configured (use flag or SYMDESK_VAULT env)")
	}

	absPath, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute vault path: %w", err)
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return "", fmt.Errorf("vault path does not exist: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("vault path is not a directory")
	}

	return absPath, nil
}

// Walk iterates over all Markdown files in the vault, respecting ignore rules.
func Walk(vaultRoot string, fn func(path string) error) error {
	return filepath.WalkDir(vaultRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Ignore hidden directories (e.g., .obsidian, .trash, .git)
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && d.Name() != "." {
				return filepath.SkipDir
			}
			return nil
		}

		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}

		if filepath.Ext(d.Name()) == ".md" {
			return fn(path)
		}

		return nil
	})
}

// ParseFile parses a markdown file, extracting frontmatter, body, and wikilinks.
func ParseFile(path string) (*Document, error) {
	fileBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}

	// Calculate SHA256
	hash := sha256.Sum256(fileBytes)
	shaHex := hex.EncodeToString(hash[:])

	doc := &Document{
		Path:        path,
		SHA256:      shaHex,
		Frontmatter: make(map[string]interface{}),
	}

	// Split Frontmatter and Body
	reader := bufio.NewReader(bytes.NewReader(fileBytes))
	var frontmatterBytes []byte
	var bodyBytes []byte
	inFrontmatter := false
	frontmatterFound := false

	for {
		line, err := reader.ReadBytes('\n')
		if err != nil && err != io.EOF {
			return nil, fmt.Errorf("read lines: %w", err)
		}

		lineStr := string(line)
		trimmed := strings.TrimRight(lineStr, "\r\n")

		if trimmed == "---" {
			if !inFrontmatter && !frontmatterFound && len(frontmatterBytes) == 0 {
				inFrontmatter = true
			} else if inFrontmatter {
				inFrontmatter = false
				frontmatterFound = true
			} else {
				// Just part of the body
				bodyBytes = append(bodyBytes, line...)
			}
		} else {
			if inFrontmatter {
				frontmatterBytes = append(frontmatterBytes, line...)
			} else {
				bodyBytes = append(bodyBytes, line...)
			}
		}

		if err == io.EOF {
			break
		}
	}

	if frontmatterFound && len(frontmatterBytes) > 0 {
		err = yaml.Unmarshal(frontmatterBytes, &doc.Frontmatter)
		if err != nil {
			return nil, fmt.Errorf("invalid frontmatter in %s: %w", path, err)
		}
	}

	// Extract standard fields
	if t, ok := doc.Frontmatter["title"].(string); ok {
		doc.Title = t
	} else {
		// Fallback to filename
		doc.Title = strings.TrimSuffix(filepath.Base(path), ".md")
	}

	if c, ok := doc.Frontmatter["created"].(string); ok {
		doc.Created = c
	}

	if tagsRaw, ok := doc.Frontmatter["tags"]; ok {
		switch tv := tagsRaw.(type) {
		case []interface{}:
			for _, item := range tv {
				if s, ok := item.(string); ok {
					doc.Tags = append(doc.Tags, s)
				}
			}
		case string:
			// single tag string
			doc.Tags = append(doc.Tags, tv)
		}
	}

	doc.Body = string(bodyBytes)
	doc.Links = extractWikilinks(doc.Body)

	return doc, nil
}

// SecurePath resolves a relative path against the vault root and ensures it does not traverse outside.
func SecurePath(vaultRoot, relPath string) (string, error) {
	absVault, err := filepath.Abs(vaultRoot)
	if err != nil {
		return "", err
	}
	targetPath := filepath.Join(absVault, relPath)
	absTarget, err := filepath.Abs(targetPath)
	if err != nil {
		return "", err
	}

	if !strings.HasPrefix(absTarget, absVault+string(filepath.Separator)) && absTarget != absVault {
		return "", fmt.Errorf("path traversal denied: %s is outside vault", relPath)
	}

	return absTarget, nil
}

// extractWikilinks extracts links from markdown body
func extractWikilinks(body string) []string {
	matches := wikilinkRegex.FindAllStringSubmatch(body, -1)
	var links []string
	for _, m := range matches {
		if len(m) > 1 {
			target := m[1]
			// Handle display text: [[Target|Display]]
			parts := strings.SplitN(target, "|", 2)
			links = append(links, strings.TrimSpace(parts[0]))
		}
	}
	return links
}
