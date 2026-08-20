package paperlessimport

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/danieljustus/symaira-ingest/internal/ingest"
	"github.com/danieljustus/symaira-ingest/internal/paperless"
	"github.com/danieljustus/symaira-ingest/internal/writer"
)

type Stats struct {
	Imported int
	Skipped  int
	Failed   int
	Total    int
	Warnings []string

	RunID       string
	ToolVersion string
	Source      string
	SourceURL   string
	Mode        string
	StartedAt   time.Time
	FinishedAt  time.Time

	// SelectedIDs lists the Paperless document IDs chosen for this run, in
	// processing order. It lets a bounded pilot import report exactly which
	// documents were touched without leaking any document content.
	SelectedIDs []int

	// Results records the per-document outcome in processing order, feeding
	// the machine-readable migration report. It never contains document
	// content, only IDs, a status, an optional reason, and written paths.
	Results []DocumentResult

	// Audit is the structured migration-readiness summary built during a
	// dry-run. Nil for a real import.
	Audit *AuditReport
}

// DocumentResult is the outcome of processing a single Paperless document.
type DocumentResult struct {
	ID                     int      `json:"id"`
	Status                 string   `json:"status"` // imported | skipped | failed | would-import | planned
	Reason                 string   `json:"reason,omitempty"`
	Error                  string   `json:"error,omitempty"`
	Stage                  string   `json:"stage,omitempty"`
	VaultPath              string   `json:"vault_path,omitempty"`
	ArchivePath            string   `json:"archive_path,omitempty"`
	SHA256                 string   `json:"sha256,omitempty"`
	MIME                   string   `json:"mime,omitempty"`
	ExpectedExtension      string   `json:"expected_extension,omitempty"`
	ActualArchiveExtension string   `json:"actual_archive_extension,omitempty"`
	ImportRunID            string   `json:"import_run_id,omitempty"`
	SourceURI              string   `json:"source_uri,omitempty"`
	DownloadURI            string   `json:"download_uri,omitempty"`
	Warnings               []string `json:"warnings,omitempty"`
}

func boundedErrorString(err error) string {
	if err == nil {
		return ""
	}
	const max = 1024
	reason := strings.Join(strings.Fields(err.Error()), " ")
	if len(reason) <= max {
		return reason
	}
	return reason[:max] + "…"
}

type Options struct {
	BaseURL string
	Token   string
	Since   time.Time
	DryRun  bool
	Plan    bool
	Resume  bool
	// DeepVerify makes verification re-download each Paperless original and
	// compare its SHA-256 with the archived original. It is intentionally opt-in
	// because it performs full network reads for every selected document.
	DeepVerify bool

	// RetryFailed restricts a real import to documents whose current-target
	// Paperless import state is recorded as failed.
	RetryFailed bool

	// Concurrency bounds concurrent document processing. The default is 1 so
	// existing serial behavior is preserved unless explicitly requested.
	Concurrency int

	// CheckpointEvery prints progress summaries after every N processed
	// documents. Zero disables periodic checkpoints.
	CheckpointEvery int

	// TargetVault and TargetArchive identify the concrete destination used for
	// resume safety. When empty they are derived from the supplied pipeline.
	TargetVault   string
	TargetArchive string

	// Progress receives operator progress/checkpoint output. Nil means stderr.
	Progress io.Writer

	// Limit caps the number of documents processed this run to the first N
	// of the listed archive (newest first). Zero means no limit. Ignored
	// when IDs is set.
	Limit int

	// IDs restricts the run to an explicit set of Paperless document IDs,
	// fetched individually. When non-empty it takes precedence over Since
	// and Limit, enabling a deterministic, inspectable pilot import.
	IDs []int

	// PreserveStoragePaths, when true, places each generated note under a
	// vault subdirectory derived from the document's Paperless storage path
	// instead of the flat vault root. Off by default for backward
	// compatibility.
	PreserveStoragePaths bool
}

