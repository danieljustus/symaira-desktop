package engine

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/danieljustus/symaira-seek/internal/db"
)

// SearchOptions configures optional behaviour for SearchHybridWithOptions.
type SearchOptions struct {
	RerankCfg  RerankConfig
	ExpandCfg  ExpandConfig
	PathFilter string
}

// SearchHybrid combines BM25 keyword search and semantic vector search using Reciprocal Rank Fusion (RRF).
// The BM25 leg uses db.Store while the vector leg uses the pluggable
// db.VectorStore interface so callers can substitute alternate vector
// backends without changing the engine layer.
func SearchHybrid(dbClient db.Store, vectorStore db.VectorStore, embedder Embedder, query string, limit int) ([]*db.SearchResult, error) {
	return SearchHybridWithOptions(dbClient, vectorStore, embedder, query, limit, SearchOptions{})
}

// SearchHybridWithOptions is like SearchHybrid but accepts SearchOptions for
// optional LLM re-ranking. Existing callers that use SearchHybrid are unchanged.
func SearchHybridWithOptions(dbClient db.Store, vectorStore db.VectorStore, embedder Embedder, query string, limit int, opts SearchOptions) ([]*db.SearchResult, error) {
	if query == "" {
		return nil, nil
	}

	// Check for mixed embedding spaces before vector search to prevent
	// silently returning zero/wrong cosine scores (issue #151).
	spaces, err := dbClient.DetectMixedEmbeddingSpaces()
	if err != nil {
		return nil, fmt.Errorf("failed to detect embedding spaces: %w", err)
	}
	if len(spaces) > 1 {
		examples := make([]string, 0, len(spaces))
		for key, count := range spaces {
			examples = append(examples, fmt.Sprintf("%s (%d chunks)", key, count))
		}
		return nil, fmt.Errorf("index contains mixed embedding spaces (%s); re-index with a single model before searching", strings.Join(examples, ", "))
	}

	// 1. Generate query vector (no retry — fast fallback when Ollama is offline, issue #162)
	queryResult := embedder.GenerateVectorNoRetryWithModel(query)
	queryVec := queryResult.Vector

	// 1b. Warn when the query vector fell back to the local hash while the index
	// was built with an Ollama model; mark the structured output so the UI and
	// MCP consumers can surface the same signal (issue #270).
	vectorMode := ""
	if queryResult.Model == localHashModelName {
		indexIsOllama := false
		indexIsFallback := false
		for key := range spaces {
			parts := strings.SplitN(key, "/", 2)
			if len(parts) == 2 {
				if parts[1] == localHashModelName {
					indexIsFallback = true
				} else {
					indexIsOllama = true
				}
			}
		}
		if indexIsOllama && !indexIsFallback {
			fmt.Fprintf(os.Stderr, "warning: query embedding fell back to local hash while the index uses an Ollama model; semantic scores may be unreliable\n")
			vectorMode = "fallback"
		}
	}

	// 1a. Optional HyDE query expansion: generate a hypothetical document
	// passage via Ollama chat and average it with the original query vector.
	searchVec := queryVec
	if opts.ExpandCfg.Enabled {
		expander := NewExpander(opts.ExpandCfg)
		if expandedText, err := expander.Expand(query); err == nil {
			searchVec = computeExpandedVec(embedder, queryVec, expandedText)
		} else {
			fmt.Fprintf(os.Stderr, "engine: HyDE expansion failed (%v), using original query vector\n", err)
		}
	}

	// We fetch a bit more than limit to ensure good fusion overlap
	fetchLimit := limit * 3
	if fetchLimit < 50 {
		fetchLimit = 50
	}
	if fetchLimit > 200 {
		fetchLimit = 200
	}

	// 2. Run BM25 and full vector scan concurrently.
	// The vector leg always runs a full scan so that semantically related
	// chunks without keyword overlap are never excluded (issue #65).
	var (
		bm25Results   []*db.SearchResult
		bm25Err       error
		vectorResults []*db.SearchResult
		vectorErr     error
		wg            sync.WaitGroup
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		if opts.PathFilter != "" {
			bm25Results, bm25Err = dbClient.SearchBM25WithPath(query, opts.PathFilter, fetchLimit)
		} else {
			bm25Results, bm25Err = dbClient.SearchBM25(query, fetchLimit)
		}
	}()
	go func() {
		defer wg.Done()
		if opts.PathFilter != "" {
			vectorResults, vectorErr = vectorStore.SearchWithPath(context.Background(), searchVec, opts.PathFilter, fetchLimit)
		} else {
			vectorResults, vectorErr = vectorStore.Search(context.Background(), searchVec, fetchLimit)
		}
	}()
	wg.Wait()

	if bm25Err != nil {
		fmt.Fprintf(os.Stderr, "warning: BM25 search failed, falling back to vector-only: %v\n", bm25Err)
		bm25Results = nil
	}

	if vectorErr != nil {
		return nil, vectorErr
	}

	// 4. Reciprocal Rank Fusion
	k := float32(60.0) // Standard RRF parameter
	merged := make(map[string]*db.SearchResult)

	// Process BM25 ranks
	for i, res := range bm25Results {
		uuid := res.Chunk.UUID
		res.BM25Rank = i + 1
		merged[uuid] = res
	}

	// Process Vector ranks
	for i, res := range vectorResults {
		uuid := res.Chunk.UUID
		rank := i + 1
		if existing, ok := merged[uuid]; ok {
			existing.VectorRank = rank
			existing.CosineScore = res.CosineScore
		} else {
			res.VectorRank = rank
			merged[uuid] = res
		}
	}

	// Calculate final RRF scores
	var combined []*db.SearchResult
	for _, res := range merged {
		var score float32
		if res.BM25Rank > 0 {
			score += 1.0 / (k + float32(res.BM25Rank))
		}
		if res.VectorRank > 0 {
			score += 1.0 / (k + float32(res.VectorRank))
		}
		res.RRFScore = score
		combined = append(combined, res)
	}

	// Sort descending by RRFScore
	sort.Slice(combined, func(i, j int) bool {
		return combined[i].RRFScore > combined[j].RRFScore
	})

	// Truncate to limit
	if len(combined) > limit {
		combined = combined[:limit]
	}

	// 5. Optional LLM re-ranking
	if opts.RerankCfg.Enabled {
		reranker := NewReranker(opts.RerankCfg)
		combined = reranker.RerankResults(query, combined)
	}

	for _, res := range combined {
		res.VectorMode = vectorMode
	}

	return combined, nil
}
