// Package writer writes Markdown sidecar files atomically.
package writer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/danieljustus/symaira-corekit/fsutil"
	"gopkg.in/yaml.v3"
)

type Note struct {
	SourcePath      string         `yaml:"source_path"`
	ImportedFrom    string         `yaml:"imported_from,omitempty"`
	ImportRunID     string         `yaml:"import_run_id,omitempty"`
	SourceURI       string         `yaml:"source_uri,omitempty"`
	DownloadURI     string         `yaml:"download_uri,omitempty"`
	IngestedAt      time.Time      `yaml:"ingested_at"`
	SHA256          string         `yaml:"sha256"`
	MIME            string         `yaml:"mime"`
	Tags            []string       `yaml:"tags"`
	Category        string         `yaml:"category"`
	Correspondent   string         `yaml:"correspondent,omitempty"`
	DocumentType    string         `yaml:"document_type,omitempty"`
	OCREngine       string         `yaml:"ocr_engine,omitempty"`
	ArchivePath     string         `yaml:"archive_path"`
	SidecarPath     string         `yaml:"sidecar_path,omitempty"`
	ExtractionCount int            `yaml:"extraction_count,omitempty"`
	Paperless       *PaperlessMeta `yaml:"paperless,omitempty"`
}

// PaperlessMeta carries traceability metadata from a migrated Paperless-ngx
// document so a generated note can be traced back to the original record and
// migration completeness can be audited. Fields are omitted individually
// when Paperless did not provide them.
type PaperlessMeta struct {
	DocumentID       int       `yaml:"document_id"`
	Title            string    `yaml:"title,omitempty"`
	Created          time.Time `yaml:"created,omitempty"`
	Added            time.Time `yaml:"added,omitempty"`
	Modified         time.Time `yaml:"modified,omitempty"`
	StoragePath      string    `yaml:"storage_path,omitempty"`
	OriginalFileName string    `yaml:"original_file_name,omitempty"`
	ArchivedFileName string    `yaml:"archived_file_name,omitempty"`
	PageCount        int       `yaml:"page_count,omitempty"`
	URL              string    `yaml:"url,omitempty"`
}

// NoteWriter writes deduplicated Markdown sidecars into a vault.
type NoteWriter struct {
	Vault string
	mu    sync.Mutex
}

// NoteLayout optionally overrides where a note is written within the vault.
// When nil, the note lands flat in the vault root, named after the source
// file. When set, the note is placed under Subdir (a relative directory) with
// BaseName as its file name; both are sanitized before use, and filename
// collisions within the same directory are resolved deterministically.
type NoteLayout struct {
	Subdir   string // relative subdirectory of the vault (e.g. from a storage path)
	BaseName string // note file name without extension
}

// SidecarPath returns the Markdown path for a source file in the vault.
func SidecarPath(vault, source string) string {
	return filepath.Join(vault, filepath.Base(source)+".md")
}

// SanitizeStoragePath converts a Paperless storage path into a safe relative
// vault subdirectory. Path separators (`/` or `\`) become nested directories;
// each segment is stripped of characters unsafe on common filesystems, and
// traversal or empty segments (".", "..", "") are dropped, so the result can
// never escape the vault. An unusable input yields "" (a flat placement).
func SanitizeStoragePath(storagePath string) string {
	segments := strings.FieldsFunc(storagePath, func(r rune) bool {
		return r == '/' || r == '\\'
	})
	clean := make([]string, 0, len(segments))
	for _, seg := range segments {
		s := sanitizeSegment(seg)
		if s == "" || s == "." || s == ".." {
			continue
		}
		clean = append(clean, s)
	}
	return filepath.Join(clean...)
}

