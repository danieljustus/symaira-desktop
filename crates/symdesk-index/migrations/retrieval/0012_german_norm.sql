-- Normalised German token index for chunk search (#672): the chunks table
-- stores the stopword-removed, stemmed, umlaut-folded form of each chunk's
-- content alongside the original, and a trigger pair keeps the chunks_norm
-- FTS table in step. Populating content_norm happens in Go (stemming lives
-- in internal/searchquery); see DB.backfillContentNorm for existing rows.
ALTER TABLE chunks ADD COLUMN content_norm TEXT;

CREATE VIRTUAL TABLE IF NOT EXISTS chunks_norm USING fts5(content_norm);

CREATE TRIGGER IF NOT EXISTS chunks_norm_ai AFTER INSERT ON chunks WHEN new.content_norm IS NOT NULL BEGIN
    INSERT INTO chunks_norm(rowid, content_norm) VALUES (new.id, new.content_norm);
END;

CREATE TRIGGER IF NOT EXISTS chunks_norm_ad AFTER DELETE ON chunks BEGIN
    DELETE FROM chunks_norm WHERE rowid = old.id;
END;

CREATE TRIGGER IF NOT EXISTS chunks_norm_au AFTER UPDATE OF content_norm ON chunks BEGIN
    DELETE FROM chunks_norm WHERE rowid = old.id;
    INSERT INTO chunks_norm(rowid, content_norm) SELECT new.id, new.content_norm WHERE new.content_norm IS NOT NULL;
END;
