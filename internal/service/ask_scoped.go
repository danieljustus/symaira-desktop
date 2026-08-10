package service

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/danieljustus/symaira-desktop/internal/ai"
	"github.com/danieljustus/symaira-desktop/internal/notebook"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

// AskScoped is Ask restricted to a notebook's sources (issue #425): every
// citation comes from the notebook's source set, and the model is told to
// answer strictly from the supplied sources (the same prompt contract Ask
// already uses — see ai.buildPrompt). notebookRef empty behaves exactly
// like Ask, so existing unscoped callers are unaffected.
func (s *Service) AskScoped(ctx context.Context, notebookRef, query string, out chan<- interface{}) {
	if notebookRef == "" {
		s.Ask(ctx, query, out)
		return
	}

	out <- ai.ToolEvent("search", "running")
	nb, err := notebook.Resolve(s.VaultRoot, notebookRef)
	if err != nil {
		out <- ai.ToolEvent("search", "error")
		out <- ai.AnswerEvent("Notebook not found: " + err.Error())
		out <- ai.DoneEvent()
		close(out)
		return
	}

	results, scopedPaths, err := s.scopedSearchResults(nb, query)
	if err != nil {
		out <- ai.ToolEvent("search", "error")
		out <- ai.AnswerEvent("Search failed: " + err.Error())
		out <- ai.DoneEvent()
		close(out)
		return
	}
	out <- ai.ToolEvent("search", "done")

	for _, r := range results {
		out <- ai.CitationEvent(r.Path, r.Title, r.Snippet, r.Score)
	}

	out <- ai.ToolEvent("llm", "running")
	chunkChan := make(chan ai.AskChunk)
	go func() {
		ai.Ask(ctx, s.Config, query, resultsToMapSlice(results), chunkChan)
	}()

	var answer strings.Builder
	for chunk := range chunkChan {
		answer.WriteString(chunk.Chunk)
		out <- ai.AnswerEvent(chunk.Chunk)
	}
	out <- ai.ToolEvent("llm", "done")

	// A citation-shaped link in the answer that points outside the
	// notebook's sources is flagged, never blocked (VAULT.md's advisory
	// citation contract, issue #408) — the notebook boundary is a
	// retrieval scope, not a hard authorization wall.
	warnings := ai.CheckCitationWarningsSafe(answer.String(), scopedPaths)
	out <- ai.AIEvent{Type: ai.AIEventDone, CitationWarnings: warnings, ReadPaths: scopedPaths}
	close(out)
}

// AskTextScoped is the buffered variant of AskScoped for non-streaming
// consumers (MCP), mirroring how AskText relates to Ask.
func (s *Service) AskTextScoped(ctx context.Context, notebookRef, query string) (string, error) {
	if notebookRef == "" {
		return s.AskText(ctx, query)
	}
	nb, err := notebook.Resolve(s.VaultRoot, notebookRef)
	if err != nil {
		return "", err
	}
	results, _, err := s.scopedSearchResults(nb, query)
	if err != nil {
		return "", err
	}
	chunkChan := make(chan ai.AskChunk)
	go ai.Ask(ctx, s.Config, query, resultsToMapSlice(results), chunkChan)

	var b strings.Builder
	for chunk := range chunkChan {
		b.WriteString(chunk.Chunk)
	}
	return b.String(), nil
}

// scopedSearchResults returns SearchResult entries restricted to a
// notebook's existing sources (missing sources are skipped, never erroring
// the whole call), ranked by FTS relevance to query where possible. A
// source with no literal keyword match still contributes a fallback
// excerpt: a notebook's corpus is a small, curated set (VAULT.md section
// 10), so a conceptual question ("summarize these") must still be grounded
// in every source, not only the ones a keyword search happens to hit.
// The second return value is the flat list of in-scope paths, used as the
// citation-warning readPaths set.
func (s *Service) scopedSearchResults(nb *notebook.Notebook, query string) ([]SearchResult, []string, error) {
	refs, err := nb.ResolveSources(s.VaultRoot)
	if err != nil {
		return nil, nil, err
	}

	type source struct {
		relPath string
		absPath string
	}
	var present []source
	scopedPaths := make([]string, 0, len(refs))
	for _, r := range refs {
		if r.Missing {
			continue
		}
		absPath, err := vault.SecurePath(s.VaultRoot, r.Path)
		if err != nil {
			continue
		}
		present = append(present, source{relPath: r.Path, absPath: absPath})
		scopedPaths = append(scopedPaths, r.Path)
	}
	if len(present) == 0 {
		return nil, scopedPaths, nil
	}

	var results []SearchResult
	matched := make(map[string]bool, len(present))
	if strings.TrimSpace(query) != "" {
		allowedAbs := make([]string, len(present))
		for i, src := range present {
			allowedAbs[i] = src.absPath
		}
		docs, err := s.DB.SearchScoped(query, allowedAbs)
		if err != nil {
			return nil, scopedPaths, err
		}
		for _, d := range docs {
			relPath, relErr := filepath.Rel(s.VaultRoot, d.Path)
			if relErr != nil {
				relPath = d.Path
			}
			results = append(results, SearchResult{Path: relPath, Title: d.Title, Snippet: d.Body, Score: 1.0})
			matched[relPath] = true
		}
	}

	for _, src := range present {
		if matched[src.relPath] {
			continue
		}
		doc, err := vault.ParseFile(src.absPath)
		if err != nil {
			continue
		}
		excerpt := doc.Body
		if len(excerpt) > 1500 {
			excerpt = excerpt[:1500]
		}
		results = append(results, SearchResult{Path: src.relPath, Title: doc.Title, Snippet: excerpt, Score: 0.0})
	}

	return results, scopedPaths, nil
}
