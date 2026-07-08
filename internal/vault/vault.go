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

	// Contract v2: first-class document metadata (all optional)
	DocumentDate string // ISO-8601 date the document refers to
	Person       string // household member
	Status       string // enum: open|paid|submitted|done|needs_review|waiting_for_reply
	DueDate      string // ISO-8601 date deadline
	Confidence   int    // 0-100 classification confidence
	OcrJSONPath  string // path to plain-text OCR JSON
	Simhash      string // 64-bit SimHash hex
}

// ValidStatuses enumerates the allowed values for Document.Status.
var ValidStatuses = map[string]bool{
	"open":              true,
	"paid":              true,
	"submitted":         true,
	"done":              true,
	"needs_review":      true,
	"waiting_for_reply": true,
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

	// Contract v2: extract document metadata from frontmatter (all optional, backwards-compatible)
	doc.DocumentDate = getStringFrontmatter(doc.Frontmatter, "document_date")
	doc.Person = getStringFrontmatter(doc.Frontmatter, "person")
	doc.Status = getStringFrontmatter(doc.Frontmatter, "status")
	doc.DueDate = getStringFrontmatter(doc.Frontmatter, "due_date")
	doc.Confidence = getIntFrontmatter(doc.Frontmatter, "confidence")
	doc.OcrJSONPath = getStringFrontmatter(doc.Frontmatter, "ocr_json_path")
	doc.Simhash = getStringFrontmatter(doc.Frontmatter, "simhash")

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

func getStringFrontmatter(fm map[string]interface{}, key string) string {
	if v, ok := fm[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func getIntFrontmatter(fm map[string]interface{}, key string) int {
	if v, ok := fm[key]; ok {
		switch n := v.(type) {
		case int:
			return n
		case int64:
			return int(n)
		case float64:
			return int(n)
		}
	}
	return 0
}

// SetFrontmatterKey writes a single YAML key=value line inside the frontmatter
// block of a markdown file, preserving every other byte exactly.  Only bare
// scalar values (strings, numbers) are supported.
func SetFrontmatterKey(filePath, key, value string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}

	lines := strings.Split(string(data), "\n")

	fmStart := -1
	fmEnd := -1
	for i, line := range lines {
		trimmed := strings.TrimRight(line, "\r")
		if trimmed == "---" {
			if fmStart == -1 {
				fmStart = i
			} else {
				fmEnd = i
				break
			}
		}
	}
	if fmStart == -1 || fmEnd == -1 {
		newLines := make([]string, 0, len(lines)+3)
		newLines = append(newLines, "---")
		newLines = append(newLines, key+": "+quoteYAML(value))
		newLines = append(newLines, "---")
		newLines = append(newLines, lines...)
		return os.WriteFile(filePath, []byte(strings.Join(newLines, "\n")), 0644)
	}

	keyPrefix := key + ": "
	replaced := false
	for i := fmStart + 1; i < fmEnd; i++ {
		trimmed := strings.TrimRight(lines[i], "\r")
		if strings.HasPrefix(trimmed, keyPrefix) || trimmed == key {
			lines[i] = keyPrefix + quoteYAML(value)
			replaced = true
			break
		}
	}

	if !replaced {
		newLine := keyPrefix + quoteYAML(value)
		lines = append(lines[:fmEnd], append([]string{newLine}, lines[fmEnd:]...)...)
	}

	return os.WriteFile(filePath, []byte(strings.Join(lines, "\n")), 0644)
}

// quoteYAML returns a YAML-safe quoted string; numeric-looking values are
// left unquoted.
func quoteYAML(v string) string {
	if v == "" {
		return `""`
	}
	isNumber := true
	for i, c := range v {
		if c >= '0' && c <= '9' {
			continue
		}
		if c == '.' {
			continue
		}
		if c == '-' && i == 0 {
			continue
		}
		isNumber = false
		break
	}
	if isNumber && len(v) > 0 {
		return v
	}
	escaped := strings.ReplaceAll(v, `"`, `\"`)
	return `"` + escaped + `"`
}
