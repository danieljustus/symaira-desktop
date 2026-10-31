//! Port of the Go retrieval vector layer: the in-memory IVF prefilter
//! (`internal/retrieval/internal/db/prefilter.go`), its persisted snapshot
//! (`index_storage.go`), and the binary-signature shortlist plus bounded
//! min-heap ranking of `search.go`.
//!
//! The IVF index is approximate: once a snapshot exists, Go's vector search
//! scores only chunks in the nearest buckets, so its results can differ from an
//! exact scan. Reproducing Go therefore needs the same clustering, the same
//! incremental churn rules and the same sort tie behaviour.

use std::collections::HashMap;

use crate::go_sort::{heap_fix, heap_up, slice_sort_by, slice_stable_sort_by};

/// Go's `indexBuildThreshold`: no IVF below this many chunks.
pub(crate) const INDEX_BUILD_THRESHOLD: usize = 128;
/// Go's `rebuildChurnThreshold`.
const REBUILD_CHURN_THRESHOLD: f64 = 0.10;
/// Go's `binarySignatureCandidateMultiplier`.
pub(crate) const BINARY_SIGNATURE_CANDIDATE_MULTIPLIER: usize = 4;
const INDEX_STORAGE_VERSION: u8 = 1;

/// Go's `CosineSimilarity`.
pub(crate) fn cosine_similarity(a: &[f32], b: &[f32]) -> f32 {
    if a.len() != b.len() || a.is_empty() {
        return 0.0;
    }
    let (mut dot, mut norm_a, mut norm_b) = (0.0_f64, 0.0_f64, 0.0_f64);
    for (left, right) in a.iter().zip(b) {
        dot += f64::from(*left * *right);
        norm_a += f64::from(*left * *left);
        norm_b += f64::from(*right * *right);
    }
    if norm_a == 0.0 || norm_b == 0.0 {
        return 0.0;
    }
    (dot / (norm_a.sqrt() * norm_b.sqrt())) as f32
}

/// Go's `CosineSimilarityWithStoredNorm`.
pub(crate) fn cosine_similarity_with_stored_norm(
    query: &[f32],
    embedding_bytes: &[u8],
    query_norm: f32,
    stored_norm: f32,
) -> f32 {
    if !embedding_bytes.len().is_multiple_of(4)
        || embedding_bytes.len() / 4 != query.len()
        || query.is_empty()
    {
        return 0.0;
    }
    let mut dot = 0.0_f64;
    let (words, _) = embedding_bytes.as_chunks::<4>();
    for (value, bytes) in query.iter().zip(words) {
        let stored = f32::from_le_bytes(*bytes);
        dot += f64::from(*value * stored);
    }
    let denominator = f64::from(query_norm) * f64::from(stored_norm);
    if denominator == 0.0 {
        return 0.0;
    }
    (dot / denominator) as f32
}

/// Go's `HammingDistance`; `usize::MAX` for incompatible signatures.
fn hamming_distance(a: &[u8], b: &[u8]) -> usize {
    if a.len() != b.len() || a.is_empty() || !a.len().is_multiple_of(8) {
        return usize::MAX;
    }
    a.iter()
        .zip(b)
        .map(|(left, right)| (left ^ right).count_ones() as usize)
        .sum()
}

/// One scanned chunk row (Go's `rowEntry`).
pub(crate) struct ScanRow {
    pub id: i64,
    pub uuid: String,
    pub document_path: String,
    pub chunk_index: i64,
    pub embedding_bytes: Vec<u8>,
    pub hash: String,
    pub norm: f32,
    pub signature: Option<Vec<u8>>,
}

/// A scored chunk before content hydration.
pub(crate) struct ScoredRow {
    pub row: ScanRow,
    pub score: f32,
    /// Go fills `Chunk.Embedding` when the caller collected embeddings for the
    /// IVF build, or when scoring fell back to the plain cosine.
    pub embedding_loaded: bool,
}

