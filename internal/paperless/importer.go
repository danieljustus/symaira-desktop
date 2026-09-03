package paperless

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

// ImportOptions configures a Paperless import run.
type ImportOptions struct {
	VaultRoot string
	ExportDir string
	DryRun    bool
	DB        *sidecar.DB // optional: when set, newly created notes are indexed
}

// ImportResult reports the outcome of importing a single document.
type ImportResult struct {
	PaperlessID int    `json:"paperless_id"`
	Title       string `json:"title"`
	Action      string `json:"action"` // "created", "updated", "skipped_idempotent", "error"
	NotePath    string `json:"note_path,omitempty"`
	ASN         int    `json:"asn,omitempty"`
	Error       string `json:"error,omitempty"`
}

// ImportSummary holds aggregate counters from an import run.
type ImportSummary struct {
	Total   int            `json:"total"`
	Created int            `json:"created"`
	Updated int            `json:"updated"`
	Skipped int            `json:"skipped"`
	Errors  int            `json:"errors"`
	Results []ImportResult `json:"results"`
}

// Import reads a Paperless-ngx export directory and creates or updates vault
// notes for every document in the manifest. It is idempotent: re-running the
// import against the same export will update existing notes rather than create
// duplicates, keyed on the source identifier (paperless:<id>:<checksum>).
//
// In dry-run mode no files are written; the function reports what would happen.
func Import(opts ImportOptions) (*ImportSummary, error) {
	manifestPath := filepath.Join(opts.ExportDir, "manifest.json")
	entries, err := ParseManifest(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}

	// Ensure archive directory exists (unless dry-run)
	archiveDir := filepath.Join(opts.VaultRoot, "archive", "paperless")
	if !opts.DryRun {
		//nolint:gosec // archive directory is intentionally user-readable under the selected vault
		if err := os.MkdirAll(archiveDir, 0755); err != nil {
			return nil, fmt.Errorf("create archive dir: %w", err)
		}
	}

	// Scan the vault once for existing ASNs and source identifiers so we can
	// detect collisions without a per-document scan.
	asnReport, err := vault.ScanASNs(opts.VaultRoot)
	if err != nil {
		return nil, fmt.Errorf("scan vault ASNs: %w", err)
	}
	existingSources := make(map[string]string) // source_identifier -> note path
	_ = vault.Walk(opts.VaultRoot, func(p string) error {
		doc, parseErr := vault.ParseFile(p)
		if parseErr != nil {
			return nil
		}
		if si, ok := doc.Frontmatter["source_identifier"]; ok {
			if s, ok2 := si.(string); ok2 {
				existingSources[s] = p
			}
		}
		return nil
	})

	now := time.Now().UTC()
	summary := &ImportSummary{Total: len(entries)}

	for i, entry := range entries {
		_ = i // for future use
		result := ImportResult{
			PaperlessID: entry.ID,
			Title:       entry.Title,
		}

		sourceID := sourceIdentifier(entry)
		noteFileName := noteFileName(entry)
		noteRelPath := filepath.Join("paperless", noteFileName)
		noteAbsPath := filepath.Join(opts.VaultRoot, noteRelPath)

		// Idempotency check: is there already a note for this source?
		existingPath, exists := existingSources[sourceID]

		// Copy the document file into the archive
		archiveFileName := archiveFileName(entry)
		archiveRelPath := filepath.Join("archive", "paperless", archiveFileName)
		archiveAbsPath := filepath.Join(opts.VaultRoot, archiveRelPath)

		srcFilePath := filepath.Join(opts.ExportDir, archiveFileName)

		// Handle ASN collision detection
		if entry.ArchiveSerialNumber != nil && *entry.ArchiveSerialNumber > 0 {
			asn := *entry.ArchiveSerialNumber
			existingPaths := asnReport.AssignedPaths(asn)
			collision := false
			for _, ep := range existingPaths {
				if ep == noteRelPath {
					continue
				}
				collision = true
				break
			}
			if collision {
				result.Action = "error"
				result.Error = fmt.Sprintf("ASN %d is already assigned to another note", asn)
				summary.Results = append(summary.Results, result)
				summary.Errors++
				continue
			}
			result.ASN = asn
		}

		// Determine action
		if exists {
			result.Action = "updated"
			result.NotePath = existingPath
			noteAbsPath = existingPath
		} else {
			result.Action = "created"
			result.NotePath = noteRelPath
			// Ensure parent dir exists
			if !opts.DryRun {
				//nolint:gosec // note directory is intentionally user-readable under the selected vault
				if err := os.MkdirAll(filepath.Dir(noteAbsPath), 0755); err != nil {
					result.Action = "error"
					result.Error = fmt.Sprintf("create note dir: %v", err)
					summary.Results = append(summary.Results, result)
					summary.Errors++
					continue
				}
			}
		}

		// In dry-run mode, report and skip
		if opts.DryRun {
			summary.Results = append(summary.Results, result)
			switch result.Action {
			case "created":
				summary.Created++
			case "updated":
				summary.Updated++
			}
			continue
		}

		// Copy the document file
		if err := copyFile(srcFilePath, archiveAbsPath); err != nil {
			if !os.IsNotExist(err) {
				result.Action = "error"
				result.Error = fmt.Sprintf("copy archive file: %v", err)
				summary.Results = append(summary.Results, result)
				summary.Errors++
				continue
			}
			// File doesn't exist in export — not fatal
		}

		// Build the note content
		noteContent := buildNote(entry, archiveRelPath, sourceID, now)

		// Write the note
		//nolint:gosec // imported vault notes intentionally remain user-readable
		if err := os.WriteFile(noteAbsPath, []byte(noteContent), 0644); err != nil {
			result.Action = "error"
			result.Error = fmt.Sprintf("write note: %v", err)
			summary.Results = append(summary.Results, result)
			summary.Errors++
			continue
		}

		// Index if DB is available
		if opts.DB != nil {
			doc, parseErr := vault.ParseFile(noteAbsPath)
			if parseErr == nil {
				_ = opts.DB.IndexDocument(doc)
			}
		}

		// Track the new source identifier
		existingSources[sourceID] = noteAbsPath

		summary.Results = append(summary.Results, result)
		switch result.Action {
		case "created":
			summary.Created++
		case "updated":
			summary.Updated++
		}
	}

	return summary, nil
}

