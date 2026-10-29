-- Directed relationships (always rooted at a person), an immutable
-- interaction timeline, and follow-up reminders. Interactions and
-- follow-ups reuse the person/organization polymorphic-by-nullable-FK
-- pattern from 0002_contacts.sql.

CREATE TABLE relationships (
	id                 TEXT PRIMARY KEY,
	from_person_id     TEXT NOT NULL REFERENCES persons(id) ON DELETE CASCADE,
	to_person_id       TEXT REFERENCES persons(id) ON DELETE CASCADE,
	to_organization_id TEXT REFERENCES organizations(id) ON DELETE CASCADE,
	relationship_type  TEXT NOT NULL,
	valid_from         TEXT,
	valid_to           TEXT,
	notes              TEXT,
	created_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
	updated_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
	CHECK ((to_person_id IS NOT NULL) <> (to_organization_id IS NOT NULL)),
	CHECK (to_person_id IS NULL OR to_person_id <> from_person_id),
	CHECK (valid_to IS NULL OR valid_from IS NULL OR valid_to >= valid_from)
);
-- Retrying the same relationship write (same from/to/type/valid_from) must
-- not create a duplicate row.
CREATE UNIQUE INDEX relationships_person_target_unique
	ON relationships(from_person_id, to_person_id, relationship_type, COALESCE(valid_from, ''))
	WHERE to_person_id IS NOT NULL;
CREATE UNIQUE INDEX relationships_org_target_unique
	ON relationships(from_person_id, to_organization_id, relationship_type, COALESCE(valid_from, ''))
	WHERE to_organization_id IS NOT NULL;
CREATE INDEX relationships_from_idx ON relationships(from_person_id);
CREATE INDEX relationships_to_person_idx ON relationships(to_person_id);
CREATE INDEX relationships_to_org_idx ON relationships(to_organization_id);

-- Interactions are append-only: no updated_at, no update path in the
-- service layer. occurred_at is caller-supplied (not always "now" — a
-- call from yesterday can be logged today) while created_at is when the
-- record was written, for audit purposes.
CREATE TABLE interactions (
	id              TEXT PRIMARY KEY,
	person_id       TEXT REFERENCES persons(id) ON DELETE CASCADE,
	organization_id TEXT REFERENCES organizations(id) ON DELETE CASCADE,
	kind            TEXT NOT NULL CHECK (kind IN ('note','call','email','meeting','external_reference')),
	occurred_at     TEXT NOT NULL,
	summary         TEXT NOT NULL,
	external_ref    TEXT,
	created_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
	CHECK ((person_id IS NOT NULL) <> (organization_id IS NOT NULL)),
	CHECK (kind <> 'external_reference' OR (external_ref IS NOT NULL AND external_ref <> ''))
);
CREATE INDEX interactions_person_idx ON interactions(person_id, occurred_at, id);
CREATE INDEX interactions_org_idx ON interactions(organization_id, occurred_at, id);

-- due_at is immutable once set (no reschedule path) so a follow-up is
-- never silently moved; only its status may change.
CREATE TABLE follow_ups (
	id              TEXT PRIMARY KEY,
	person_id       TEXT REFERENCES persons(id) ON DELETE CASCADE,
	organization_id TEXT REFERENCES organizations(id) ON DELETE CASCADE,
	due_at          TEXT NOT NULL,
	notes           TEXT,
	status          TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','completed','cancelled')),
	completed_at    TEXT,
	cancelled_at    TEXT,
	created_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
	updated_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
	CHECK ((person_id IS NOT NULL) <> (organization_id IS NOT NULL))
);
CREATE INDEX follow_ups_person_idx ON follow_ups(person_id, due_at, id);
CREATE INDEX follow_ups_org_idx ON follow_ups(organization_id, due_at, id);
CREATE INDEX follow_ups_status_due_idx ON follow_ups(status, due_at);
