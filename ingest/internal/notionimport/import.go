// Package notionimport converts a Notion Markdown + CSV export into
// contract-compliant vault Markdown notes.
package notionimport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/danieljustus/symaira-corekit/fsutil"
	"github.com/danieljustus/symaira-ingest/internal/writer"
)

// Options configures a Notion import run.
type Options struct {
	// SourceDir is the root of the Notion export directory.
	SourceDir string
	// Vault is the target vault directory.
	Vault string
	// DryRun when true lists what would be imported without writing.
	DryRun bool
	// ImportRunID when set is used for idempotency; re-running with the
	// same ID skips notes already carrying that run ID.
	ImportRunID string
	// Progress receives operator progress output. Nil means stderr.
	Progress io.Writer
}

// PageResult records the outcome of processing a single Notion page or CSV row.
type PageResult struct {
	SourcePath string `json:"source_path"`
	VaultPath  string `json:"vault_path,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
	Status     string `json:"status"` // imported | skipped | failed | would-import
	Reason     string `json:"reason,omitempty"`
	Error      string `json:"error,omitempty"`
}

// Stats holds the aggregate outcome of a Notion import run.
type Stats struct {
	Imported int
	Skipped  int
	Failed   int
	Total    int
	Warnings []string

	RunID      string
	Source     string
	Mode       string
	StartedAt  time.Time
	FinishedAt time.Time

	Results []PageResult
}

func newRunID(t time.Time) string {
	return "notion-" + t.Format("20060102T150405.000000000Z")
}

func progressWriter(opts Options) io.Writer {
	if opts.Progress != nil {
		return opts.Progress
	}
	return os.Stderr
}

// Run executes a Notion export import.
func Run(ctx context.Context, opts Options) (*Stats, error) {
	started := time.Now().UTC()
	runID := opts.ImportRunID
	if runID == "" {
		runID = newRunID(started)
	}

	mode := "import"
	if opts.DryRun {
		mode = "dry-run"
	}

	stats := &Stats{
		RunID:     runID,
		Source:    "notion",
		Mode:      mode,
		StartedAt: started,
	}
	defer func() { stats.FinishedAt = time.Now().UTC() }()

	exportDir, err := resolveExportDir(opts.SourceDir)
	if err != nil {
		return stats, err
	}

	// Discover all export entries.
	entries, err := discoverExport(exportDir)
	if err != nil {
		return stats, fmt.Errorf("discover export: %w", err)
	}
	stats.Total = len(entries)
	progress := progressWriter(opts)

	// Build a map from Notion page IDs to display names for link rewriting.
	pageNameMap := buildPageNameMap(entries)

	for i, entry := range entries {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		fmt.Fprintf(progress, "[%d/%d] %s\n", i+1, stats.Total, entry.DisplayName)

		results := processEntry(ctx, entry, opts, runID, pageNameMap)
		stats.Results = append(stats.Results, results...)

		for _, r := range results {
			switch r.Status {
			case "imported", "would-import":
				stats.Imported++
			case "skipped":
				stats.Skipped++
			case "failed":
				stats.Failed++
				if r.Error != "" {
					stats.Warnings = append(stats.Warnings, fmt.Sprintf("%s: %s", entry.SourcePath, r.Error))
				}
			}
		}
	}

	// Total reflects the number of produced notes (pages + CSV rows + indices).
	stats.Total = len(stats.Results)

	return stats, nil
}

// processEntry handles one discovered export entry (page, CSV, or asset).
func processEntry(_ context.Context, entry ExportEntry, opts Options, runID string, pageNameMap map[string]string) []PageResult {
	if entry.Kind == EntryCSV {
		return convertCSV(entry, opts.Vault, runID, opts.DryRun)
	}

	single := func(r PageResult) []PageResult { return []PageResult{r} }

	result := PageResult{
		SourcePath: entry.SourcePath,
		Status:     "failed",
	}

	if opts.DryRun {
		result.Status = "would-import"
		return single(result)
	}

	// Read the source file.
	data, err := os.ReadFile(entry.SourcePath)
	if err != nil {
		result.Error = fmt.Sprintf("read source: %v", err)
		return single(result)
	}

	hash := sha256Hex(data)

	// Check idempotency: if a note with this run ID and source path already exists, skip.
	if opts.Vault != "" && runID != "" {
		if existing := findExistingNote(opts.Vault, entry.SourcePath, runID); existing != "" {
			result.Status = "skipped"
			result.Reason = "already imported in a previous run"
			result.VaultPath = existing
			result.SHA256 = hash
			return single(result)
		}
	}

	// Copy assets if present.
	text := string(data)
	if entry.Kind == EntryPage && entry.AssetsRoot != "" {
		assetDir := filepath.Join(opts.Vault, "assets")
		text, err = copyAndRewriteAssets(entry.AssetsRoot, assetDir, text)
		if err != nil {
			log.Printf("  warning: asset copy failed: %v", err)
		}
	}

	// Rewrite internal links.
	text = rewriteNotionLinks(text, pageNameMap, entry.NotionID)

	// Generate frontmatter.
	vaultPath := computeVaultPath(opts.Vault, entry)
	note := writer.Note{
		SourcePath:   entry.SourcePath,
		ImportedFrom: "notion",
		ImportRunID:  runID,
		IngestedAt:   time.Now().UTC(),
		SHA256:       hash,
		MIME:         entry.MIME,
		Tags:         []string{"notion"},
		Category:     entry.Category,
	}

	if err := writeNoteWithFrontmatter(vaultPath, note, text); err != nil {
		result.Error = fmt.Sprintf("write note: %v", err)
		return single(result)
	}

	result.Status = "imported"
	result.VaultPath = vaultPath
	result.SHA256 = hash
	return single(result)
}

// findExistingNote checks if a note for this source path and run ID already exists.
func findExistingNote(vault, sourcePath, runID string) string {
	var found string
	_ = filepath.WalkDir(vault, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		note := parseNoteFrontmatter(data)
		if note != nil && note.ImportRunID == runID && note.SourcePath == sourcePath {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

func noteExistsWithRunID(vaultPath, runID string) bool {
	data, err := os.ReadFile(vaultPath)
	if err != nil {
		return false
	}
	note := parseNoteFrontmatter(data)
	return note != nil && note.ImportRunID == runID
}

// computeVaultPath determines where a note should be written in the vault,
// preserving the relative folder structure of the export entry.
func computeVaultPath(vault string, entry ExportEntry) string {
	if vault == "" {
		return ""
	}
	base := sanitizeBaseName(entry.DisplayName)
	// RelPath is relative to the resolved export root (workspace dir). Keep
	// any parent directories, but drop the original filename so we can use the
	// sanitized display name as the note file name.
	subdir := filepath.Dir(entry.RelPath)
	if subdir == "." || subdir == "/" {
		return filepath.Join(vault, base+".md")
	}
	// Sanitize each path segment.
	segments := strings.Split(subdir, string(filepath.Separator))
	clean := make([]string, 0, len(segments))
	for _, seg := range segments {
		if s := sanitizeBaseName(seg); s != "" && s != "." && s != ".." {
			clean = append(clean, s)
		}
	}
	return filepath.Join(append(append([]string{vault}, clean...), base+".md")...)
}

// writeNoteWithFrontmatter writes a complete vault note with YAML frontmatter.
func writeNoteWithFrontmatter(vaultPath string, note any, body string) error {
	if err := os.MkdirAll(filepath.Dir(vaultPath), 0o700); err != nil {
		return fmt.Errorf("create vault directory: %w", err)
	}

	data, err := marshalYAML(note)
	if err != nil {
		return err
	}

	var sb strings.Builder
	sb.WriteString("---\n")
	sb.Write(data)
	sb.WriteString("---\n\n")
	sb.WriteString(body)
	if !strings.HasSuffix(body, "\n") {
		sb.WriteByte('\n')
	}

	return fsutil.AtomicWriteFile(vaultPath, []byte(sb.String()), 0o600)
}

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