// sourceIdentifier returns a stable identifier for idempotency.
func sourceIdentifier(entry ManifestEntry) string {
	checksum := ""
	if entry.Checksum != nil {
		checksum = *entry.Checksum
	}
	return fmt.Sprintf("paperless:%d:%s", entry.ID, checksum)
}

// noteFileName returns the safe file name for a note derived from a Paperless document.
func noteFileName(entry ManifestEntry) string {
	// Paperless_<id>_<sanitized_title>.md
	safeTitle := sanitizeFileName(entry.Title)
	if safeTitle == "" {
		safeTitle = fmt.Sprintf("doc_%d", entry.ID)
	}
	return fmt.Sprintf("Paperless_%d_%s.md", entry.ID, safeTitle)
}

// archiveFileName returns the original or archived file name for the document.
func archiveFileName(entry ManifestEntry) string {
	if entry.ArchivedFileName != nil && *entry.ArchivedFileName != "" {
		return *entry.ArchivedFileName
	}
	if entry.OriginalFileName != nil && *entry.OriginalFileName != "" {
		return *entry.OriginalFileName
	}
	return fmt.Sprintf("paperless_%d", entry.ID)
}

// sanitizeFileName removes characters unsafe for file names.
func sanitizeFileName(s string) string {
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
		"\n", "_",
		"\r", "_",
		"\t", "_",
	)
	s = replacer.Replace(s)
	// Trim trailing dots and spaces
	s = strings.TrimRight(s, ". ")
	// Collapse runs of underscores
	for strings.Contains(s, "__") {
		s = strings.ReplaceAll(s, "__", "_")
	}
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

// copyFile copies a file from src to dst.
func copyFile(src, dst string) error {
	s, err := os.Open(src) //nolint:gosec // source is under the caller-selected Paperless export directory
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	d, err := os.Create(dst) //nolint:gosec // destination is under the caller-selected vault archive directory
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()

	if _, err := io.Copy(d, s); err != nil {
		return err
	}
	return d.Sync()
}

