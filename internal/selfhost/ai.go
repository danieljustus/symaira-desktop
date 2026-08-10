package selfhost

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/ai"
	"github.com/danieljustus/symaira-desktop/internal/service"
)

// This file implements the streaming AI endpoints:
//
//	POST /api/v1/ai/ask       — vault-grounded question answering
//	POST /api/v1/ai/transform — intent-based text transformation
//
// Both emit the existing AIEvent NDJSON contract (the same event types
// `symdesk ask --json` writes), so clients that already parse AIEvent —
// the iOS chat surface, the CLI — consume the stream without a second
// format. Retrieval is scoped by the caller's permissions exactly like
// handleSnapshot: a user without read access to a document never receives
// it as context or as a citation.

const (
	aiAskTimeout     = 10 * time.Minute
	aiMaxQueryBytes  = 8 << 10
	aiMaxTextBytes   = 256 << 10
	aiMaxContextDocs = 8
	aiRateWindow     = 30 * time.Second
	aiRateMax        = 12
	aiRateBlock      = 60 * time.Second
)

type aiAskRequest struct {
	Query string `json:"query"`
	// Notebook is optional: when set, retrieval and citations are
	// restricted to that notebook's sources instead of the whole vault
	// (issue #428). Omitted or empty behaves exactly like before.
	Notebook string `json:"notebook,omitempty"`
}

type aiTransformRequest struct {
	Text   string `json:"text"`
	Intent string `json:"intent"`
}

// handleAIAsk streams a grounded answer as NDJSON AIEvents. Citations carry
// vault-relative paths the client can resolve through GET /api/v1/files.
func (s *Server) handleAIAsk(w http.ResponseWriter, r *http.Request) {
	if !s.allowAIRateLimited(w, r) {
		return
	}
	var request aiAskRequest
	if err := decodeJSON(r, &request, aiMaxQueryBytes); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	request.Query = strings.TrimSpace(request.Query)
	if request.Query == "" {
		writeError(w, http.StatusBadRequest, "query is required")
		return
	}

	user := userFromContext(r)
	svc := service.New(s.cfg.VaultRoot, s.db)
	var results []service.SearchResult
	var scopedPaths []string
	var retrievalErr error
	if request.Notebook != "" {
		results, scopedPaths, retrievalErr = svc.ScopedSearchResults(request.Notebook, request.Query)
	} else {
		results, retrievalErr = svc.Search(request.Query)
	}
	if retrievalErr != nil {
		writeError(w, http.StatusInternalServerError, "retrieval failed: "+retrievalErr.Error())
		return
	}

	// Permission scope: keep only documents the caller may read, in the
	// same order the search ranked them.
	allowed := s.perm.CanReadMany(user, pathsOf(results))
	allowedSet := make(map[string]bool, len(allowed))
	for _, p := range allowed {
		allowedSet[p] = true
	}
	contextDocs := make([]map[string]interface{}, 0, min(len(results), aiMaxContextDocs))
	for _, result := range results {
		if !allowedSet[result.Path] {
			continue
		}
		contextDocs = append(contextDocs, map[string]interface{}{
			"path":    result.Path,
			"title":   result.Title,
			"snippet": result.Snippet,
			"score":   result.Score,
		})
		if len(contextDocs) >= aiMaxContextDocs {
			break
		}
	}

	// Citation-warning readPaths (notebook-scoped requests only) must go
	// through the same permission filter as contextDocs — a source the
	// caller cannot read was never actually part of this run's grounding.
	var readPaths []string
	if request.Notebook != "" {
		for _, p := range scopedPaths {
			if allowedSet[p] {
				readPaths = append(readPaths, p)
			}
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), aiAskTimeout)
	defer cancel()

	writer := newNDJSONWriter(w)
	if !writer.begin() {
		return
	}
	writer.event(ai.ToolEvent("search", "running"))
	writer.event(ai.ToolEvent("search", "done"))
	for _, doc := range contextDocs {
		writer.event(ai.CitationEvent(
			doc["path"].(string),
			doc["title"].(string),
			doc["snippet"].(string),
			doc["score"].(float64),
		))
	}

	chunks := make(chan ai.AskChunk)
	go ai.Ask(ctx, svc.Config, request.Query, contextDocs, chunks)

	writer.event(ai.ToolEvent("llm", "running"))
	var answer strings.Builder
	for {
		select {
		case <-ctx.Done():
			// Client disconnected or deadline hit: abort the upstream
			// model request (ai.Ask cancels streamLLM via ctx). Drain the
			// channel so ai.Ask's goroutine can finish and close it.
			for range chunks {
			}
			return
		case chunk, ok := <-chunks:
			if !ok {
				writer.event(ai.ToolEvent("llm", "done"))
				if request.Notebook != "" {
					warnings := ai.CheckCitationWarningsSafe(answer.String(), readPaths)
					writer.event(ai.AIEvent{Type: ai.AIEventDone, CitationWarnings: warnings, ReadPaths: readPaths})
				} else {
					writer.event(ai.DoneEvent())
				}
				return
			}
			answer.WriteString(chunk.Chunk)
			if !writer.event(ai.AnswerEvent(chunk.Chunk)) {
				// Client went away mid-stream: drain so the producer
				// goroutine can finish and close chunks instead of leaking.
				for range chunks {
				}
				return
			}
		}
	}
}

