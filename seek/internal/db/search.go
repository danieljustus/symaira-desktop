package db

import (
	"container/heap"
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strings"
)

// binarySignatureCandidateMultiplier controls the Hamming shortlist size as
// limit * multiplier, capped at the total row count. Higher values improve
// recall at the cost of more cosine computations in stage 2.
const binarySignatureCandidateMultiplier = 4

func escapeFTS5Query(query string) string {
	tokens := strings.Fields(query)
	if len(tokens) == 0 {
		return ""
	}
	for i, token := range tokens {
		escaped := strings.ReplaceAll(token, "\"", "\"\"")
		tokens[i] = "\"" + escaped + "\""
	}
	return strings.Join(tokens, " AND ")
}

func (db *DB) SearchBM25(queryStr string, limit int) ([]*SearchResult, error) {
	return db.SearchBM25WithPath(queryStr, "", limit)
}

func (db *DB) SearchBM25WithPath(queryStr string, pathPrefix string, limit int) ([]*SearchResult, error) {
	var sqlQuery string
	var args []any
	escapedQuery := escapeFTS5Query(queryStr)
	if pathPrefix != "" {
		sqlQuery = `
			SELECT c.id, c.uuid, c.document_path, c.chunk_index, c.content, c.embedding, c.hash
			FROM chunks c
			JOIN chunks_fts f ON c.id = f.rowid
			WHERE chunks_fts MATCH ? AND c.document_path LIKE ? || '%'
			ORDER BY bm25(chunks_fts) ASC
			LIMIT ?`
		args = []any{escapedQuery, pathPrefix, limit}
	} else {
		sqlQuery = `
			SELECT c.id, c.uuid, c.document_path, c.chunk_index, c.content, c.embedding, c.hash
			FROM chunks c
			JOIN chunks_fts f ON c.id = f.rowid
			WHERE chunks_fts MATCH ?
			ORDER BY bm25(chunks_fts) ASC
			LIMIT ?`
		args = []any{escapedQuery, limit}
	}

	rows, err := db.conn.Query(sqlQuery, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []*SearchResult
	rank := 1
	for rows.Next() {
		var c Chunk
		var embBytes []byte
		if err := rows.Scan(&c.ID, &c.UUID, &c.DocumentPath, &c.ChunkIndex, &c.Content, &embBytes, &c.Hash); err != nil {
			return nil, err
		}
		c.Embedding = BytesToFloat32Slice(embBytes)

		results = append(results, &SearchResult{
			Chunk:    &c,
			BM25Rank: rank,
		})
		rank++
	}
	return results, nil
}

// searchVectorScanSelect omits the content column on purpose: vector scoring
// needs only the embedding and its precomputed norm. Streaming every chunk's
// text on every query is the dominant cost on large indexes, so content is
// fetched afterwards for just the surviving top-k rows (see hydrateContent).
const searchVectorScanSelect = "SELECT id, uuid, document_path, chunk_index, embedding, hash, norm, binary_signature, embedding_dim, embedding_model FROM chunks"

func (db *DB) SearchVector(queryVec []float32, limit int) ([]*SearchResult, error) {
	return db.SearchVectorWithPath(queryVec, "", limit)
}

func (db *DB) SearchVectorWithPath(queryVec []float32, pathPrefix string, limit int) ([]*SearchResult, error) {
	if limit <= 0 {
		return nil, nil
	}

	queryNorm := l2Norm(queryVec)

	// Detect writes from other processes before trusting the warm index.
	db.checkGeneration()

	// Fast path: use IVF prefilter when an index is ready.
	if idx := db.vectorIndex; idx != nil && idx.IsReady() {
		candidateIDs := idx.CandidateIDs(queryVec, idx.ProbeCount())
		if candidateIDs != nil {
			return db.searchVectorFilteredWithPath(queryVec, queryNorm, pathPrefix, candidateIDs, limit)
		}
	}

	return db.searchVectorFullScanWithPath(queryVec, queryNorm, pathPrefix, limit)
}

func hammingDistFallback(query, stored []byte) int {
	if stored == nil {
		return math.MaxInt
	}
	return HammingDistance(query, stored)
}

type rowEntry struct {
	chunk    Chunk
	embBytes []byte
	sigBytes []byte
	norm     float32
}

// hammingShortlist ranks allRows by Hamming distance from querySig and returns
// the top candidates for cosine rescoring. When the Hamming pre-filter
// provides no discrimination (all distances equal, querySig nil, or the
// shortlist covers all rows), it returns all rows so exact cosine scoring is
// preserved. Distances are precomputed once per row and rows are ordered
// through an index permutation, so the XOR/popcount runs exactly n times
// instead of O(n log n) times per query.
func hammingShortlist(allRows []rowEntry, querySig []byte, limit int) []rowEntry {
	hammingSize := limit * binarySignatureCandidateMultiplier
	if hammingSize > len(allRows) {
		hammingSize = len(allRows)
	}

	if querySig == nil || len(allRows) <= 1 || hammingSize >= len(allRows) {
		return allRows
	}

	dists := make([]int, len(allRows))
	order := make([]int, len(allRows))
	for i := range allRows {
		dists[i] = hammingDistFallback(querySig, allRows[i].sigBytes)
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return dists[order[a]] < dists[order[b]]
	})

	sorted := make([]rowEntry, len(allRows))
	for i := range sorted {
		sorted[i] = allRows[order[i]]
	}

	if dists[order[0]] == dists[order[hammingSize-1]] {
		return sorted
	}
	return sorted[:hammingSize]
}

