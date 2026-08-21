package engine

import (
	"fmt"
	"os"

	"github.com/danieljustus/symaira-corekit/vectorkit/turboquant"
	"github.com/danieljustus/symaira-seek/internal/db"
)

func BackfillQuantSidecars(d *db.DB, bitWidth int, seed int, onProgress func(processed, total int)) (int, error) {
	dim := 768

	codec, err := turboquant.NewCodec(dim, turboquant.BitWidth(bitWidth), seed, 0)
	if err != nil {
		return 0, fmt.Errorf("create codec: %w", err)
	}

	fn := func(chunkID int64, embedding []float32, norm float32) ([]byte, *db.QuantSidecarMeta, error) {
		blob, sideMeta, err := codec.EncodeSidecar(embedding, norm)
		if err != nil {
			return nil, nil, err
		}
		meta := &db.QuantSidecarMeta{
			CodecVersion:   sideMeta.CodecVersion,
			Dimension:      sideMeta.Dimension,
			BitWidth:       sideMeta.BitWidth,
			QuantizerMode:  sideMeta.QuantizerMode,
			ProjectionSeed: sideMeta.ProjectionSeed,
			Norm:           sideMeta.Norm,
		}
		return blob, meta, nil
	}

	count, err := d.BackfillQuantizedSidecar(fn, onProgress)
	if err != nil {
		return count, err
	}

	fmt.Fprintf(os.Stderr, "Backfilled %d chunks with quantized sidecars (bit_width=%d, seed=%d)\n", count, bitWidth, seed)
	return count, nil
}
