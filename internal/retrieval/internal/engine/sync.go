package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/google/uuid"

	"github.com/danieljustus/symaira-desktop/internal/documentformat"
	"github.com/danieljustus/symaira-desktop/internal/retrieval/internal/db"
	"github.com/danieljustus/symaira-desktop/internal/retrieval/internal/parser"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

// chunkNamespace is the deterministic UUID namespace used for all chunk IDs.
// It is derived from a project-specific URL so that IDs are stable across
// installations and do not collide with generic UUID namespaces.
//
// See: deriveChunkID for how individual chunk IDs are computed.
var chunkNamespace = uuid.NewSHA1(uuid.NameSpaceURL, []byte("https://github.com/danieljustus/symaira-seek/chunk"))

// deriveChunkID returns a deterministic UUIDv5 for a chunk. The ID is derived
// from the document identity, the chunk's content hash, and the chunk's
// character start offset in the source text. This keeps a chunk's ID stable
// across reindex runs as long as the document path, chunk content, and start
// position do not change. Editing a chunk produces a new content hash and
// therefore a new ID.
//
// Inputs are separated by null bytes so that accidental concatenation of two
// different chunks cannot produce the same name as another valid chunk
// (e.g. "doc/a" + "bc" vs "doc/ab" + "c").
func deriveChunkID(documentPath, contentHash string, charStart int) string {
	name := documentPath + "\x00" + contentHash + "\x00" + strconv.Itoa(charStart)
	return uuid.NewSHA1(chunkNamespace, []byte(name)).String()
}

// isWithinDir reports whether path is dir itself or located inside dir.
// It uses a trailing path separator to avoid false matches where one
// directory name is a string prefix of another (e.g. /docs vs /docs2).
func isWithinDir(path, dir string) bool {
	if path == dir {
		return true
	}
	return strings.HasPrefix(path, dir+string(os.PathSeparator))
}

func shouldSkipDir(name string) bool {
	if strings.HasPrefix(name, ".") && name != "." && name != ".." {
		return true
	}
	switch name {
	case "node_modules", "dist", "vendor", "build", "target":
		return true
	}
	return false
}

var supportedExtensions = func() map[string]bool {
	result := map[string]bool{
		".go": true, ".py": true, ".js": true, ".ts": true,
		".json": true, ".yaml": true, ".yml": true, ".sh": true, ".css": true,
	}
	for _, ext := range documentformat.SupportedExtensions() {
		result[ext] = true
	}
	return result
}()

// canonicalIndexDirectory resolves a directory before it becomes an index
// identity. This prevents symlink aliases from creating duplicate documents and
// makes the watched/indexed boundary explicit.
func canonicalIndexDirectory(dirPath string) (string, error) {
	absPath, err := filepath.Abs(dirPath)
	if err != nil {
		return "", userFriendlyError(err, "failed to get absolute path",
			"Check that the path is valid and try again")
	}
	absPath = filepath.Clean(absPath)
	// Preserve lexical absolute paths unless the supplied directory itself is a
	// symlink. This avoids rewriting ordinary macOS paths through /var aliases,
	// while collapsing an explicit source alias to one stable root.
	if linkInfo, lstatErr := os.Lstat(absPath); lstatErr == nil && linkInfo.Mode()&os.ModeSymlink != 0 {
		absPath, err = filepath.EvalSymlinks(absPath)
		if err != nil {
			return "", userFriendlyError(err, "cannot access directory",
				"Check that the path is valid and try again")
		}
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return "", userFriendlyError(err, "cannot access directory",
			"Check file permissions and ensure the directory exists")
	}
	if !info.IsDir() {
		return "", fmt.Errorf("target path is not a directory: %s", absPath)
	}
	f, err := os.Open(absPath) // #nosec G304 -- explicit local index source.
	if err != nil {
		return "", fmt.Errorf("source directory is not readable: %w", err)
	}
	_, readErr := f.Readdirnames(1)
	closeErr := f.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return "", fmt.Errorf("source directory is not readable: %w", readErr)
	}
	if closeErr != nil {
		return "", fmt.Errorf("close source directory: %w", closeErr)
	}
	return filepath.Clean(absPath), nil
}

