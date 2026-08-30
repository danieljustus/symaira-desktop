-- Normalised German token index (#672): stores the stopword-removed,
-- stemmed, umlaut-folded token stream of every note so inflected and
-- umlaut-bearing queries match the same normal form. Populated in Go
-- (stemming lives in internal/searchquery); see DB.backfillNormIndex for
-- existing rows.
CREATE VIRTUAL TABLE IF NOT EXISTS fts_norm USING fts5(norm);
