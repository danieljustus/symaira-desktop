// Package retrieval maintains the vault's hybrid search index.
//
// The retrieval engine is the absorbed SymSeek pipeline, which lives directly in
// internal/retrieval/ (with internal subpackages config, db, engine, errors,
// parser, pathutil). It is linked in-process — there is no symseek binary to
// find, no PATH probe, and no subprocess.
//
// Indexing is best-effort by design: a vault write must succeed even when the
// index cannot be updated (a missing embedding backend, a locked database).
// Callers therefore get a nil-safe degradation rather than a hard failure, and
// the sidecar index in internal/sidecar remains the authoritative one.
package retrieval

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	desktopconfig "github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/retrieval/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/retrieval/internal/db"
	"github.com/danieljustus/symaira-desktop/internal/retrieval/internal/engine"
)

// LocationAnchor is re-exported so service and transport consumers do not
// depend on the retrieval database's internal package.
type LocationAnchor = db.LocationAnchor

// SearchMetadata is the stable metadata contract added to each local file's
// hybrid-search representation. Fields are sorted into a canonical order for
// deterministic repeatable indexing.
type SearchMetadata = engine.SearchMetadata

// SearchMetadataField is one field in SearchMetadata.
type SearchMetadataField = engine.SearchMetadataField

// Result is one search hit, reduced to the fields a consumer displays.
type Result struct {
	Path    string  `json:"path"`
	Score   float64 `json:"score"`
	Snippet string  `json:"snippet"`
	// Anchor is omitted for legacy index rows that predate location metadata.
	Anchor *db.LocationAnchor `json:"anchor,omitempty"`
	// MetadataMatches identifies the indexed fields that matched the free-text
	// query, allowing consumers to distinguish metadata-only hits from body hits.
	MetadataMatches []string `json:"metadata_matches,omitempty"`
	// VectorMode reports how the vector leg of the search was scored:
	// "semantic" for a real embedding model, "fallback" when the query fell
	// back to the local hash vector while the index uses an Ollama model, or
	// empty when only the keyword leg contributed. It lets a consumer warn the
	// user that semantic scores may be unreliable (#663/#681).
	VectorMode string `json:"vector_mode,omitempty"`
}

// DefaultLimit is the number of hits Search returns by default.
const DefaultLimit = 5

// Client holds an open index plus the configured embedding backend. Use it
// when issuing several calls in a row; a caller making a single call can use
// the package-level Index/Delete/Search helpers instead.
type Client struct {
	db       *db.DB
	embedder engine.Embedder
	expand   engine.ExpandConfig
	// indexPath is the resolved database path this client actually opened —
	// the legacy shared path, an explicit cfg.IndexPath override, or a
	// per-vault path from OpenForVault. Status() reports this directly
	// instead of recomputing IndexLocation(), which only knows the legacy
	// (vault-agnostic) resolution and would otherwise misreport a per-vault
	// client's location (#756).
	indexPath string
	// vaultScoped is true when this client was opened via OpenForVault at
	// its computed per-vault path (not a plain Open or an explicit
	// cfg.IndexPath override), and drives Status().IndexScope (#756).
	vaultScoped bool
}

// Open opens the local index and prepares the configured embedding backend.
// The caller must Close the returned client.
//
// This always resolves to the legacy shared index (or the cfg.IndexPath
// override): it has no vault to scope to. Callers that know their vault
// should call OpenForVault instead, which is what service.Service does; Open
// remains for standalone maintenance tooling with no vault context (index
// backup/restore/relocate) and as the fallback openActive uses when no vault
// is active (#756).
func Open() (*Client, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	indexPath, err := configuredIndexPath(cfg)
	if err != nil {
		return nil, err
	}
	return openClientAt(cfg, indexPath)
}

// OpenForVault opens the retrieval index scoped to vaultRoot:
// <SidecarRoot>/<hash>/retrieval.db, right next to the FTS sidecar's own
// <hash>/sidecar.db for the same vault (see vaultIndexPath). cfg.IndexPath,
// when set, remains an explicit override and takes priority over the
// computed per-vault default, exactly as it did for the legacy shared index.
//
// On first open of the computed per-vault path, a pre-existing legacy shared
// index (db.DefaultPath()) is moved into it, so upgrading users keep their
// already-built index instead of starting over (#756).
func OpenForVault(vaultRoot string) (*Client, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if cfg != nil && strings.TrimSpace(cfg.IndexPath) != "" {
		return Open()
	}
	indexPath, err := vaultIndexPath(vaultRoot)
	if err != nil {
		return nil, err
	}
	if err := migrateLegacyIndex(indexPath); err != nil {
		return nil, fmt.Errorf("migrate legacy retrieval index: %w", err)
	}
	client, err := openClientAt(cfg, indexPath)
	if err != nil {
		return nil, err
	}
	client.vaultScoped = true
	return client, nil
}