// IndexDirectory crawls a directory, computes hashes, parses changed files,
// generates embeddings, saves to DB, and deletes orphan documents.
func IndexDirectory(dbClient db.Store, embedder Embedder, dirPath string) error {
	absPath, err := canonicalIndexDirectory(dirPath)
	if err != nil {
		return err
	}

	// 1. Scan directory for valid files
	foundPaths := make(map[string]bool)
	var documentIssues []string
	err = filepath.WalkDir(absPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			if shouldSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		// Do not follow file symlinks: a source must not index content outside
		// its registered directory, and the symlink path is not a stable file
		// identity when its target changes.
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		if supportedExtensions[ext] {
			di, infoErr := d.Info()
			if infoErr == nil && di.Size() > parser.MaxIndexFileSize {
				fmt.Fprintf(os.Stderr, "Skipping %s: file size %d exceeds %d byte limit\n", path, di.Size(), parser.MaxIndexFileSize)
				return nil
			}
			foundPaths[path] = true
		} else if parser.IsKnownDocumentExtension(ext) {
			// Make unindexable document formats visible instead of
			// silently ignoring them (issue #341).
			message := parser.UnsupportedDocumentSkipMessage(path, ext)
			fmt.Fprintln(os.Stderr, message)
			documentIssues = append(documentIssues, message)
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("failed walking directory: %w", err)
	}

	// 2. Fetch existing documents from DB
	existingDocs, err := dbClient.ListDocuments()
	if err != nil {
		return fmt.Errorf("failed listing existing documents: %w", err)
	}

	documentIssues = append(documentIssues, processFilesInParallel(dbClient, embedder, foundPaths)...)

	// 4. Orphan detection: delete DB documents that no longer exist on disk
	for _, doc := range existingDocs {
		if isWithinDir(doc.Path, absPath) && !foundPaths[doc.Path] {
			err = dbClient.DeleteDocument(doc.Path)
			if err != nil {
				return fmt.Errorf("failed to delete orphaned document %s: %w", doc.Path, err)
			}
			fmt.Fprintf(os.Stderr, "Removed deleted file from index: %s\n", doc.Path)
		}
	}

	if len(documentIssues) > 0 {
		return fmt.Errorf("document format issues:\n%s", strings.Join(documentIssues, "\n"))
	}
	return nil
}

// RemoveDirectory removes only indexed documents whose canonical identities are
// within dirPath. It never touches the source folder on disk, so unregistering
// a source is safe even when the folder is read-only or already gone.
func RemoveDirectory(dbClient db.Store, dirPath string) (int, error) {
	absPath, err := filepath.Abs(dirPath)
	if err != nil {
		return 0, fmt.Errorf("resolve source directory: %w", err)
	}
	absPath = filepath.Clean(absPath)
	docs, err := dbClient.ListDocuments()
	if err != nil {
		return 0, fmt.Errorf("failed listing existing documents: %w", err)
	}
	removed := 0
	for _, doc := range docs {
		if !isWithinDir(doc.Path, absPath) {
			continue
		}
		if err := dbClient.DeleteDocument(doc.Path); err != nil {
			return removed, fmt.Errorf("failed to remove indexed document %s: %w", doc.Path, err)
		}
		removed++
	}
	return removed, nil
}

// IndexFileWithSource indexes sourcePath's extracted content under source. This
// is used for generated Markdown notes whose archive_path points at the
// original PDF/Office file; search results still resolve to the note path while
// retaining the original's durable location anchor.
func IndexFileWithSource(dbClient db.Store, embedder Embedder, source, sourcePath string) (string, error) {
	return IndexFileWithSourceAndMetadata(dbClient, embedder, source, sourcePath, SearchMetadata{})
}

// IndexFileWithSourceAndMetadata is the metadata-aware variant of
// IndexFileWithSource. Keeping the original API above preserves callers that do
// not have parsed vault metadata.
func IndexFileWithSourceAndMetadata(dbClient db.Store, embedder Embedder, source, sourcePath string, metadata SearchMetadata) (string, error) {
	currentHash, err := parser.GetFileHash(source)
	if err != nil {
		return "", fmt.Errorf("failed to compute hash for %s: %w", source, err)
	}
	existing, err := dbClient.GetDocument(source)
	if err != nil {
		return "", fmt.Errorf("failed to query document from DB: %w", err)
	}
	if existing != nil && existing.Hash == currentHash {
		return currentHash, nil
	}
	sections, err := parser.ParseFileSections(sourcePath)
	if err != nil {
		return "", fmt.Errorf("failed to parse %s: %w", sourcePath, err)
	}
	sections = prependSearchMetadata(sections, metadata)
	chunks := buildChunksFromSections(embedder, source, sections)
	doc := &db.Document{Path: source, Hash: currentHash, UpdatedAt: time.Now()}
	return currentHash, commitIndex(dbClient, source, chunks, doc, existing, "")
}

// IndexFileWithMetadata indexes a file with a deterministic synthetic metadata
// section followed by its source sections. Metadata-only edits therefore change
// the indexed representation and are not skipped by the file hash check.
func IndexFileWithMetadata(dbClient db.Store, embedder Embedder, path string, metadata SearchMetadata) (string, error) {
	chunks, doc, existing, skipped, _, err := prepareIndexWithMetadata(dbClient, embedder, path, metadata)
	if err != nil {
		return "", err
	}
	if skipped {
		currentHash, _ := parser.GetFileHash(path)
		return currentHash, nil
	}
	if err := commitIndex(dbClient, path, chunks, doc, existing, ""); err != nil {
		return "", err
	}
	return doc.Hash, nil
}

// IndexFile indexes a single file by delegating to the shared prepareIndex/commitIndex pipeline.
func IndexFile(dbClient db.Store, embedder Embedder, path string) (string, error) {
	chunks, doc, existing, skipped, sidecarPath, err := prepareIndex(dbClient, embedder, path)
	if err != nil {
		return "", err
	}
	if skipped {
		currentHash, _ := parser.GetFileHash(path)
		return currentHash, nil
	}
	if err := commitIndex(dbClient, path, chunks, doc, existing, sidecarPath); err != nil {
		return "", err
	}
	return doc.Hash, nil
}

// WatchDirectory watches a directory for changes and re-indexes when files change.
// It uses fsnotify for efficient event-based watching instead of polling.
func WatchDirectory(ctx context.Context, dbClient db.Store, embedder Embedder, dirPath string) error {
	absPath, err := canonicalIndexDirectory(dirPath)
	if err != nil {
		return err
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return userFriendlyError(err, "failed to create file watcher",
			"Ensure you have the necessary permissions to watch the directory")
	}
	defer watcher.Close()

	// Walk the directory and add all subdirectories to the watcher
	err = filepath.WalkDir(absPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if shouldSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return watcher.Add(path)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to setup watchers: %w", err)
	}

	// Perform initial sync
	fmt.Fprintf(os.Stderr, "Performing initial sync for: %s\n", absPath)
	if err := IndexDirectory(dbClient, embedder, absPath); err != nil {
		return fmt.Errorf("initial sync failed: %w", err)
	}

	// Count files being watched from database
	existingDocs, err := dbClient.ListDocuments()
	if err != nil {
		return fmt.Errorf("failed to list documents: %w", err)
	}
	fileCount := 0
	for _, doc := range existingDocs {
		if isWithinDir(doc.Path, absPath) {
			fileCount++
		}
	}
	fmt.Fprintf(os.Stderr, "Watching %d files in %s\n", fileCount, absPath)

	// Periodic status ticker
	statusTicker := time.NewTicker(30 * time.Second)
	defer statusTicker.Stop()
	lastChangeTime := time.Now()

	// Debounce timer to batch rapid events. The debounce window collects
	// all changed paths so a single file edit only re-indexes that file
	// (issue #46) instead of forcing a full directory crawl.
	var debounceTimer *time.Timer
	const debounceDelay = 500 * time.Millisecond
	pendingChanges := make(map[string]struct{})
	var pendingMu sync.Mutex

	for {
		select {
		case <-ctx.Done():
			return nil

		case <-statusTicker.C:
			fmt.Fprintf(os.Stderr, "Watching %d files, last change at %s\n",
				fileCount, lastChangeTime.Format("15:04:05"))

		case event, ok := <-watcher.Events:
			if !ok {
				return fmt.Errorf("watcher event channel closed")
			}
			if event.Op&fsnotify.Write == fsnotify.Write ||
				event.Op&fsnotify.Create == fsnotify.Create ||
				event.Op&fsnotify.Remove == fsnotify.Remove ||
				event.Op&fsnotify.Rename == fsnotify.Rename {

				if event.Op&fsnotify.Create == fsnotify.Create {
					info, err := os.Stat(event.Name)
					if err == nil && info.IsDir() && !shouldSkipDir(info.Name()) {
						watcher.Add(event.Name)
					}
				}

				pendingMu.Lock()
				pendingChanges[event.Name] = struct{}{}
				pendingMu.Unlock()

				lastChangeTime = time.Now()

				if debounceTimer != nil {
					debounceTimer.Stop()
				}
				debounceTimer = time.AfterFunc(debounceDelay, func() {
					pendingMu.Lock()
					changed := make(map[string]struct{}, len(pendingChanges))
					for p := range pendingChanges {
						changed[p] = struct{}{}
					}
					pendingChanges = make(map[string]struct{})
					pendingMu.Unlock()

					if err := applyIncrementalChanges(dbClient, embedder, absPath, changed); err != nil {
						fmt.Fprintf(os.Stderr, "Incremental re-index error: %v\n", err)
					}
				})
			}

		case err, ok := <-watcher.Errors:
			if !ok {
				return fmt.Errorf("watcher error channel closed")
			}
			fmt.Fprintf(os.Stderr, "Watcher error: %v\n", err)
		}
	}
}

// applyIncrementalChanges re-indexes each changed path and drops
// documents whose backing file no longer exists.
func applyIncrementalChanges(dbClient db.Store, embedder Embedder, absPath string, changed map[string]struct{}) error {
	indexed := 0
	removed := 0
	for path := range changed {
		if !isWithinDir(path, absPath) {
			continue
		}

		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				if delErr := dbClient.DeleteDocument(path); delErr != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to delete %s: %v\n", path, delErr)
				} else {
					removed++
				}
				continue
			}
			fmt.Fprintf(os.Stderr, "Warning: stat %s: %v\n", path, err)
			continue
		}

		if info.IsDir() {
			continue
		}

		ext := strings.ToLower(filepath.Ext(path))
		if !supportedExtensions[ext] {
			if parser.IsKnownDocumentExtension(ext) {
				fmt.Fprintf(os.Stderr, "%s\n", parser.UnsupportedDocumentSkipMessage(path, ext))
			}
			continue
		}

		if _, err := IndexFile(dbClient, embedder, path); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
			continue
		}
		indexed++
	}

	// A Rename event that moves a tracked file out of the watched
	// tree does not produce a second event for the original path, so
	// sweep documents under absPath and drop any whose backing file
	// has disappeared. This matches IndexDirectory's orphan behavior
	// without forcing a full directory walk.
	existingDocs, err := dbClient.ListDocuments()
	if err != nil {
		return fmt.Errorf("failed listing existing documents: %w", err)
	}
	for _, doc := range existingDocs {
		if !isWithinDir(doc.Path, absPath) {
			continue
		}
		if _, statErr := os.Stat(doc.Path); statErr == nil {
			continue
		}
		if delErr := dbClient.DeleteDocument(doc.Path); delErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to drop orphan %s: %v\n", doc.Path, delErr)
			continue
		}
		removed++
	}

	if indexed > 0 || removed > 0 {
		fmt.Fprintf(os.Stderr, "Watch: indexed=%d removed=%d\n", indexed, removed)
	}
	return nil
}

