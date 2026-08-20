package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/danieljustus/symaira-ingest/internal/annotate"
	"github.com/danieljustus/symaira-ingest/internal/extract"
	"github.com/danieljustus/symaira-ingest/internal/store"
	"github.com/danieljustus/symaira-ingest/internal/writer"
)

// Pipeline orchestrates extraction, persistence, and Markdown output.
type Pipeline struct {
	Engine       extract.Engine
	Store        *store.Store
	Writer       *writer.NoteWriter
	ArchiveDir   string
	ProcessedDir string
	FailedDir    string
	// PostIndex optionally indexes the generated Markdown note after a
	// successful write. Errors are logged, not fatal: search indexing must not
	// corrupt or roll back a completed ingest.
	PostIndex func(context.Context, string) error
	// PostExtract optionally processes extraction results after sidecar write.
	// Errors are logged, not fatal: annotation must not roll back a completed ingest.
	PostExtract func(context.Context, *Result, []annotate.Extraction) error
	// ExtractionProfile is the annotation profile name to use for extraction.
	// Empty string disables extraction.
	ExtractionProfile string
}

// ErrDuplicate is returned when a source has already been ingested.
var ErrDuplicate = errors.New("source already ingested")

// ReprocessJobKind is the stable queue kind used by the reocr command.
const ReprocessJobKind = "reocr"

// ReprocessResult describes a synchronous reprocessing request.
type ReprocessResult struct {
	Result         *Result
	Job            *store.Job
	AlreadyRunning bool
}

