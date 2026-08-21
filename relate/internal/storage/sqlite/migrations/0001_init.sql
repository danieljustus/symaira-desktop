-- Foundation schema: a small key/value settings table used for
-- bookkeeping that is not tied to any single domain (e.g. profile id,
-- first-run timestamp). Domain schemas are added by later migrations.
CREATE TABLE settings (
	key        TEXT PRIMARY KEY,
	value      TEXT NOT NULL,
	updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