// lookups resolves Paperless tag/correspondent/document-type/storage-path
// IDs to names. Real documents reference these entities by integer ID; the
// name tables are fetched once per run.
type lookups struct {
	tags           map[int]string
	correspondents map[int]string
	documentTypes  map[int]string
	storagePaths   map[int]string
}

func loadLookups(ctx context.Context, client *paperless.Client) (*lookups, error) {
	tags, err := client.ListTags(ctx)
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	correspondents, err := client.ListCorrespondents(ctx)
	if err != nil {
		return nil, fmt.Errorf("list correspondents: %w", err)
	}
	documentTypes, err := client.ListDocumentTypes(ctx)
	if err != nil {
		return nil, fmt.Errorf("list document types: %w", err)
	}
	storagePaths, err := client.ListStoragePaths(ctx)
	if err != nil {
		return nil, fmt.Errorf("list storage paths: %w", err)
	}

	l := &lookups{
		tags:           make(map[int]string, len(tags)),
		correspondents: make(map[int]string, len(correspondents)),
		documentTypes:  make(map[int]string, len(documentTypes)),
		storagePaths:   make(map[int]string, len(storagePaths)),
	}
	for _, t := range tags {
		l.tags[t.ID] = t.Name
	}
	for _, c := range correspondents {
		l.correspondents[c.ID] = c.Name
	}
	for _, d := range documentTypes {
		l.documentTypes[d.ID] = d.Name
	}
	for _, sp := range storagePaths {
		l.storagePaths[sp.ID] = sp.Name
	}
	return l, nil
}

// paperlessCreated returns the document's creation timestamp, preferring the
// full "created" timestamp over the date-only "created_date" fallback that
// some Paperless-ngx endpoints emit instead.
func paperlessCreated(doc paperless.Document) time.Time {
	if !doc.Created.IsZero() {
		return doc.Created.Time
	}
	return doc.CreatedDate.Time
}

// resolveRef returns the display name for ref, preferring an already
// embedded name over a table lookup by ID. ok is false only when ref
// carries an ID that has no name anywhere (an unresolved reference).
func resolveRef(ref paperless.Ref, table map[int]string) (name string, ok bool) {
	if ref.Name != "" {
		return ref.Name, true
	}
	if ref.ID == 0 {
		return "", true
	}
	name, found := table[ref.ID]
	return name, found
}

// resolvedMeta is the display metadata a document contributes to a generated
// note: tag names, correspondent, document type, and storage path, plus any
// warnings raised for references that could not be resolved to a name.
type resolvedMeta struct {
	Tags          []string
	Correspondent string
	DocumentType  string
	StoragePath   string
	Warnings      []string
}

// resolveDocMeta turns a document's Paperless references into the exact
// display values written into its note. Both the importer and the post-import
// verifier use it, so a verification compares against what the import would
// have produced rather than a re-derived guess.
func resolveDocMeta(doc paperless.Document, lu *lookups) resolvedMeta {
	var m resolvedMeta

	m.Tags = make([]string, 0, len(doc.Tags))
	for _, t := range doc.Tags {
		name, ok := resolveRef(t, lu.tags)
		if !ok {
			m.Warnings = append(m.Warnings, fmt.Sprintf("document %d (%s): unresolved tag ID %d", doc.ID, doc.Title, t.ID))
			name = fmt.Sprintf("id:%d", t.ID)
		}
		if name != "" {
			m.Tags = append(m.Tags, name)
		}
	}

	if doc.Correspondent != nil {
		name, ok := resolveRef(*doc.Correspondent, lu.correspondents)
		if !ok {
			m.Warnings = append(m.Warnings, fmt.Sprintf("document %d (%s): unresolved correspondent ID %d", doc.ID, doc.Title, doc.Correspondent.ID))
			name = fmt.Sprintf("id:%d", doc.Correspondent.ID)
		}
		m.Correspondent = name
	}
	if doc.DocumentType != nil {
		name, ok := resolveRef(*doc.DocumentType, lu.documentTypes)
		if !ok {
			m.Warnings = append(m.Warnings, fmt.Sprintf("document %d (%s): unresolved document type ID %d", doc.ID, doc.Title, doc.DocumentType.ID))
			name = fmt.Sprintf("id:%d", doc.DocumentType.ID)
		}
		m.DocumentType = name
	}
	if doc.StoragePath != nil {
		name, ok := resolveRef(*doc.StoragePath, lu.storagePaths)
		if !ok {
			m.Warnings = append(m.Warnings, fmt.Sprintf("document %d (%s): unresolved storage path ID %d", doc.ID, doc.Title, doc.StoragePath.ID))
		}
		m.StoragePath = name
	}

	return m
}

