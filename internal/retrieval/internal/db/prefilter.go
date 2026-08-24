package db

import (
	"math"
	"sort"
	"sync"
)

// rebuildChurnThreshold is the fraction of the indexed corpus that must be
// added or deleted before a full re-cluster is triggered.  Keeping this low
// enough prevents recall drift from centroid drift, while high enough avoids
// rebuilding on every small write.
const rebuildChurnThreshold = 0.10

// VectorIndex is an in-memory IVF (Inverted File) index for approximate
// nearest neighbor search.  It partitions chunk embeddings into K buckets
// by centroid proximity so that a query only needs to score the chunks in
// the nprobe nearest buckets instead of the full corpus.
//
// The index supports incremental updates: writes add or remove chunk IDs
// from the inverted lists without discarding the whole index.  A full
// re-cluster is triggered once the fraction of added/removed chunks exceeds
// rebuildChurnThreshold.
type VectorIndex struct {
	dim       int
	centroids [][]float32
	inverted  [][]int64 // inverted[bucket] = chunk IDs
	k         int
	nprobe    int
	totalN    int
	ready     bool
	mu        sync.RWMutex

	// churn tracks how many chunks have been added/removed since the last
	// full rebuild.  It is used to decide when centroid drift warrants a
	// re-cluster.
	churnAdded   int
	churnDeleted int
	baseTotalN   int
}

// NewVectorIndex creates a new, empty VectorIndex.
func NewVectorIndex() *VectorIndex {
	return &VectorIndex{}
}

// Build constructs the IVF index from the given normalized chunks.
// It uses a simple k-means-like assignment over cosine similarity.
// Safe to call multiple times; each call replaces the previous index.
func (vi *VectorIndex) Build(chunks []*Chunk) {
	vi.mu.Lock()
	defer vi.mu.Unlock()

	vi.resetState()
	if len(chunks) == 0 {
		return
	}

	vi.dim = len(chunks[0].Embedding)
	vi.totalN = len(chunks)

	vi.initializeCentroids(chunks)
	if vi.k == 0 {
		return
	}

	vi.runKMeans(chunks)
	vi.reseedEmptyBuckets(chunks)
	vi.ready = true
	vi.baseTotalN = vi.totalN
}

func (vi *VectorIndex) resetState() {
	vi.dim = 0
	vi.centroids = nil
	vi.inverted = nil
	vi.k = 0
	vi.nprobe = 0
	vi.totalN = 0
	vi.ready = false
	vi.churnAdded = 0
	vi.churnDeleted = 0
	vi.baseTotalN = 0
}

func (vi *VectorIndex) initializeCentroids(chunks []*Chunk) {
	// Determine K: sqrt(N), clamped to [4, 256].
	k := int(math.Sqrt(float64(vi.totalN)))
	if k < 4 {
		k = 4
	}
	if k > 256 {
		k = 256
	}
	if k > vi.totalN {
		k = vi.totalN
	}

	// Initialize centroids by evenly sampling chunks.
	step := vi.totalN / k
	if step < 1 {
		step = 1
	}
	vi.centroids = make([][]float32, 0, k)
	for i := 0; i < k && i*step < vi.totalN; i++ {
		c := make([]float32, vi.dim)
		copy(c, chunks[i*step].Embedding)
		vi.centroids = append(vi.centroids, c)
	}
	vi.k = len(vi.centroids)

	// Determine nprobe.
	vi.nprobe = int(math.Sqrt(float64(vi.k))) + 1
	if vi.nprobe < 3 {
		vi.nprobe = 3
	}
	if vi.nprobe > vi.k {
		vi.nprobe = vi.k
	}
}

func (vi *VectorIndex) runKMeans(chunks []*Chunk) {
	vi.inverted = make([][]int64, vi.k)

	// Run 3 iterations of k-means assignment.
	for iter := 0; iter < 3; iter++ {
		// Sums and counts for each bucket, accumulated in a single pass.
		sums := make([][]float64, vi.k)
		counts := make([]int, vi.k)
		for b := range sums {
			sums[b] = make([]float64, vi.dim)
		}

		// Clear buckets.
		for b := range vi.inverted {
			vi.inverted[b] = vi.inverted[b][:0]
		}

		// Assign each chunk to its nearest centroid and accumulate bucket sums.
		for _, chunk := range chunks {
			bestBucket := vi.nearestCentroid(chunk.Embedding)
			vi.inverted[bestBucket] = append(vi.inverted[bestBucket], chunk.ID)
			counts[bestBucket]++
			sum := sums[bestBucket]
			for d := 0; d < vi.dim; d++ {
				sum[d] += float64(chunk.Embedding[d])
			}
		}

		// Update centroids from the single-pass sums.
		for bi := 0; bi < vi.k; bi++ {
			if counts[bi] == 0 {
				continue
			}
			mean := make([]float32, vi.dim)
			inv := float64(counts[bi])
			for d := 0; d < vi.dim; d++ {
				mean[d] = float32(sums[bi][d] / inv)
			}
			vi.centroids[bi] = mean
		}
	}
}

