package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

// QuantSidecarMeta holds the parameters needed to decode a quantized
// embedding sidecar.  The struct is deliberately versioned so that stale
// sidecars can be detected and rebuilt when the codec changes.
type QuantSidecarMeta struct {
	// CodecVersion is an integer that must be bumped whenever the
	// quantization algorithm or wire format changes.  A sidecar written
	// with a different CodecVersion must be treated as stale.
	CodecVersion int `json:"codec_version"`

	// Dimension is the original float32 vector dimension (e.g. 768).
	Dimension int `json:"dimension"`

	// BitWidth is the number of bits per quantized code entry (e.g. 4, 8).
	BitWidth int `json:"bit_width"`

	// QuantizerMode describes the quantizer family (e.g. "scalar",
	// "product", "residual").  The exact semantics are defined by the
	// codec; the field is a forward-compatible tag.
	QuantizerMode string `json:"quantizer_mode"`

	// ProjectionSeed is an optional seed used for random rotation /
	// projection before quantization.  Zero means no projection was used.
	ProjectionSeed int64 `json:"projection_seed,omitempty"`

	// Norm is the L2 norm of the original float32 vector, stored so the
	// quantized representation can be rescaled if needed.
	Norm float32 `json:"norm,omitempty"`
}

// QuantSidecar bundles the raw quantized bytes together with their
// metadata.  This is the unit written to / read from the database.
type QuantSidecar struct {
	Quant []byte           `json:"-"`
	Meta  QuantSidecarMeta `json:"-"`
}

// marshalMeta serialises a QuantSidecarMeta to JSON bytes suitable for
// storage in the embedding_quant_meta TEXT column.
func marshalMeta(meta *QuantSidecarMeta) ([]byte, error) {
	return json.Marshal(meta)
}

// unmarshalMeta deserialises JSON bytes from the database into a
// QuantSidecarMeta.  An empty or nil input returns a zero-value meta
// with no error (the chunk simply has no sidecar).
func unmarshalMeta(raw []byte) (QuantSidecarMeta, error) {
	var meta QuantSidecarMeta
	if len(raw) == 0 {
		return meta, nil
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return meta, fmt.Errorf("unmarshal quant sidecar meta: %w", err)
	}
	return meta, nil
}

// SaveQuantizedSidecar stores the quantized bytes and metadata for a
// single chunk, identified by its row ID.  Both columns are updated
// together so they never drift apart.  A nil meta or empty quant bytes
// clears the sidecar.
func (db *DB) SaveQuantizedSidecar(chunkID int64, quant []byte, meta *QuantSidecarMeta) error {
	if chunkID <= 0 {
		return fmt.Errorf("invalid chunk ID: %d", chunkID)
	}

	var metaBytes []byte
	if meta != nil {
		var err error
		metaBytes, err = marshalMeta(meta)
		if err != nil {
			return fmt.Errorf("marshal quant sidecar meta: %w", err)
		}
	}

	_, err := db.conn.Exec(
		"UPDATE chunks SET embedding_quant = ?, embedding_quant_meta = ? WHERE id = ?",
		quant, metaBytes, chunkID,
	)
	return err
}

// GetQuantizedSidecar reads the quantized bytes and metadata for a
// single chunk.  Returns (nil, zero meta, nil) when the chunk has no
// sidecar data — this is not an error.
func (db *DB) GetQuantizedSidecar(chunkID int64) ([]byte, QuantSidecarMeta, error) {
	if chunkID <= 0 {
		return nil, QuantSidecarMeta{}, fmt.Errorf("invalid chunk ID: %d", chunkID)
	}

	var quant []byte
	var metaRaw sql.NullString
	err := db.conn.QueryRow(
		"SELECT embedding_quant, embedding_quant_meta FROM chunks WHERE id = ?",
		chunkID,
	).Scan(&quant, &metaRaw)
	if err == sql.ErrNoRows {
		return nil, QuantSidecarMeta{}, fmt.Errorf("chunk %d not found", chunkID)
	}
	if err != nil {
		return nil, QuantSidecarMeta{}, err
	}

	meta, err := unmarshalMeta([]byte(metaRaw.String))
	if err != nil {
		return nil, QuantSidecarMeta{}, err
	}
	return quant, meta, nil
}

