package db

import (
	"math"
	"math/bits"
)

func Float32SliceToBytes(slice []float32) []byte {
	buf := make([]byte, len(slice)*4)
	for i, f := range slice {
		bits := math.Float32bits(f)
		buf[i*4] = byte(bits)
		buf[i*4+1] = byte(bits >> 8)
		buf[i*4+2] = byte(bits >> 16)
		buf[i*4+3] = byte(bits >> 24)
	}
	return buf
}

func BytesToFloat32Slice(buf []byte) []float32 {
	if len(buf)%4 != 0 {
		return nil
	}
	slice := make([]float32, len(buf)/4)
	for i := range slice {
		bits := uint32(buf[i*4]) |
			uint32(buf[i*4+1])<<8 |
			uint32(buf[i*4+2])<<16 |
			uint32(buf[i*4+3])<<24
		slice[i] = math.Float32frombits(bits)
	}
	return slice
}

func CosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dotProduct, normA, normB float64
	for i := 0; i < len(a); i++ {
		dotProduct += float64(a[i] * b[i])
		normA += float64(a[i] * a[i])
		normB += float64(b[i] * b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return float32(dotProduct / (math.Sqrt(normA) * math.Sqrt(normB)))
}

func CosineSimilarityWithStoredNorm(queryVec []float32, embBytes []byte, queryNorm, storedNorm float32) float32 {
	if len(embBytes)%4 != 0 || len(embBytes)/4 != len(queryVec) || len(queryVec) == 0 {
		return 0
	}
	var dotProduct float64
	for i, q := range queryVec {
		offset := i * 4
		bits := uint32(embBytes[offset]) |
			uint32(embBytes[offset+1])<<8 |
			uint32(embBytes[offset+2])<<16 |
			uint32(embBytes[offset+3])<<24
		dotProduct += float64(q * math.Float32frombits(bits))
	}
	denom := float64(queryNorm) * float64(storedNorm)
	if denom == 0 {
		return 0
	}
	return float32(dotProduct / denom)
}

func l2Norm(v []float32) float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x * x)
	}
	return float32(math.Sqrt(sum))
}

// SearchResultHeap is a min-heap of SearchResults.
type SearchResultHeap []*SearchResult

func (h SearchResultHeap) Len() int           { return len(h) }
func (h SearchResultHeap) Less(i, j int) bool { return h[i].CosineScore < h[j].CosineScore }
func (h SearchResultHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }

func (h *SearchResultHeap) Push(x interface{}) {
	*h = append(*h, x.(*SearchResult))
}

func (h *SearchResultHeap) Pop() interface{} {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[0 : n-1]
	return x
}

// SignBinarySignature packs one bit per dimension (1 if value >= 0, else 0),
// padding to whole bytes and grouping into uint64 words for fast Hamming
// distance computation. For a 768-dim vector this produces 96 bytes (12 words).
func SignBinarySignature(vec []float32) []byte {
	if len(vec) == 0 {
		return nil
	}
	nWords := (len(vec) + 63) / 64
	words := make([]uint64, nWords)
	for i, v := range vec {
		if v >= 0 {
			words[i/64] |= 1 << uint(i%64)
		}
	}
	// Encode as little-endian uint64 words.
	buf := make([]byte, nWords*8)
	for i, w := range words {
		off := i * 8
		buf[off] = byte(w)
		buf[off+1] = byte(w >> 8)
		buf[off+2] = byte(w >> 16)
		buf[off+3] = byte(w >> 24)
		buf[off+4] = byte(w >> 32)
		buf[off+5] = byte(w >> 40)
		buf[off+6] = byte(w >> 48)
		buf[off+7] = byte(w >> 56)
	}
	return buf
}

// HammingDistance returns the number of differing bits between two binary
// signatures encoded as byte slices of little-endian uint64 words.
// Returns math.MaxInt if the slices have different lengths or are not
// multiples of 8 bytes.
func HammingDistance(a, b []byte) int {
	if len(a) != len(b) || len(a) == 0 || len(a)%8 != 0 {
		return math.MaxInt
	}
	dist := 0
	for i := 0; i < len(a); i += 8 {
		wa := uint64(a[i]) | uint64(a[i+1])<<8 | uint64(a[i+2])<<16 | uint64(a[i+3])<<24 |
			uint64(a[i+4])<<32 | uint64(a[i+5])<<40 | uint64(a[i+6])<<48 | uint64(a[i+7])<<56
		wb := uint64(b[i]) | uint64(b[i+1])<<8 | uint64(b[i+2])<<16 | uint64(b[i+3])<<24 |
			uint64(b[i+4])<<32 | uint64(b[i+5])<<40 | uint64(b[i+6])<<48 | uint64(b[i+7])<<56
		dist += bits.OnesCount64(wa ^ wb)
	}
	return dist
}