// selectDocuments resolves the set of Paperless documents to import for this
// run. An explicit --ids list is fetched document-by-document (bounded, no
// full-archive scan); otherwise the archive is listed with the --since bound
// and capped to --limit when set.
func selectDocuments(ctx context.Context, client *paperless.Client, opts Options) ([]paperless.Document, error) {
	if len(opts.IDs) > 0 {
		docs := make([]paperless.Document, 0, len(opts.IDs))
		for _, id := range opts.IDs {
			doc, err := client.GetDocument(ctx, id)
			if err != nil {
				return nil, fmt.Errorf("get document %d: %w", id, err)
			}
			docs = append(docs, *doc)
		}
		return docs, nil
	}

	docs, err := client.ListDocuments(ctx, opts.Since, opts.Limit)
	if err != nil {
		return nil, fmt.Errorf("list documents: %w", err)
	}
	if opts.Limit > 0 && len(docs) > opts.Limit {
		docs = docs[:opts.Limit]
	}
	return docs, nil
}

type stagedError struct {
	stage string
	err   error
}

func (e *stagedError) Error() string { return e.err.Error() }
func (e *stagedError) Unwrap() error { return e.err }

func stageError(stage string, err error) error {
	if err == nil {
		return nil
	}
	return &stagedError{stage: stage, err: err}
}

func stageFromError(err error) string {
	var se *stagedError
	if errors.As(err, &se) && se.stage != "" {
		return se.stage
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "download"):
		return "download"
	case strings.Contains(msg, "detect source type"):
		return "detect"
	case strings.Contains(msg, "extract text"):
		return "extract"
	case strings.Contains(msg, "archive file"):
		return "archive"
	case strings.Contains(msg, "write note"):
		return "write"
	case strings.Contains(msg, "record document"), strings.Contains(msg, "set vault path"), strings.Contains(msg, "complete job"):
		return "metadata"
	default:
		return "import"
	}
}

func paperlessSourceURI(id int) string   { return fmt.Sprintf("paperless://documents/%d", id) }
func paperlessDownloadURI(id int) string { return paperlessSourceURI(id) + "/download" }

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

func newRunID(t time.Time) string { return "paperless-" + t.Format("20060102T150405.000000000Z") }

func plannedDocumentResult(doc paperless.Document, runID, status string) DocumentResult {
	ext := paperlessDownloadExtension(doc)
	if ext == "" {
		ext = ".bin"
	}
	return DocumentResult{
		ID:                doc.ID,
		Status:            status,
		MIME:              doc.MimeType,
		ExpectedExtension: ext,
		ImportRunID:       runID,
		SourceURI:         paperlessSourceURI(doc.ID),
		DownloadURI:       paperlessDownloadURI(doc.ID),
	}
}

func progressWriter(opts Options) io.Writer {
	if opts.Progress != nil {
		return opts.Progress
	}
	return os.Stderr
}

func importTargets(opts Options, pipeline *ingest.Pipeline) (vault, archive string) {
	vault = opts.TargetVault
	archive = opts.TargetArchive
	if vault == "" && pipeline != nil && pipeline.Writer != nil {
		vault = pipeline.Writer.Vault
	}
	if archive == "" && pipeline != nil {
		archive = pipeline.ArchiveDir
	}
	return vault, archive
}

