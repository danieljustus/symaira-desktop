-- Beta contact model: persons and organizations are mastered separately,
-- with polymorphic-by-nullable-FK child tables (aliases, tags,
-- classifications, contact points) so both entity kinds share the same
-- vocabulary without a shared "entities" supertable.

CREATE TABLE persons (
	id           TEXT PRIMARY KEY,
	display_name TEXT NOT NULL,
	given_name   TEXT,
	family_name  TEXT,
	notes        TEXT,
	source       TEXT,
	source_ref   TEXT,
	created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
	updated_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX persons_display_name_idx ON persons(display_name COLLATE NOCASE);
CREATE INDEX persons_created_at_idx ON persons(created_at, id);

CREATE TABLE organizations (
	id         TEXT PRIMARY KEY,
	name       TEXT NOT NULL,
	notes      TEXT,
	source     TEXT,
	source_ref TEXT,
	created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
	updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX organizations_name_idx ON organizations(name COLLATE NOCASE);
CREATE INDEX organizations_created_at_idx ON organizations(created_at, id);

CREATE TABLE aliases (
	id              TEXT PRIMARY KEY,
	person_id       TEXT REFERENCES persons(id) ON DELETE CASCADE,
	organization_id TEXT REFERENCES organizations(id) ON DELETE CASCADE,
	alias           TEXT NOT NULL,
	created_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
	CHECK ((person_id IS NOT NULL) <> (organization_id IS NOT NULL))
);
CREATE INDEX aliases_person_idx ON aliases(person_id);
CREATE INDEX aliases_organization_idx ON aliases(organization_id);

CREATE TABLE tags (
	id   TEXT PRIMARY KEY,
	name TEXT NOT NULL UNIQUE
);

CREATE TABLE entity_tags (
	id              TEXT PRIMARY KEY,
	tag_id          TEXT NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
	person_id       TEXT REFERENCES persons(id) ON DELETE CASCADE,
	organization_id TEXT REFERENCES organizations(id) ON DELETE CASCADE,
	CHECK ((person_id IS NOT NULL) <> (organization_id IS NOT NULL))
);
CREATE UNIQUE INDEX entity_tags_person_unique ON entity_tags(tag_id, person_id) WHERE person_id IS NOT NULL;
CREATE UNIQUE INDEX entity_tags_org_unique ON entity_tags(tag_id, organization_id) WHERE organization_id IS NOT NULL;

-- Classification is a controlled vocabulary (unlike free-form tags) so a
-- contact's personal/business/customer/lead/partner status can drive
-- workflow filters without relying on tag-name spelling.
CREATE TABLE entity_classifications (
	id              TEXT PRIMARY KEY,
	person_id       TEXT REFERENCES persons(id) ON DELETE CASCADE,
	organization_id TEXT REFERENCES organizations(id) ON DELETE CASCADE,
	classification  TEXT NOT NULL CHECK (classification IN ('personal','business','customer','lead','partner')),
	created_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
	CHECK ((person_id IS NOT NULL) <> (organization_id IS NOT NULL))
);
CREATE UNIQUE INDEX entity_classifications_person_unique ON entity_classifications(classification, person_id) WHERE person_id IS NOT NULL;
CREATE UNIQUE INDEX entity_classifications_org_unique ON entity_classifications(classification, organization_id) WHERE organization_id IS NOT NULL;

CREATE TABLE contact_points (
	id               TEXT PRIMARY KEY,
	person_id        TEXT REFERENCES persons(id) ON DELETE CASCADE,
	organization_id  TEXT REFERENCES organizations(id) ON DELETE CASCADE,
	kind             TEXT NOT NULL CHECK (kind IN ('email','phone','address','url','handle')),
	raw_value        TEXT NOT NULL,
	normalized_value TEXT NOT NULL,
	label            TEXT,
	is_preferred     INTEGER NOT NULL DEFAULT 0 CHECK (is_preferred IN (0,1)),
	is_verified      INTEGER NOT NULL DEFAULT 0 CHECK (is_verified IN (0,1)),
	created_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
	updated_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
	CHECK ((person_id IS NOT NULL) <> (organization_id IS NOT NULL))
);
-- Prevents obvious duplicate entries (same kind + normalized value) on the
-- same entity while the raw_value column preserves what the user typed.
CREATE UNIQUE INDEX contact_points_person_unique ON contact_points(person_id, kind, normalized_value) WHERE person_id IS NOT NULL;
CREATE UNIQUE INDEX contact_points_org_unique ON contact_points(organization_id, kind, normalized_value) WHERE organization_id IS NOT NULL;
CREATE INDEX contact_points_normalized_idx ON contact_points(kind, normalized_value);

CREATE TABLE organization_memberships (
	id              TEXT PRIMARY KEY,
	person_id       TEXT NOT NULL REFERENCES persons(id) ON DELETE CASCADE,
	organization_id TEXT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
	role            TEXT,
	title           TEXT,
	valid_from      TEXT,
	valid_to        TEXT,
	created_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
	updated_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
	CHECK (valid_to IS NULL OR valid_from IS NULL OR valid_to >= valid_from)
);
CREATE INDEX organization_memberships_person_idx ON organization_memberships(person_id);
CREATE INDEX organization_memberships_org_idx ON organization_memberships(organization_id);
