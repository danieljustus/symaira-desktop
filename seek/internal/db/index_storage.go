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

	buf := make([]byte, totalSize)
	off := 0

	buf[off] = indexStorageVersion
	off++
	binary.LittleEndian.PutUint64(buf[off:], uint64(generation))
	off += 8
	binary.LittleEndian.PutUint32(buf[off:], uint32(vi.dim))
	off += 4
	binary.LittleEndian.PutUint32(buf[off:], uint32(vi.k))
	off += 4
	binary.LittleEndian.PutUint32(buf[off:], uint32(vi.nprobe))
	off += 4
	binary.LittleEndian.PutUint32(buf[off:], uint32(vi.totalN))
	off += 4
	binary.LittleEndian.PutUint32(buf[off:], uint32(vi.baseTotalN))
	off += 4
	binary.LittleEndian.PutUint32(buf[off:], uint32(vi.churnAdded))
	off += 4
	binary.LittleEndian.PutUint32(buf[off:], uint32(vi.churnDeleted))
	off += 4

	for _, cent := range vi.centroids {
		for _, v := range cent {
			binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(v))
			off += 4
		}
	}

	for _, ids := range vi.inverted {
		binary.LittleEndian.PutUint32(buf[off:], uint32(len(ids)))
		off += 4
		for _, id := range ids {
			binary.LittleEndian.PutUint64(buf[off:], uint64(id))
			off += 8
		}
	}

	return buf, nil
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

	generation := int64(binary.LittleEndian.Uint64(data[off:]))
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
			ids[j] = int64(binary.LittleEndian.Uint64(data[off:]))
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