func formatETA(start time.Time, processed, total int) string {
	if processed <= 0 || total <= processed {
		return "0s"
	}
	elapsed := time.Since(start)
	if elapsed <= 0 {
		return "unknown"
	}
	remaining := total - processed
	eta := time.Duration(float64(elapsed) / float64(processed) * float64(remaining))
	return eta.Round(time.Second).String()
}

func printCheckpoint(w io.Writer, stats *Stats) {
	processed := stats.Imported + stats.Skipped + stats.Failed
	elapsed := time.Since(stats.StartedAt)
	rate := 0.0
	if elapsed > 0 {
		rate = float64(processed) / elapsed.Minutes()
	}
	fmt.Fprintf(w, "checkpoint: processed=%d/%d imported=%d skipped=%d failed=%d rate=%.1f/min eta=%s\n",
		processed, stats.Total, stats.Imported, stats.Skipped, stats.Failed, rate, formatETA(stats.StartedAt, processed, stats.Total))
}

func Run(ctx context.Context, opts Options, pipeline *ingest.Pipeline) (*Stats, error) {
	client := paperless.NewClient(opts.BaseURL, opts.Token)
	started := time.Now().UTC()

	docs, err := selectDocuments(ctx, client, opts)
	if err != nil {
		return nil, err
	}

	lu, err := loadLookups(ctx, client)
	if err != nil {
		return nil, fmt.Errorf("load lookup maps: %w", err)
	}

	mode := "import"
	if opts.Plan {
		mode = "plan"
	} else if opts.DryRun {
		mode = "dry-run"
	} else if opts.RetryFailed {
		mode = "retry-failed"
	} else if opts.Resume {
		mode = "resume"
	}

	stats := &Stats{
		Total:     len(docs),
		RunID:     newRunID(started),
		Source:    "paperless",
		SourceURL: opts.BaseURL,
		Mode:      mode,
		StartedAt: started,
	}
	defer func() { stats.FinishedAt = time.Now().UTC() }()
	stats.SelectedIDs = make([]int, 0, len(docs))
	for _, doc := range docs {
		stats.SelectedIDs = append(stats.SelectedIDs, doc.ID)
	}

	if opts.Plan || opts.DryRun {
		status := "would-import"
		verb := "would import"
		if opts.Plan {
			status = "planned"
			verb = "planned"
		}
		for i, doc := range docs {
			if err := ctx.Err(); err != nil {
				return stats, err
			}
			fmt.Fprintf(os.Stderr, "[%d/%d] %s: %s (created: %s)\n", i+1, stats.Total, verb, doc.Title, doc.CreatedDate.Format("2006-01-02"))
			stats.Skipped++
			stats.Results = append(stats.Results, plannedDocumentResult(doc, stats.RunID, status))
		}
		stats.Audit = buildAuditReport(docs, lu)
		printAuditReport(stats.Audit)
		return stats, nil
	}

	progressOut := progressWriter(opts)
	targetVault, targetArchive := importTargets(opts, pipeline)
	concurrency := opts.Concurrency
	if concurrency < 1 {
		concurrency = 1
	}

	results := make([]DocumentResult, len(docs))
	recorded := make([]bool, len(docs))
	var mu sync.Mutex
	var progressMu sync.Mutex
	record := func(i int, result DocumentResult, warnings []string, outcome string) {
		mu.Lock()
		defer mu.Unlock()
		stats.Warnings = append(stats.Warnings, warnings...)
		switch outcome {
		case "imported":
			stats.Imported++
		case "skipped":
			stats.Skipped++
		case "failed":
			stats.Failed++
		}
		results[i] = result
		recorded[i] = true
		processed := stats.Imported + stats.Skipped + stats.Failed
		if opts.CheckpointEvery > 0 && processed%opts.CheckpointEvery == 0 {
			progressMu.Lock()
			printCheckpoint(progressOut, stats)
			progressMu.Unlock()
		}
	}

	processDoc := func(i int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		doc := docs[i]
		progressMu.Lock()
		fmt.Fprintf(progressOut, "[%d/%d] %s\n", i+1, stats.Total, doc.Title)
		progressMu.Unlock()

		state, serr := pipeline.Store.PaperlessImportStateForTarget(ctx, opts.BaseURL, targetVault, targetArchive, doc.ID)
		found := serr == nil
		if errors.Is(serr, sql.ErrNoRows) {
			serr = nil
			found = false
		}
		status := ""
		if state != nil {
			status = state.Status
		}
		if serr != nil {
			mu.Lock()
			stats.Warnings = append(stats.Warnings, fmt.Sprintf("document %d: read import state: %v", doc.ID, serr))
			mu.Unlock()
		}
		if serr == nil && found && status == "imported" {
			progressMu.Lock()
			fmt.Fprintf(progressOut, "  skipped (already imported in a previous run)\n")
			progressMu.Unlock()
			result := plannedDocumentResult(doc, stats.RunID, "skipped")
			result.Reason = "already imported in a previous run"
			if state != nil {
				result.VaultPath = state.VaultPath
				result.ArchivePath = state.ArchivePath
				result.SHA256 = state.SHA256
				result.ActualArchiveExtension = filepath.Ext(state.ArchivePath)
			}
			record(i, result, nil, "skipped")
			return ctx.Err()
		}
		if opts.RetryFailed && (!found || status != "failed") {
			result := plannedDocumentResult(doc, stats.RunID, "skipped")
			result.Reason = "not recorded as failed for this target"
			record(i, result, nil, "skipped")
			return ctx.Err()
		}

		result, warnings, err := importOne(ctx, client, doc, lu, pipeline, opts.PreserveStoragePaths, stats.RunID)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			progressMu.Lock()
			fmt.Fprintf(progressOut, "  failed: %v\n", err)
			progressMu.Unlock()
			reason := boundedErrorString(err)
			result.Status = "failed"
			result.Error = reason
			if result.Stage == "" {
				result.Stage = stageFromError(err)
			}
			if serr := pipeline.Store.UpsertPaperlessImportStateForTarget(ctx, opts.BaseURL, targetVault, targetArchive, doc.ID, "failed", reason, result.VaultPath, result.ArchivePath, result.SHA256); serr != nil {
				warnings = append(warnings, fmt.Sprintf("document %d: record import state: %v", doc.ID, serr))
			}
			record(i, result, warnings, "failed")
			return nil
		}
		// Every Paperless ID must be mapped to its vault/archive path in the
		// import state, including duplicate-content documents that point at the
		// canonical note. Use "imported" in the state so later runs and
		// verification can treat the source document as accounted for.
		stateStatus := "imported"
		if result.Status == "skipped" {
			stateStatus = "imported"
		}
		if serr := pipeline.Store.UpsertPaperlessImportStateForTarget(ctx, opts.BaseURL, targetVault, targetArchive, doc.ID, stateStatus, "", result.VaultPath, result.ArchivePath, result.SHA256); serr != nil {
			warnings = append(warnings, fmt.Sprintf("document %d: record import state: %v", doc.ID, serr))
		}
		record(i, result, warnings, result.Status)
		return ctx.Err()
	}

	if concurrency == 1 || len(docs) <= 1 {
		for i := range docs {
			if err := processDoc(i); err != nil {
				return stats, err
			}
		}
	} else {
		jobs := make(chan int)
		var wg sync.WaitGroup
		var errMu sync.Mutex
		var firstErr error
		setErr := func(err error) {
			if err == nil {
				return
			}
			errMu.Lock()
			defer errMu.Unlock()
			if firstErr == nil {
				firstErr = err
			}
		}
		workerCount := concurrency
		if workerCount > len(docs) {
			workerCount = len(docs)
		}
		for w := 0; w < workerCount; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range jobs {
					if ctx.Err() != nil {
						setErr(ctx.Err())
						continue
					}
					setErr(processDoc(i))
				}
			}()
		}
		for i := range docs {
			if ctx.Err() != nil {
				setErr(ctx.Err())
				break
			}
			jobs <- i
		}
		close(jobs)
		wg.Wait()
		if firstErr != nil {
			return stats, firstErr
		}
	}

	for i := range results {
		if recorded[i] {
			stats.Results = append(stats.Results, results[i])
		}
	}
	if opts.CheckpointEvery > 0 && stats.Total > 0 {
		printCheckpoint(progressOut, stats)
	}

	return stats, nil
}

