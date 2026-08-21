// Package relationship defines the pure domain types for directed
// relationships, the interaction timeline, and follow-up reminders.
package relationship

import "time"

// Relationship is a directed link from a person to another person or an
// organization (see Relationship.ToPersonID / ToOrganizationID — exactly
// one is set).
type Relationship struct {
	ID               string
	FromPersonID     string
	ToPersonID       string // empty when the target is an organization
	ToOrganizationID string // empty when the target is a person
	Type             string
	ValidFrom        *time.Time
	ValidTo          *time.Time
	Notes            string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// RelationshipInput creates a Relationship. Exactly one of ToPersonID /
// ToOrganizationID must be set.
type RelationshipInput struct {
	FromPersonID     string
	ToPersonID       string
	ToOrganizationID string
	Type             string
	ValidFrom        *time.Time
	ValidTo          *time.Time
	Notes            string
}

// InteractionKind enumerates the ways an interaction was recorded.
type InteractionKind string

const (
	InteractionNote              InteractionKind = "note"
	InteractionCall              InteractionKind = "call"
	InteractionEmail             InteractionKind = "email"
	InteractionMeeting           InteractionKind = "meeting"
	InteractionExternalReference InteractionKind = "external_reference"
)

func (k InteractionKind) Valid() bool {
	switch k {
	case InteractionNote, InteractionCall, InteractionEmail, InteractionMeeting, InteractionExternalReference:
		return true
	}
	return false
}

// Interaction is an immutable record in a contact's timeline. ExternalRef
// points at an artifact owned elsewhere (a meeting id, a message id) —
// interactions never copy transcripts or attachments.
type Interaction struct {
	ID          string
	Kind        InteractionKind
	OccurredAt  time.Time
	Summary     string
	ExternalRef string
	CreatedAt   time.Time
}

// InteractionInput records a new interaction against a person or
// organization (set exactly one of PersonID / OrganizationID at the call
// site — the service methods take the entity explicitly instead of
// encoding it here).
type InteractionInput struct {
	Kind        InteractionKind
	OccurredAt  time.Time
	Summary     string
	ExternalRef string
}

// FollowUpStatus is the lifecycle state of a follow-up.
type FollowUpStatus string

const (
	FollowUpOpen      FollowUpStatus = "open"
	FollowUpCompleted FollowUpStatus = "completed"
	FollowUpCancelled FollowUpStatus = "cancelled"
)

// FollowUp is a due-dated reminder tied to a person or organization.
// DueAt is immutable once created — completing or cancelling changes
// Status, nothing reschedules a follow-up silently.
type FollowUp struct {
	ID          string
	DueAt       time.Time
	Notes       string
	Status      FollowUpStatus
	CompletedAt *time.Time
	CancelledAt *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// FollowUpInput creates a new open follow-up.
type FollowUpInput struct {
	DueAt time.Time
	Notes string
}

// TimelineEntryKind distinguishes the two record types combined in a
// contact timeline.
type TimelineEntryKind string

const (
	TimelineInteraction TimelineEntryKind = "interaction"
	TimelineFollowUp    TimelineEntryKind = "follow_up"
)

// TimelineEntry unifies an Interaction and a FollowUp into one
// chronologically ordered feed. At returns the entry's ordering timestamp
// (Interaction.OccurredAt or FollowUp.DueAt).
type TimelineEntry struct {
	Kind        TimelineEntryKind
	At          time.Time
	Interaction *Interaction
	FollowUp    *FollowUp
}
