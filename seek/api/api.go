// Package api is the stable in-process entry point into symseek.
//
// symseek's retrieval logic lives in internal/ packages, which Go's import
// rules make unreachable from other modules. Consumers that link this module
// rather than executing the symseek binary — symdesk since the repo
// consolidation — go through this package instead.
//
// Every call reads the user's symseek configuration, so an embedded consumer
// shares one index and one embedding backend with the CLI rather than opening
// a private one.
package api

import (
	"strings"

	"github.com/danieljustus/symaira-seek/internal/config"
	"github.com/danieljustus/symaira-seek/internal/db"
	"github.com/danieljustus/symaira-seek/internal/engine"
)

// Result is one search hit, reduced to the fields a consumer displays.
type Result struct {
	Path    string  `json:"path"`
	Score   float64 `json:"score"`
	Snippet string  `json:"snippet"`
}

// DefaultLimit is the number of hits Search returns when limit <= 0. It
// matches the symseek CLI default.
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

// Index opens the index, adds or replaces one document, and closes again.
func Index(source, body string) error {
	c, err := Open()
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	return c.Index(source, body)
}

// Delete opens the index, removes one document, and closes again.
func Delete(path string) error {
	c, err := Open()
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	return c.Delete(path)
}

// Search opens the index, runs one hybrid search, and closes again.
func Search(query string, limit int) ([]Result, error) {
	c, err := Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()
	return c.Search(query, limit)
}
