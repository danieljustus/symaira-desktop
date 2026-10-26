-- memory_links is a Relate-owned mapping from a contact/organization to a
-- confirmed SymMemory entity id (see docs/integrations/SYMMEMORY.md and
-- internal/integration/symmemory). Relate never stores anything from the
-- linked entity here beyond its opaque id -- no names, no memory content --
-- and linking is always an explicit, reviewed action, never automatic.
CREATE TABLE memory_links (
	id                TEXT PRIMARY KEY,
	person_id         TEXT REFERENCES persons(id) ON DELETE CASCADE,
	organization_id   TEXT REFERENCES organizations(id) ON DELETE CASCADE,
	memory_entity_id  TEXT NOT NULL,
	memory_entity_type TEXT NOT NULL DEFAULT '',
	linked_by         TEXT NOT NULL,
	created_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
	CHECK ((person_id IS NULL) != (organization_id IS NULL))
);
CREATE UNIQUE INDEX memory_links_person_idx ON memory_links(person_id) WHERE person_id IS NOT NULL;
CREATE UNIQUE INDEX memory_links_organization_idx ON memory_links(organization_id) WHERE organization_id IS NOT NULL;