// openActive is used only by the package-level one-shot helpers. Callers
// with vault context, such as service.Service, own a Client opened with
// OpenForVault instead of sharing mutable package state.
func openActive() (*Client, error) {
	return Open()
}

func openClientAt(cfg *config.Config, indexPath string) (*Client, error) {
	dbClient, err := db.OpenAt(indexPath)
	if err != nil {
		return nil, err
	}
	return &Client{
		db:        dbClient,
		embedder:  engine.NewEmbeddingsGeneratorWithOllamaConfig(cfg.OllamaConfig()),
		expand:    cfg.ExpandConfig(),
		indexPath: indexPath,
	}, nil
}

// configuredIndexPath resolves the legacy shared index location: the
// cfg.IndexPath override when set, otherwise db.DefaultPath(). It has no
// vault context; see vaultIndexPath for the per-vault equivalent.
func configuredIndexPath(cfg *config.Config) (string, error) {
	if cfg != nil && strings.TrimSpace(cfg.IndexPath) != "" {
		path, err := filepath.Abs(cfg.IndexPath)
		if err != nil {
			return "", fmt.Errorf("absolute index path: %w", err)
		}
		return filepath.Clean(path), nil
	}
	return db.DefaultPath()
}

// vaultIndexPath returns <SidecarRoot>/<hash>/retrieval.db for vaultRoot,
// resolved through the shared internal/config path resolver.
func vaultIndexPath(vaultRoot string) (string, error) {
	return desktopconfig.RetrievalPath(vaultRoot)
}

// migrateLegacyIndex performs the one-time upgrade from the absorbed
// retrieval store's legacy shared path. It returns an error for a failed move
// instead of opening a fresh database and silently losing the old index.
func migrateLegacyIndex(newPath string) error {
	if _, err := os.Stat(newPath); err == nil || !os.IsNotExist(err) {
		// Already migrated, already has data, or unreadable: leave it alone.
		return nil
	}
	legacyPath, err := db.DefaultPath()
	if err != nil {
		return err
	}
	if filepath.Clean(legacyPath) == filepath.Clean(newPath) {
		return nil
	}
	info, err := os.Stat(legacyPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("legacy retrieval index is not a regular file: %s", legacyPath)
	}
	if err := copyIndexFile(legacyPath, newPath); err != nil {
		return err
	}
	if err := os.Remove(legacyPath); err != nil {
		if cleanupErr := os.Remove(newPath); cleanupErr != nil {
			return fmt.Errorf("remove migrated legacy retrieval index: %w (remove incomplete destination: %v)", err, cleanupErr)
		}
		return fmt.Errorf("remove migrated legacy retrieval index: %w", err)
	}
	slog.Info("migrated legacy retrieval index to per-vault location", "legacy_path", legacyPath, "new_path", newPath)
	return nil
}

// IndexPathForVault returns the retrieval index path OpenForVault would use
// for vaultRoot, without opening the database or migrating a legacy index.
// It exists for reporting commands (`symdesk doctor`) that need the path
// without a side-effecting open (#756).
func IndexPathForVault(vaultRoot string) (string, error) {
	cfg, err := config.Load()
	if err != nil {
		return "", err
	}
	if cfg != nil && strings.TrimSpace(cfg.IndexPath) != "" {
		return configuredIndexPath(cfg)
	}
	return vaultIndexPath(vaultRoot)
}

// Close releases the index handle.
func (c *Client) Close() error {
	if c == nil || c.db == nil {
		return nil
	}
	return c.db.Close()
}

// IndexDirectory indexes a read-only external folder in place. The folder is
// never copied or modified; its canonical file paths are the search identities.
func (c *Client) IndexDirectory(dirPath string) error {
	return engine.IndexDirectory(c.db, c.embedder, dirPath)
}

// WatchDirectory keeps an external folder incrementally synchronized until ctx
// is cancelled. It never writes to the watched folder.
func (c *Client) WatchDirectory(ctx context.Context, dirPath string) error {
	return engine.WatchDirectory(ctx, c.db, c.embedder, dirPath)
}

// RemoveDirectory removes indexed documents under dirPath without touching the
// folder on disk.
func (c *Client) RemoveDirectory(dirPath string) (int, error) {
	return engine.RemoveDirectory(c.db, dirPath)
}

