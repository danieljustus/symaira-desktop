// Package memorylink defines the pure domain type for a Relate-owned link
// from a contact/organization to a confirmed SymMemory entity id. See
// internal/integration/symmemory for the runtime discovery of SymMemory
// itself, and docs/integrations/SYMMEMORY.md for the full design.
package memorylink

import "time"

// Link is one confirmed, reviewed mapping from a person or organization
// to a SymMemory entity. Exactly one of PersonID/OrganizationID is set.
// Relate stores nothing about the linked entity beyond its opaque id and
// type — no name, no memory content — matching the privacy boundary in
// docs/PRIVACY.md.
type Link struct {
	ID               string
	PersonID         string
	OrganizationID   string
	MemoryEntityID   string
	MemoryEntityType string
	LinkedBy         string
	CreatedAt        time.Time
}

// LinkInput creates a Link. Exactly one of PersonID/OrganizationID must be
// set by the caller (the service methods take the entity explicitly
// instead of encoding it here, mirroring internal/domain/relationship).
type LinkInput struct {
	MemoryEntityID   string
	MemoryEntityType string
	LinkedBy         string
}
