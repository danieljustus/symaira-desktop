-- Append-only audit trail for sensitive mutations (erase, backup,
-- restore). detail must never contain a contact-point value or key
-- material -- only counts and non-sensitive identifiers, enforced at the
-- service layer (see internal/service/security).
CREATE TABLE audit_events (
	id          TEXT PRIMARY KEY,
	occurred_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
	operation   TEXT NOT NULL,
	actor       TEXT NOT NULL,
	entity_type TEXT,
	entity_id   TEXT,
	detail      TEXT
);
CREATE INDEX audit_events_occurred_at_idx ON audit_events(occurred_at, id);
