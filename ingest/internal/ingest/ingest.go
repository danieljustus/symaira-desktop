// Package ingest implements the one-shot ingestion pipeline.
package ingest

import (
	"context"
	"fmt"

	"github.com/danieljustus/symaira-ingest/internal/extract"
	"github.com/danieljustus/symaira-ingest/internal/writer"
)

// IngestOptions holds optional preset metadata that overrides classification rule results.
// When set, preset values take precedence over any matching rules (first wins: Preset > Rules).
type IngestOptions struct {
	PresetCategory      string
	PresetTags          []string
	PresetCorrespondent string
	PresetDocumentType  string
	// SourcePathOverride, when set, is written to the note frontmatter instead
	// of the temporary local processing path. It must not affect extraction,
	// hashing, archive writes, or store source paths.
	SourcePathOverride string
	ImportedFrom       string
	ImportRunID        string
	SourceURI          string
	DownloadURI        string
	// Paperless carries traceability metadata when the source originates
	// from a Paperless-ngx migration. Nil for ordinary ingests.
	Paperless *writer.PaperlessMeta
	// Layout optionally overrides the note's placement within the vault
	// (subdirectory and file name). Nil keeps the flat sidecar layout.
	Layout *writer.NoteLayout
	// AllowDuplicateContent writes a separate note even when the file hash is
	// already present in the local document table. Paperless imports use this so
	// every Paperless document ID gets its own traceable Markdown note while the
	// archive can still deduplicate identical originals by SHA-256.
	AllowDuplicateContent bool
	// ExistingVaultPath updates an already-written note in place while
	// preserving user-owned frontmatter fields. Used by reprocessing only.
	ExistingVaultPath string
	// ArchivePathOverride keeps the original archived path during reprocessing.
	ArchivePathOverride string
}

// Result is the outcome of a one-shot ingest.
type Result struct {
	SourcePath      string
	SHA256          string
	Kind            extract.Kind
	Extract         *extract.Result
	VaultPath       string
	ArchivePath     string
	Category        string
	Tags            []string
	Correspondent   string
	DocumentType    string
	SidecarPath     string
	ExtractionCount int
}

func extractText(ctx context.Context, source string, kind extract.Kind, engine extract.Engine) (*extract.Result, error) {
	var res *extract.Result
	var err error

	switch kind {
	case extract.KindText, extract.KindMarkdown, extract.KindCSV:
		res, err = extract.ReadTextKind(ctx, source, kind)
	case extract.KindHTML, extract.KindRTF, extract.KindDOCX, extract.KindXLSX, extract.KindPPTX, extract.KindODT, extract.KindODS, extract.KindODP, extract.KindEPUB, extract.KindEML:
		res, err = extract.ReadStructuredKind(ctx, source, kind)
	default:
		if engine == nil {
			return nil, fmt.Errorf("no extraction engine available for %q", kind)
		}
		res, err = engine.Extract(ctx, source, kind)
	}

	if err != nil {
		return nil, fmt.Errorf("extract text: %w", err)
	}

	return res, nil
}