// Index adds or replaces one document in the index. The source label is the
// document's stable identity (symdesk passes the vault-relative path), and
// body is its full text.
func (c *Client) Index(source, body string) error {
	// Prefer the source file when available: format-aware parsing retains page
	// and heading anchors. Body indexing remains the compatibility fallback
	// for URLs, stdin labels, and callers whose source is not a local file.
	if info, err := os.Stat(source); err == nil && info.Mode().IsRegular() {
		if archivePath := archivePathFromMarkdown(source); archivePath != "" {
			if archiveInfo, archiveErr := os.Stat(archivePath); archiveErr == nil && archiveInfo.Mode().IsRegular() {
				_, err := engine.IndexFileWithSource(c.db, c.embedder, source, archivePath)
				return err
			}
		}
		_, err := engine.IndexFile(c.db, c.embedder, source)
		return err
	}
	return engine.IndexStdin(c.db, c.embedder, strings.NewReader(body), source)
}

func archivePathFromMarkdown(path string) string {
	data, err := os.ReadFile(path) // #nosec G304 -- path is constrained by the indexed document boundary.
	if err != nil {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return ""
	}
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "---" {
			break
		}
		if key, value, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(key) == "archive_path" {
			return strings.Trim(strings.TrimSpace(value), "\"'")
		}
	}
	return ""
}

// IndexWithMetadata adds or replaces one local document and includes the
// supplied metadata in its hybrid-search representation. Non-local sources
// use the compatibility body-only path because they have no vault metadata
// file to update.
func (c *Client) IndexWithMetadata(source, body string, metadata SearchMetadata) error {
	if info, err := os.Stat(source); err == nil && info.Mode().IsRegular() {
		if archivePath := archivePathFromMarkdown(source); archivePath != "" {
			if archiveInfo, archiveErr := os.Stat(archivePath); archiveErr == nil && archiveInfo.Mode().IsRegular() {
				_, err := engine.IndexFileWithSourceAndMetadata(c.db, c.embedder, source, archivePath, metadata)
				return err
			}
		}
		_, err := engine.IndexFileWithMetadata(c.db, c.embedder, source, metadata)
		return err
	}
	return engine.IndexStdin(c.db, c.embedder, strings.NewReader(body), source)
}

// Delete removes a document and its chunks from the index. Deleting a
// document that is not indexed is not an error.
func (c *Client) Delete(path string) error {
	existing, err := c.db.GetDocument(path)
	if err != nil {
		return err
	}
	if existing == nil {
		return nil
	}
	return c.db.DeleteDocument(path)
}

// ReembedPending re-embeds every document that still holds pending
// (unembeddable) chunks now that the backend is available, returning the
// number of documents repaired (#663/#679).
func (c *Client) ReembedPending() (int, error) {
	return engine.ReembedPending(c.db, c.embedder)
}

// ReembedPending is the package-level entry point for the forced re-embed
// repair path; see Client.ReembedPending.
func ReembedPending() (int, error) {
	c, err := openActive()
	if err != nil {
		return 0, err
	}
	defer func() { _ = c.Close() }()
	return c.ReembedPending()
}

// CountPendingChunks returns how many chunks in the hybrid index are still
// pending (unembeddable fallback placeholders) — used by the doctor check
// (#663/#680) to surface a degraded index without opening the app.
func CountPendingChunks() (int, error) {
	return CountPendingChunksFunc()
}

// defaultCountPendingChunks is the production implementation behind
// CountPendingChunksFunc; it opens the index and reads the pending count.
func defaultCountPendingChunks() (int, error) {
	c, err := openActive()
	if err != nil {
		return 0, err
	}
	defer func() { _ = c.Close() }()
	return c.db.CountPendingChunks()
}

// Search runs the hybrid keyword+vector search and returns at most limit hits,
// each with a query-centered snippet. A limit <= 0 means DefaultLimit.
func (c *Client) Search(query string, limit int) ([]Result, error) {
	return c.searchWithOptions(query, limit, engine.SearchOptions{ExpandCfg: c.expand})
}