/// Go's `hammingShortlist`, returning row indexes in shortlist order.
fn hamming_shortlist(rows: &[ScanRow], query_signature: Option<&[u8]>, limit: usize) -> Vec<usize> {
    let mut hamming_size = limit.saturating_mul(BINARY_SIGNATURE_CANDIDATE_MULTIPLIER);
    if hamming_size > rows.len() {
        hamming_size = rows.len();
    }
    let all = || (0..rows.len()).collect::<Vec<_>>();
    let Some(query_signature) = query_signature else {
        return all();
    };
    if rows.len() <= 1 || hamming_size >= rows.len() {
        return all();
    }
    let distances = rows
        .iter()
        .map(|row| match &row.signature {
            None => usize::MAX,
            Some(stored) => hamming_distance(query_signature, stored),
        })
        .collect::<Vec<_>>();
    let mut order = all();
    slice_stable_sort_by(&mut order, |a, b| distances[*a] < distances[*b]);
    if distances[order[0]] == distances[order[hamming_size - 1]] {
        return order;
    }
    order.truncate(hamming_size);
    order
}

/// Go's `scanAndScore` after the rows were read: Hamming shortlist (when
/// `use_hamming`), bounded min-heap selection and the stable descending sort.
/// `collected` marks that the caller read every embedding for an IVF build.
pub(crate) fn score_rows(
    rows: Vec<ScanRow>,
    query: &[f32],
    query_norm: f32,
    limit: usize,
    use_hamming: bool,
    collected: bool,
) -> Vec<ScoredRow> {
    let shortlist = if use_hamming {
        let signature = crate::retrieval::sign_binary_signature(query);
        hamming_shortlist(&rows, signature.as_deref(), limit)
    } else {
        (0..rows.len()).collect()
    };
    struct Entry {
        index: usize,
        score: f32,
        embedding_loaded: bool,
    }
    let less = |a: &Entry, b: &Entry| a.score < b.score;
    let mut heap: Vec<Entry> = Vec::new();
    for index in shortlist {
        let row = &rows[index];
        let mut embedding_loaded = collected;
        let score = if query_norm > 0.0 && row.norm > 0.0 {
            cosine_similarity_with_stored_norm(query, &row.embedding_bytes, query_norm, row.norm)
        } else {
            embedding_loaded = true;
            let embedding = crate::retrieval::decode_embedding(&row.embedding_bytes);
            cosine_similarity(query, &embedding)
        };
        if heap.len() < limit {
            heap.push(Entry {
                index,
                score,
                embedding_loaded,
            });
            let last = heap.len() - 1;
            heap_up(&mut heap, last, &less);
        } else if score > heap[0].score {
            heap[0] = Entry {
                index,
                score,
                embedding_loaded,
            };
            heap_fix(&mut heap, 0, &less);
        }
    }
    slice_stable_sort_by(&mut heap, |a, b| a.score > b.score);
    let mut rows = rows.into_iter().map(Some).collect::<Vec<_>>();
    heap.into_iter()
        .map(|entry| ScoredRow {
            row: rows[entry.index].take().expect("each row is selected once"),
            score: entry.score,
            embedding_loaded: entry.embedding_loaded,
        })
        .collect()
}

/// A chunk id with its embedding, as fed to the IVF index.
#[derive(Clone)]
pub(crate) struct IndexChunk {
    pub id: i64,
    pub embedding: Vec<f32>,
}

/// Go's `VectorIndex`.
#[derive(Clone, Debug, Default, PartialEq)]
pub(crate) struct VectorIndex {
    dim: usize,
    centroids: Vec<Vec<f32>>,
    inverted: Vec<Vec<i64>>,
    k: usize,
    nprobe: usize,
    total_n: usize,
    ready: bool,
    churn_added: usize,
    churn_deleted: usize,
    base_total_n: usize,
}

impl VectorIndex {
    pub fn is_ready(&self) -> bool {
        self.ready
    }

    pub fn total_chunks(&self) -> usize {
        self.total_n
    }

    pub fn probe_count(&self) -> usize {
        self.nprobe
    }

