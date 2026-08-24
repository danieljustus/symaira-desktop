// Package retrieval maintains the vault's hybrid search index.
//
// The retrieval engine is symseek, which lives in this repository as the
// nested seek/ module since the repo consolidation. It is linked in-process
// through seek/api — there is no symseek binary to find and no subprocess.
//
// Indexing is best-effort by design: a vault write must succeed even when the
// index cannot be updated (a missing embedding backend, a locked database).
// Callers therefore get a nil-safe degradation rather than a hard failure, and
// the sidecar index in internal/sidecar remains the authoritative one.
package retrieval

import (
	"log/slog"

	seekapi "github.com/danieljustus/symaira-seek/api"
)

// Result is one search hit.
type Result = seekapi.Result

// Status is a snapshot of the hybrid index and its embedding backend.
type Status = seekapi.Status

// DefaultLimit is the number of hits Search returns by default.
const DefaultLimit = seekapi.DefaultLimit

// The engine seam. These are the single injectable entry points into the
// hybrid index, so a test can substitute a double without touching the
// user's real index (which lives under $HOME, not in a temp dir).
//
// Production code never reassigns them; a test that does must restore the
// original in t.Cleanup.
var (
	IndexFunc  = seekapi.Index
	DeleteFunc = seekapi.Delete
	SearchFunc = seekapi.Search
	StatusFunc = seekapi.CurrentStatus
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