func (vi *VectorIndex) nearestCentroid(vec []float32) int {
	bestBucket := 0
	bestScore := float32(-2)
	for bi, cent := range vi.centroids {
		score := CosineSimilarity(vec, cent)
		if score > bestScore {
			bestScore = score
			bestBucket = bi
		}
	}
	return bestBucket
}

// reseedEmptyBuckets relocates the centroids of buckets that ended up with
// no assigned chunks by splitting the largest bucket.  This improves cluster
// balance and prefilter recall, especially on skewed data.
func (vi *VectorIndex) reseedEmptyBuckets(chunks []*Chunk) {
	if len(chunks) == 0 {
		return
	}

	emptyBuckets := make([]int, 0)
	largestBucket := -1
	largestSize := 0
	for bi, ids := range vi.inverted {
		if len(ids) == 0 {
			emptyBuckets = append(emptyBuckets, bi)
		} else if len(ids) > largestSize {
			largestSize = len(ids)
			largestBucket = bi
		}
	}

	if largestBucket < 0 || len(emptyBuckets) == 0 {
		return
	}

	// Build a quick lookup from chunk ID to embedding so we can recompute
	// centroids from actual chunk vectors rather than stale means.
	embeddingByID := make(map[int64][]float32, len(chunks))
	for _, c := range chunks {
		embeddingByID[c.ID] = c.Embedding
	}

	for _, emptyBi := range emptyBuckets {
		// Split the largest bucket: move the second half of its IDs to the
		// empty bucket and recompute both centroids.
		srcIDs := vi.inverted[largestBucket]
		if len(srcIDs) < 2 {
			// Nothing useful to split; seed from a random chunk.
			seed := embeddingByID[chunks[emptyBi%len(chunks)].ID]
			newCent := make([]float32, vi.dim)
			copy(newCent, seed)
			vi.centroids[emptyBi] = newCent
			continue
		}

		split := len(srcIDs) / 2
		newIDs := make([]int64, split)
		copy(newIDs, srcIDs[split:])
		vi.inverted[largestBucket] = srcIDs[:split]
		vi.inverted[emptyBi] = newIDs

		// Recompute both centroids from the split IDs.
		vi.recomputeCentroid(largestBucket, embeddingByID)
		vi.recomputeCentroid(emptyBi, embeddingByID)

		// The formerly largest bucket is now smaller; find the new largest.
		largestBucket = emptyBi
		largestSize = len(newIDs)
		for bi, ids := range vi.inverted {
			if len(ids) > largestSize {
				largestSize = len(ids)
				largestBucket = bi
			}
		}
	}
}

func (vi *VectorIndex) recomputeCentroid(bucket int, embeddingByID map[int64][]float32) {
	ids := vi.inverted[bucket]
	if len(ids) == 0 {
		return
	}
	mean := make([]float64, vi.dim)
	for _, id := range ids {
		emb := embeddingByID[id]
		for d := 0; d < vi.dim; d++ {
			mean[d] += float64(emb[d])
		}
	}
	inv := float64(len(ids))
	newCent := make([]float32, vi.dim)
	for d := 0; d < vi.dim; d++ {
		newCent[d] = float32(mean[d] / inv)
	}
	vi.centroids[bucket] = newCent
}

// AddChunks assigns the given chunks to their nearest existing centroid and
// appends their IDs to the inverted lists.  It updates churn so that a full
// rebuild can be triggered once enough new chunks have arrived.
func (vi *VectorIndex) AddChunks(chunks []*Chunk) {
	vi.mu.Lock()
	defer vi.mu.Unlock()

	if !vi.ready || vi.k == 0 {
		return
	}

	for _, chunk := range chunks {
		if len(chunk.Embedding) != vi.dim {
			continue
		}
		bestBucket := vi.nearestCentroid(chunk.Embedding)
		vi.inverted[bestBucket] = append(vi.inverted[bestBucket], chunk.ID)
		vi.totalN++
		vi.churnAdded++
	}
}