// printAuditReport writes a concise migration-readiness summary to stderr so
// a dry-run does not leave the operator with only per-document spam.
func printAuditReport(r *AuditReport) {
	fmt.Fprintf(os.Stderr, "\n--- migration audit summary ---\n")
	fmt.Fprintf(os.Stderr, "total documents: %d\n", r.TotalDocuments)
	fmt.Fprintf(os.Stderr, "by MIME type: %v\n", r.ByMIME)
	fmt.Fprintf(os.Stderr, "tags: %d distinct, correspondents: %d distinct, document types: %d distinct, storage paths: %d distinct\n",
		len(r.TagCounts), len(r.CorrespondentCounts), len(r.DocumentTypeCounts), len(r.StoragePathCounts))
	if len(r.UnresolvedTagIDs) > 0 {
		fmt.Fprintf(os.Stderr, "unresolved tag IDs: %v\n", r.UnresolvedTagIDs)
	}
	if len(r.UnresolvedCorrespondentIDs) > 0 {
		fmt.Fprintf(os.Stderr, "unresolved correspondent IDs: %v\n", r.UnresolvedCorrespondentIDs)
	}
	if len(r.UnresolvedDocumentTypeIDs) > 0 {
		fmt.Fprintf(os.Stderr, "unresolved document type IDs: %v\n", r.UnresolvedDocumentTypeIDs)
	}
	if len(r.UnresolvedStoragePathIDs) > 0 {
		fmt.Fprintf(os.Stderr, "unresolved storage path IDs: %v\n", r.UnresolvedStoragePathIDs)
	}
	if len(r.UnsupportedFileTypes) > 0 {
		fmt.Fprintf(os.Stderr, "unsupported file types: %v\n", r.UnsupportedFileTypes)
	}
}

