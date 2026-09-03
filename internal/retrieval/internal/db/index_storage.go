package db

import (
	"encoding/binary"
	"fmt"
	"math"
)

const indexStorageVersion = 1

// Serialize packs the IVF index into a small binary representation suitable
// for storing in SQLite.  The supplied generation is stored alongside the
// index so a reopened process can detect a stale snapshot.
func (vi *VectorIndex) Serialize(generation int64) ([]byte, error) {
	vi.mu.RLock()
	defer vi.mu.RUnlock()

	if !vi.ready {
		return nil, fmt.Errorf("index not ready")
	}

	headerSize := 1 + 8 + 4*8 // version + generation + 8 int32 fields
	centroidBytes := vi.k * vi.dim * 4
	totalSize := headerSize + centroidBytes
	for _, ids := range vi.inverted {
		totalSize += 4 + len(ids)*8
	}

	generationValue, err := checkedUint64("generation", generation)
	if err != nil {
		return nil, err
	}
	dimValue, err := checkedUint32("dimension", vi.dim)
	if err != nil {
		return nil, err
	}
	kValue, err := checkedUint32("cluster count", vi.k)
	if err != nil {
		return nil, err
	}
	nprobeValue, err := checkedUint32("probe count", vi.nprobe)
	if err != nil {
		return nil, err
	}
	totalNValue, err := checkedUint32("total chunk count", vi.totalN)
	if err != nil {
		return nil, err
	}
	baseTotalNValue, err := checkedUint32("base chunk count", vi.baseTotalN)
	if err != nil {
		return nil, err
	}
	churnAddedValue, err := checkedUint32("added chunk count", vi.churnAdded)
	if err != nil {
		return nil, err
	}
	churnDeletedValue, err := checkedUint32("deleted chunk count", vi.churnDeleted)
	if err != nil {
		return nil, err
	}

	buf := make([]byte, totalSize)
	off := 0

	buf[off] = indexStorageVersion
	off++
	binary.LittleEndian.PutUint64(buf[off:], generationValue)
	off += 8
	binary.LittleEndian.PutUint32(buf[off:], dimValue)
	off += 4
	binary.LittleEndian.PutUint32(buf[off:], kValue)
	off += 4
	binary.LittleEndian.PutUint32(buf[off:], nprobeValue)
	off += 4
	binary.LittleEndian.PutUint32(buf[off:], totalNValue)
	off += 4
	binary.LittleEndian.PutUint32(buf[off:], baseTotalNValue)
	off += 4
	binary.LittleEndian.PutUint32(buf[off:], churnAddedValue)
	off += 4
	binary.LittleEndian.PutUint32(buf[off:], churnDeletedValue)
	off += 4

	for _, cent := range vi.centroids {
		for _, v := range cent {
			binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(v))
			off += 4
		}
	}

	for _, ids := range vi.inverted {
		bucketSize, err := checkedUint32("bucket size", len(ids))
		if err != nil {
			return nil, err
		}
		binary.LittleEndian.PutUint32(buf[off:], bucketSize)
		off += 4
		for _, id := range ids {
			idValue, err := checkedUint64("chunk ID", id)
			if err != nil {
				return nil, err
			}
			binary.LittleEndian.PutUint64(buf[off:], idValue)
			off += 8
		}
	}

	return buf, nil
}

func checkedUint32(name string, value int) (uint32, error) {
	if value < 0 || uint64(value) > uint64(^uint32(0)) {
		return 0, fmt.Errorf("%s does not fit in uint32: %d", name, value)
	}
	return uint32(value), nil // #nosec G115 -- range checked above.
}

func checkedUint64(name string, value int64) (uint64, error) {
	if value < 0 {
		return 0, fmt.Errorf("%s must be non-negative: %d", name, value)
	}
	return uint64(value), nil // #nosec G115 -- non-negative value checked above.
}

// DeserializeIndex unpacks a binary IVF index snapshot.  It returns the index
// and the generation that was stored with it.
func DeserializeIndex(data []byte) (*VectorIndex, int64, error) {
	if len(data) < 1+8+4*8 {
		return nil, 0, fmt.Errorf("index data too short")
	}
	off := 0

	version := data[off]
	off++
	if version != indexStorageVersion {
		return nil, 0, fmt.Errorf("unsupported index storage version: %d", version)
	}

	generationRaw := binary.LittleEndian.Uint64(data[off:])
	if generationRaw > uint64(1<<63-1) {
		return nil, 0, fmt.Errorf("index generation overflows int64: %d", generationRaw)
	}
	generation := int64(generationRaw) // #nosec G115 -- range checked above.
	off += 8
	dim := int(binary.LittleEndian.Uint32(data[off:]))
	off += 4
	k := int(binary.LittleEndian.Uint32(data[off:]))
	off += 4
	nprobe := int(binary.LittleEndian.Uint32(data[off:]))
	off += 4
	totalN := int(binary.LittleEndian.Uint32(data[off:]))
	off += 4
	baseTotalN := int(binary.LittleEndian.Uint32(data[off:]))
	off += 4
	churnAdded := int(binary.LittleEndian.Uint32(data[off:]))
	off += 4
	churnDeleted := int(binary.LittleEndian.Uint32(data[off:]))
	off += 4

	expectedCentroidBytes := k * dim * 4
	if len(data) < off+expectedCentroidBytes {
		return nil, 0, fmt.Errorf("index data truncated at centroids")
	}
	centroids := make([][]float32, k)
	for i := 0; i < k; i++ {
		cent := make([]float32, dim)
		for d := 0; d < dim; d++ {
			bits := binary.LittleEndian.Uint32(data[off:])
			cent[d] = math.Float32frombits(bits)
			off += 4
		}
		centroids[i] = cent
	}

	inverted := make([][]int64, k)
	for i := 0; i < k; i++ {
		if len(data) < off+4 {
			return nil, 0, fmt.Errorf("index data truncated at inverted length")
		}
		n := int(binary.LittleEndian.Uint32(data[off:]))
		off += 4
		expected := n * 8
		if len(data) < off+expected {
			return nil, 0, fmt.Errorf("index data truncated at inverted IDs")
		}
		ids := make([]int64, n)
		for j := 0; j < n; j++ {
			idRaw := binary.LittleEndian.Uint64(data[off:])
			if idRaw > uint64(1<<63-1) {
				return nil, 0, fmt.Errorf("chunk ID overflows int64: %d", idRaw)
			}
			ids[j] = int64(idRaw) // #nosec G115 -- range checked above.
			off += 8
		}
		inverted[i] = ids
	}

	return &VectorIndex{
		dim:          dim,
		centroids:    centroids,
		inverted:     inverted,
		k:            k,
		nprobe:       nprobe,
		totalN:       totalN,
		ready:        true,
		baseTotalN:   baseTotalN,
		churnAdded:   churnAdded,
		churnDeleted: churnDeleted,
	}, generation, nil
}