// RemoveChunk deletes a chunk ID from every inverted list.  It updates churn
// so that a full rebuild can be triggered once enough chunks have been
// removed.
func (vi *VectorIndex) RemoveChunk(id int64) {
	vi.mu.Lock()
	defer vi.mu.Unlock()

	if !vi.ready || vi.k == 0 {
		return
	}

	for bi := range vi.inverted {
		ids := vi.inverted[bi]
		for i, existing := range ids {
			if existing == id {
				vi.inverted[bi] = append(ids[:i], ids[i+1:]...)
				vi.totalN--
				vi.churnDeleted++
				break
			}
		}
	}
}

// NeedsRebuild reports whether the incremental churn since the last rebuild
// has exceeded the configured threshold.
func (vi *VectorIndex) NeedsRebuild() bool {
	vi.mu.RLock()
	defer vi.mu.RUnlock()

	if !vi.ready {
		return false
	}
	base := vi.baseTotalN
	if base == 0 {
		base = vi.totalN
	}
	if base == 0 {
		return false
	}
	churn := vi.churnAdded + vi.churnDeleted
	return float64(churn) > rebuildChurnThreshold*float64(base)
}

// Rebuild performs a full re-cluster from the supplied chunks and resets
// churn tracking.
func (vi *VectorIndex) Rebuild(chunks []*Chunk) {
	vi.Build(chunks)
}

// CandidateIDs returns chunk IDs from the nprobe nearest centroid buckets.
// If the index is not ready, has no buckets, or the candidate set covers
// more than half the total chunks (making the filter counterproductive),
// it returns nil to signal that the caller should fall back to a full scan.
func (vi *VectorIndex) CandidateIDs(queryVec []float32, nprobe int) []int64 {
	vi.mu.RLock()
	defer vi.mu.RUnlock()

	if !vi.ready || vi.k == 0 {
		return nil
	}

	if nprobe > vi.k {
		nprobe = vi.k
	}

	// Score the query against every centroid.
	type centScore struct {
		idx   int
		score float32
	}
	ranked := make([]centScore, vi.k)
	for i, cent := range vi.centroids {
		ranked[i] = centScore{idx: i, score: CosineSimilarity(queryVec, cent)}
	}

	// Partial sort to pick the top-nprobe centroids.
	sort.Slice(ranked, func(i, j int) bool {
		return ranked[i].score > ranked[j].score
	})

	// Count candidates in the top-nprobe buckets.
	candidateCount := 0
	for i := 0; i < nprobe; i++ {
		candidateCount += len(vi.inverted[ranked[i].idx])
	}

	// If this covers >50 % of the corpus, just do a full scan.
	if vi.totalN > 0 && candidateCount > vi.totalN/2 {
		return nil
	}

	// Deduplicate (a chunk can appear in multiple buckets from k-means
	// iterations, though in practice each chunk maps to exactly one
	// bucket after the final iteration).
	seen := make(map[int64]struct{}, candidateCount)
	candidates := make([]int64, 0, candidateCount)
	for i := 0; i < nprobe; i++ {
		for _, id := range vi.inverted[ranked[i].idx] {
			if _, ok := seen[id]; !ok {
				seen[id] = struct{}{}
				candidates = append(candidates, id)
			}
		}
	}

	return candidates
}

// ProbeCount returns the default number of centroids to probe for a query.
func (vi *VectorIndex) ProbeCount() int {
	vi.mu.RLock()
	defer vi.mu.RUnlock()
	return vi.nprobe
}

// IsReady reports whether the index has been built and is usable.
func (vi *VectorIndex) IsReady() bool {
	vi.mu.RLock()
	defer vi.mu.RUnlock()
	return vi.ready
}

// BucketCount returns the number of buckets (K) in the index.
func (vi *VectorIndex) BucketCount() int {
	vi.mu.RLock()
	defer vi.mu.RUnlock()
	return vi.k
}

// TotalChunks returns the number of chunks that are indexed.
func (vi *VectorIndex) TotalChunks() int {
	vi.mu.RLock()
	defer vi.mu.RUnlock()
	return vi.totalN
}

// indexBuildThreshold is the minimum number of chunks required before the
// IVF prefilter is activated.  Below this threshold the full scan is fast
// enough and the WHERE-IN overhead would dominate.
const indexBuildThreshold = 128
