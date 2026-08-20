package notionimport

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// NotionIDRe extracts a Notion page/database ID from a filename like
// "Page Name <abc123...>.md" or "Page Name abc123....md".
var NotionIDRe = regexp.MustCompile(`\s*<?([0-9a-f]{32})>?(?:\.md)?\s*$`)

// ExportEntry represents one item discovered in a Notion export.
type ExportEntry struct {
	// SourcePath is the absolute path to the source file.
	SourcePath string
	// DisplayName is the human-readable name (ID suffix stripped).
	DisplayName string
	// NotionID is the 32-char hex Notion page/database ID, if present.
	NotionID string
	// Kind classifies the entry.
	Kind EntryKind
	// MIME is the detected MIME type.
	MIME string
	// Category is the frontmatter category for the note.
	Category string
	// AssetsRoot is the top-level assets directory of the export (if any).
	AssetsRoot string
	// RelPath is the path of the source file relative to the resolved export
	// root (the workspace directory). It preserves the folder hierarchy and is
	// used to place the note at the same relative location inside the vault.
	RelPath string
}

// EntryKind classifies a discovered export entry.
type EntryKind string

const (
	EntryPage     EntryKind = "page"
	EntryDatabase EntryKind = "database"
	EntryCSV      EntryKind = "csv"
	EntryAsset    EntryKind = "asset"
)

// resolveExportDir finds the actual Notion export directory. Notion exports
// are often nested one level (e.g. "My Workspace/"), so we walk down if the
// root contains exactly one subdirectory and no markdown files.
func resolveExportDir(dir string) (string, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("stat export dir: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("export path is not a directory: %s", dir)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read export dir: %w", err)
	}

	var subdirs []os.DirEntry
	hasMarkdown := false
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			subdirs = append(subdirs, e)
		} else if strings.EqualFold(filepath.Ext(e.Name()), ".md") {
			hasMarkdown = true
		}
	}

	if len(subdirs) == 1 && !hasMarkdown {
		return filepath.Join(dir, subdirs[0].Name()), nil
	}
	return dir, nil
}

// discoverExport walks the export directory and returns all entries.
func discoverExport(dir string) ([]ExportEntry, error) {
	exportRoot, err := resolveExportDir(dir)
	if err != nil {
		return nil, err
	}

	var entries []ExportEntry

	err = filepath.WalkDir(exportRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Skip the root itself.
		if path == exportRoot {
			return nil
		}
		rel, _ := filepath.Rel(exportRoot, path)

		// Skip hidden directories and the assets directory at workspace level.
		name := d.Name()
		if d.IsDir() && (strings.HasPrefix(name, ".") || name == "assets") {
			if name == "assets" && filepath.Dir(rel) == "." {
				// Workspace assets dir — skip walking into it.
				return filepath.SkipDir
			}
			if strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}

		if d.IsDir() {
			return nil
		}

		lower := strings.ToLower(name)
		ext := filepath.Ext(lower)

		switch {
		case ext == ".md":
			entry := parseMarkdownEntry(path, rel, exportRoot)
			entries = append(entries, entry)
		case ext == ".csv":
			entry := parseCSVEntry(path, rel, exportRoot)
			entries = append(entries, entry)
		case ext == ".png" || ext == ".jpg" || ext == ".jpeg" || ext == ".gif" || ext == ".svg" || ext == ".webp" || ext == ".pdf" || ext == ".mp3" || ext == ".mp4":
			// Assets are handled separately during page processing.
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk export: %w", err)
	}

	return entries, nil
}

// parseMarkdownEntry creates an ExportEntry for a markdown file.
func parseMarkdownEntry(absPath, rel string, exportRoot string) ExportEntry {
	base := strings.TrimSuffix(filepath.Base(absPath), filepath.Ext(absPath))

	// Extract Notion ID from filename.
	notionID := ""
	displayName := base
	if m := NotionIDRe.FindStringSubmatch(base); m != nil {
		notionID = m[1]
		displayName = strings.TrimSuffix(base, m[0])
	}

	// Determine category from parent directory.
	category := ""
	parent := filepath.Base(filepath.Dir(absPath))
	if parent != "." && parent != filepath.Base(exportRoot) {
		category = parent
	}

	// Find the top-level assets directory for this export.
	assetsRoot := ""
	candidate := filepath.Join(exportRoot, "assets")
	if info, err := os.Stat(candidate); err == nil && info.IsDir() {
		assetsRoot = candidate
	}

	return ExportEntry{
		SourcePath:  absPath,
		DisplayName: displayName,
		NotionID:    notionID,
		Kind:        EntryPage,
		MIME:        "text/markdown",
		Category:    category,
		AssetsRoot:  assetsRoot,
		RelPath:     rel,
	}
}

// parseCSVEntry creates an ExportEntry for a CSV database file.
func parseCSVEntry(absPath, rel string, _ string) ExportEntry {
	base := strings.TrimSuffix(filepath.Base(absPath), filepath.Ext(absPath))
	displayName := base
	notionID := ""
	if m := NotionIDRe.FindStringSubmatch(base); m != nil {
		notionID = m[1]
		displayName = strings.TrimSuffix(base, m[0])
	}

	return ExportEntry{
		SourcePath:  absPath,
		DisplayName: displayName,
		NotionID:    notionID,
		Kind:        EntryCSV,
		MIME:        "text/csv",
		Category:    "database",
		RelPath:     rel,
	}
}

// buildPageNameMap creates a mapping from Notion IDs to display names
// for all discovered pages, used for internal link rewriting.
func buildPageNameMap(entries []ExportEntry) map[string]string {
	m := make(map[string]string)
	for _, e := range entries {
		if e.NotionID != "" {
			m[e.NotionID] = e.DisplayName
		}
	}
	return m
}

// noteMeta is a minimal frontmatter structure for idempotency checks.
type noteMeta struct {
	SourcePath  string `yaml:"source_path"`
	ImportRunID string `yaml:"import_run_id,omitempty"`
}

// parseNoteFrontmatter extracts YAML frontmatter from a vault note.
func parseNoteFrontmatter(data []byte) *noteMeta {
	s := string(data)
	const open = "---\n"
	if !strings.HasPrefix(s, open) {
		return nil
	}
	rest := s[len(open):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return nil
	}
	var meta noteMeta
	if err := yaml.Unmarshal([]byte(rest[:end]), &meta); err != nil {
		return nil
	}
	return &meta
}

// sanitizeBaseName removes characters unsafe for filenames.
func sanitizeBaseName(name string) string {
	// Replace path separators and other unsafe chars.
	replacer := strings.NewReplacer(
		"/", "_",
		"\\", "_",
		":", "_",
		"*", "_",
		"?", "_",
		"\"", "_",
		"<", "_",
		">", "_",
		"|", "_",
	)
	name = replacer.Replace(name)
	name = strings.TrimSpace(name)
	if name == "" {
		name = "untitled"
	}
	return name
}