func scoreShortlist(h *SearchResultHeap, limit int, queryVec []float32, queryNorm float32, chunk *Chunk, embBytes []byte, norm float32) {
	var score float32
	if queryNorm > 0 && norm > 0 {
		score = CosineSimilarityWithStoredNorm(queryVec, embBytes, queryNorm, norm)
	} else {
		if chunk.Embedding == nil {
			chunk.Embedding = BytesToFloat32Slice(embBytes)
		}
		score = CosineSimilarity(queryVec, chunk.Embedding)
	}

	if h.Len() < limit {
		heap.Push(h, &SearchResult{
			Chunk:       chunk,
			CosineScore: score,
		})
	} else if score > (*h)[0].CosineScore {
		(*h)[0] = &SearchResult{
			Chunk:       chunk,
			CosineScore: score,
		}
		heap.Fix(h, 0)
	}
}

// scanAndScore is the single scan → shortlist → heap-score → hydrate
// implementation shared by all vector search entry points; rows must come from
// a searchVectorScanSelect-shaped query. useHamming=false scores every row
// exactly (cosine baseline). collectIndex, when non-nil, collects each row's
// {ID, Embedding} pair for IVF index building by the caller.
func (db *DB) scanAndScore(rows *sql.Rows, queryVec []float32, queryNorm float32, limit int, useHamming bool, collectIndex *[]*Chunk) ([]*SearchResult, error) {
	var allRows []rowEntry
	for rows.Next() {
		var e rowEntry
		var sigPtr *[]byte
		if err := rows.Scan(&e.chunk.ID, &e.chunk.UUID, &e.chunk.DocumentPath, &e.chunk.ChunkIndex, &e.embBytes, &e.chunk.Hash, &e.norm, &sigPtr, &e.chunk.Dim, &e.chunk.Model); err != nil {
			return nil, err
		}
		if sigPtr != nil {
			e.sigBytes = *sigPtr
		}
		e.chunk.Norm = e.norm

		if collectIndex != nil {
			e.chunk.Embedding = BytesToFloat32Slice(e.embBytes)
			*collectIndex = append(*collectIndex, &Chunk{ID: e.chunk.ID, Embedding: e.chunk.Embedding})
		}

		allRows = append(allRows, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	shortlist := allRows
	if useHamming {
		querySig := SignBinarySignature(queryVec)
		shortlist = hammingShortlist(allRows, querySig, limit)
	}

	h := &SearchResultHeap{}
	for i := range shortlist {
		e := &shortlist[i]
		scoreShortlist(h, limit, queryVec, queryNorm, &e.chunk, e.embBytes, e.norm)
	}

	sort.SliceStable(*h, func(i, j int) bool {
		return (*h)[i].CosineScore > (*h)[j].CosineScore
	})
	results := ([]*SearchResult)(*h)

	for i, r := range results {
		r.VectorRank = i + 1
	}

	if err := db.hydrateContent(results); err != nil {
		return nil, err
	}
	return results, nil
}

func (db *DB) searchVectorFilteredWithPath(queryVec []float32, queryNorm float32, pathPrefix string, candidateIDs []int64, limit int) ([]*SearchResult, error) {
	placeholders := make([]string, len(candidateIDs))
	args := make([]interface{}, 0, len(candidateIDs)+1)
	for i, id := range candidateIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}

	whereClause := fmt.Sprintf("id IN (%s)", strings.Join(placeholders, ","))
	if pathPrefix != "" {
		whereClause += " AND document_path LIKE ? || '%'"
		args = append(args, pathPrefix)
	}

	rows, err := db.conn.Query(searchVectorScanSelect+" WHERE "+whereClause, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return db.scanAndScore(rows, queryVec, queryNorm, limit, true, nil)
}

func (db *DB) searchVectorFullScanWithPath(queryVec []float32, queryNorm float32, pathPrefix string, limit int) ([]*SearchResult, error) {
	query := searchVectorScanSelect
	args := []any{}
	if pathPrefix != "" {
		query += " WHERE document_path LIKE ? || '%'"
		args = append(args, pathPrefix)
	}

	rows, err := db.conn.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Only build the IVF index from a full, unfiltered scan so the index covers
	// the entire corpus and remains useful for future queries regardless of path
	// scope.
	var indexChunks []*Chunk
	var collectIndex *[]*Chunk
	if pathPrefix == "" && (db.vectorIndex == nil || !db.vectorIndex.IsReady()) {
		collectIndex = &indexChunks
	}

	results, err := db.scanAndScore(rows, queryVec, queryNorm, limit, true, collectIndex)
	if err != nil {
		return nil, err
	}

	if len(indexChunks) >= indexBuildThreshold {
		if db.vectorIndex == nil {
			db.vectorIndex = NewVectorIndex()
		}
		db.vectorIndex.Build(indexChunks)
		db.saveVectorIndex()
	}
	return results, nil
}

// searchVectorFullScanCosine scores every chunk with exact cosine similarity
// without binary pre-filtering. Used as a baseline for benchmarks and recall
// tests.
func (db *DB) searchVectorFullScanCosine(queryVec []float32, queryNorm float32, limit int) ([]*SearchResult, error) {
	rows, err := db.conn.Query(searchVectorScanSelect)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return db.scanAndScore(rows, queryVec, queryNorm, limit, false, nil)
}

// hydrateContent fills in Chunk.Content for the given results using a single
// IN-list query keyed on the surviving chunk ids.
func (db *DB) hydrateContent(results []*SearchResult) error {
	if len(results) == 0 {
		return nil
	}

	byID := make(map[int64]*Chunk, len(results))
	args := make([]interface{}, len(results))
	for i, r := range results {
		byID[r.Chunk.ID] = r.Chunk
		args[i] = r.Chunk.ID
	}

	query := "SELECT id, content FROM chunks WHERE id IN (" + strings.Repeat("?,", len(results)-1) + "?)"
	rows, err := db.conn.Query(query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var id int64
		var content string
		if err := rows.Scan(&id, &content); err != nil {
			return err
		}
		if c, ok := byID[id]; ok {
			c.Content = content
		}
	}
	return rows.Err()
}
