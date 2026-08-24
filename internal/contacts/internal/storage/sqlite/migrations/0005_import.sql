-- import_sources makes re-import idempotent: a row already linked to a
-- person via the same (source_kind, source_ref) is recognized instead of
-- creating a duplicate person on every re-run of the same source.
CREATE TABLE import_sources (
	id          TEXT PRIMARY KEY,
	source_kind TEXT NOT NULL CHECK (source_kind IN ('vcard','csv')),
	source_ref  TEXT NOT NULL,
	person_id   TEXT NOT NULL REFERENCES persons(id) ON DELETE CASCADE,
	imported_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
	UNIQUE (source_kind, source_ref)
);
CREATE INDEX import_sources_person_idx ON import_sources(person_id);

-- import_runs is a per-apply report: how many rows landed in each
-- outcome bucket, so "re-importing the same source" has something to show
-- besides silence.
CREATE TABLE import_runs (
	id          TEXT PRIMARY KEY,
	source_kind TEXT NOT NULL CHECK (source_kind IN ('vcard','csv')),
	started_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
	created     INTEGER NOT NULL DEFAULT 0,
	merged      INTEGER NOT NULL DEFAULT 0,
	skipped     INTEGER NOT NULL DEFAULT 0,
	failed      INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX import_runs_started_at_idx ON import_runs(started_at, id);
