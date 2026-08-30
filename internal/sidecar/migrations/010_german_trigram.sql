-- Trigram substring index (#673): makes terms findable anywhere inside a
-- token, so the parts of German compounds match without a splitting dictionary.
CREATE VIRTUAL TABLE IF NOT EXISTS fts_tri USING fts5(body, tokenize='trigram remove_diacritics 2');

INSERT INTO fts_tri(rowid, body) SELECT rowid, body FROM fts_search;