    /// Go's `Build` (also `Rebuild`).
    pub fn build(&mut self, chunks: &[IndexChunk]) {
        *self = Self::default();
        if chunks.is_empty() {
            return;
        }
        self.dim = chunks[0].embedding.len();
        self.total_n = chunks.len();
        self.initialize_centroids(chunks);
        if self.k == 0 {
            return;
        }
        self.run_kmeans(chunks);
        self.reseed_empty_buckets(chunks);
        self.ready = true;
        self.base_total_n = self.total_n;
    }

    fn initialize_centroids(&mut self, chunks: &[IndexChunk]) {
        let mut k = (self.total_n as f64).sqrt() as usize;
        k = k.clamp(4, 256).min(self.total_n);
        let step = (self.total_n / k.max(1)).max(1);
        self.centroids = Vec::with_capacity(k);
        let mut i = 0;
        while i < k && i * step < self.total_n {
            let mut centroid = vec![0.0_f32; self.dim];
            let source = &chunks[i * step].embedding;
            let count = source.len().min(self.dim);
            centroid[..count].copy_from_slice(&source[..count]);
            self.centroids.push(centroid);
            i += 1;
        }
        self.k = self.centroids.len();
        let mut nprobe = (self.k as f64).sqrt() as usize + 1;
        nprobe = nprobe.max(3).min(self.k);
        self.nprobe = nprobe;
    }

    fn run_kmeans(&mut self, chunks: &[IndexChunk]) {
        self.inverted = vec![Vec::new(); self.k];
        for _ in 0..3 {
            let mut sums = vec![vec![0.0_f64; self.dim]; self.k];
            let mut counts = vec![0_usize; self.k];
            for bucket in &mut self.inverted {
                bucket.clear();
            }
            for chunk in chunks {
                let best = self.nearest_centroid(&chunk.embedding);
                self.inverted[best].push(chunk.id);
                counts[best] += 1;
                // Go indexes `Embedding[d]` for every `d < dim` and would
                // panic on a shorter vector; a missing value counts as zero.
                for (d, sum) in sums[best].iter_mut().enumerate() {
                    *sum += f64::from(chunk.embedding.get(d).copied().unwrap_or(0.0));
                }
            }
            for bucket in 0..self.k {
                if counts[bucket] == 0 {
                    continue;
                }
                let inverse = counts[bucket] as f64;
                self.centroids[bucket] = sums[bucket]
                    .iter()
                    .map(|sum| (*sum / inverse) as f32)
                    .collect();
            }
        }
    }

    fn nearest_centroid(&self, vector: &[f32]) -> usize {
        let mut best_bucket = 0;
        let mut best_score = -2.0_f32;
        for (bucket, centroid) in self.centroids.iter().enumerate() {
            let score = cosine_similarity(vector, centroid);
            if score > best_score {
                best_score = score;
                best_bucket = bucket;
            }
        }
        best_bucket
    }

    fn reseed_empty_buckets(&mut self, chunks: &[IndexChunk]) {
        if chunks.is_empty() {
            return;
        }
        let mut empty = Vec::new();
        let mut largest: Option<usize> = None;
        let mut largest_size = 0;
        for (bucket, ids) in self.inverted.iter().enumerate() {
            if ids.is_empty() {
                empty.push(bucket);
            } else if ids.len() > largest_size {
                largest_size = ids.len();
                largest = Some(bucket);
            }
        }
        let Some(mut largest) = largest else { return };
        if empty.is_empty() {
            return;
        }
        let embedding_by_id = chunks
            .iter()
            .map(|chunk| (chunk.id, chunk.embedding.as_slice()))
            .collect::<HashMap<_, _>>();
        for empty_bucket in empty {
            let source = self.inverted[largest].clone();
            if source.len() < 2 {
                let seed = embedding_by_id
                    .get(&chunks[empty_bucket % chunks.len()].id)
                    .copied()
                    .unwrap_or(&[]);
                let mut centroid = vec![0.0_f32; self.dim];
                let count = seed.len().min(self.dim);
                centroid[..count].copy_from_slice(&seed[..count]);
                self.centroids[empty_bucket] = centroid;
                continue;
            }
            let split = source.len() / 2;
            // Go allocates the new bucket with `split` slots and copies into
            // it, so an odd-sized source bucket silently drops its last id.
            let new_ids = source[split..split * 2].to_vec();
            self.inverted[largest] = source[..split].to_vec();
            self.inverted[empty_bucket] = new_ids.clone();
            self.recompute_centroid(largest, &embedding_by_id);
            self.recompute_centroid(empty_bucket, &embedding_by_id);
            largest = empty_bucket;
            largest_size = new_ids.len();
            for (bucket, ids) in self.inverted.iter().enumerate() {
                if ids.len() > largest_size {
                    largest_size = ids.len();
                    largest = bucket;
                }
            }
        }
    }