var indexParallelism = func() int {
	n := runtime.NumCPU()
	if n < 2 {
		return 2
	}
	if n > 8 {
		return 8
	}
	return n
}()

func processFilesInParallel(dbClient db.Store, embedder Embedder, paths map[string]bool) []string {
	if len(paths) == 0 {
		return nil
	}

	workers := indexParallelism
	if workers > len(paths) {
		workers = len(paths)
	}
	var documentIssues []string

	type result struct {
		path        string
		chunks      []*db.Chunk
		doc         *db.Document
		existing    *db.Document
		sidecarPath string
		err         error
		skipped     bool
	}

	jobs := make(chan string, len(paths))
	prepared := make(chan result, len(paths))

	for p := range paths {
		jobs <- p
	}
	close(jobs)

	var prepWG sync.WaitGroup
	for w := 0; w < workers; w++ {
		prepWG.Add(1)
		go func() {
			defer prepWG.Done()
			for path := range jobs {
				chunks, doc, existing, skipped, sidecarPath, err := prepareIndex(dbClient, embedder, path)
				prepared <- result{path: path, chunks: chunks, doc: doc, existing: existing, sidecarPath: sidecarPath, err: err, skipped: skipped}
			}
		}()
	}

	go func() {
		prepWG.Wait()
		close(prepared)
	}()

	for r := range prepared {
		if r.skipped {
			continue
		}
		if r.err != nil {
			fmt.Fprintf(os.Stderr, "Warning: %v\n", r.err)
			if errors.Is(r.err, documentformat.ErrDRMProtected) {
				documentIssues = append(documentIssues, fmt.Sprintf("DRM-protected document %s: %v", r.path, r.err))
			}
			continue
		}
		if err := commitIndex(dbClient, r.path, r.chunks, r.doc, r.existing, r.sidecarPath); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
			continue
		}
	}
	return documentIssues
}

