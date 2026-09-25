-- Add optional quantized embedding sidecar columns.
-- These columns are nullable so existing rows remain valid and no
-- existing queries break.  The float32 `embedding` column stays
-- untouched and authoritative.

ALTER TABLE chunks ADD COLUMN embedding_quant BLOB;
ALTER TABLE chunks ADD COLUMN embedding_quant_meta TEXT;
