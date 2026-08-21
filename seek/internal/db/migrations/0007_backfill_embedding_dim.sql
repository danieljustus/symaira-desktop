-- Backfill embedding_dim and embedding_model for legacy rows that were
-- inserted before the metadata columns were added (migration 0004).
--
-- embedding_dim is derived from the raw embedding blob length: each float32
-- occupies 4 bytes, so dim = length(embedding) / 4.  Only rows whose blob
-- length is an exact multiple of 4 are touched.
--
-- embedding_model is set to 'unknown' (not the configured model) because we
-- cannot reliably infer which model produced a legacy embedding.

UPDATE chunks
SET embedding_dim = length(embedding) / 4
WHERE embedding_dim IS NULL
  AND embedding IS NOT NULL
  AND length(embedding) % 4 = 0;

UPDATE chunks
SET embedding_model = 'unknown'
WHERE embedding_dim IS NOT NULL
  AND (embedding_model IS NULL OR embedding_model = '');