func prepareIndex(dbClient db.Store, embedder Embedder, path string) ([]*db.Chunk, *db.Document, *db.Document, bool, string, error) {
	return prepareIndexWithMetadata(dbClient, embedder, path, searchMetadataForPath(path))
}

func searchMetadataForPath(path string) SearchMetadata {
	if strings.EqualFold(filepath.Ext(path), ".md") || strings.EqualFold(filepath.Ext(path), ".markdown") {
		if doc, err := vault.ParseFile(path); err == nil {
			return SearchMetadataFromVault(doc)
		}
	}
	return SearchMetadata{}
}

func prepareIndexWithMetadata(dbClient db.Store, embedder Embedder, path string, metadata SearchMetadata) ([]*db.Chunk, *db.Document, *db.Document, bool, string, error) {
	currentHash, err := parser.GetFileHash(path)
	if err != nil {
		return nil, nil, nil, false, "", fmt.Errorf("failed to compute hash for %s: %w", path, err)
	}

	existing, err := dbClient.GetDocument(path)
	if err != nil {
		return nil, nil, nil, false, "", fmt.Errorf("failed to query document from DB: %w", err)
	}
	if existing != nil && existing.Hash == currentHash {
		// An unchanged document is normally skipped — but if it still holds
		// pending (unembeddable) chunks, re-embed it now that the backend may
		// be available again (#663/#679).
		pending, perr := dbClient.CountPendingChunksForDocument(path)
		if perr == nil && pending > 0 {
			// fall through to re-index below
		} else {
			return nil, nil, existing, true, "", nil
		}
	}

	sections, err := parser.ParseFileSections(path)
	if err != nil {
		return nil, nil, nil, false, "", fmt.Errorf("failed to parse %s: %w", path, err)
	}
	metadataSections := prependSearchMetadata(sections, metadata)
	chunks := buildChunksFromSections(embedder, path, metadataSections)
	rawContent, err := parser.ParseFile(path)
	if err != nil {
		return nil, nil, nil, false, "", fmt.Errorf("failed to parse %s: %w", path, err)
	}
	sidecarPath := detectSidecarPath(path, rawContent)

	return chunks, &db.Document{
		Path:      path,
		Hash:      currentHash,
		UpdatedAt: time.Now(),
	}, existing, false, sidecarPath, nil
}

