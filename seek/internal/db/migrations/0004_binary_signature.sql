-- Add binary_signature column for two-stage Hamming pre-filter.
-- The column is nullable so existing rows remain valid; new inserts
-- backfill the signature via SignBinarySignature.
ALTER TABLE chunks ADD COLUMN binary_signature BLOB;

-- Add embedding dimension and model metadata for mixed-space detection.
-- Nullable columns so existing rows (with no metadata) remain valid.
ALTER TABLE chunks ADD COLUMN embedding_dim INTEGER;
ALTER TABLE chunks ADD COLUMN embedding_model TEXT;
