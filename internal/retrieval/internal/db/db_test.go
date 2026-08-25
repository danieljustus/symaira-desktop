package db

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCosineSimilarity(t *testing.T) {
	tests := []struct {
		name string
		a    []float32
		b    []float32
		want float32
	}{
		{
			name: "Identical vectors",
			a:    []float32{1, 0, 0},
			b:    []float32{1, 0, 0},
			want: 1.0,
		},
		{
			name: "Orthogonal vectors",
			a:    []float32{1, 0, 0},
			b:    []float32{0, 1, 0},
			want: 0.0,
		},
		{
			name: "Opposite vectors",
			a:    []float32{1, 2, 3},
			b:    []float32{-1, -2, -3},
			want: -1.0,
		},
		{
			name: "Length mismatch",
			a:    []float32{1, 2},
			b:    []float32{1, 2, 3},
			want: 0.0,
		},
		{
			name: "Empty vectors",
			a:    []float32{},
			b:    []float32{},
			want: 0.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CosineSimilarity(tt.a, tt.b)
			if math.Abs(float64(got-tt.want)) > 1e-6 {
				t.Errorf("CosineSimilarity() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDatabaseOperations(t *testing.T) {
	// Set home dir override for test
	tempDir, err := os.MkdirTemp("", "seek-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	t.Setenv("HOME", tempDir)

	db, err := Open()
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	defer db.Close()

	// Test SaveDocument
	docPath := filepath.Join(tempDir, "test.md")
	doc := &Document{
		Path:      docPath,
		Hash:      "abcd123",
		UpdatedAt: time.Now(),
	}

	err = db.SaveDocument(doc)
	if err != nil {
		t.Fatalf("failed to save document: %v", err)
	}

	// Test GetDocument
	fetched, err := db.GetDocument(docPath)
	if err != nil {
		t.Fatalf("failed to get document: %v", err)
	}
	if fetched == nil || fetched.Hash != doc.Hash {
		t.Errorf("fetched document mismatch: got %+v, want %+v", fetched, doc)
	}

	// Test SaveChunks
	chunks := []*Chunk{
		{
			UUID:         "c1",
			DocumentPath: docPath,
			ChunkIndex:   0,
			Content:      "The quick brown fox jumps over the lazy dog.",
			Embedding:    []float32{1.0, 0.0, 0.0},
			Hash:         "chunkhash1",
		},
		{
			UUID:         "c2",
			DocumentPath: docPath,
			ChunkIndex:   1,
			Content:      "A fast auburn canine leaps across an inactive hound.",
			Embedding:    []float32{0.0, 1.0, 0.0},
			Hash:         "chunkhash2",
		},
	}

	err = db.SaveChunks(chunks)
	if err != nil {
		t.Fatalf("failed to save chunks: %v", err)
	}

	// Test GetChunksForDocument
	fetchedChunks, err := db.GetChunksForDocument(docPath)
	if err != nil {
		t.Fatalf("failed to get chunks: %v", err)
	}
	if len(fetchedChunks) != 2 {
		t.Errorf("expected 2 chunks, got %d", len(fetchedChunks))
	}

	// Test SearchBM25
	bm25Res, err := db.SearchBM25("fox", 10)
	if err != nil {
		t.Fatalf("failed BM25 search: %v", err)
	}
	if len(bm25Res) == 0 {
		t.Errorf("expected BM25 search results for 'fox'")
	} else if bm25Res[0].Chunk.UUID != "c1" {
		t.Errorf("expected chunk c1 to be returned, got %s", bm25Res[0].Chunk.UUID)
	}

	// Test SearchVector
	vecRes, err := db.SearchVector([]float32{0.0, 0.9, 0.1}, 10)
	if err != nil {
		t.Fatalf("failed vector search: %v", err)
	}
	if len(vecRes) != 2 {
		t.Fatalf("expected 2 vector search results, got %d", len(vecRes))
	}
	if vecRes[0].Chunk.UUID != "c2" {
		t.Errorf("expected nearest vector match to be chunk c2, got %s", vecRes[0].Chunk.UUID)
	}

	// Test GetStats
	stats, err := db.GetStats()
	if err != nil {
		t.Fatalf("failed to get stats: %v", err)
	}
	if stats.DocumentCount != 1 || stats.ChunkCount != 2 {
		t.Errorf("unexpected stats: %+v", stats)
	}

	// Test DeleteDocument
	err = db.DeleteDocument(docPath)
	if err != nil {
		t.Fatalf("failed to delete document: %v", err)
	}

	deletedDoc, err := db.GetDocument(docPath)
	if err != nil {
		t.Fatalf("error fetching deleted document: %v", err)
	}
	if deletedDoc != nil {
		t.Errorf("document should have been deleted")
	}

	stats2, _ := db.GetStats()
	if stats2.DocumentCount != 0 || stats2.ChunkCount != 0 {
		t.Errorf("expected 0 docs and 0 chunks in stats after deletion, got %+v", stats2)
	}
}

// seedBenchmarkDB populates d with nChunks chunks whose embeddings
// are 768-dim normalized random-ish vectors. The seeding cost is
// excluded from the timed benchmark via b.ResetTimer below.
func seedBenchmarkDB(b *testing.B, d *DB, nChunks int) {
	b.Helper()
	docPath := "/bench/doc.md"
	if err := d.SaveDocument(&Document{Path: docPath, Hash: "bench", UpdatedAt: time.Now()}); err != nil {
		b.Fatalf("SaveDocument: %v", err)
	}
	chunks := make([]*Chunk, nChunks)
	for i := 0; i < nChunks; i++ {
		emb := make([]float32, 768)
		for j := range emb {
			emb[j] = float32(i+j) / float32(nChunks+j+1)
		}
		var sumSquares float64
		for _, v := range emb {
			sumSquares += float64(v * v)
		}
		norm := float32(math.Sqrt(sumSquares))
		if norm > 0 {
			for j := range emb {
				emb[j] /= norm
			}
		}
		chunks[i] = &Chunk{
			UUID:         "bench-" + strconv.Itoa(i),
			DocumentPath: docPath,
			ChunkIndex:   i,
			Content:      "bench content",
			Embedding:    emb,
			Hash:         "bench-hash",
		}
	}
	if err := d.SaveChunks(chunks); err != nil {
		b.Fatalf("SaveChunks: %v", err)
	}
}

// BenchmarkSearchVectorSinglePass vs BenchmarkSearchVectorTwoPass
// measures the impact of issue #49's single-pass rewrite. The
// single-pass variant issues one query for the full chunk payload
// (embedding + content + metadata) and keeps a top-K window in
// memory; the two-pass variant issues a paginated scoring query
// followed by a top-K detail query.
//
// Run with:
//
//	go test -bench=BenchmarkSearchVector -benchtime=2s ./internal/db
func BenchmarkSearchVectorSinglePass(b *testing.B) {
	d := setupDB(b)
	const nChunks = 1000
	seedBenchmarkDB(b, d, nChunks)
	query := make([]float32, 768)
	for i := range query {
		query[i] = 0.5
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		results, err := d.SearchVector(query, 10)
		if err != nil {
			b.Fatalf("SearchVector: %v", err)
		}
		if len(results) == 0 {
			b.Fatal("expected non-empty results")
		}
	}
}

func BenchmarkSearchVectorTwoPass(b *testing.B) {
	d := setupDB(b)
	const nChunks = 1000
	seedBenchmarkDB(b, d, nChunks)
	query := make([]float32, 768)
	for i := range query {
		query[i] = 0.5
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		results, err := d.searchVectorTwoPass(query, nil, 10)
		if err != nil {
			b.Fatalf("searchVectorTwoPass: %v", err)
		}
		if len(results) == 0 {
			b.Fatal("expected non-empty results")
		}
	}
}

type scoredEntry struct {
	id    int64
	score float32
}

func insertSorted(list []*scoredEntry, item *scoredEntry, maxLen int) []*scoredEntry {
	pos := sort.Search(len(list), func(i int) bool {
		return list[i].score < item.score
	})

	if len(list) < maxLen {
		list = append(list, nil)
		copy(list[pos+1:], list[pos:len(list)-1])
		list[pos] = item
	} else if pos < maxLen {
		copy(list[pos+1:], list[pos:maxLen-1])
		list[pos] = item
	}
	return list
}

func (db *DB) searchVectorTwoPass(queryVec []float32, candidateIDs []int64, limit int) ([]*SearchResult, error) {
	const vectorBatchSize = 500
	var topK []*scoredEntry

	candidateSet := make(map[int64]struct{}, len(candidateIDs))
	for _, id := range candidateIDs {
		candidateSet[id] = struct{}{}
	}
	hasFilter := len(candidateSet) > 0

	offset := 0
	for {
		rows, err := db.conn.Query(
			"SELECT id, embedding, norm FROM chunks ORDER BY id LIMIT ? OFFSET ?",
			vectorBatchSize, offset,
		)
		if err != nil {
			return nil, err
		}

		batchCount := 0
		for rows.Next() {
			var id int64
			var embBytes []byte
			var discard float32 // SQL still selects norm; scan it to keep column alignment
			if err := rows.Scan(&id, &embBytes, &discard); err != nil {
				rows.Close()
				return nil, err
			}

			if hasFilter {
				if _, ok := candidateSet[id]; !ok {
					continue
				}
			}

			vec := BytesToFloat32Slice(embBytes)
			score := CosineSimilarity(queryVec, vec)
			batchCount++

			topK = insertSorted(topK, &scoredEntry{id: id, score: score}, limit)
		}
		rows.Close()

		if batchCount < vectorBatchSize {
			break
		}
		offset += vectorBatchSize
	}

	if len(topK) == 0 {
		return nil, nil
	}

	scoreMap := make(map[int64]float32)
	idOrderMap := make(map[int64]int)
	topIDs := make([]interface{}, len(topK))
	for i, s := range topK {
		topIDs[i] = s.id
		scoreMap[s.id] = s.score
		idOrderMap[s.id] = i
	}

	placeholders := make([]string, len(topK))
	for i := range placeholders {
		placeholders[i] = "?"
	}
	query := fmt.Sprintf(
		"SELECT id, uuid, document_path, chunk_index, content, embedding, hash FROM chunks WHERE id IN (%s)",
		strings.Join(placeholders, ","),
	)

	detailRows, err := db.conn.Query(query, topIDs...)
	if err != nil {
		return nil, err
	}
	defer detailRows.Close()

	unsortedResults := make([]*SearchResult, 0, len(topK))
	for detailRows.Next() {
		var c Chunk
		var embBytes []byte
		if err := detailRows.Scan(&c.ID, &c.UUID, &c.DocumentPath, &c.ChunkIndex, &c.Content, &embBytes, &c.Hash); err != nil {
			return nil, err
		}
		c.Embedding = BytesToFloat32Slice(embBytes)

		unsortedResults = append(unsortedResults, &SearchResult{
			Chunk:       &c,
			VectorRank:  0,
			CosineScore: scoreMap[c.ID],
		})
	}

	sort.Slice(unsortedResults, func(i, j int) bool {
		return idOrderMap[unsortedResults[i].Chunk.ID] < idOrderMap[unsortedResults[j].Chunk.ID]
	})

	for i, res := range unsortedResults {
		res.VectorRank = i + 1
	}

	return unsortedResults, nil
}

func TestChunkDimModelMetadata(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-dim-model-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)
	t.Setenv("HOME", tempDir)

	db, err := Open()
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	defer db.Close()

	docPath := filepath.Join(tempDir, "dimtest.md")
	doc := &Document{Path: docPath, Hash: "dimhash", UpdatedAt: time.Now()}
	if err := db.SaveDocument(doc); err != nil {
		t.Fatalf("SaveDocument: %v", err)
	}

	chunks := []*Chunk{
		{
			UUID:         "dim-c1",
			DocumentPath: docPath,
			ChunkIndex:   0,
			Content:      "content with dim metadata",
			Embedding:    []float32{1.0, 0.0, 0.0},
			Hash:         "dimhash1",
			Dim:          3,
			Model:        "test-model",
		},
		{
			UUID:         "dim-c2",
			DocumentPath: docPath,
			ChunkIndex:   1,
			Content:      "content without dim",
			Embedding:    []float32{0.0, 1.0, 0.0},
			Hash:         "dimhash2",
		},
	}

	if err := db.SaveChunks(chunks); err != nil {
		t.Fatalf("SaveChunks: %v", err)
	}

	fetched, err := db.GetChunksForDocument(docPath)
	if err != nil {
		t.Fatalf("GetChunksForDocument: %v", err)
	}
	if len(fetched) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(fetched))
	}

	if fetched[0].Dim != 3 {
		t.Errorf("expected Dim=3, got %d", fetched[0].Dim)
	}
	if fetched[0].Model != "test-model" {
		t.Errorf("expected Model=test-model, got %q", fetched[0].Model)
	}

	if fetched[1].Dim != 0 {
		t.Errorf("expected Dim=0 (unset), got %d", fetched[1].Dim)
	}
	if fetched[1].Model != "" {
		t.Errorf("expected Model='' (unset), got %q", fetched[1].Model)
	}
}

func TestDetectMixedEmbeddingSpaces_Uniform(t *testing.T) {
	d := setupDB(t)
	docPath := filepath.Join(t.TempDir(), "uniform.md")
	if err := d.SaveDocument(&Document{Path: docPath, Hash: "u", UpdatedAt: time.Now()}); err != nil {
		t.Fatalf("SaveDocument: %v", err)
	}
	chunks := make([]*Chunk, 5)
	for i := range chunks {
		emb := make([]float32, 768)
		emb[0] = 1.0
		chunks[i] = &Chunk{
			UUID:         "u-" + strconv.Itoa(i),
			DocumentPath: docPath,
			ChunkIndex:   i,
			Content:      "content",
			Embedding:    emb,
			Hash:         "u-hash",
			Dim:          768,
			Model:        "nomic-embed-text",
		}
	}
	if err := d.SaveChunks(chunks); err != nil {
		t.Fatalf("SaveChunks: %v", err)
	}

	spaces, err := d.DetectMixedEmbeddingSpaces()
	if err != nil {
		t.Fatalf("DetectMixedEmbeddingSpaces: %v", err)
	}
	if len(spaces) != 1 {
		t.Errorf("expected 1 embedding space, got %d", len(spaces))
	}
	key := "768/nomic-embed-text"
	if count, ok := spaces[key]; !ok || count != 5 {
		t.Errorf("expected %s with count 5, got %v", key, spaces)
	}
}

func TestDetectMixedEmbeddingSpaces_Mixed(t *testing.T) {
	d := setupDB(t)
	docPath := filepath.Join(t.TempDir(), "mixed.md")
	if err := d.SaveDocument(&Document{Path: docPath, Hash: "m", UpdatedAt: time.Now()}); err != nil {
		t.Fatalf("SaveDocument: %v", err)
	}
	chunks := []*Chunk{
		{UUID: "m1", DocumentPath: docPath, ChunkIndex: 0, Content: "c1", Embedding: make([]float32, 768), Hash: "h1", Dim: 768, Model: "model-a"},
		{UUID: "m2", DocumentPath: docPath, ChunkIndex: 1, Content: "c2", Embedding: make([]float32, 384), Hash: "h2", Dim: 384, Model: "model-b"},
		{UUID: "m3", DocumentPath: docPath, ChunkIndex: 2, Content: "c3", Embedding: make([]float32, 768), Hash: "h3", Dim: 768, Model: "model-a"},
	}
	if err := d.SaveChunks(chunks); err != nil {
		t.Fatalf("SaveChunks: %v", err)
	}

	spaces, err := d.DetectMixedEmbeddingSpaces()
	if err != nil {
		t.Fatalf("DetectMixedEmbeddingSpaces: %v", err)
	}
	if len(spaces) != 2 {
		t.Errorf("expected 2 embedding spaces, got %d: %v", len(spaces), spaces)
	}
}

func TestDetectMixedEmbeddingSpaces_Empty(t *testing.T) {
	d := setupDB(t)

	spaces, err := d.DetectMixedEmbeddingSpaces()
	if err != nil {
		t.Fatalf("DetectMixedEmbeddingSpaces: %v", err)
	}
	if len(spaces) != 0 {
		t.Errorf("expected 0 embedding spaces for empty DB, got %d", len(spaces))
	}
}

func TestListDocuments_Empty(t *testing.T) {
	d := setupDB(t)

	docs, err := d.ListDocuments()
	if err != nil {
		t.Fatalf("ListDocuments on empty DB: %v", err)
	}
	if len(docs) != 0 {
		t.Errorf("expected 0 documents, got %d", len(docs))
	}
}

func TestListDocuments_Multiple(t *testing.T) {
	d := setupDB(t)

	paths := []string{"/doc/a.md", "/doc/b.md", "/doc/c.md"}
	for i, p := range paths {
		doc := &Document{Path: p, Hash: fmt.Sprintf("hash%d", i), UpdatedAt: time.Now()}
		if err := d.SaveDocument(doc); err != nil {
			t.Fatalf("SaveDocument(%q): %v", p, err)
		}
	}

	docs, err := d.ListDocuments()
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	if len(docs) != len(paths) {
		t.Fatalf("expected %d documents, got %d", len(paths), len(docs))
	}

	found := make(map[string]bool)
	for _, d := range docs {
		found[d.Path] = true
	}
	for _, p := range paths {
		if !found[p] {
			t.Errorf("expected document %q in list, not found", p)
		}
	}

	// Ensure order is by updated_at DESC (most recent first).
	for i := 1; i < len(docs); i++ {
		if docs[i].UpdatedAt.After(docs[i-1].UpdatedAt) {
			t.Errorf("documents not in descending updated_at order: %s (%v) before %s (%v)",
				docs[i].Path, docs[i].UpdatedAt, docs[i-1].Path, docs[i-1].UpdatedAt)
		}
	}
}

func TestListDocuments_AfterDelete(t *testing.T) {
	d := setupDB(t)

	doc := &Document{Path: "/doc/to-delete.md", Hash: "del1", UpdatedAt: time.Now()}
	if err := d.SaveDocument(doc); err != nil {
		t.Fatalf("SaveDocument: %v", err)
	}
	doc2 := &Document{Path: "/doc/keep.md", Hash: "keep1", UpdatedAt: time.Now()}
	if err := d.SaveDocument(doc2); err != nil {
		t.Fatalf("SaveDocument: %v", err)
	}

	if err := d.DeleteDocument("/doc/to-delete.md"); err != nil {
		t.Fatalf("DeleteDocument: %v", err)
	}

	docs, err := d.ListDocuments()
	if err != nil {
		t.Fatalf("ListDocuments after delete: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected 1 document after delete, got %d", len(docs))
	}
	if docs[0].Path != "/doc/keep.md" {
		t.Errorf("expected remaining document path 'keep.md', got %q", docs[0].Path)
	}
}

func TestSetFolderContext_Create(t *testing.T) {
	d := setupDB(t)

	err := d.SetFolderContext("/home/user/docs", "Documentation root")
	if err != nil {
		t.Fatalf("SetFolderContext: %v", err)
	}

	contexts, err := d.GetFolderContexts()
	if err != nil {
		t.Fatalf("GetFolderContexts: %v", err)
	}
	if len(contexts) != 1 {
		t.Fatalf("expected 1 context, got %d", len(contexts))
	}
	if contexts[0].PathPrefix != "/home/user/docs" {
		t.Errorf("expected path_prefix /home/user/docs, got %q", contexts[0].PathPrefix)
	}
	if contexts[0].ContextText != "Documentation root" {
		t.Errorf("expected context_text 'Documentation root', got %q", contexts[0].ContextText)
	}
}

func TestSetFolderContext_Update(t *testing.T) {
	d := setupDB(t)

	if err := d.SetFolderContext("/home/user/docs", "Original text"); err != nil {
		t.Fatalf("SetFolderContext (create): %v", err)
	}
	if err := d.SetFolderContext("/home/user/docs", "Updated text"); err != nil {
		t.Fatalf("SetFolderContext (update): %v", err)
	}

	contexts, err := d.GetFolderContexts()
	if err != nil {
		t.Fatalf("GetFolderContexts: %v", err)
	}
	if len(contexts) != 1 {
		t.Fatalf("expected 1 context, got %d", len(contexts))
	}
	if contexts[0].ContextText != "Updated text" {
		t.Errorf("expected updated context_text 'Updated text', got %q", contexts[0].ContextText)
	}
}

func TestGetFolderContexts_Multiple(t *testing.T) {
	d := setupDB(t)

	entries := []struct {
		path string
		text string
	}{
		{"/projects/api", "API docs"},
		{"/projects/web", "Web app docs"},
		{"/personal", "Personal notes"},
	}
	for _, e := range entries {
		if err := d.SetFolderContext(e.path, e.text); err != nil {
			t.Fatalf("SetFolderContext(%q): %v", e.path, err)
		}
	}

	contexts, err := d.GetFolderContexts()
	if err != nil {
		t.Fatalf("GetFolderContexts: %v", err)
	}
	if len(contexts) != len(entries) {
		t.Fatalf("expected %d contexts, got %d", len(entries), len(contexts))
	}

	// Verify ordered by path_prefix.
	for i := 1; i < len(contexts); i++ {
		if contexts[i].PathPrefix < contexts[i-1].PathPrefix {
			t.Errorf("contexts not in ascending path_prefix order: %q before %q",
				contexts[i].PathPrefix, contexts[i-1].PathPrefix)
		}
	}
}

func TestGetFolderContexts_Empty(t *testing.T) {
	d := setupDB(t)

	contexts, err := d.GetFolderContexts()
	if err != nil {
		t.Fatalf("GetFolderContexts on empty DB: %v", err)
	}
	if len(contexts) != 0 {
		t.Errorf("expected 0 contexts, got %d", len(contexts))
	}
}

func TestGetMatchingContext_LongestPrefix(t *testing.T) {
	d := setupDB(t)

	if err := d.SetFolderContext("/home/user", "User root"); err != nil {
		t.Fatalf("SetFolderContext: %v", err)
	}
	if err := d.SetFolderContext("/home/user/docs", "User docs"); err != nil {
		t.Fatalf("SetFolderContext: %v", err)
	}
	if err := d.SetFolderContext("/home/user/docs/api", "API docs"); err != nil {
		t.Fatalf("SetFolderContext: %v", err)
	}

	tests := []struct {
		name     string
		path     string
		wantText string
	}{
		{"exact match api", "/home/user/docs/api", "API docs"},
		{"exact match docs", "/home/user/docs", "User docs"},
		{"nested in api", "/home/user/docs/api/handlers", "API docs"},
		{"nested in docs", "/home/user/docs/guide.md", "User docs"},
		{"root prefix", "/home/user/other", "User root"},
		{"root prefix dotfiles", "/home/user/config/settings", "User root"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := d.GetMatchingContext(tt.path)
			if err != nil {
				t.Fatalf("GetMatchingContext(%q): %v", tt.path, err)
			}
			if got == nil {
				t.Fatalf("GetMatchingContext(%q) returned nil, expected context with text %q", tt.path, tt.wantText)
			}
			if got.ContextText != tt.wantText {
				t.Errorf("GetMatchingContext(%q) = %q, want %q", tt.path, got.ContextText, tt.wantText)
			}
		})
	}
}

func TestGetMatchingContext_NoMatch(t *testing.T) {
	d := setupDB(t)

	if err := d.SetFolderContext("/other/path", "Other"); err != nil {
		t.Fatalf("SetFolderContext: %v", err)
	}

	got, err := d.GetMatchingContext("/unrelated/path/file.md")
	if err != nil {
		t.Fatalf("GetMatchingContext: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for non-matching path, got %+v", got)
	}
}

func TestGetMatchingContext_EmptyDB(t *testing.T) {
	d := setupDB(t)

	got, err := d.GetMatchingContext("/any/path")
	if err != nil {
		t.Fatalf("GetMatchingContext on empty DB: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil on empty DB, got %+v", got)
	}
}

func TestLegacyNULLEmbeddingDimBackfill(t *testing.T) {
	d := setupDB(t)

	docPath := filepath.Join(t.TempDir(), "legacy.md")
	if err := d.SaveDocument(&Document{Path: docPath, Hash: "leg", UpdatedAt: time.Now()}); err != nil {
		t.Fatalf("SaveDocument: %v", err)
	}

	emb := []float32{1.0, 0.0, 0.0}
	embBytes := Float32SliceToBytes(emb)
	sigBytes := SignBinarySignature(emb)
	norm := l2Norm(emb)

	_, err := d.conn.Exec(
		`INSERT INTO chunks (uuid, document_path, chunk_index, content, embedding, hash, norm, binary_signature, embedding_dim, embedding_model)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL)`,
		"legacy-1", docPath, 0, "legacy content", embBytes, "leg-hash", norm, sigBytes,
	)
	if err != nil {
		t.Fatalf("direct INSERT with NULLs: %v", err)
	}

	spaces, err := d.DetectMixedEmbeddingSpaces()
	if err != nil {
		t.Fatalf("DetectMixedEmbeddingSpaces on pre-backfill data: %v", err)
	}
	if count, ok := spaces["unknown/unknown"]; !ok || count != 1 {
		t.Errorf("expected unknown/unknown with count 1 before backfill, got %v", spaces)
	}

	_, err = d.conn.Exec(`UPDATE chunks SET embedding_dim = length(embedding) / 4 WHERE embedding_dim IS NULL AND embedding IS NOT NULL AND length(embedding) % 4 = 0`)
	if err != nil {
		t.Fatalf("backfill embedding_dim: %v", err)
	}
	_, err = d.conn.Exec(`UPDATE chunks SET embedding_model = 'unknown' WHERE embedding_dim IS NOT NULL AND (embedding_model IS NULL OR embedding_model = '')`)
	if err != nil {
		t.Fatalf("backfill embedding_model: %v", err)
	}

	spaces, err = d.DetectMixedEmbeddingSpaces()
	if err != nil {
		t.Fatalf("DetectMixedEmbeddingSpaces after backfill: %v", err)
	}
	key := "3/unknown"
	if count, ok := spaces[key]; !ok || count != 1 {
		t.Errorf("expected %s with count 1 after backfill, got %v", key, spaces)
	}

	results, err := d.SearchVector([]float32{0.9, 0.1, 0.0}, 10)
	if err != nil {
		t.Fatalf("SearchVector after backfill: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected non-empty search results after backfill")
	}
}

func TestGetMatchingContext_ExactPrefix(t *testing.T) {
	d := setupDB(t)

	if err := d.SetFolderContext("/home/user/docs", "Docs"); err != nil {
		t.Fatalf("SetFolderContext: %v", err)
	}

	got, err := d.GetMatchingContext("/home/user/docs")
	if err != nil {
		t.Fatalf("GetMatchingContext: %v", err)
	}
	if got == nil {
		t.Fatal("GetMatchingContext returned nil for exact prefix match")
	}
	if got.ContextText != "Docs" {
		t.Errorf("want 'Docs', got %q", got.ContextText)
	}
}