// Reprocess validates an archived original, queues one reprocessing job for
// the existing document, and runs that job through the normal extraction and
// note-writing pipeline. A pending/running reprocessing job is returned without
// creating another job.
func (p *Pipeline) Reprocess(ctx context.Context, documentID int64, source string, opts *IngestOptions) (*ReprocessResult, error) {
	doc, err := p.Store.ByID(ctx, documentID)
	if err != nil {
		return nil, fmt.Errorf("get document: %w", err)
	}
	if doc.ArchivePath == nil || *doc.ArchivePath == "" {
		return nil, fmt.Errorf("archived source is not recorded for document %d", documentID)
	}
	if doc.VaultPath == nil || *doc.VaultPath == "" {
		return nil, fmt.Errorf("output note is not recorded for document %d", documentID)
	}
	if source == "" {
		source = *doc.ArchivePath
	}
	info, err := os.Stat(source)
	if err != nil {
		return nil, fmt.Errorf("stat archived source: %w", err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("archived source is a directory: %s", source)
	}
	hash, err := hashFile(source)
	if err != nil {
		return nil, fmt.Errorf("hash archived source: %w", err)
	}
	if hash != doc.SHA256 {
		return nil, fmt.Errorf("archived source hash mismatch for document %d", documentID)
	}
	if _, err := extract.Detect(source); err != nil {
		return nil, fmt.Errorf("detect archived source type: %w", err)
	}

	job, created, err := p.Store.EnqueueReprocessJob(ctx, documentID)
	if err != nil {
		return nil, fmt.Errorf("enqueue reprocess job: %w", err)
	}
	if !created {
		return &ReprocessResult{Job: job, AlreadyRunning: true}, nil
	}
	claimed, err := p.Store.ClaimJobByID(ctx, job.ID)
	if err != nil {
		return nil, fmt.Errorf("claim reprocess job: %w", err)
	}
	if claimed == nil {
		return nil, fmt.Errorf("reprocess job %d is no longer available", job.ID)
	}

	if opts == nil {
		opts = &IngestOptions{}
	}
	opts = cloneIngestOptions(opts)
	opts.SourcePathOverride = doc.SourcePath
	opts.ExistingVaultPath = *doc.VaultPath
	opts.ArchivePathOverride = *doc.ArchivePath
	res, err := p.processJob(ctx, claimed, opts)
	if err != nil {
		if failErr := p.Store.FailJob(ctx, claimed.ID, err.Error()); failErr != nil {
			return nil, fmt.Errorf("reprocess failed: %v (failed to mark job as failed: %v)", err, failErr)
		}
		return nil, err
	}
	if err := p.Store.SetVaultAndArchivePath(ctx, documentID, res.VaultPath, res.ArchivePath, res.Category, res.Tags, res.Correspondent, res.DocumentType); err != nil {
		return nil, fmt.Errorf("set reprocessed document paths: %w", err)
	}
	if err := p.Store.CompleteJob(ctx, claimed.ID); err != nil {
		return nil, fmt.Errorf("complete reprocess job: %w", err)
	}
	return &ReprocessResult{Result: res, Job: claimed}, nil
}

func cloneIngestOptions(opts *IngestOptions) *IngestOptions {
	clone := *opts
	if opts.PresetTags != nil {
		clone.PresetTags = append([]string(nil), opts.PresetTags...)
	}
	return &clone
}

// DuplicateError holds details of a duplicate document that was already ingested.
type DuplicateError struct {
	SourcePath  string
	VaultPath   string
	ArchivePath string
}

func (e *DuplicateError) Error() string {
	return fmt.Sprintf("source already ingested: %s (vault: %s, archive: %s)", e.SourcePath, e.VaultPath, e.ArchivePath)
}

func (e *DuplicateError) Is(target error) bool {
	return target == ErrDuplicate
}

// Ingest processes a single source file through the full one-shot pipeline.
// opts may be nil; when non-nil, preset metadata overrides classification rule results.
func (p *Pipeline) Ingest(ctx context.Context, source string, opts *IngestOptions) (*Result, error) {
	info, err := os.Stat(source)
	if err != nil {
		return nil, fmt.Errorf("stat source: %w", err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("source is a directory: %s", source)
	}

	kind, err := extract.Detect(source)
	if err != nil {
		return nil, fmt.Errorf("detect source type: %w", err)
	}

	hash, err := hashFile(source)
	if err != nil {
		return nil, fmt.Errorf("hash source: %w", err)
	}

	doc, created, err := p.Store.CreateOrGet(ctx, source, hash, string(kind))
	if err != nil {
		return nil, fmt.Errorf("record document: %w", err)
	}
	if !created {
		if opts != nil && opts.AllowDuplicateContent {
			return p.processSource(ctx, source, hash, kind, opts)
		}
		if _, err := p.Store.EnqueueSkippedJob(ctx, doc.ID, string(kind), "duplicate"); err != nil {
			return nil, fmt.Errorf("enqueue skipped job: %w", err)
		}
		var vPath, aPath string
		if doc.VaultPath != nil {
			vPath = *doc.VaultPath
		}
		if doc.ArchivePath != nil {
			aPath = *doc.ArchivePath
		}
		return nil, &DuplicateError{
			SourcePath:  source,
			VaultPath:   vPath,
			ArchivePath: aPath,
		}
	}

	// Enqueue the job
	var enqueueErr error
	enqueued, enqueueErr := p.Store.EnqueueJob(ctx, doc.ID, string(kind))
	if enqueueErr != nil {
		return nil, fmt.Errorf("enqueue job: %w", enqueueErr)
	}

	// Claim the exact job we just enqueued to avoid racing with background workers
	claimed, err := p.Store.ClaimJobByID(ctx, enqueued.ID)
	if err != nil {
		return nil, fmt.Errorf("claim job: %w", err)
	}
	if claimed == nil {
		return nil, fmt.Errorf("failed to claim enqueued job immediately")
	}

	// Run processJob
	res, err := p.processJob(ctx, claimed, opts)
	if err != nil {
		if failErr := p.Store.FailJob(ctx, claimed.ID, err.Error()); failErr != nil {
			return nil, fmt.Errorf("process job failed: %v (failed to mark job as failed: %v)", err, failErr)
		}
		return nil, err
	}

	// Complete the job
	if err := p.Store.SetVaultAndArchivePath(ctx, doc.ID, res.VaultPath, res.ArchivePath, res.Category, res.Tags, res.Correspondent, res.DocumentType); err != nil {
		return nil, fmt.Errorf("set vault path: %w", err)
	}
	if err := p.Store.CompleteJob(ctx, claimed.ID); err != nil {
		return nil, fmt.Errorf("complete job: %w", err)
	}

	return res, nil
}

// processJob performs the text extraction and writes the resulting note.
func (p *Pipeline) processJob(ctx context.Context, job *store.Job, opts *IngestOptions) (*Result, error) {
	doc, err := p.Store.ByID(ctx, job.DocumentID)
	if err != nil {
		return nil, fmt.Errorf("get document: %w", err)
	}
	if job.Kind == ReprocessJobKind {
		if doc.ArchivePath == nil || *doc.ArchivePath == "" {
			return nil, fmt.Errorf("archived source is not recorded for document %d", doc.ID)
		}
		kind, err := extract.Detect(*doc.ArchivePath)
		if err != nil {
			return nil, fmt.Errorf("detect archived source type: %w", err)
		}
		if opts == nil {
			opts = &IngestOptions{}
		} else {
			opts = cloneIngestOptions(opts)
		}
		opts.SourcePathOverride = doc.SourcePath
		opts.ArchivePathOverride = *doc.ArchivePath
		if doc.VaultPath != nil {
			opts.ExistingVaultPath = *doc.VaultPath
		}
		return p.processSource(ctx, *doc.ArchivePath, doc.SHA256, kind, opts)
	}
	return p.processSource(ctx, doc.SourcePath, doc.SHA256, extract.Kind(job.Kind), opts)
}

func (p *Pipeline) processSource(ctx context.Context, source, hash string, kind extract.Kind, opts *IngestOptions) (*Result, error) {
	var extractRes *extract.Result
	var err error
	switch kind {
	case extract.KindText, extract.KindMarkdown, extract.KindCSV:
		extractRes, err = extractText(ctx, source, kind, nil)
	default:
		if p.Engine == nil {
			return nil, fmt.Errorf("no extraction engine available for %q", kind)
		}
		extractRes, err = extractText(ctx, source, kind, p.Engine)
	}
	if err != nil {
		return nil, err
	}

	var archivePath string
	if opts != nil && opts.ArchivePathOverride != "" {
		archivePath = opts.ArchivePathOverride
	} else if p.ArchiveDir != "" {
		ext := filepath.Ext(source)
		archivePath = filepath.Join(p.ArchiveDir, hash+ext)
		if err := atomicCopy(source, archivePath); err != nil {
			return nil, fmt.Errorf("archive file: %w", err)
		}
	}

	// Classify based on rules
	var category string
	var tags []string
	var correspondent string
	var documentType string

	rules, rErr := p.Store.ListRules(ctx)
	if rErr != nil {
		// Classification failures do not block basic OCR/text ingestion
		log.Printf("Failed to load classification rules: %v", rErr)
	} else {
		lowerText := strings.ToLower(extractRes.Text)
		for _, rule := range rules {
			patternLower := strings.ToLower(rule.Pattern)
			if strings.Contains(lowerText, patternLower) {
				switch rule.Kind {
				case "category":
					if category == "" {
						category = rule.Value
					}
				case "tag":
					// Avoid duplicates
					found := false
					for _, t := range tags {
						if t == rule.Value {
							found = true
							break
						}
					}
					if !found {
						tags = append(tags, rule.Value)
					}
				case "correspondent":
					if correspondent == "" {
						correspondent = rule.Value
					}
				case "document_type":
					if documentType == "" {
						documentType = rule.Value
					}
				}
			}
		}
	}

	if opts != nil {
		if opts.PresetCategory != "" {
			category = opts.PresetCategory
		}
		if len(opts.PresetTags) > 0 {
			tags = opts.PresetTags
		}
		if opts.PresetCorrespondent != "" {
			correspondent = opts.PresetCorrespondent
		}
		if opts.PresetDocumentType != "" {
			documentType = opts.PresetDocumentType
		}
	}

	var paperlessMeta *writer.PaperlessMeta
	var layout *writer.NoteLayout
	noteSourcePath := source
	var importedFrom, importRunID, sourceURI, downloadURI string
	if opts != nil {
		paperlessMeta = opts.Paperless
		layout = opts.Layout
		if opts.SourcePathOverride != "" {
			noteSourcePath = opts.SourcePathOverride
		}
		importedFrom = opts.ImportedFrom
		importRunID = opts.ImportRunID
		sourceURI = opts.SourceURI
		downloadURI = opts.DownloadURI
	}

	var vaultPath string
	if opts != nil && opts.ExistingVaultPath != "" {
		vaultPath = opts.ExistingVaultPath
		err = p.Writer.UpdateNote(vaultPath, writer.Note{
			SourcePath:    noteSourcePath,
			IngestedAt:    time.Now().UTC(),
			SHA256:        hash,
			MIME:          extractRes.MIME,
			Tags:          tags,
			Category:      category,
			Correspondent: correspondent,
			DocumentType:  documentType,
			OCREngine:     extractRes.Engine,
			ArchivePath:   archivePath,
		}, extractRes.Text)
	} else {
		vaultPath, err = p.Writer.WriteNote(
			noteSourcePath,
			hash,
			extractRes.MIME,
			extractRes.Engine,
			extractRes.Text,
			archivePath,
			time.Now().UTC(),
			category,
			tags,
			correspondent,
			documentType,
			importedFrom,
			importRunID,
			sourceURI,
			downloadURI,
			paperlessMeta,
			layout,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("write note: %w", err)
	}
	if p.PostIndex != nil {
		if err := p.PostIndex(ctx, vaultPath); err != nil {
			log.Printf("symseek post-ingest index failed for %s: %v", vaultPath, err)
		}
	}

	res := &Result{
		SourcePath:    noteSourcePath,
		SHA256:        hash,
		Kind:          kind,
		Extract:       extractRes,
		VaultPath:     vaultPath,
		ArchivePath:   archivePath,
		Category:      category,
		Tags:          tags,
		Correspondent: correspondent,
		DocumentType:  documentType,
	}

	if p.ExtractionProfile != "" && strings.TrimSpace(extractRes.Text) != "" {
		profile, pErr := annotate.GetProfile(p.ExtractionProfile)
		if pErr != nil {
			log.Printf("annotate: invalid profile %q: %v", p.ExtractionProfile, pErr)
		} else {
			extractions := annotate.Extract(profile, extractRes.Text)
			if len(extractions) > 0 {
				sidecarPath := annotate.SidecarPath(p.Writer.Vault, hash)
				if wErr := annotate.WriteSidecar(p.Writer.Vault, hash, extractions); wErr != nil {
					log.Printf("annotate: write sidecar failed: %v", wErr)
				} else {
					if uErr := p.Writer.UpdateNoteSidecar(vaultPath, sidecarPath, len(extractions)); uErr != nil {
						log.Printf("annotate: update note frontmatter failed: %v", uErr)
					}
					res.SidecarPath = sidecarPath
					res.ExtractionCount = len(extractions)
				}
			}
			if p.Store != nil && len(extractions) > 0 {
				doc, dErr := p.Store.ByHash(ctx, hash)
				if dErr == nil {
					if sErr := p.Store.RecordExtractions(ctx, doc.ID, p.ExtractionProfile, extractions); sErr != nil {
						log.Printf("annotate: record extractions to store failed: %v", sErr)
					}
				}
			}
			if p.PostExtract != nil {
				if peErr := p.PostExtract(ctx, res, extractions); peErr != nil {
					log.Printf("annotate: PostExtract hook failed: %v", peErr)
				}
			}
		}
	}

	return res, nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func atomicCopy(src, dst string) error {
	// If it already exists, do nothing (no conflicting copies)
	if _, err := os.Stat(dst); err == nil {
		return nil
	}

	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstDir := filepath.Dir(dst)
	if err := os.MkdirAll(dstDir, 0o700); err != nil {
		return err
	}

	tmpFile, err := os.CreateTemp(dstDir, "symingest-archive-tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmpFile.Name()
	defer func() {
		if tmpFile != nil {
			tmpFile.Close()
		}
		os.Remove(tmpName)
	}()

	if _, err := io.Copy(tmpFile, srcFile); err != nil {
		return err
	}

	if err := tmpFile.Sync(); err != nil {
		return err
	}

	if err := tmpFile.Close(); err != nil {
		return err
	}
	tmpFile = nil

	if err := os.Rename(tmpName, dst); err != nil {
		return err
	}

	return nil
}