// handleAITransform streams an intent-based transformation
// (summarize | rewrite | continue) of the provided text. It operates
// purely on the text and never touches the vault, so no retrieval
// permissions apply — authentication and rate limiting still do.
func (s *Server) handleAITransform(w http.ResponseWriter, r *http.Request) {
	if !s.allowAIRateLimited(w, r) {
		return
	}
	var request aiTransformRequest
	if err := decodeJSON(r, &request, aiMaxTextBytes); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	request.Text = strings.TrimSpace(request.Text)
	request.Intent = strings.TrimSpace(request.Intent)
	if request.Text == "" {
		writeError(w, http.StatusBadRequest, "text is required")
		return
	}
	if request.Intent == "" {
		request.Intent = ai.IntentSummarize
	}

	ctx, cancel := context.WithTimeout(r.Context(), aiAskTimeout)
	defer cancel()

	svc := service.New(s.cfg.VaultRoot, s.db)
	chunks := make(chan ai.AskChunk)
	go ai.Transform(ctx, svc.Config, request.Text, request.Intent, chunks)

	writer := newNDJSONWriter(w)
	if !writer.begin() {
		return
	}
	for {
		select {
		case <-ctx.Done():
			// Client disconnected: abort upstream and drain so the
			// Transform goroutine can finish and close the channel.
			for range chunks {
			}
			return
		case chunk, ok := <-chunks:
			if !ok {
				writer.event(ai.DoneEvent())
				return
			}
			if !writer.event(ai.AnswerEvent(chunk.Chunk)) {
				// Client went away mid-stream: drain so the producer
				// goroutine can finish and close chunks instead of leaking.
				for range chunks {
				}
				return
			}
		}
	}
}

// allowAIRateLimited applies the AI rate-limit bucket (per source IP) and
// writes the 429 response when the caller is blocked. Mirrors how auth
// failures are throttled, but for successful AI requests.
func (s *Server) allowAIRateLimited(w http.ResponseWriter, r *http.Request) bool {
	ip := clientIP(r)
	if allowed, retryAfter := s.throttle.recordAIRequest(ip); !allowed {
		w.Header().Set("Retry-After", retryAfterSeconds(retryAfter))
		writeError(w, http.StatusTooManyRequests, "too many AI requests — try again shortly")
		return false
	}
	return true
}

func pathsOf(results []service.SearchResult) []string {
	paths := make([]string, len(results))
	for i, result := range results {
		paths[i] = result.Path
	}
	return paths
}

// ndjsonWriter flushes every event as one NDJSON line, exactly like
// streamCommand, so a client observes tokens as they are produced. A write
// failure (client gone) stops the stream; the caller aborts upstream via
// the request context.
type ndjsonWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
	started bool
}

func newNDJSONWriter(w http.ResponseWriter) *ndjsonWriter {
	flusher, _ := w.(http.Flusher)
	return &ndjsonWriter{w: w, flusher: flusher}
}

func (n *ndjsonWriter) begin() bool {
	n.w.Header().Set("Content-Type", "application/x-ndjson")
	n.w.WriteHeader(http.StatusOK)
	n.started = true
	n.flush()
	return true
}

// event writes one AIEvent as a JSON line. Returns false when the client
// is gone so the handler can abort the upstream model request.
func (n *ndjsonWriter) event(event ai.AIEvent) bool {
	if !n.started {
		return false
	}
	line, err := json.Marshal(event)
	if err != nil {
		return false
	}
	if _, err := n.w.Write(append(line, '\n')); err != nil {
		return false
	}
	n.flush()
	return true
}

func (n *ndjsonWriter) flush() {
	if n.flusher != nil {
		n.flusher.Flush()
	}
}
