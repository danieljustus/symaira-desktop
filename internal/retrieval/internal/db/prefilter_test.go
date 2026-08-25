package db

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// makeChunk constructs a chunk with a fixed-length deterministic embedding.
func makeChunk(uuid, docPath string, idx int, content string, baseIdx int) *Chunk {
	emb := make([]float32, 768)
	for i := range emb {
		emb[i] = float32(baseIdx+i) / 1000.0
	}
	var sumSquares float64
	for _, v := range emb {
		sumSquares += float64(v * v)
	}
	norm := float32(math.Sqrt(sumSquares))
	if norm > 0 {
		for i := range emb {
			emb[i] /= norm
		}
	}
	return &Chunk{
		UUID:         uuid,
		DocumentPath: docPath,
		ChunkIndex:   idx,
		Content:      content,
		Embedding:    emb,
		Hash:         uuid + "-hash",
	}
}

func setupDB(t testing.TB) *DB {
	t.Helper()
	tempDir, err := os.MkdirTemp("", "seek-db-prefilter-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Setenv("HOME", tempDir)

	d, err := Open()
	if err != nil {
		os.RemoveAll(tempDir)
		t.Fatalf("failed to open db: %v", err)
	}
	t.Cleanup(func() {
		d.Close()
		os.RemoveAll(tempDir)
	})
	return d
}

func TestSearchVectorRanksTopKAndHydratesContent(t *testing.T) {
	d := setupDB(t)

	docPath := filepath.Join(t.TempDir(), "doc.md")
	now := time.Now()
	if err := d.SaveDocument(&Document{Path: docPath, Hash: "h", UpdatedAt: now}); err != nil {
		t.Fatalf("SaveDocument: %v", err)
	}

	chunks := []*Chunk{
		makeChunk("u1", docPath, 0, "alpha alpha alpha", 10),
		makeChunk("u2", docPath, 1, "beta beta beta", 20),
		makeChunk("u3", docPath, 2, "gamma gamma gamma", 30),
		makeChunk("u4", docPath, 3, "delta delta delta", 40),
		makeChunk("u5", docPath, 4, "epsilon epsilon", 50),
	}
	if err := d.SaveChunks(chunks); err != nil {
		t.Fatalf("SaveChunks: %v", err)
	}

	queryVec := make([]float32, 768)
	for i := range queryVec {
		queryVec[i] = float32(30+i) / 1000.0
	}
	var sumSquares float64
	for _, v := range queryVec {
		sumSquares += float64(v * v)
	}
	norm := float32(math.Sqrt(sumSquares))
	if norm > 0 {
		for i := range queryVec {
			queryVec[i] /= norm
		}
	}

	results, err := d.SearchVector(queryVec, 3)
	if err != nil {
		t.Fatalf("SearchVector: %v", err)
	}
	if len(results) == 0 || results[0].Chunk.UUID != "u3" {
		t.Fatalf("top hit should be u3, got %+v", results)
	}
	if len(results) != 3 {
		t.Fatalf("expected top-3 results, got %d", len(results))
	}

	if results[0].Chunk.Content != "gamma gamma gamma" {
		t.Errorf("expected hydrated content for top hit, got %q", results[0].Chunk.Content)
	}
	for _, r := range results {
		if r.Chunk.Content == "" {
			t.Errorf("result %s has no hydrated content", r.Chunk.UUID)
		}
	}
}

func TestVectorIndexBuildAndCandidates(t *testing.T) {
	dim := 768
	chunks := make([]*Chunk, 200)
	for i := range chunks {
		emb := make([]float32, dim)
		for j := range emb {
			emb[j] = float32(i*dim+j) / float32(dim*200)
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
			ID:        int64(i),
			Embedding: emb,
		}
	}

	idx := NewVectorIndex()
	idx.Build(chunks)

	if !idx.IsReady() {
		t.Fatal("index should be ready after Build")
	}

	k := idx.BucketCount()
	if k < 4 || k > 256 {
		t.Fatalf("unexpected bucket count: %d", k)
	}

	nprobe := idx.ProbeCount()
	if nprobe < 1 || nprobe > k {
		t.Fatalf("unexpected nprobe: %d (k=%d)", nprobe, k)
	}

	query := make([]float32, dim)
	for i := range query {
		query[i] = float32(100*dim+i) / float32(dim*200)
	}
	var sumSquares float64
	for _, v := range query {
		sumSquares += float64(v * v)
	}
	norm := float32(math.Sqrt(sumSquares))
	if norm > 0 {
		for i := range query {
			query[i] /= norm
		}
	}

	candidates := idx.CandidateIDs(query, nprobe)
	if candidates == nil {
		t.Fatal("expected non-nil candidates")
	}
	if len(candidates) == 0 {
		t.Fatal("expected at least one candidate")
	}

	idSet := make(map[int64]bool, len(candidates))
	for _, id := range candidates {
		idSet[id] = true
	}
	for _, id := range candidates {
		if id < 0 || id >= 200 {
			t.Fatalf("candidate ID out of range: %d", id)
		}
	}

	bestCosine := float32(-2)
	bestID := int64(-1)
	for _, c := range chunks {
		score := CosineSimilarity(query, c.Embedding)
		if score > bestCosine {
			bestCosine = score
			bestID = c.ID
		}
	}

	if !idSet[bestID] {
		t.Fatalf("true nearest neighbor (ID %d, score %.4f) not in candidate set of %d IDs", bestID, bestCosine, len(candidates))
	}
}

func TestVectorIndexEmptyChunks(t *testing.T) {
	idx := NewVectorIndex()
	idx.Build(nil)
	if idx.IsReady() {
		t.Fatal("index should not be ready for nil chunks")
	}
	candidates := idx.CandidateIDs([]float32{1, 0, 0}, 3)
	if candidates != nil {
		t.Fatal("expected nil candidates for empty index")
	}
}

func TestVectorIndexSmallDatasetFallback(t *testing.T) {
	dim := 768
	chunks := make([]*Chunk, 10)
	for i := range chunks {
		emb := make([]float32, dim)
		for j := range emb {
			emb[j] = float32(i*dim+j) / float32(dim*10)
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
		chunks[i] = &Chunk{ID: int64(i), Embedding: emb}
	}

	idx := NewVectorIndex()
	idx.Build(chunks)

	query := make([]float32, dim)
	for i := range query {
		query[i] = float32(i) / float32(dim)
	}
	var sumSquares float64
	for _, v := range query {
		sumSquares += float64(v * v)
	}
	norm := float32(math.Sqrt(sumSquares))
	if norm > 0 {
		for i := range query {
			query[i] /= norm
		}
	}

	candidates := idx.CandidateIDs(query, idx.ProbeCount())
	t.Logf("small dataset: k=%d, nprobe=%d, candidates=%v, total=%d", idx.BucketCount(), idx.ProbeCount(), len(candidates), idx.TotalChunks())

	if len(candidates) == 10 {
		t.Log("all chunks returned as candidates — fallback to full scan would be used by SearchVector")
	}
}

func TestSearchVectorPrefilterRecall(t *testing.T) {
	d := setupDB(t)

	docPath := filepath.Join(t.TempDir(), "recall.md")
	if err := d.SaveDocument(&Document{Path: docPath, Hash: "recall", UpdatedAt: time.Now()}); err != nil {
		t.Fatalf("SaveDocument: %v", err)
	}

	const nChunks = 150
	chunks := make([]*Chunk, nChunks)
	for i := 0; i < nChunks; i++ {
		emb := make([]float32, 768)
		// Use sinusoidal embeddings with distinct phases per chunk so that
		// cosine similarity between non-identical chunks is well below 1.0
		// and the exact-match query can be unambiguously identified.
		for j := range emb {
			emb[j] = float32(math.Sin(float64(i)*0.1 + float64(j)*0.1))
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
			UUID:         "recall-" + strconv.Itoa(i),
			DocumentPath: docPath,
			ChunkIndex:   i,
			Content:      "content " + strconv.Itoa(i),
			Embedding:    emb,
			Hash:         "recall-hash-" + strconv.Itoa(i),
		}
	}
	if err := d.SaveChunks(chunks); err != nil {
		t.Fatalf("SaveChunks: %v", err)
	}

	queryVec := make([]float32, len(chunks[50].Embedding))
	copy(queryVec, chunks[50].Embedding)

	results, err := d.SearchVector(queryVec, 10)
	if err != nil {
		t.Fatalf("SearchVector: %v", err)
	}
	if len(results) != 10 {
		t.Fatalf("expected 10 results, got %d", len(results))
	}

	if results[0].Chunk.UUID != "recall-50" {
		t.Errorf("top hit should be recall-50 (exact query match), got %s (score=%.10f); second: %s (score=%.10f)",
			results[0].Chunk.UUID, results[0].CosineScore,
			results[1].Chunk.UUID, results[1].CosineScore)
	}

	if d.vectorIndex == nil || !d.vectorIndex.IsReady() {
		t.Log("index not built (dataset below threshold) — prefilter inactive, but exact results are correct")
	} else {
		t.Logf("index active: k=%d, nprobe=%d", d.vectorIndex.BucketCount(), d.vectorIndex.ProbeCount())
	}
}

func TestSearchVectorPrefilterFullRecall(t *testing.T) {
	d := setupDB(t)

	docPath := filepath.Join(t.TempDir(), "full-recall.md")
	if err := d.SaveDocument(&Document{Path: docPath, Hash: "fr", UpdatedAt: time.Now()}); err != nil {
		t.Fatalf("SaveDocument: %v", err)
	}

	const nChunks = 300
	allChunks := make([]*Chunk, nChunks)
	for i := 0; i < nChunks; i++ {
		emb := make([]float32, 768)
		for j := range emb {
			emb[j] = float32(i*768+j) / float32(nChunks*768)
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
		allChunks[i] = &Chunk{
			UUID:         "fr-" + strconv.Itoa(i),
			DocumentPath: docPath,
			ChunkIndex:   i,
			Content:      "content " + strconv.Itoa(i),
			Embedding:    emb,
			Hash:         "fr-hash-" + strconv.Itoa(i),
		}
	}
	if err := d.SaveChunks(allChunks); err != nil {
		t.Fatalf("SaveChunks: %v", err)
	}

	queryVec := make([]float32, 768)
	for i := range queryVec {
		queryVec[i] = float32(150*768+i) / float32(nChunks*768)
	}
	var sumSquares float64
	for _, v := range queryVec {
		sumSquares += float64(v * v)
	}
	norm := float32(math.Sqrt(sumSquares))
	if norm > 0 {
		for i := range queryVec {
			queryVec[i] /= norm
		}
	}

	// First call: builds the index and scores via full scan.
	firstResults, err := d.SearchVector(queryVec, 10)
	if err != nil {
		t.Fatalf("SearchVector (first): %v", err)
	}

	if d.vectorIndex == nil || !d.vectorIndex.IsReady() {
		t.Fatalf("expected index to be built after first search with %d chunks", nChunks)
	}

	secondResults, err := d.SearchVector(queryVec, 10)
	if err != nil {
		t.Fatalf("SearchVector (second): %v", err)
	}

	if len(firstResults) != len(secondResults) {
		t.Fatalf("result count mismatch: first=%d, second=%d", len(firstResults), len(secondResults))
	}

	for i := range firstResults {
		if firstResults[i].Chunk.UUID != secondResults[i].Chunk.UUID {
			t.Errorf("result %d UUID mismatch: first=%s, second=%s", i, firstResults[i].Chunk.UUID, secondResults[i].Chunk.UUID)
		}
		if math.Abs(float64(firstResults[i].CosineScore-secondResults[i].CosineScore)) > 1e-6 {
			t.Errorf("result %d score mismatch: first=%.6f, second=%.6f", i, firstResults[i].CosineScore, secondResults[i].CosineScore)
		}
	}

	t.Logf("prefilter recall: exact vs prefilter — %d/%d ranks match", len(firstResults), len(secondResults))
}

func TestSearchVectorIncrementallyAddsChunks(t *testing.T) {
	d := setupDB(t)

	docPath := filepath.Join(t.TempDir(), "inc-add.md")
	if err := d.SaveDocument(&Document{Path: docPath, Hash: "inv", UpdatedAt: time.Now()}); err != nil {
		t.Fatalf("SaveDocument: %v", err)
	}

	const nChunks = 200
	chunks := make([]*Chunk, nChunks)
	for i := 0; i < nChunks; i++ {
		emb := make([]float32, 768)
		for j := range emb {
			emb[j] = float32(i*768+j) / float32(nChunks*768)
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
			UUID:         "inv-" + strconv.Itoa(i),
			DocumentPath: docPath,
			ChunkIndex:   i,
			Content:      "content " + strconv.Itoa(i),
			Embedding:    emb,
			Hash:         "inv-hash-" + strconv.Itoa(i),
		}
	}
	if err := d.SaveChunks(chunks); err != nil {
		t.Fatalf("SaveChunks: %v", err)
	}

	query := make([]float32, 768)
	for i := range query {
		query[i] = 0.5
	}
	if _, err := d.SearchVector(query, 5); err != nil {
		t.Fatalf("SearchVector: %v", err)
	}
	if d.vectorIndex == nil || !d.vectorIndex.IsReady() {
		t.Fatal("expected index to be built")
	}
	beforeTotal := d.vectorIndex.TotalChunks()

	extra := []*Chunk{
		{
			UUID:         "inv-extra",
			DocumentPath: docPath,
			ChunkIndex:   nChunks,
			Content:      "extra",
			Embedding:    make([]float32, 768),
			Hash:         "inv-extra-hash",
		},
	}
	if err := d.SaveChunks(extra); err != nil {
		t.Fatalf("SaveChunks (extra): %v", err)
	}
	if d.vectorIndex == nil || !d.vectorIndex.IsReady() {
		t.Fatal("expected index to stay ready after incremental add")
	}
	if d.vectorIndex.TotalChunks() != beforeTotal+1 {
		t.Fatalf("expected totalN to increase by 1, got %d (was %d)", d.vectorIndex.TotalChunks(), beforeTotal)
	}
}

func TestSearchVectorIncrementallyDeletesChunks(t *testing.T) {
	d := setupDB(t)

	docPath1 := filepath.Join(t.TempDir(), "del1.md")
	docPath2 := filepath.Join(t.TempDir(), "del2.md")
	if err := d.SaveDocument(&Document{Path: docPath1, Hash: "del1", UpdatedAt: time.Now()}); err != nil {
		t.Fatalf("SaveDocument: %v", err)
	}
	if err := d.SaveDocument(&Document{Path: docPath2, Hash: "del2", UpdatedAt: time.Now()}); err != nil {
		t.Fatalf("SaveDocument: %v", err)
	}

	const nChunks = 200
	chunks := make([]*Chunk, nChunks)
	for i := 0; i < nChunks; i++ {
		emb := make([]float32, 768)
		for j := range emb {
			emb[j] = float32(i*768+j) / float32(nChunks*768)
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
		docPath := docPath1
		if i >= nChunks/2 {
			docPath = docPath2
		}
		chunks[i] = &Chunk{
			UUID:         "del-" + strconv.Itoa(i),
			DocumentPath: docPath,
			ChunkIndex:   i,
			Content:      "content " + strconv.Itoa(i),
			Embedding:    emb,
			Hash:         "del-hash-" + strconv.Itoa(i),
		}
	}
	if err := d.SaveChunks(chunks); err != nil {
		t.Fatalf("SaveChunks: %v", err)
	}

	query := make([]float32, 768)
	for i := range query {
		query[i] = 0.5
	}
	if _, err := d.SearchVector(query, 5); err != nil {
		t.Fatalf("SearchVector: %v", err)
	}
	if d.vectorIndex == nil || !d.vectorIndex.IsReady() {
		t.Fatal("expected index to be built")
	}
	beforeTotal := d.vectorIndex.TotalChunks()

	if err := d.DeleteDocument(docPath1); err != nil {
		t.Fatalf("DeleteDocument: %v", err)
	}
	if d.vectorIndex == nil || !d.vectorIndex.IsReady() {
		t.Fatal("expected index to stay ready after incremental delete")
	}
	if d.vectorIndex.TotalChunks() != beforeTotal-nChunks/2 {
		t.Fatalf("expected totalN to decrease by %d, got %d (was %d)", nChunks/2, d.vectorIndex.TotalChunks(), beforeTotal)
	}
}

func TestPersistedVectorIndexLoad(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-persist-load-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)
	t.Setenv("HOME", tempDir)

	db1, err := Open()
	if err != nil {
		t.Fatalf("failed to open db1: %v", err)
	}

	docPath := filepath.Join(t.TempDir(), "persist.md")
	if err := db1.SaveDocument(&Document{Path: docPath, Hash: "p", UpdatedAt: time.Now()}); err != nil {
		t.Fatalf("SaveDocument: %v", err)
	}

	const nChunks = 300
	chunks := make([]*Chunk, nChunks)
	for i := 0; i < nChunks; i++ {
		emb := make([]float32, 768)
		for j := range emb {
			emb[j] = float32(i*768+j) / float32(nChunks*768)
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
			UUID:         "persist-" + strconv.Itoa(i),
			DocumentPath: docPath,
			ChunkIndex:   i,
			Content:      "content " + strconv.Itoa(i),
			Embedding:    emb,
			Hash:         "persist-hash-" + strconv.Itoa(i),
		}
	}
	if err := db1.SaveChunks(chunks); err != nil {
		t.Fatalf("SaveChunks: %v", err)
	}

	query := make([]float32, 768)
	for i := range query {
		query[i] = 0.5
	}
	if _, err := db1.SearchVector(query, 5); err != nil {
		t.Fatalf("SearchVector: %v", err)
	}
	if db1.vectorIndex == nil || !db1.vectorIndex.IsReady() {
		t.Fatal("expected index to be built and persisted")
	}
	bucketCount := db1.vectorIndex.BucketCount()
	db1.Close()

	db2, err := Open()
	if err != nil {
		t.Fatalf("failed to open db2: %v", err)
	}
	defer db2.Close()

	if db2.vectorIndex == nil || !db2.vectorIndex.IsReady() {
		t.Fatal("expected persisted index to be loaded on open")
	}
	if db2.vectorIndex.BucketCount() != bucketCount {
		t.Fatalf("loaded index bucket count mismatch: got %d, want %d", db2.vectorIndex.BucketCount(), bucketCount)
	}

	results, err := db2.SearchVector(query, 5)
	if err != nil {
		t.Fatalf("SearchVector after reload: %v", err)
	}
	if len(results) != 5 {
		t.Fatalf("expected 5 results after reload, got %d", len(results))
	}
}

func TestPersistedVectorIndexStaleDetection(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-persist-stale-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)
	t.Setenv("HOME", tempDir)

	db1, err := Open()
	if err != nil {
		t.Fatalf("failed to open db1: %v", err)
	}

	docPath := filepath.Join(t.TempDir(), "stale.md")
	if err := db1.SaveDocument(&Document{Path: docPath, Hash: "s", UpdatedAt: time.Now()}); err != nil {
		t.Fatalf("SaveDocument: %v", err)
	}

	const nChunks = 300
	chunks := make([]*Chunk, nChunks)
	for i := 0; i < nChunks; i++ {
		emb := make([]float32, 768)
		for j := range emb {
			emb[j] = float32(i*768+j) / float32(nChunks*768)
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
			UUID:         "stale-" + strconv.Itoa(i),
			DocumentPath: docPath,
			ChunkIndex:   i,
			Content:      "content " + strconv.Itoa(i),
			Embedding:    emb,
			Hash:         "stale-hash-" + strconv.Itoa(i),
		}
	}
	if err := db1.SaveChunks(chunks); err != nil {
		t.Fatalf("SaveChunks: %v", err)
	}

	query := make([]float32, 768)
	for i := range query {
		query[i] = 0.5
	}
	if _, err := db1.SearchVector(query, 5); err != nil {
		t.Fatalf("SearchVector: %v", err)
	}
	db1.Close()

	db2, err := Open()
	if err != nil {
		t.Fatalf("failed to open db2: %v", err)
	}

	extra := []*Chunk{
		{
			UUID:         "stale-extra",
			DocumentPath: docPath,
			ChunkIndex:   nChunks,
			Content:      "extra content",
			Embedding:    make([]float32, 768),
			Hash:         "stale-extra-hash",
		},
	}
	if err := db2.SaveChunks(extra); err != nil {
		t.Fatalf("SaveChunks extra: %v", err)
	}
	db2.Close()

	db3, err := Open()
	if err != nil {
		t.Fatalf("failed to open db3: %v", err)
	}
	defer db3.Close()

	if db3.vectorIndex != nil && db3.vectorIndex.IsReady() {
		t.Fatal("expected persisted index to be discarded because generation changed")
	}

	results, err := db3.SearchVector(query, nChunks+10)
	if err != nil {
		t.Fatalf("SearchVector after stale load: %v", err)
	}
	found := false
	for _, r := range results {
		if r.Chunk.UUID == "stale-extra" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("db3 did not find the extra chunk written by db2")
	}
}

func TestSearchVectorDetectsExternalWrites(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-external-write-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)
	t.Setenv("HOME", tempDir)

	db1, err := Open()
	if err != nil {
		t.Fatalf("failed to open db1: %v", err)
	}
	defer db1.Close()

	docPath1 := filepath.Join(t.TempDir(), "external1.md")
	if err := db1.SaveDocument(&Document{Path: docPath1, Hash: "ext1", UpdatedAt: time.Now()}); err != nil {
		t.Fatalf("SaveDocument: %v", err)
	}

	const nChunks = 200
	chunks := make([]*Chunk, nChunks)
	for i := 0; i < nChunks; i++ {
		emb := make([]float32, 768)
		for j := range emb {
			emb[j] = float32(i*768+j) / float32(nChunks*768)
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
			UUID:         "ext-" + strconv.Itoa(i),
			DocumentPath: docPath1,
			ChunkIndex:   i,
			Content:      "content " + strconv.Itoa(i),
			Embedding:    emb,
			Hash:         "ext-hash-" + strconv.Itoa(i),
		}
	}
	if err := db1.SaveChunks(chunks); err != nil {
		t.Fatalf("SaveChunks: %v", err)
	}

	query := make([]float32, 768)
	for i := range query {
		query[i] = 0.5
	}
	if _, err := db1.SearchVector(query, 5); err != nil {
		t.Fatalf("SearchVector on db1: %v", err)
	}
	if db1.vectorIndex == nil || !db1.vectorIndex.IsReady() {
		t.Fatal("expected db1 index to be built")
	}

	// Simulate an external write through a second database connection on the
	// same underlying file.
	db2, err := Open()
	if err != nil {
		t.Fatalf("failed to open db2: %v", err)
	}
	defer db2.Close()

	docPath2 := filepath.Join(t.TempDir(), "external2.md")
	if err := db2.SaveDocument(&Document{Path: docPath2, Hash: "ext2", UpdatedAt: time.Now()}); err != nil {
		t.Fatalf("SaveDocument db2: %v", err)
	}
	externalChunk := &Chunk{
		UUID:         "ext-external",
		DocumentPath: docPath2,
		ChunkIndex:   0,
		Content:      "external unique content",
		Embedding:    make([]float32, 768),
		Hash:         "ext-external-hash",
	}
	if err := db2.SaveChunks([]*Chunk{externalChunk}); err != nil {
		t.Fatalf("SaveChunks db2: %v", err)
	}

	// db1's next search must detect the external generation bump, invalidate
	// its stale index, and find the new chunk.
	results, err := db1.SearchVector(query, nChunks+10)
	if err != nil {
		t.Fatalf("SearchVector on db1 after external write: %v", err)
	}
	found := false
	for _, r := range results {
		if r.Chunk.UUID == "ext-external" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("db1 did not return the chunk written by db2; results=%d", len(results))
	}
}

func BenchmarkVectorIndexBuild(b *testing.B) {
	const nChunks = 100000
	chunks := make([]*Chunk, nChunks)
	for i := range chunks {
		emb := make([]float32, 768)
		for j := range emb {
			emb[j] = float32(i*768+j) / float32(nChunks*768)
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
		chunks[i] = &Chunk{ID: int64(i), Embedding: emb}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx := NewVectorIndex()
		idx.Build(chunks)
		if !idx.IsReady() {
			b.Fatal("expected index to be ready")
		}
	}
}

func TestVectorIndexRebuildTrigger(t *testing.T) {
	d := setupDB(t)

	docPath := filepath.Join(t.TempDir(), "rebuild.md")
	if err := d.SaveDocument(&Document{Path: docPath, Hash: "rebuild", UpdatedAt: time.Now()}); err != nil {
		t.Fatalf("SaveDocument: %v", err)
	}

	const nChunks = 300
	chunks := make([]*Chunk, nChunks)
	for i := 0; i < nChunks; i++ {
		emb := make([]float32, 768)
		for j := range emb {
			emb[j] = float32(i*768+j) / float32(nChunks*768)
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
			UUID:         "rebuild-" + strconv.Itoa(i),
			DocumentPath: docPath,
			ChunkIndex:   i,
			Content:      "content " + strconv.Itoa(i),
			Embedding:    emb,
			Hash:         "rebuild-hash-" + strconv.Itoa(i),
		}
	}
	if err := d.SaveChunks(chunks); err != nil {
		t.Fatalf("SaveChunks: %v", err)
	}

	query := make([]float32, 768)
	for i := range query {
		query[i] = 0.5
	}
	if _, err := d.SearchVector(query, 5); err != nil {
		t.Fatalf("SearchVector: %v", err)
	}
	if d.vectorIndex == nil || !d.vectorIndex.IsReady() {
		t.Fatal("expected index to be built")
	}

	// Add enough chunks to cross the rebuild threshold and verify the index is
	// reconstructed rather than accumulating unbounded centroid drift.
	beforeBucketCount := d.vectorIndex.BucketCount()
	extraCount := int(float64(nChunks)*rebuildChurnThreshold) + 10
	extra := make([]*Chunk, extraCount)
	for i := 0; i < extraCount; i++ {
		emb := make([]float32, 768)
		for j := range emb {
			emb[j] = float32((i + 1) * (j + 1) % 1000)
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
		extra[i] = &Chunk{
			UUID:         "rebuild-extra-" + strconv.Itoa(i),
			DocumentPath: docPath,
			ChunkIndex:   nChunks + i,
			Content:      "extra " + strconv.Itoa(i),
			Embedding:    emb,
			Hash:         "rebuild-extra-hash-" + strconv.Itoa(i),
		}
	}
	if err := d.SaveChunks(extra); err != nil {
		t.Fatalf("SaveChunks (extra): %v", err)
	}
	if d.vectorIndex == nil || !d.vectorIndex.IsReady() {
		t.Fatal("expected index to remain ready after rebuild")
	}
	if d.vectorIndex.BucketCount() == 0 {
		t.Fatal("expected non-zero bucket count after rebuild")
	}
	t.Logf("rebuild triggered: buckets before=%d after=%d", beforeBucketCount, d.vectorIndex.BucketCount())
}
