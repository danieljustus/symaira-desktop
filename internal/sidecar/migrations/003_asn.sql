-- Archive serial numbers are an optional, positive integer contract field.
-- The unique-vault invariant is checked against the filesystem so doctor can
-- diagnose manual conflicts instead of making a sidecar reindex impossible.
ALTER TABLE files ADD COLUMN asn INTEGER CHECK (asn IS NULL OR asn > 0);

CREATE INDEX IF NOT EXISTS idx_files_asn ON files(asn);
