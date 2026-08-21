package engine

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danieljustus/symaira-seek/internal/db"
)

// makeTestEmbedding returns a deterministic 768-dim embedding with L2 norm = 1.
func makeTestEmbedding(seed float32) []float32 {
	vec := make([]float32, 768)
	for i := range vec {
		vec[i] = float32(math.Sin(float64(seed)*float64(i+1))) / float32(i+1)
	}
	var sum float64
	for _, v := range vec {
		sum += float64(v) * float64(v)
	}
	norm := float32(math.Sqrt(sum))
	for i := range vec {
		vec[i] /= norm
	}
	return vec
}

// makeTestChunks returns n chunks with valid embeddings but no sidecars.
func makeTestChunks(docPath string, n int) []*db.Chunk {
	chunks := make([]*db.Chunk, n)
	for i := range chunks {
		emb := makeTestEmbedding(float32(i + 1))
		var norm float64
		for _, v := range emb {
			norm += float64(v) * float64(v)
		}
		chunks[i] = &db.Chunk{
			UUID:         fmt.Sprintf("chunk-backfill-%d", i),
			DocumentPath: docPath,
			ChunkIndex:   i,
			Content:      "test content",
			Embedding:    emb,
			Hash:         fmt.Sprintf("hash-backfill-%d", i),
			Norm:         float32(math.Sqrt(norm)),
			Dim:          768,
			Model:        "test-model",
		}
	}
	return chunks
}

// TestBackfillQuantSidecars verifies the wiring: create a codec, define the
// encode closure, and confirm every chunk gets a quantized sidecar.
func TestBackfillQuantSidecars(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-backfill-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)
	t.Setenv("HOME", tempDir)

	d, err := db.Open()
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer d.Close()

	// Insert a document and chunks (no sidecars yet).
	docPath := filepath.Join(tempDir, "test.md")
	if err := d.SaveDocument(&db.Document{
		Path:      docPath,
		Hash:      "backfill-doc-hash",
		UpdatedAt: timeNow(),
	}); err != nil {
		t.Fatalf("SaveDocument: %v", err)
	}
	chunks := makeTestChunks(docPath, 3)
	if err := d.SaveChunks(chunks); err != nil {
		t.Fatalf("SaveChunks: %v", err)
	}
	t.Logf("saved %d chunks, IDs: %d, %d, %d", len(chunks), chunks[0].ID, chunks[1].ID, chunks[2].ID)

	// Verify no sidecars exist before backfill.
	for _, ch := range chunks {
		has, err := d.HasQuantizedSidecar(ch.ID)
		if err != nil {
			t.Fatalf("HasQuantizedSidecar pre-check for chunk %d: %v", ch.ID, err)
		}
		if has {
			t.Fatalf("chunk %d (UUID=%s) unexpectedly has sidecar before backfill", ch.ID, ch.UUID)
		}
	}

	// Run BackfillQuantSidecars with a valid bit width.
	count, err := BackfillQuantSidecars(d, 4, 42, nil)
	if err != nil {
		t.Fatalf("BackfillQuantSidecars returned error: %v", err)
	}
	if count != 3 {
		t.Errorf("expected count=3, got %d", count)
	}

	// Verify sidecars now exist.
	for _, ch := range chunks {
		has, err := d.HasQuantizedSidecar(ch.ID)
		if err != nil {
			t.Fatalf("HasQuantizedSidecar post-check for chunk %d: %v", ch.ID, err)
		}
		if !has {
			t.Errorf("chunk %d (UUID=%s) missing sidecar after backfill", ch.ID, ch.UUID)
		}
	}
}

// TestBackfillQuantSidecarsInvalidBitWidth verifies that an unsupported
// bit width causes BackfillQuantSidecars to return an error.
func TestBackfillQuantSidecarsInvalidBitWidth(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-backfill-err-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)
	t.Setenv("HOME", tempDir)

	d, err := db.Open()
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer d.Close()

	count, err := BackfillQuantSidecars(d, 99, 42, nil)
	if err == nil {
		t.Fatal("expected error for invalid bit width, got nil")
	}
	if count != 0 {
		t.Errorf("expected count=0 on error, got %d", count)
	}
}

// TestBackfillQuantSidecarsEmptyDB verifies that backfilling an empty DB
// returns count=0 without error.
func TestBackfillQuantSidecarsEmptyDB(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-backfill-empty-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)
	t.Setenv("HOME", tempDir)

	d, err := db.Open()
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer d.Close()

	count, err := BackfillQuantSidecars(d, 4, 42, nil)
	if err != nil {
		t.Fatalf("BackfillQuantSidecars on empty DB returned error: %v", err)
	}
	if count != 0 {
		t.Errorf("expected count=0 for empty DB, got %d", count)
	}
}

// timeNow returns current time for document timestamps.
func timeNow() time.Time {
	return time.Now()
}
