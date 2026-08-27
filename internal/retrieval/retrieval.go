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
	"log/slog"
	"strings"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/retrieval/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/retrieval/internal/db"
	"github.com/danieljustus/symaira-desktop/internal/retrieval/internal/engine"
)

// Result is one search hit, reduced to the fields a consumer displays.
type Result struct {
	Path    string  `json:"path"`
	Score   float64 `json:"score"`
	Snippet string  `json:"snippet"`
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
}

// Open opens the local index and prepares the configured embedding backend.
// The caller must Close the returned client.
func Open() (*Client, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	dbClient, err := db.Open()
	if err != nil {
		return nil, err
	}
	return &Client{
		db:       dbClient,
		embedder: engine.NewEmbeddingsGeneratorWithOllamaConfig(cfg.OllamaConfig()),
		expand:   cfg.ExpandConfig(),
	}, nil
}

// Close releases the index handle.
func (c *Client) Close() error {
	if c == nil || c.db == nil {
		return nil
	}
	return c.db.Close()
}

// Index adds or replaces one document in the index. The source label is the
// document's stable identity (symdesk passes the vault-relative path), and
// body is its full text.
func (c *Client) Index(source, body string) error {
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
	c, err := Open()
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
	c, err := Open()
	if err != nil {
		return 0, err
	}
	defer func() { _ = c.Close() }()
	return c.db.CountPendingChunks()
}

// Search runs the hybrid keyword+vector search and returns at most limit hits,
// each with a query-centered snippet. A limit <= 0 means DefaultLimit.
func (c *Client) Search(query string, limit int) ([]Result, error) {
	if limit <= 0 {
		limit = DefaultLimit
	}
	hits, err := engine.SearchHybridWithOptions(c.db, c.db, c.embedder, query, limit,
		engine.SearchOptions{ExpandCfg: c.expand})
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
			Path:    s.Path,
			Score:   float64(s.Score),
			Snippet: engine.BuildSnippet(s.Snippet, terms, engine.DefaultSnippetBound),
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
}

// Status reports the index snapshot plus a live probe of the embedding
// backend. The probe embeds a fixed short string without retrying, so it
// costs one request and never blocks an interactive caller for long.
func (c *Client) Status() (*Status, error) {
	stats, err := c.db.GetStats()
	if err != nil {
		return nil, err
	}
	probe := c.embedder.GenerateVectorNoRetryWithModel("symdesk retrieval status probe")
	status := &Status{
		DocumentCount:    stats.DocumentCount,
		ChunkCount:       stats.ChunkCount,
		DatabaseBytes:    stats.DatabaseSize,
		EmbeddingModel:   probe.Model,
		BackendAvailable: probe.Model != engine.LocalHashModelName,
	}
	if !stats.LastIndexedAt.IsZero() {
		status.LastIndexedAt = stats.LastIndexedAt.UTC().Format(time.RFC3339)
	}
	return status, nil
}

func defaultIndex(source, body string) error {
	c, err := Open()
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	return c.Index(source, body)
}

func defaultDelete(path string) error {
	c, err := Open()
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	return c.Delete(path)
}

func defaultSearch(query string, limit int) ([]Result, error) {
	c, err := Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()
	return c.Search(query, limit)
}

func defaultStatus() (*Status, error) {
	c, err := Open()
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