    fn recompute_centroid(&mut self, bucket: usize, embeddings: &HashMap<i64, &[f32]>) {
        let ids = &self.inverted[bucket];
        if ids.is_empty() {
            return;
        }
        let mut mean = vec![0.0_f64; self.dim];
        for id in ids {
            let embedding = embeddings.get(id).copied().unwrap_or(&[]);
            for (d, sum) in mean.iter_mut().enumerate() {
                *sum += f64::from(embedding.get(d).copied().unwrap_or(0.0));
            }
        }
        let inverse = ids.len() as f64;
        self.centroids[bucket] = mean.iter().map(|sum| (*sum / inverse) as f32).collect();
    }

    /// Go's `AddChunks`.
    pub fn add_chunks(&mut self, chunks: &[IndexChunk]) {
        if !self.ready || self.k == 0 {
            return;
        }
        for chunk in chunks {
            if chunk.embedding.len() != self.dim {
                continue;
            }
            let best = self.nearest_centroid(&chunk.embedding);
            self.inverted[best].push(chunk.id);
            self.total_n += 1;
            self.churn_added += 1;
        }
    }

    /// Go's `RemoveChunk`: scans every bucket and keeps going after a hit.
    pub fn remove_chunk(&mut self, id: i64) {
        if !self.ready || self.k == 0 {
            return;
        }
        for bucket in &mut self.inverted {
            if let Some(position) = bucket.iter().position(|existing| *existing == id) {
                bucket.remove(position);
                self.total_n = self.total_n.saturating_sub(1);
                self.churn_deleted += 1;
            }
        }
    }

    /// Go's `NeedsRebuild`.
    pub fn needs_rebuild(&self) -> bool {
        if !self.ready {
            return false;
        }
        let base = if self.base_total_n == 0 {
            self.total_n
        } else {
            self.base_total_n
        };
        if base == 0 {
            return false;
        }
        (self.churn_added + self.churn_deleted) as f64 > REBUILD_CHURN_THRESHOLD * base as f64
    }

    /// Go's `CandidateIDs`; `None` means "do a full scan".
    pub fn candidate_ids(&self, query: &[f32], nprobe: usize) -> Option<Vec<i64>> {
        if !self.ready || self.k == 0 {
            return None;
        }
        let nprobe = nprobe.min(self.k);
        let mut ranked = self
            .centroids
            .iter()
            .enumerate()
            .map(|(index, centroid)| (index, cosine_similarity(query, centroid)))
            .collect::<Vec<_>>();
        slice_sort_by(&mut ranked, |a, b| a.1 > b.1);
        let candidate_count = ranked[..nprobe]
            .iter()
            .map(|(index, _)| self.inverted[*index].len())
            .sum::<usize>();
        if self.total_n > 0 && candidate_count > self.total_n / 2 {
            return None;
        }
        let mut seen = std::collections::HashSet::with_capacity(candidate_count);
        let mut candidates = Vec::with_capacity(candidate_count);
        for (index, _) in &ranked[..nprobe] {
            for id in &self.inverted[*index] {
                if seen.insert(*id) {
                    candidates.push(*id);
                }
            }
        }
        Some(candidates)
    }

