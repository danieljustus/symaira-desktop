package security

import "time"

// AuditEvent is an append-only record of a sensitive mutation (erase,
// backup, restore). Detail must never contain a contact-point value or
// key material — only counts and non-sensitive identifiers.
type AuditEvent struct {
	ID         string
	OccurredAt time.Time
	Operation  string
	Actor      string
	EntityType string // "person" | "organization" | ""
	EntityID   string // opaque id, empty when not entity-scoped
	Detail     string
}

// EraseSummary reports what an erase removed, without naming any of it.
type EraseSummary struct {
	EntityType           string
	EntityID             string
	ContactPointsRemoved int
	AliasesRemoved       int
	MembershipsRemoved   int
	RelationshipsRemoved int
	InteractionsRemoved  int
	FollowUpsRemoved     int
}
