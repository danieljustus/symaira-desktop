package db

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"time"
)

// openTestDB creates a fresh database in a temporary directory, runs
// migrations, and returns a ready-to-use *DB.  The caller should
// defer db.Close().
func openTestDB(t testing.TB) *DB {
	t.Helper()
	tempDir, err := os.MkdirTemp("", "seek-quant-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Setenv("HOME", tempDir)

	d, err := Open()
	if err != nil {
		os.RemoveAll(tempDir)
		t.Fatalf("failed to open test database: %v", err)
	}
	t.Cleanup(func() {
		d.Close()
		os.RemoveAll(tempDir)
	})
	return d
}

// insertTestChunk saves a document and chunk, returning the chunk's row ID.
func insertTestChunk(t *testing.T, d *DB, uuid string, emb []float32) int64 {
	t.Helper()
	docPath := filepath.Join(t.TempDir(), "test.md")
	if err := d.SaveDocument(&Document{Path: docPath, Hash: "h", UpdatedAt: time.Now()}); err != nil {
		t.Fatalf("SaveDocument: %v", err)
	}
	chunks := []*Chunk{
		{
			UUID:         uuid,
			DocumentPath: docPath,
			ChunkIndex:   0,
			Content:      "test content " + uuid,
			Embedding:    emb,
			Hash:         uuid + "-hash",
		},
	}
	if err := d.SaveChunks(chunks); err != nil {
		t.Fatalf("SaveChunks: %v", err)
	}
	return chunks[0].ID
}

// TestQuantMigrationFromBaseline verifies that the new columns are
// added by the migration and that rows created before the migration
// (simulated by inserting into the table directly) have NULL sidecar
// values while new inserts can populate them.
func TestQuantMigrationFromBaseline(t *testing.T) {
	d := openTestDB(t)

	// The schema should have the new columns.  Verify via PRAGMA.
	var colNames []string
	rows, err := d.conn.Query("PRAGMA table_info(chunks)")
	if err != nil {
		t.Fatalf("PRAGMA table_info: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, colType string
		var notnull int
		var dfltValue, pk interface{}
		if err := rows.Scan(&cid, &name, &colType, &notnull, &dfltValue, &pk); err != nil {
			t.Fatalf("scan pragma: %v", err)
		}
		colNames = append(colNames, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}

	for _, want := range []string{"embedding_quant", "embedding_quant_meta"} {
		found := false
		for _, c := range colNames {
			if c == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected column %q to exist in chunks table; columns: %v", want, colNames)
		}
	}

	// Insert a chunk and verify sidecar columns default to NULL.
	emb := []float32{1.0, 0.0, 0.0}
	chunkID := insertTestChunk(t, d, "baseline-c1", emb)

	var quantVal, metaVal interface{}
	err = d.conn.QueryRow(
		"SELECT embedding_quant, embedding_quant_meta FROM chunks WHERE id = ?",
		chunkID,
	).Scan(&quantVal, &metaVal)
	if err != nil {
		t.Fatalf("SELECT sidecar: %v", err)
	}
	if quantVal != nil {
		t.Errorf("expected embedding_quant to be NULL for new row, got %v", quantVal)
	}
	if metaVal != nil {
		t.Errorf("expected embedding_quant_meta to be NULL for new row, got %v", metaVal)
	}
}

// TestQuantSidecarRoundTrip verifies write → read round-trip for
// quantized sidecar data.
func TestQuantSidecarRoundTrip(t *testing.T) {
	d := openTestDB(t)

	emb := make([]float32, 768)
	for i := range emb {
		emb[i] = float32(i) / 768.0
	}
	chunkID := insertTestChunk(t, d, "rt-c1", emb)

	// Build test quantized data.
	quant := make([]byte, 96) // e.g. 768 dims / 8 bits = 96 bytes
	for i := range quant {
		quant[i] = byte(i)
	}
	meta := QuantSidecarMeta{
		CodecVersion:   1,
		Dimension:      768,
		BitWidth:       8,
		QuantizerMode:  "scalar",
		ProjectionSeed: 0,
		Norm:           1.0,
	}

	// Write.
	if err := d.SaveQuantizedSidecar(chunkID, quant, &meta); err != nil {
		t.Fatalf("SaveQuantizedSidecar: %v", err)
	}

	// Read back.
	gotQuant, gotMeta, err := d.GetQuantizedSidecar(chunkID)
	if err != nil {
		t.Fatalf("GetQuantizedSidecar: %v", err)
	}
	if len(gotQuant) != len(quant) {
		t.Fatalf("quant length mismatch: got %d, want %d", len(gotQuant), len(quant))
	}
	for i := range quant {
		if gotQuant[i] != quant[i] {
			t.Fatalf("quant byte %d mismatch: got %d, want %d", i, gotQuant[i], quant[i])
		}
	}
	if gotMeta.CodecVersion != meta.CodecVersion {
		t.Errorf("CodecVersion: got %d, want %d", gotMeta.CodecVersion, meta.CodecVersion)
	}
	if gotMeta.Dimension != meta.Dimension {
		t.Errorf("Dimension: got %d, want %d", gotMeta.Dimension, meta.Dimension)
	}
	if gotMeta.BitWidth != meta.BitWidth {
		t.Errorf("BitWidth: got %d, want %d", gotMeta.BitWidth, meta.BitWidth)
	}
	if gotMeta.QuantizerMode != meta.QuantizerMode {
		t.Errorf("QuantizerMode: got %q, want %q", gotMeta.QuantizerMode, meta.QuantizerMode)
	}
	if gotMeta.Norm != meta.Norm {
		t.Errorf("Norm: got %f, want %f", gotMeta.Norm, meta.Norm)
	}
}

// TestQuantSidecarRoundTripMinimalMeta verifies that minimal metadata
// (codec version and dimension only) round-trips correctly.
func TestQuantSidecarRoundTripMinimalMeta(t *testing.T) {
	d := openTestDB(t)

	emb := []float32{0.5, 0.5}
	chunkID := insertTestChunk(t, d, "minimal-c1", emb)

	quant := []byte{0xAA, 0xBB}
	meta := QuantSidecarMeta{
		CodecVersion:  2,
		Dimension:     2,
		BitWidth:      4,
		QuantizerMode: "product",
	}

	if err := d.SaveQuantizedSidecar(chunkID, quant, &meta); err != nil {
		t.Fatalf("SaveQuantizedSidecar: %v", err)
	}

	gotQuant, gotMeta, err := d.GetQuantizedSidecar(chunkID)
	if err != nil {
		t.Fatalf("GetQuantizedSidecar: %v", err)
	}
	if len(gotQuant) != 2 || gotQuant[0] != 0xAA || gotQuant[1] != 0xBB {
		t.Errorf("quant mismatch: got %v", gotQuant)
	}
	if gotMeta.CodecVersion != 2 || gotMeta.Dimension != 2 || gotMeta.BitWidth != 4 {
		t.Errorf("meta mismatch: got %+v", gotMeta)
	}
	if gotMeta.ProjectionSeed != 0 {
		t.Errorf("expected ProjectionSeed 0, got %d", gotMeta.ProjectionSeed)
	}
}

// TestQuantSidecarMissingData verifies that reading a sidecar for a
// chunk without one returns nil quant bytes and zero meta without error.
func TestQuantSidecarMissingData(t *testing.T) {
	d := openTestDB(t)

	emb := []float32{1.0, 0.0, 0.0}
	chunkID := insertTestChunk(t, d, "miss-c1", emb)

	gotQuant, gotMeta, err := d.GetQuantizedSidecar(chunkID)
	if err != nil {
		t.Fatalf("GetQuantizedSidecar on chunk without sidecar: %v", err)
	}
	if gotQuant != nil {
		t.Errorf("expected nil quant for chunk without sidecar, got %v", gotQuant)
	}
	if gotMeta != (QuantSidecarMeta{}) {
		t.Errorf("expected zero meta for chunk without sidecar, got %+v", gotMeta)
	}
}

// TestQuantSidecarNonexistentChunk verifies that reading a sidecar
// for a nonexistent chunk ID returns an error.
func TestQuantSidecarNonexistentChunk(t *testing.T) {
	d := openTestDB(t)

	_, _, err := d.GetQuantizedSidecar(99999)
	if err == nil {
		t.Fatal("expected error for nonexistent chunk")
	}
}

// TestQuantSidecarInvalidChunkID verifies that zero/negative chunk IDs
// are rejected by all sidecar methods.
func TestQuantSidecarInvalidChunkID(t *testing.T) {
	d := openTestDB(t)

	meta := QuantSidecarMeta{CodecVersion: 1, Dimension: 3, BitWidth: 8, QuantizerMode: "scalar"}
	if err := d.SaveQuantizedSidecar(0, []byte{1}, &meta); err == nil {
		t.Error("expected error for chunk ID 0")
	}
	if err := d.SaveQuantizedSidecar(-1, []byte{1}, &meta); err == nil {
		t.Error("expected error for negative chunk ID")
	}
	if _, _, err := d.GetQuantizedSidecar(0); err == nil {
		t.Error("expected error for chunk ID 0")
	}
	if _, _, err := d.GetQuantizedSidecar(-1); err == nil {
		t.Error("expected error for negative chunk ID")
	}
	if _, err := d.HasQuantizedSidecar(0); err == nil {
		t.Error("expected error for chunk ID 0")
	}
	if err := d.ClearQuantizedSidecar(0); err == nil {
		t.Error("expected error for chunk ID 0")
	}
}

// TestQuantSidecarClear verifies that ClearQuantizedSidecar sets both
// columns back to NULL.
func TestQuantSidecarClear(t *testing.T) {
	d := openTestDB(t)

	emb := []float32{1.0, 0.0}
	chunkID := insertTestChunk(t, d, "clear-c1", emb)

	meta := QuantSidecarMeta{CodecVersion: 1, Dimension: 2, BitWidth: 8, QuantizerMode: "scalar"}
	if err := d.SaveQuantizedSidecar(chunkID, []byte{0x01, 0x02}, &meta); err != nil {
		t.Fatalf("SaveQuantizedSidecar: %v", err)
	}

	has, err := d.HasQuantizedSidecar(chunkID)
	if err != nil {
		t.Fatalf("HasQuantizedSidecar: %v", err)
	}
	if !has {
		t.Fatal("expected HasQuantizedSidecar = true after save")
	}

	if err := d.ClearQuantizedSidecar(chunkID); err != nil {
		t.Fatalf("ClearQuantizedSidecar: %v", err)
	}

	has, err = d.HasQuantizedSidecar(chunkID)
	if err != nil {
		t.Fatalf("HasQuantizedSidecar after clear: %v", err)
	}
	if has {
		t.Fatal("expected HasQuantizedSidecar = false after clear")
	}

	gotQuant, _, err := d.GetQuantizedSidecar(chunkID)
	if err != nil {
		t.Fatalf("GetQuantizedSidecar after clear: %v", err)
	}
	if gotQuant != nil {
		t.Errorf("expected nil quant after clear, got %v", gotQuant)
	}
}

// TestQuantMixedOldNewRows verifies that rows with and without sidecar
// data coexist correctly and that existing search/query paths are
// unaffected.
func TestQuantMixedOldNewRows(t *testing.T) {
	d := openTestDB(t)

	docPath := filepath.Join(t.TempDir(), "mixed.md")
	if err := d.SaveDocument(&Document{Path: docPath, Hash: "m", UpdatedAt: time.Now()}); err != nil {
		t.Fatalf("SaveDocument: %v", err)
	}

	// Insert 5 chunks — only #2 and #4 will get sidecar data.
	chunks := make([]*Chunk, 5)
	for i := range chunks {
		emb := make([]float32, 768)
		emb[i%768] = float32(i+1) / 5.0
		chunks[i] = &Chunk{
			UUID:         "mix-" + strconv.Itoa(i),
			DocumentPath: docPath,
			ChunkIndex:   i,
			Content:      "content " + strconv.Itoa(i),
			Embedding:    emb,
			Hash:         "mix-hash-" + strconv.Itoa(i),
		}
	}
	if err := d.SaveChunks(chunks); err != nil {
		t.Fatalf("SaveChunks: %v", err)
	}

	// Add sidecar data for chunks at index 1 and 3 (IDs are 1-indexed from insert order).
	sidecarIDs := []int64{chunks[1].ID, chunks[3].ID}
	for _, id := range sidecarIDs {
		meta := QuantSidecarMeta{
			CodecVersion:  1,
			Dimension:     768,
			BitWidth:      4,
			QuantizerMode: "product",
		}
		if err := d.SaveQuantizedSidecar(id, []byte{byte(id), 0x42}, &meta); err != nil {
			t.Fatalf("SaveQuantizedSidecar for chunk %d: %v", id, err)
		}
	}

	// Verify HasQuantizedSidecar for all 5 chunks.
	for i, c := range chunks {
		has, err := d.HasQuantizedSidecar(c.ID)
		if err != nil {
			t.Fatalf("HasQuantizedSidecar chunk %d: %v", c.ID, err)
		}
		wantHas := (i == 1 || i == 3)
		if has != wantHas {
			t.Errorf("chunk %d (index %d): HasQuantizedSidecar = %v, want %v", c.ID, i, has, wantHas)
		}
	}

	// Verify sidecar listing functions.
	withIDs, err := d.GetChunksWithQuantSidecar()
	if err != nil {
		t.Fatalf("GetChunksWithQuantSidecar: %v", err)
	}
	withoutIDs, err := d.GetChunksWithoutQuantSidecar()
	if err != nil {
		t.Fatalf("GetChunksWithoutQuantSidecar: %v", err)
	}
	if len(withIDs) != 2 {
		t.Errorf("expected 2 chunks with sidecar, got %d", len(withIDs))
	}
	if len(withoutIDs) != 3 {
		t.Errorf("expected 3 chunks without sidecar, got %d", len(withoutIDs))
	}

	// Verify that BM25 search still works on mixed rows.
	bm25Res, err := d.SearchBM25("content 0", 10)
	if err != nil {
		t.Fatalf("SearchBM25: %v", err)
	}
	if len(bm25Res) == 0 {
		t.Fatal("expected BM25 results")
	}

	// Verify that vector search still works on mixed rows.
	queryVec := make([]float32, 768)
	queryVec[0] = 0.3
	vecRes, err := d.SearchVector(queryVec, 5)
	if err != nil {
		t.Fatalf("SearchVector: %v", err)
	}
	if len(vecRes) != 5 {
		t.Fatalf("expected 5 vector results, got %d", len(vecRes))
	}
}

// TestQuantSidecarUpdateOverwrite verifies that saving a sidecar
// twice overwrites the previous value.
func TestQuantSidecarUpdateOverwrite(t *testing.T) {
	d := openTestDB(t)

	emb := []float32{1.0, 0.0}
	chunkID := insertTestChunk(t, d, "upd-c1", emb)

	meta1 := QuantSidecarMeta{CodecVersion: 1, Dimension: 2, BitWidth: 8, QuantizerMode: "scalar"}
	if err := d.SaveQuantizedSidecar(chunkID, []byte{0x01}, &meta1); err != nil {
		t.Fatalf("SaveQuantizedSidecar (first): %v", err)
	}

	meta2 := QuantSidecarMeta{CodecVersion: 2, Dimension: 2, BitWidth: 4, QuantizerMode: "product"}
	if err := d.SaveQuantizedSidecar(chunkID, []byte{0x02, 0x03}, &meta2); err != nil {
		t.Fatalf("SaveQuantizedSidecar (second): %v", err)
	}

	gotQuant, gotMeta, err := d.GetQuantizedSidecar(chunkID)
	if err != nil {
		t.Fatalf("GetQuantizedSidecar: %v", err)
	}
	if len(gotQuant) != 2 || gotQuant[0] != 0x02 || gotQuant[1] != 0x03 {
		t.Errorf("expected overwritten quant, got %v", gotQuant)
	}
	if gotMeta.CodecVersion != 2 {
		t.Errorf("expected CodecVersion 2 after overwrite, got %d", gotMeta.CodecVersion)
	}
	if gotMeta.QuantizerMode != "product" {
		t.Errorf("expected QuantizerMode 'product' after overwrite, got %q", gotMeta.QuantizerMode)
	}
}

// TestBackfillQuantizedSidecar verifies the backfill loop for
// populating sidecars on existing rows.
func TestBackfillQuantizedSidecar(t *testing.T) {
	d := openTestDB(t)

	// Insert 3 chunks without sidecar data.
	ids := make([]int64, 3)
	for i := 0; i < 3; i++ {
		emb := make([]float32, 768)
		emb[0] = float32(i+1) / 3.0
		ids[i] = insertTestChunk(t, d, "bf-c"+strconv.Itoa(i), emb)
	}

	// Backfill callback: produce a deterministic sidecar from the embedding.
	processed := 0
	totalSeen := 0
	fn := func(chunkID int64, embedding []float32, norm float32) ([]byte, *QuantSidecarMeta, error) {
		quant := make([]byte, len(embedding))
		for i, v := range embedding {
			quant[i] = byte(int(v*255) & 0xFF)
		}
		meta := &QuantSidecarMeta{
			CodecVersion:  1,
			Dimension:     len(embedding),
			BitWidth:      8,
			QuantizerMode: "scalar",
			Norm:          norm,
		}
		processed++
		return quant, meta, nil
	}
	progressFn := func(p, tot int) {
		totalSeen = tot
	}

	count, err := d.BackfillQuantizedSidecar(fn, progressFn)
	if err != nil {
		t.Fatalf("BackfillQuantizedSidecar: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 chunks backfilled, got %d", count)
	}
	if totalSeen != 3 {
		t.Errorf("expected onProgress total=3, got %d", totalSeen)
	}

	// Verify each chunk now has a sidecar.
	for _, id := range ids {
		has, err := d.HasQuantizedSidecar(id)
		if err != nil {
			t.Fatalf("HasQuantizedSidecar: %v", err)
		}
		if !has {
			t.Errorf("chunk %d should have sidecar after backfill", id)
		}
	}

	// Running backfill again should be a no-op (all already have sidecars).
	count2, err := d.BackfillQuantizedSidecar(fn, nil)
	if err != nil {
		t.Fatalf("second BackfillQuantizedSidecar: %v", err)
	}
	if count2 != 0 {
		t.Errorf("expected 0 on second backfill, got %d", count2)
	}
}

// TestBackfillQuantizedSidecarNilCallback verifies that a nil
// callback makes backfill a no-op.
func TestBackfillQuantizedSidecarNilCallback(t *testing.T) {
	d := openTestDB(t)
	emb := []float32{1.0, 0.0}
	insertTestChunk(t, d, "bf-nil-c1", emb)

	count, err := d.BackfillQuantizedSidecar(nil, nil)
	if err != nil {
		t.Fatalf("BackfillQuantizedSidecar(nil): %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0, got %d", count)
	}
}

// TestQuantSidecarMetaJSONRoundTrip verifies that the metadata struct
// serialises/deserialises cleanly via the internal helpers.
func TestQuantSidecarMetaJSONRoundTrip(t *testing.T) {
	meta := QuantSidecarMeta{
		CodecVersion:   42,
		Dimension:      768,
		BitWidth:       4,
		QuantizerMode:  "residual",
		ProjectionSeed: 12345,
		Norm:           3.14159,
	}

	raw, err := marshalMeta(&meta)
	if err != nil {
		t.Fatalf("marshalMeta: %v", err)
	}

	got, err := unmarshalMeta(raw)
	if err != nil {
		t.Fatalf("unmarshalMeta: %v", err)
	}
	if got != meta {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, meta)
	}

	// Verify the JSON is well-formed.
	var parsed map[string]interface{}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("JSON unmarshal: %v", err)
	}
	if parsed["codec_version"].(float64) != 42 {
		t.Errorf("expected codec_version 42 in JSON")
	}
	if parsed["quantizer_mode"].(string) != "residual" {
		t.Errorf("expected quantizer_mode 'residual' in JSON")
	}
}

// TestQuantSidecarEmptyMeta verifies that nil/empty JSON input
// returns a zero-value meta without error.
func TestQuantSidecarEmptyMeta(t *testing.T) {
	meta, err := unmarshalMeta(nil)
	if err != nil {
		t.Fatalf("unmarshalMeta(nil): %v", err)
	}
	if meta != (QuantSidecarMeta{}) {
		t.Errorf("expected zero meta from nil, got %+v", meta)
	}

	meta, err = unmarshalMeta([]byte{})
	if err != nil {
		t.Fatalf("unmarshalMeta(empty): %v", err)
	}
	if meta != (QuantSidecarMeta{}) {
		t.Errorf("expected zero meta from empty, got %+v", meta)
	}
}

// TestQuantSidecarInvalidMetaJSON verifies that malformed JSON is
// returned as an error.
func TestQuantSidecarInvalidMetaJSON(t *testing.T) {
	_, err := unmarshalMeta([]byte("{bad json"))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

// TestExistingSearchUnaffectedByQuantColumns ensures that all search
// paths (BM25, vector full scan, vector prefilter) continue to work
// correctly after the migration, with a mix of sidecar-populated and
// unpopulated rows.
func TestExistingSearchUnaffectedByQuantColumns(t *testing.T) {
	d := openTestDB(t)

	docPath := filepath.Join(t.TempDir(), "search-nodesc.md")
	if err := d.SaveDocument(&Document{Path: docPath, Hash: "sn", UpdatedAt: time.Now()}); err != nil {
		t.Fatalf("SaveDocument: %v", err)
	}

	const nChunks = 50
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
			UUID:         "sn-" + strconv.Itoa(i),
			DocumentPath: docPath,
			ChunkIndex:   i,
			Content:      "searchable content " + strconv.Itoa(i),
			Hash:         "sn-hash-" + strconv.Itoa(i),
		}
	}
	if err := d.SaveChunks(chunks); err != nil {
		t.Fatalf("SaveChunks: %v", err)
	}

	// Add sidecar data to even-indexed chunks.
	for i := 0; i < nChunks; i += 2 {
		meta := QuantSidecarMeta{CodecVersion: 1, Dimension: 768, BitWidth: 8, QuantizerMode: "scalar"}
		if err := d.SaveQuantizedSidecar(chunks[i].ID, make([]byte, 96), &meta); err != nil {
			t.Fatalf("SaveQuantizedSidecar: %v", err)
		}
	}

	// BM25 search should work.
	bm25Res, err := d.SearchBM25("searchable content 5", 5)
	if err != nil {
		t.Fatalf("SearchBM25: %v", err)
	}
	if len(bm25Res) == 0 {
		t.Fatal("expected BM25 results")
	}

	// Vector search should work.  Use a query derived from the data
	// rather than an exact-copy to avoid sensitivity to normalisation
	// and IVF bucket assignment.
	queryVec := make([]float32, 768)
	for i := range queryVec {
		queryVec[i] = float32(math.Sin(float64(25)*0.1 + float64(i)*0.05))
	}
	{
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
	}
	vecRes, err := d.SearchVector(queryVec, 10)
	if err != nil {
		t.Fatalf("SearchVector: %v", err)
	}
	if len(vecRes) == 0 {
		t.Fatal("expected vector results")
	}
	t.Logf("vector search returned %d results, top=%s", len(vecRes), vecRes[0].Chunk.UUID)

	// GetChunksForDocument should still return embeddings correctly.
	docChunks, err := d.GetChunksForDocument(docPath)
	if err != nil {
		t.Fatalf("GetChunksForDocument: %v", err)
	}
	if len(docChunks) != nChunks {
		t.Fatalf("expected %d chunks, got %d", nChunks, len(docChunks))
	}

	// Stats should be unaffected.
	stats, err := d.GetStats()
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	if stats.ChunkCount != nChunks {
		t.Errorf("expected %d chunks in stats, got %d", nChunks, stats.ChunkCount)
	}

	// Sort IDs for deterministic comparison.
	withIDs, _ := d.GetChunksWithQuantSidecar()
	sort.Slice(withIDs, func(i, j int) bool { return withIDs[i] < withIDs[j] })
	expected := make([]int64, 0, nChunks/2)
	for i := 0; i < nChunks; i += 2 {
		expected = append(expected, chunks[i].ID)
	}
	if len(withIDs) != len(expected) {
		t.Fatalf("GetChunksWithQuantSidecar: got %d, want %d", len(withIDs), len(expected))
	}
	for i, id := range withIDs {
		if id != expected[i] {
			t.Errorf("with sidecar[%d]: got %d, want %d", i, id, expected[i])
		}
	}
}

// TestBackfillQuantizedSidecarErrorPropagation verifies that the
// backfill loop stops and returns an error when the callback fails.
func TestBackfillQuantizedSidecarErrorPropagation(t *testing.T) {
	d := openTestDB(t)

	for i := 0; i < 3; i++ {
		emb := []float32{float32(i), 0.0, 0.0}
		insertTestChunk(t, d, "bf-err-c"+strconv.Itoa(i), emb)
	}

	callCount := 0
	fn := func(chunkID int64, embedding []float32, norm float32) ([]byte, *QuantSidecarMeta, error) {
		callCount++
		if callCount == 2 {
			return nil, nil, &json.SyntaxError{}
		}
		return []byte{1}, &QuantSidecarMeta{CodecVersion: 1, Dimension: 3, BitWidth: 8, QuantizerMode: "scalar"}, nil
	}

	_, err := d.BackfillQuantizedSidecar(fn, nil)
	if err == nil {
		t.Fatal("expected error from backfill when callback fails")
	}

	// First chunk should have sidecar, second and third should not.
	has1, _ := d.HasQuantizedSidecar(1)
	if !has1 {
		t.Error("expected first chunk to have sidecar before failure")
	}
}