    /// Go's `Serialize`.
    pub fn serialize(&self, generation: i64) -> Option<Vec<u8>> {
        if !self.ready {
            return None;
        }
        let generation = u64::try_from(generation).ok()?;
        let mut out = Vec::new();
        out.push(INDEX_STORAGE_VERSION);
        out.extend_from_slice(&generation.to_le_bytes());
        for value in [
            self.dim,
            self.k,
            self.nprobe,
            self.total_n,
            self.base_total_n,
            self.churn_added,
            self.churn_deleted,
        ] {
            out.extend_from_slice(&u32::try_from(value).ok()?.to_le_bytes());
        }
        for centroid in &self.centroids {
            for value in centroid {
                out.extend_from_slice(&value.to_bits().to_le_bytes());
            }
        }
        for ids in &self.inverted {
            out.extend_from_slice(&u32::try_from(ids.len()).ok()?.to_le_bytes());
            for id in ids {
                out.extend_from_slice(&u64::try_from(*id).ok()?.to_le_bytes());
            }
        }
        // Go sizes the buffer with room for eight 32-bit header fields but
        // writes seven, leaving four zero bytes at the end of the snapshot.
        out.extend_from_slice(&[0_u8; 4]);
        Some(out)
    }

    /// Go's `DeserializeIndex`: the index and the generation stored with it.
    pub fn deserialize(data: &[u8]) -> Result<(Self, i64), String> {
        if data.len() < 1 + 8 + 4 * 8 {
            return Err("index data too short".to_owned());
        }
        if data[0] != INDEX_STORAGE_VERSION {
            return Err(format!("unsupported index storage version: {}", data[0]));
        }
        let mut offset = 1;
        let generation_raw = read_u64(data, &mut offset);
        let generation =
            i64::try_from(generation_raw).map_err(|_| "index generation overflows int64")?;
        let mut header = [0_usize; 7];
        for slot in &mut header {
            *slot = read_u32(data, &mut offset) as usize;
        }
        let [
            dim,
            k,
            nprobe,
            total_n,
            base_total_n,
            churn_added,
            churn_deleted,
        ] = header;
        let centroid_bytes = k
            .checked_mul(dim)
            .and_then(|count| count.checked_mul(4))
            .ok_or("index data truncated at centroids")?;
        if data.len() < offset.saturating_add(centroid_bytes) {
            return Err("index data truncated at centroids".to_owned());
        }
        let mut centroids = Vec::with_capacity(k);
        for _ in 0..k {
            let mut centroid = Vec::with_capacity(dim);
            for _ in 0..dim {
                centroid.push(f32::from_bits(read_u32(data, &mut offset)));
            }
            centroids.push(centroid);
        }
        let mut inverted = Vec::with_capacity(k);
        for _ in 0..k {
            if data.len() < offset + 4 {
                return Err("index data truncated at inverted length".to_owned());
            }
            let count = read_u32(data, &mut offset) as usize;
            if data.len() < offset.saturating_add(count.saturating_mul(8)) {
                return Err("index data truncated at inverted IDs".to_owned());
            }
            let mut ids = Vec::with_capacity(count);
            for _ in 0..count {
                let raw = read_u64(data, &mut offset);
                ids.push(
                    i64::try_from(raw).map_err(|_| format!("chunk ID overflows int64: {raw}"))?,
                );
            }
            inverted.push(ids);
        }
        Ok((
            Self {
                dim,
                centroids,
                inverted,
                k,
                nprobe,
                total_n,
                ready: true,
                churn_added,
                churn_deleted,
                base_total_n,
            },
            generation,
        ))
    }
}

fn read_u32(data: &[u8], offset: &mut usize) -> u32 {
    let value = u32::from_le_bytes([
        data[*offset],
        data[*offset + 1],
        data[*offset + 2],
        data[*offset + 3],
    ]);
    *offset += 4;
    value
}

fn read_u64(data: &[u8], offset: &mut usize) -> u64 {
    let mut bytes = [0_u8; 8];
    bytes.copy_from_slice(&data[*offset..*offset + 8]);
    *offset += 8;
    u64::from_le_bytes(bytes)
}
