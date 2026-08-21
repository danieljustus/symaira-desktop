package db

import (
	"math"
	"strconv"
	"testing"
	"time"
)

func benchmarkSearchSetup(b *testing.B, nChunks int) (*DB, []float32) {
	b.Helper()
	d := openTestDB(b)

	docPath := "/bench/docs.md"
	if err := d.SaveDocument(&Document{Path: docPath, Hash: "bench", UpdatedAt: time.Now()}); err != nil {
		b.Fatalf("SaveDocument: %v", err)
	}

	chunks := make([]*Chunk, nChunks)
	for i := 0; i < nChunks; i++ {
		emb := make([]float32, 768)
		for j := range emb {
			emb[j] = float32(math.Sin(float64(i)*0.1 + float64(j)*0.05))
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
			Content:      "bench content " + strconv.Itoa(i),
			Hash:         "bench-hash-" + strconv.Itoa(i),
		}
		chunks[i].Embedding = emb
	}
	if err := d.SaveChunks(chunks); err != nil {
		b.Fatalf("SaveChunks: %v", err)
	}

	queryVec := make([]float32, 768)
	for i := range queryVec {
		queryVec[i] = float32(math.Sin(float64(25)*0.1 + float64(i)*0.05))
	}
	var sum float64
	for _, v := range queryVec {
		sum += float64(v * v)
	}
	n := float32(math.Sqrt(sum))
	if n > 0 {
		for i := range queryVec {
			queryVec[i] /= n
		}
	}

	return d, queryVec
}

func BenchmarkSearchVector(b *testing.B) {
	for _, size := range []int{100, 500, 1000} {
		b.Run("n="+strconv.Itoa(size), func(b *testing.B) {
			d, queryVec := benchmarkSearchSetup(b, size)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				d.vectorIndex = nil
				d.SearchVector(queryVec, 10)
			}
		})
	}
}

func BenchmarkSearchVectorQuantized(b *testing.B) {
	for _, size := range []int{100, 500, 1000} {
		b.Run("n="+strconv.Itoa(size), func(b *testing.B) {
			d, queryVec := benchmarkSearchSetup(b, size)

			codec, err := newTestCodec(len(queryVec), 4, 42)
			if err != nil {
				b.Fatalf("newTestCodec: %v", err)
			}

			for i := 0; i < size; i++ {
				chunkID := int64(i + 1)
				emb := make([]float32, 768)
				for j := range emb {
					emb[j] = float32(math.Sin(float64(i)*0.1 + float64(j)*0.05))
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

				blob, meta, err := codec.EncodeSidecar(emb, norm)
				if err != nil {
					b.Fatalf("EncodeSidecar: %v", err)
				}
				metaBytes, _ := marshalMeta(&QuantSidecarMeta{
					CodecVersion:   meta.CodecVersion,
					Dimension:      meta.Dimension,
					BitWidth:       meta.BitWidth,
					QuantizerMode:  meta.QuantizerMode,
					ProjectionSeed: meta.ProjectionSeed,
					Norm:           meta.Norm,
				})
				d.conn.Exec(
					"UPDATE chunks SET embedding_quant = ?, embedding_quant_meta = ? WHERE id = ?",
					blob, string(metaBytes), chunkID,
				)
			}

			d.SetQuantConfig(&QuantConfig{
				Enabled:     true,
				BitWidth:    4,
				Shortlist:   200,
				ExactRerank: true,
				Seed:        42,
			})

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				d.vectorIndex = nil
				d.SearchVectorQuantized(queryVec, 10)
			}
		})
	}
}