// sanitizeSegment strips a single path segment of characters that are unsafe
// or reserved on common filesystems, and of leading/trailing dots and spaces.
func sanitizeSegment(seg string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(seg) {
		if r < 0x20 || strings.ContainsRune(`<>:"|?*/\`, r) {
			b.WriteRune('_')
			continue
		}
		b.WriteRune(r)
	}
	return strings.Trim(strings.TrimSpace(b.String()), ".")
}

// resolveNotePath computes the on-disk note path for a source, honoring an
// optional layout. Without a layout the note is a flat sidecar in the vault
// root. With a layout it is placed under the sanitized subdirectory using the
// sanitized base name; if that file already exists (a different document with
// the same name in the same directory) a numeric suffix is appended
// deterministically until a free name is found.
func (w *NoteWriter) resolveNotePath(sourcePath string, layout *NoteLayout) string {
	if layout == nil {
		return SidecarPath(w.Vault, sourcePath)
	}
	dir := filepath.Join(w.Vault, layout.Subdir)
	base := sanitizeSegment(layout.BaseName)
	if base == "" {
		base = strings.TrimSuffix(filepath.Base(sourcePath), filepath.Ext(sourcePath))
	}
	candidate := filepath.Join(dir, base+".md")
	for i := 2; fileExists(candidate); i++ {
		candidate = filepath.Join(dir, fmt.Sprintf("%s-%d.md", base, i))
	}
	return candidate
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// WriteNote writes a Markdown note with YAML frontmatter atomically.
// It returns the vault path and any error. A write failure must not leave
// a partially written file behind.
func (w *NoteWriter) WriteNote(sourcePath, sha256, mime, ocrEngine, text, archivePath string, ingestedAt time.Time, category string, tags []string, correspondent, documentType, importedFrom, importRunID, sourceURI, downloadURI string, paperless *PaperlessMeta, layout *NoteLayout) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	vaultPath := w.resolveNotePath(sourcePath, layout)
	if err := os.MkdirAll(filepath.Dir(vaultPath), 0o700); err != nil {
		return "", fmt.Errorf("create vault directory: %w", err)
	}

	meta := Note{
		SourcePath:    sourcePath,
		ImportedFrom:  importedFrom,
		ImportRunID:   importRunID,
		SourceURI:     sourceURI,
		DownloadURI:   downloadURI,
		IngestedAt:    ingestedAt,
		SHA256:        sha256,
		MIME:          mime,
		Tags:          tags,
		Category:      category,
		Correspondent: correspondent,
		DocumentType:  documentType,
		OCREngine:     ocrEngine,
		ArchivePath:   archivePath,
		Paperless:     paperless,
	}
	if meta.Tags == nil {
		meta.Tags = []string{}
	}

	yamlBytes, err := yaml.Marshal(meta)
	if err != nil {
		return "", fmt.Errorf("marshal frontmatter: %w", err)
	}

	var sb strings.Builder
	sb.WriteString("---\n")
	sb.Write(yamlBytes)
	sb.WriteString("---\n\n")
	sb.WriteString(text)
	if !strings.HasSuffix(text, "\n") {
		sb.WriteByte('\n')
	}
	if archivePath != "" {
		sb.WriteString("\n---\n")
		sb.WriteString(fmt.Sprintf("[Archived Original](file://%s)\n", filepath.ToSlash(archivePath)))
	}

	if err := fsutil.AtomicWriteFile(vaultPath, []byte(sb.String()), 0o600); err != nil {
		return "", fmt.Errorf("write note: %w", err)
	}
	return vaultPath, nil
}

// UpdateNote replaces the machine-owned frontmatter and Markdown body of an
// existing note while preserving any frontmatter fields owned by the user.
// The note path is kept stable so reprocessing does not create a second note.
func (w *NoteWriter) UpdateNote(vaultPath string, meta Note, text string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	data, err := os.ReadFile(vaultPath)
	if err != nil {
		return fmt.Errorf("read note: %w", err)
	}
	fields, _, err := parseFrontmatter(string(data))
	if err != nil {
		return err
	}
	updateMachineFields(fields, meta)

	body := renderBody(text, meta.ArchivePath)
	if err := writeFrontmatterAndBody(vaultPath, fields, body); err != nil {
		return fmt.Errorf("rewrite note: %w", err)
	}
	return nil
}

// UpdateNoteSidecar rewrites a note's YAML frontmatter to include sidecar
// path and extraction count without touching the Markdown body.
func (w *NoteWriter) UpdateNoteSidecar(vaultPath, sidecarPath string, extractionCount int) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	data, err := os.ReadFile(vaultPath)
	if err != nil {
		return fmt.Errorf("read note: %w", err)
	}

	fields, body, err := parseFrontmatter(string(data))
	if err != nil {
		return err
	}
	fields["sidecar_path"] = sidecarPath
	fields["extraction_count"] = extractionCount

	if err := writeFrontmatterAndBody(vaultPath, fields, body); err != nil {
		return fmt.Errorf("rewrite note: %w", err)
	}
	return nil
}

func parseFrontmatter(content string) (map[string]interface{}, string, error) {
	if !strings.HasPrefix(content, "---\n") {
		return nil, "", fmt.Errorf("note missing YAML frontmatter")
	}
	endIdx := strings.Index(content[4:], "\n---\n")
	if endIdx < 0 {
		return nil, "", fmt.Errorf("note missing closing frontmatter delimiter")
	}
	endIdx += 4
	fields := make(map[string]interface{})
	if err := yaml.Unmarshal([]byte(content[4:endIdx]), &fields); err != nil {
		return nil, "", fmt.Errorf("parse frontmatter: %w", err)
	}
	return fields, content[endIdx+5:], nil
}

func updateMachineFields(fields map[string]interface{}, meta Note) {
	fields["source_path"] = meta.SourcePath
	fields["ingested_at"] = meta.IngestedAt
	fields["sha256"] = meta.SHA256
	fields["mime"] = meta.MIME
	if meta.Tags == nil {
		fields["tags"] = []string{}
	} else {
		fields["tags"] = meta.Tags
	}
	fields["category"] = meta.Category
	if meta.Correspondent == "" {
		delete(fields, "correspondent")
	} else {
		fields["correspondent"] = meta.Correspondent
	}
	if meta.DocumentType == "" {
		delete(fields, "document_type")
	} else {
		fields["document_type"] = meta.DocumentType
	}
	fields["ocr_engine"] = meta.OCREngine
	fields["archive_path"] = meta.ArchivePath
}

func renderBody(text, archivePath string) string {
	var sb strings.Builder
	sb.WriteString(text)
	if !strings.HasSuffix(text, "\n") {
		sb.WriteByte('\n')
	}
	if archivePath != "" {
		sb.WriteString("\n---\n")
		sb.WriteString(fmt.Sprintf("[Archived Original](file://%s)\n", filepath.ToSlash(archivePath)))
	}
	return sb.String()
}

func writeFrontmatterAndBody(path string, fields map[string]interface{}, body string) error {
	yamlBytes, err := yaml.Marshal(fields)
	if err != nil {
		return fmt.Errorf("marshal frontmatter: %w", err)
	}
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.Write(yamlBytes)
	sb.WriteString("---\n\n")
	sb.WriteString(body)
	return fsutil.AtomicWriteFile(path, []byte(sb.String()), 0o600)
}