// HasQuantizedSidecar returns true when the given chunk has non-nil
// quantized embedding data stored in the sidecar columns.
func (db *DB) HasQuantizedSidecar(chunkID int64) (bool, error) {
	if chunkID <= 0 {
		return false, fmt.Errorf("invalid chunk ID: %d", chunkID)
	}

	var has bool
	err := db.conn.QueryRow(
		"SELECT embedding_quant IS NOT NULL FROM chunks WHERE id = ?",
		chunkID,
	).Scan(&has)
	if err == sql.ErrNoRows {
		return false, fmt.Errorf("chunk %d not found", chunkID)
	}
	return has, err
}

// ClearQuantizedSidecar removes the quantized sidecar data for a chunk,
// setting both columns back to NULL.
func (db *DB) ClearQuantizedSidecar(chunkID int64) error {
	if chunkID <= 0 {
		return fmt.Errorf("invalid chunk ID: %d", chunkID)
	}
	_, err := db.conn.Exec(
		"UPDATE chunks SET embedding_quant = NULL, embedding_quant_meta = NULL WHERE id = ?",
		chunkID,
	)
	return err
}

// BackfillQuantizedSidecar iterates over all chunks that do not yet
// have a sidecar and applies the provided callback to produce one.
// The callback receives the chunk's row ID, float32 embedding, and
// L2 norm, and returns the quantized bytes and metadata to store.
//
// This is intentionally a stub/plumbing method: the callback contract
// lets a future TurboQuant codec be wired in without changing the
// backfill loop.  When fn is nil the method is a no-op.
//
// Progress is reported via the onProgress callback (may be nil).
// The returned count is the number of chunks successfully backfilled.
func (db *DB) BackfillQuantizedSidecar(
	fn func(chunkID int64, embedding []float32, norm float32) ([]byte, *QuantSidecarMeta, error),
	onProgress func(processed, total int),
) (int, error) {
	if fn == nil {
		return 0, nil
	}

	rows, err := db.conn.Query(
		"SELECT id, embedding, norm FROM chunks WHERE embedding_quant IS NULL",
	)
	if err != nil {
		return 0, fmt.Errorf("backfill query: %w", err)
	}
	defer rows.Close()

	type pendingRow struct {
		id   int64
		emb  []float32
		norm float32
	}
	var pending []pendingRow
	for rows.Next() {
		var r pendingRow
		var embBytes []byte
		if err := rows.Scan(&r.id, &embBytes, &r.norm); err != nil {
			return 0, fmt.Errorf("backfill scan: %w", err)
		}
		r.emb = BytesToFloat32Slice(embBytes)
		pending = append(pending, r)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("backfill rows: %w", err)
	}

	total := len(pending)
	backfilled := 0
	for i, r := range pending {
		quant, meta, err := fn(r.id, r.emb, r.norm)
		if err != nil {
			return backfilled, fmt.Errorf("backfill fn chunk %d: %w", r.id, err)
		}
		if err := db.SaveQuantizedSidecar(r.id, quant, meta); err != nil {
			return backfilled, fmt.Errorf("backfill save chunk %d: %w", r.id, err)
		}
		backfilled++
		if onProgress != nil {
			onProgress(i+1, total)
		}
	}
	return backfilled, nil
}

// GetChunksWithQuantSidecar returns the IDs of all chunks that have
// quantized sidecar data.  Useful for diagnostics and testing.
func (db *DB) GetChunksWithQuantSidecar() ([]int64, error) {
	rows, err := db.conn.Query("SELECT id FROM chunks WHERE embedding_quant IS NOT NULL ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// GetChunksWithoutQuantSidecar returns the IDs of all chunks that
// lack quantized sidecar data.
func (db *DB) GetChunksWithoutQuantSidecar() ([]int64, error) {
	rows, err := db.conn.Query("SELECT id FROM chunks WHERE embedding_quant IS NULL ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