// buildNote constructs the full Markdown content for a document note.
func buildNote(entry ManifestEntry, archiveRelPath, sourceID string, now time.Time) string {
	var sb strings.Builder

	// Frontmatter
	sb.WriteString("---\n")
	_, _ = fmt.Fprintf(&sb, "title: %q\n", entry.Title)
	_, _ = fmt.Fprintf(&sb, "created: %q\n", now.Format(time.RFC3339))

	// Tags
	if len(entry.Tags) > 0 {
		sb.WriteString("tags:\n")
		for _, t := range entry.Tags {
			_, _ = fmt.Fprintf(&sb, "  - %q\n", t)
		}
	} else {
		sb.WriteString("tags: []\n")
	}

	// Status: imported paperless documents start as "open"
	sb.WriteString("status: \"open\"\n")

	// Source traceability
	_, _ = fmt.Fprintf(&sb, "source_identifier: %q\n", sourceID)
	sb.WriteString("imported_from: \"paperless\"\n")

	// Correspondent
	if entry.Correspondent != nil && *entry.Correspondent != "" {
		_, _ = fmt.Fprintf(&sb, "correspondent: %q\n", *entry.Correspondent)
	}

	// Document type
	if entry.DocumentType != nil && *entry.DocumentType != "" {
		_, _ = fmt.Fprintf(&sb, "document_type: %q\n", *entry.DocumentType)
	}

	// Document date (use created date from Paperless)
	if entry.Created != nil && *entry.Created != "" {
		docDate := extractDate(*entry.Created)
		if docDate != "" {
			_, _ = fmt.Fprintf(&sb, "document_date: %q\n", docDate)
		}
	}

	// ASN
	if entry.ArchiveSerialNumber != nil && *entry.ArchiveSerialNumber > 0 {
		_, _ = fmt.Fprintf(&sb, "asn: %d\n", *entry.ArchiveSerialNumber)
	}

	// Archive path
	_, _ = fmt.Fprintf(&sb, "archive_path: %q\n", archiveRelPath)

	// Paperless metadata
	sb.WriteString("paperless:\n")
	_, _ = fmt.Fprintf(&sb, "  id: %d\n", entry.ID)
	if entry.Checksum != nil {
		_, _ = fmt.Fprintf(&sb, "  checksum: %q\n", *entry.Checksum)
	}
	if entry.Added != nil {
		_, _ = fmt.Fprintf(&sb, "  added: %q\n", *entry.Added)
	}
	if entry.Modified != nil {
		_, _ = fmt.Fprintf(&sb, "  modified: %q\n", *entry.Modified)
	}
	if entry.OriginalFileName != nil {
		_, _ = fmt.Fprintf(&sb, "  original_file_name: %q\n", *entry.OriginalFileName)
	}

	// Confidence: paperless source of truth = 100
	sb.WriteString("confidence: 100\n")

	// MIME type (inferred from file extension)
	if entry.ArchivedFileName != nil {
		ext := strings.ToLower(filepath.Ext(*entry.ArchivedFileName))
		mime := mimeFromExt(ext)
		if mime != "" {
			_, _ = fmt.Fprintf(&sb, "mime: %q\n", mime)
		}
	}

	sb.WriteString("---\n\n")

	// Body: OCR content
	if entry.Content != "" {
		sb.WriteString(entry.Content)
		sb.WriteString("\n\n")
	}

	// Reference the archived file
	_, _ = fmt.Fprintf(&sb, "[[%s]]\n", archiveRelPath)

	// Notes from Paperless
	if len(entry.Notes) > 0 {
		sb.WriteString("\n## Paperless Notes\n\n")
		for _, note := range entry.Notes {
			_, _ = fmt.Fprintf(&sb, "- %s\n", note.Note)
			if note.Created != nil {
				_, _ = fmt.Fprintf(&sb, "  (added: %s)\n", *note.Created)
			}
		}
	}

	return sb.String()
}

// extractDate extracts the date portion (YYYY-MM-DD) from an ISO-8601 timestamp.
func extractDate(ts string) string {
	if len(ts) >= 10 {
		return ts[:10]
	}
	return ""
}

// mimeFromExt returns a MIME type for common file extensions.
func mimeFromExt(ext string) string {
	switch ext {
	case ".pdf":
		return "application/pdf"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".tiff", ".tif":
		return "image/tiff"
	case ".gif":
		return "image/gif"
	case ".txt":
		return "text/plain"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".odt":
		return "application/vnd.oasis.opendocument.text"
	default:
		return ""
	}
}