// buildChunks splits content into overlapping chunks, generates their
// embeddings, and assembles db.Chunk records. It is the single chunk-building
// pipeline shared by file/directory indexing (prepareIndex) and content-based
// indexing (indexContent for URLs and stdin).
func buildChunks(embedder Embedder, source, content string) []*db.Chunk {
	return buildChunksFromSections(embedder, source, []parser.Section{{
		Text: content, Anchor: parser.Anchor{Kind: "text", Value: "offset:0"},
	}})
}

func joinSectionText(sections []parser.Section) string {
	parts := make([]string, 0, len(sections))
	for _, section := range sections {
		if strings.TrimSpace(section.Text) != "" {
			parts = append(parts, section.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// buildChunksFromSections keeps chunks inside the parser's durable boundaries
// (PDF pages and Markdown heading sections), while preserving global byte spans.
func buildChunksFromSections(embedder Embedder, source string, sections []parser.Section) []*db.Chunk {
	var spans []struct {
		span      parser.Span
		anchor    parser.Anchor
		synthetic bool
	}
	for _, section := range sections {
		for _, span := range parser.SplitTextWithSpans(section.Text, 1000, 200) {
			if !section.Synthetic {
				span.Start += section.Start
				span.End += section.Start
			}
			anchor := section.Anchor
			if anchor.Kind == "text" && !section.Synthetic {
				anchor.Value = fmt.Sprintf("offset:%d", span.Start)
			}
			spans = append(spans, struct {
				span      parser.Span
				anchor    parser.Anchor
				synthetic bool
			}{span: span, anchor: anchor, synthetic: section.Synthetic})
		}
	}
	textChunks := make([]string, len(spans))
	for i, item := range spans {
		textChunks[i] = item.span.Text
	}
	embeddings := embedder.GenerateVectorsWithModel(textChunks)

	chunks := make([]*db.Chunk, 0, len(textChunks))
	for idx, tc := range textChunks {
		hashSum := sha256.Sum256([]byte(tc))
		chunkHash := hex.EncodeToString(hashSum[:])
		start, end := spans[idx].span.Start, spans[idx].span.End
		res := embeddings[idx]
		var startPtr, endPtr *int
		if !spans[idx].synthetic {
			startPtr, endPtr = &start, &end
		}
		chunk := &db.Chunk{
			UUID:         deriveChunkID(source, chunkHash, start),
			DocumentPath: source,
			ChunkIndex:   idx,
			Content:      tc,
			Embedding:    res.Vector,
			Hash:         chunkHash,
			Dim:          len(res.Vector),
			Model:        res.Model,
			CharStart:    startPtr,
			CharEnd:      endPtr,
			AnchorKind:   spans[idx].anchor.Kind,
			AnchorValue:  spans[idx].anchor.Value,
		}
		if res.Model == localHashModelName {
			chunk.EmbeddingPending = true
			chunk.Embedding = nil
			chunk.Dim = 0
		}
		chunks = append(chunks, chunk)
	}
	return chunks
}

// commitIndex persists a prepared document and its chunks to the database.
// The caller must pass the already-fetched existing document (or nil for new
// documents) so commitIndex avoids a redundant GetDocument round-trip.
// sidecarPath, when non-empty, is a detected extraction sidecar to import
// after the chunks are saved (so chunk spans are available for linking).
func commitIndex(dbClient db.Store, path string, chunks []*db.Chunk, doc *db.Document, existing *db.Document, sidecarPath string) error {
	if existing != nil {
		if err := dbClient.DeleteDocument(path); err != nil {
			return fmt.Errorf("failed to delete old document version: %w", err)
		}
	}

	if err := dbClient.SaveDocument(doc); err != nil {
		return fmt.Errorf("failed to save document metadata: %w", err)
	}
	if err := dbClient.SaveChunks(chunks); err != nil {
		return fmt.Errorf("failed to save chunks: %w", err)
	}

	if sidecarPath != "" {
		if err := ImportExtractionSidecar(dbClient, path, sidecarPath); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to import extraction sidecar %s for %s: %v\n", sidecarPath, path, err)
		} else {
			fmt.Fprintf(os.Stderr, "Imported extraction sidecar: %s\n", sidecarPath)
		}
	}

	fallbackCount := 0
	for _, c := range chunks {
		if c.EmbeddingPending {
			fallbackCount++
		}
	}
	if fallbackCount > 0 {
		fmt.Fprintf(os.Stderr, "Indexed: %s (%d chunks, %d pending — embedding backend unavailable, will re-embed when available)\n", path, len(chunks), fallbackCount)
	} else {
		fmt.Fprintf(os.Stderr, "Indexed: %s (%d chunks)\n", path, len(chunks))
	}
	return nil
}