// paperlessNoteBaseName derives a stable, human-readable file name (without
// extension) for a document's note, used by --preserve-storage-paths. It
// prefers the original Paperless file name, then the title, and finally a
// document-ID fallback. Sanitization is applied by the writer.
func paperlessNoteBaseName(doc paperless.Document) string {
	if doc.OriginalFileName != "" {
		if b := strings.TrimSuffix(filepath.Base(doc.OriginalFileName), filepath.Ext(doc.OriginalFileName)); b != "" {
			return b
		}
	}
	if doc.Title != "" {
		return doc.Title
	}
	return fmt.Sprintf("document-%d", doc.ID)
}

// importOne downloads and ingests a single document, returning the written
// vault and archive paths on success. A content duplicate returns empty paths
// with a nil error (the document is already represented in the vault).
func importOne(ctx context.Context, client *paperless.Client, doc paperless.Document, lu *lookups, pipeline *ingest.Pipeline, preserveStoragePaths bool, runID string) (DocumentResult, []string, error) {
	result := plannedDocumentResult(doc, runID, "")
	var warnings []string

	tmpFile, err := os.CreateTemp("", "symingest-import-*.tmp")

	if err != nil {
		return result, warnings, stageError("download", fmt.Errorf("create temp file: %w", err))
	}
	tmpName := tmpFile.Name()
	defer os.Remove(tmpName)

	downloadMeta, err := client.DownloadDocumentWithMetadata(ctx, doc.ID, tmpFile)
	if err != nil {
		tmpFile.Close()
		return result, warnings, stageError("download", fmt.Errorf("download document: %w", err))
	}
	if err := tmpFile.Close(); err != nil {
		return result, warnings, stageError("download", fmt.Errorf("close temp file: %w", err))
	}
	if ct := normalizeContentType(downloadMeta.ContentType); ct != "" {
		result.MIME = ct
	}

	ext := paperlessDownloadExtensionWithMetadata(doc, downloadMeta)
	if ext == "" {
		ext = ".bin"
	}
	result.ExpectedExtension = ext
	finalPath := tmpName + ext
	if err := os.Rename(tmpName, finalPath); err != nil {
		return result, warnings, stageError("download", fmt.Errorf("rename temp file: %w", err))
	}
	defer os.Remove(finalPath)

	result.SHA256, err = hashFile(finalPath)
	if err != nil {
		return result, warnings, stageError("download", fmt.Errorf("hash downloaded file: %w", err))
	}

	meta := resolveDocMeta(doc, lu)
	warnings = append(warnings, meta.Warnings...)
	result.Warnings = append(result.Warnings, meta.Warnings...)
	tags := meta.Tags
	correspondent := meta.Correspondent
	documentType := meta.DocumentType
	storagePath := meta.StoragePath

	baseName := paperlessNoteBaseName(doc)
	if !preserveStoragePaths {
		baseName = fmt.Sprintf("paperless-%d-%s", doc.ID, baseName)
	}
	layout := &writer.NoteLayout{BaseName: baseName}
	if preserveStoragePaths {
		layout.Subdir = writer.SanitizeStoragePath(storagePath)
	}

	preset := &ingest.IngestOptions{
		PresetCategory:        documentType,
		PresetTags:            tags,
		PresetCorrespondent:   correspondent,
		PresetDocumentType:    documentType,
		Layout:                layout,
		SourcePathOverride:    result.DownloadURI,
		ImportedFrom:          "paperless",
		ImportRunID:           runID,
		SourceURI:             result.SourceURI,
		DownloadURI:           result.DownloadURI,
		AllowDuplicateContent: true,
		Paperless: &writer.PaperlessMeta{
			DocumentID:       doc.ID,
			Title:            doc.Title,
			Created:          paperlessCreated(doc),
			Added:            doc.Added.Time,
			Modified:         doc.Modified.Time,
			StoragePath:      storagePath,
			OriginalFileName: doc.OriginalFileName,
			ArchivedFileName: doc.ArchivedFileName,
			PageCount:        doc.PageCount,
			URL:              client.DocumentURL(doc.ID),
		},
	}

	res, err := pipeline.Ingest(ctx, finalPath, preset)
	if err != nil {
		if errors.Is(err, ingest.ErrDuplicate) {
			log.Printf("  skipped (duplicate): %s", doc.Title)
			result.Status = "skipped"
			result.Reason = "duplicate content; note already present in the vault"
			var dup *ingest.DuplicateError
			if errors.As(err, &dup) {
				result.VaultPath = dup.VaultPath
				result.ArchivePath = dup.ArchivePath
				result.ActualArchiveExtension = filepath.Ext(dup.ArchivePath)
			}
			return result, warnings, nil
		}
		return result, warnings, stageError(stageFromError(err), fmt.Errorf("ingest: %w", err))
	}

	log.Printf("  imported: %s → vault", doc.Title)
	result.Status = "imported"
	result.VaultPath = res.VaultPath
	result.ArchivePath = res.ArchivePath
	result.SHA256 = res.SHA256
	if result.MIME == "" {
		result.MIME = res.Extract.MIME
	}
	result.ActualArchiveExtension = filepath.Ext(res.ArchivePath)
	return result, warnings, nil
}
