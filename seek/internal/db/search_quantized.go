package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/danieljustus/symaira-corekit/vectorkit/turboquant"
)

type quantCandidate struct {
	id       int64
	uuid     string
	docPath  string
	chunkIdx int
	hash     string
	norm     float32
	dim      int
	model    string
	score    float32
}

func (db *DB) SearchVectorQuantized(queryVec []float32, limit int) ([]*SearchResult, error) {
	return db.SearchVectorQuantizedWithPath(queryVec, "", limit)
}

func (db *DB) SearchVectorQuantizedWithPath(queryVec []float32, pathPrefix string, limit int) ([]*SearchResult, error) {
	if limit <= 0 {
		return nil, nil
	}
	cfg := db.quantConfig
	if cfg == nil || !cfg.Enabled {
		return db.SearchVectorWithPath(queryVec, pathPrefix, limit)
	}

	queryNorm := l2Norm(queryVec)
	db.checkGeneration()

	results, err := db.searchVectorQuantizedInnerWithPath(queryVec, queryNorm, pathPrefix, cfg, limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: quantized search failed, falling back to standard search: %v\n", err)
		return db.SearchVectorWithPath(queryVec, pathPrefix, limit)
	}
	return results, nil
}

func (db *DB) searchVectorQuantizedInnerWithPath(queryVec []float32, queryNorm float32, pathPrefix string, cfg *QuantConfig, limit int) ([]*SearchResult, error) {
	codec, err := turboquant.NewCodec(len(queryVec), turboquant.BitWidth(cfg.BitWidth), cfg.Seed, 0)
	if err != nil {
		return nil, fmt.Errorf("create codec: %w", err)
	}

	query := `SELECT id, uuid, document_path, chunk_index, hash, norm, embedding_dim, embedding_model,
		        embedding_quant, embedding_quant_meta
		 FROM chunks WHERE embedding_quant IS NOT NULL`
	args := []any{}
	if pathPrefix != "" {
		query += " AND document_path LIKE ? || '%'"
		args = append(args, pathPrefix)
	}

	rows, err := db.conn.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("quantized scan query: %w", err)
	}
	defer rows.Close()

	var candidates []quantCandidate
	for rows.Next() {
		var c quantCandidate
		var quantBytes []byte
		var metaRaw sql.NullString
		if err := rows.Scan(
			&c.id, &c.uuid, &c.docPath, &c.chunkIdx, &c.hash, &c.norm, &c.dim, &c.model,
			&quantBytes, &metaRaw,
		); err != nil {
			continue
		}

		if len(quantBytes) == 0 {
			continue
		}

		var sideMeta turboquant.SidecarMeta
		if metaRaw.Valid && metaRaw.String != "" {
			if err := json.Unmarshal([]byte(metaRaw.String), &sideMeta); err != nil {
				continue
			}
		}

		if sideMeta.CodecVersion != turboquant.CodecVersion {
			continue
		}
		if sideMeta.Dimension != len(queryVec) {
			continue
		}

		code, err := turboquant.UnpackSidecarBlob(quantBytes)
		if err != nil {
			continue
		}

		score := codec.Score(queryVec, code)
		c.score = score
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("quantized scan rows: %w", err)
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("no quantized sidecars found")
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})

	shortlistSize := cfg.Shortlist
	if shortlistSize > len(candidates) {
		shortlistSize = len(candidates)
	}
	candidates = candidates[:shortlistSize]

	if !cfg.ExactRerank {
		return db.buildQuantResults(candidates, limit)
	}

	shortlistIDs := make([]int64, len(candidates))
	for i, c := range candidates {
		shortlistIDs[i] = c.id
	}

	return db.exactRerankShortlistWithPath(queryVec, queryNorm, pathPrefix, shortlistIDs, limit)
}

func (db *DB) buildQuantResults(candidates []quantCandidate, limit int) ([]*SearchResult, error) {
	results := make([]*SearchResult, 0, limit)
	for i := range candidates {
		if i >= limit {
			break
		}
		c := &candidates[i]
		results = append(results, &SearchResult{
			Chunk: &Chunk{
				ID:           c.id,
				UUID:         c.uuid,
				DocumentPath: c.docPath,
				ChunkIndex:   c.chunkIdx,
				Hash:         c.hash,
				Norm:         c.norm,
				Dim:          c.dim,
				Model:        c.model,
			},
			CosineScore: c.score,
			VectorRank:  i + 1,
		})
	}
	if err := db.hydrateContent(results); err != nil {
		return nil, err
	}
	return results, nil
}

func (db *DB) exactRerankShortlistWithPath(queryVec []float32, queryNorm float32, pathPrefix string, shortlistIDs []int64, limit int) ([]*SearchResult, error) {
	placeholders := make([]string, len(shortlistIDs))
	args := make([]interface{}, 0, len(shortlistIDs)+1)
	for i, id := range shortlistIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}

	whereClause := fmt.Sprintf("id IN (%s)", strings.Join(placeholders, ","))
	if pathPrefix != "" {
		whereClause += " AND document_path LIKE ? || '%'"
		args = append(args, pathPrefix)
	}

	query := fmt.Sprintf(
		"SELECT id, uuid, document_path, chunk_index, embedding, hash, norm, embedding_dim, embedding_model FROM chunks WHERE %s",
		whereClause,
	)

	fetchRows, err := db.conn.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("rerank fetch query: %w", err)
	}
	defer fetchRows.Close()

	type fetchedRow struct {
		chunk    Chunk
		embBytes []byte
		norm     float32
	}

	rowByID := make(map[int64]*fetchedRow)
	for fetchRows.Next() {
		var r fetchedRow
		if err := fetchRows.Scan(
			&r.chunk.ID, &r.chunk.UUID, &r.chunk.DocumentPath, &r.chunk.ChunkIndex,
			&r.embBytes, &r.chunk.Hash, &r.norm, &r.chunk.Dim, &r.chunk.Model,
		); err != nil {
			continue
		}
		r.chunk.Norm = r.norm
		rowByID[r.chunk.ID] = &r
	}
	if err := fetchRows.Err(); err != nil {
		return nil, fmt.Errorf("rerank fetch rows: %w", err)
	}

	h := &SearchResultHeap{}
	for _, id := range shortlistIDs {
		r, ok := rowByID[id]
		if !ok {
			continue
		}

		scoreShortlist(h, limit, queryVec, queryNorm, &r.chunk, r.embBytes, r.norm)
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
