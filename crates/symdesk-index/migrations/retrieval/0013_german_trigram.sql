-- Trigram substring index for chunk search (#673): makes terms findable
-- anywhere inside a token, so parts of German compounds match directly.
CREATE VIRTUAL TABLE IF NOT EXISTS chunks_tri USING fts5(content, tokenize='trigram remove_diacritics 2');

INSERT INTO chunks_tri(rowid, content) SELECT id, content FROM chunks;

CREATE TRIGGER IF NOT EXISTS chunks_tri_ai AFTER INSERT ON chunks BEGIN
    INSERT INTO chunks_tri(rowid, content) VALUES (new.id, new.content);
END;

CREATE TRIGGER IF NOT EXISTS chunks_tri_ad AFTER DELETE ON chunks BEGIN
    DELETE FROM chunks_tri WHERE rowid = old.id;
END;

CREATE TRIGGER IF NOT EXISTS chunks_tri_au AFTER UPDATE OF content ON chunks BEGIN
    DELETE FROM chunks_tri WHERE rowid = old.id;
    INSERT INTO chunks_tri(rowid, content) VALUES (new.id, new.content);
END;