// SearchInPaths searches only documents below the supplied canonical directory
// roots. The retrieval database is shared across vaults, so callers must use
// this scoped entry point when combining a vault with its registered external
// sources.
func (c *Client) SearchInPaths(query string, paths []string, limit int) ([]Result, error) {
	if limit <= 0 {
		limit = DefaultLimit
	}
	if len(paths) == 0 {
		return []Result{}, nil
	}
	byPath := make(map[string]Result)
	for _, path := range paths {
		absPath, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("resolve search scope %q: %w", path, err)
		}
		absPath = filepath.Clean(absPath)
		if linkInfo, lstatErr := os.Lstat(absPath); lstatErr == nil && linkInfo.Mode()&os.ModeSymlink != 0 {
			if canonical, evalErr := filepath.EvalSymlinks(absPath); evalErr == nil {
				absPath = filepath.Clean(canonical)
			}
		}
		hits, err := c.searchWithOptions(query, limit, engine.SearchOptions{
			ExpandCfg:  c.expand,
			PathFilter: absPath + string(os.PathSeparator),
		})
		if err != nil {
			return nil, err
		}
		for _, hit := range hits {
			if _, exists := byPath[hit.Path]; !exists {
				byPath[hit.Path] = hit
			}
		}
	}
	out := make([]Result, 0, len(byPath))
	for _, result := range byPath {
		out = append(out, result)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (c *Client) searchWithOptions(query string, limit int, opts engine.SearchOptions) ([]Result, error) {
	if limit <= 0 {
		limit = DefaultLimit
	}
	hits, err := engine.SearchHybridWithOptions(c.db, c.db, c.embedder, query, limit, opts)
	if err != nil {
		return nil, err
	}
	terms := strings.Fields(query)
	out := make([]Result, 0, len(hits))
	for _, h := range hits {
		s := h.Structured()
		if s == nil {
			continue
		}
		out = append(out, Result{
			Path:            s.Path,
			Score:           float64(s.Score),
			Snippet:         engine.StripSearchMetadata(engine.BuildSnippet(s.Snippet, terms, engine.DefaultSnippetBound)),
			Anchor:          s.Anchor,
			MetadataMatches: s.MetadataMatches,
			VectorMode:      s.VectorMode,
		})
	}
	return out, nil
}

// Status is a snapshot of the local index and its embedding backend. It is
// what a consumer needs to tell "no hits because nothing matches" from "no
// hits because retrieval is degraded".
type Status struct {
	// DocumentCount and ChunkCount describe how much is indexed.
	DocumentCount int `json:"document_count"`
	ChunkCount    int `json:"chunk_count"`
	// DatabaseBytes is the on-disk size of the index.
	DatabaseBytes int64 `json:"database_bytes"`
	// LastIndexedAt is the most recent document update, empty when the
	// index carries no such metadata.
	LastIndexedAt string `json:"last_indexed_at,omitempty"`
	// EmbeddingModel is the model the backend actually answered with.
	EmbeddingModel string `json:"embedding_model"`
	// BackendAvailable reports whether the configured embedding backend
	// answered. When false, the model above is the local hash fallback and
	// search quality is degraded even though queries still return.
	BackendAvailable bool `json:"backend_available"`
	// PendingChunkCount reports stored chunks that still need a real
	// embedding. MixedEmbeddingSpaces reports whether the stored vectors use
	// more than one dimension/model pair. Both describe persisted index state,
	// unlike the live backend probe above.
	PendingChunkCount    int  `json:"pending_chunk_count"`
	MixedEmbeddingSpaces bool `json:"mixed_embedding_spaces"`
	// IndexScope is explicit because a client opened via the legacy Open
	// resolves to an index shared across vaults ("shared"); document counts
	// there must not be read as active-vault counts. A client opened via
	// OpenForVault reports "vault" since its index is exclusive to that one
	// vault (#756).
	IndexScope string `json:"index_scope"`
	// VaultDocumentCount is populated by the CLI when an active vault can be
	// enumerated. It is a comparison figure for the shared index.
	VaultDocumentCount *int   `json:"vault_document_count,omitempty"`
	IndexLocation      string `json:"index_location"`
}

// Status reports the index snapshot plus a live probe of the embedding
// backend. The probe embeds a fixed short string without retrying, so it
// costs one request and never blocks an interactive caller for long.
func (c *Client) Status() (*Status, error) {
	stats, err := c.db.GetStats()
	if err != nil {
		return nil, err
	}
	pending, err := c.db.CountPendingChunks()
	if err != nil {
		return nil, err
	}
	spaces, err := c.db.DetectMixedEmbeddingSpaces()
	if err != nil {
		return nil, err
	}
	probe := c.embedder.GenerateVectorNoRetryWithModel("symdesk retrieval status probe")
	scope := "shared"
	if c.vaultScoped {
		scope = "vault"
	}
	status := &Status{
		DocumentCount:        stats.DocumentCount,
		ChunkCount:           stats.ChunkCount,
		DatabaseBytes:        stats.DatabaseSize,
		EmbeddingModel:       probe.Model,
		BackendAvailable:     probe.Model != engine.LocalHashModelName,
		PendingChunkCount:    pending,
		MixedEmbeddingSpaces: len(spaces) > 1,
		IndexScope:           scope,
		IndexLocation:        c.indexPath,
	}
	if !stats.LastIndexedAt.IsZero() {
		status.LastIndexedAt = stats.LastIndexedAt.UTC().Format(time.RFC3339)
	}
	return status, nil
}

func defaultIndex(source, body string) error {
	c, err := openActive()
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	return c.Index(source, body)
}

func defaultDelete(path string) error {
	c, err := openActive()
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	return c.Delete(path)
}

func defaultSearch(query string, limit int) ([]Result, error) {
	c, err := openActive()
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()
	return c.Search(query, limit)
}

func defaultStatus() (*Status, error) {
	c, err := openActive()
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()
	return c.Status()
}

// The engine seam. These are the single injectable entry points into the
// hybrid index, so a test can substitute a double without touching the
// user's real index (which lives under $HOME, not in a temp dir).
//
// Production code never reassigns them; a test that does must restore the
// original in t.Cleanup.
var (
	DefaultIndexFunc  = defaultIndex
	DefaultDeleteFunc = defaultDelete
	DefaultSearchFunc = defaultSearch
	DefaultStatusFunc = defaultStatus
	// DefaultCountPendingChunksFunc is the production implementation of
	// CountPendingChunksFunc; see CountPendingChunks (#663/#680).
	DefaultCountPendingChunksFunc = defaultCountPendingChunks
	IndexFunc                     = DefaultIndexFunc
	DeleteFunc                    = DefaultDeleteFunc
	SearchFunc                    = DefaultSearchFunc
	StatusFunc                    = DefaultStatusFunc
	CountPendingChunksFunc        = DefaultCountPendingChunksFunc
)

// IndexWithMetadata adds or replaces a local document with its parsed vault
// metadata included in the hybrid index. Errors are returned so callers that
// already maintain an authoritative sidecar can log them as best effort.
func IndexWithMetadata(path, body string, metadata SearchMetadata) error {
	c, err := openActive()
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	return c.IndexWithMetadata(path, body, metadata)
}

// IndexDirectory indexes one external folder using the configured retrieval
// client. Unlike Index, errors are returned because registration callers need
// to report a failed initial pass.
func IndexDirectory(dirPath string) error {
	c, err := openActive()
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	return c.IndexDirectory(dirPath)
}

// RemoveDirectory removes indexed documents under dirPath and leaves the
// external folder untouched.
func RemoveDirectory(dirPath string) (int, error) {
	c, err := openActive()
	if err != nil {
		return 0, err
	}
	defer func() { _ = c.Close() }()
	return c.RemoveDirectory(dirPath)
}

// WatchDirectory watches one external folder until ctx is cancelled.
func WatchDirectory(ctx context.Context, dirPath string) error {
	c, err := openActive()
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	return c.WatchDirectory(ctx, dirPath)
}

// Index adds or replaces one document in the search index. A failure is
// logged and swallowed: the document is already stored in the vault and in
// the sidecar index, and a degraded hybrid search must not fail the write.
func Index(path, body string) {
	if err := IndexFunc(path, body); err != nil {
		slog.Warn("search index update failed", "path", path, "error", err)
	}
}

// Delete removes one document from the search index. As with Index, a
// failure is logged rather than propagated.
func Delete(path string) {
	if err := DeleteFunc(path); err != nil {
		slog.Warn("search index delete failed", "path", path, "error", err)
	}
}

// SearchInPaths searches only documents below the supplied directory roots.
// Errors are returned so callers can distinguish an unavailable shared index
// from a valid empty result.
func SearchInPaths(query string, paths []string, limit int) ([]Result, error) {
	c, err := openActive()
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()
	return c.SearchInPaths(query, paths, limit)
}

// Search runs the hybrid keyword+vector search. It returns nil (not an error)
// when the index cannot be reached, so callers fall back to the sidecar
// full-text search exactly as they did when symseek was an optional sibling.
func Search(query string) []Result {
	results, err := SearchFunc(query, DefaultLimit)
	if err != nil {
		slog.Debug("hybrid search unavailable, falling back to sidecar", "error", err)
		return nil
	}
	return results
}

// CurrentStatus reports the hybrid index snapshot and whether the embedding
// backend answered. Unlike Index/Delete/Search this does propagate its error:
// a caller asking for the health of retrieval must be told when even that
// cannot be determined, rather than shown a confident zero.
func CurrentStatus() (*Status, error) {
	return StatusFunc()
}
