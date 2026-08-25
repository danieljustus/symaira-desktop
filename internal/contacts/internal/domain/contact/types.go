// Package contact defines the pure domain types for the beta contact,
// organization and contact-point model. Nothing here touches SQL.
package contact

import "time"

// Classification is a controlled-vocabulary tag distinguishing how a
// contact participates in the user's personal and business life. Unlike
// free-form tags, a set of contacts filtered by classification must stay
// meaningful without the user having invented the vocabulary themselves.
type Classification string

const (
	ClassificationPersonal Classification = "personal"
	ClassificationBusiness Classification = "business"
	ClassificationCustomer Classification = "customer"
	ClassificationLead     Classification = "lead"
	ClassificationPartner  Classification = "partner"
)

func (c Classification) Valid() bool {
	switch c {
	case ClassificationPersonal, ClassificationBusiness, ClassificationCustomer, ClassificationLead, ClassificationPartner:
		return true
	}
	return false
}

// ContactPointKind enumerates the typed ways a person or organization can
// be reached.
type ContactPointKind string

const (
	ContactPointEmail   ContactPointKind = "email"
	ContactPointPhone   ContactPointKind = "phone"
	ContactPointAddress ContactPointKind = "address"
	ContactPointURL     ContactPointKind = "url"
	ContactPointHandle  ContactPointKind = "handle"
)

func (k ContactPointKind) Valid() bool {
	switch k {
	case ContactPointEmail, ContactPointPhone, ContactPointAddress, ContactPointURL, ContactPointHandle:
		return true
	}
	return false
}

// ContactPoint is one typed way to reach a person or organization.
type ContactPoint struct {
	ID              string
	Kind            ContactPointKind
	RawValue        string
	NormalizedValue string
	Label           string
	IsPreferred     bool
	IsVerified      bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Membership links a person to an organization for a role over an
// optionally bounded period, so historical roles are preserved rather than
// overwritten.
type Membership struct {
	ID             string
	PersonID       string
	OrganizationID string
	Role           string
	Title          string
	ValidFrom      *time.Time
	ValidTo        *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Person is the authoritative record for an individual.
type Person struct {
	ID          string
	DisplayName string
	GivenName   string
	FamilyName  string
	Notes       string
	Source      string
	SourceRef   string
	CreatedAt   time.Time
	UpdatedAt   time.Time

	Aliases         []string
	Tags            []string
	Classifications []Classification
	ContactPoints   []ContactPoint
}

// Organization is the authoritative record for a company, non-profit or
// other named group.
type Organization struct {
	ID        string
	Name      string
	Notes     string
	Source    string
	SourceRef string
	CreatedAt time.Time
	UpdatedAt time.Time

	Aliases         []string
	Tags            []string
	Classifications []Classification
	ContactPoints   []ContactPoint
}

// PersonInput creates a new Person. Source/SourceRef record provenance
// (e.g. "import:vcard", the source file's UID) and are optional for
// manually entered contacts.
type PersonInput struct {
	DisplayName string
	GivenName   string
	FamilyName  string
	Notes       string
	Source      string
	SourceRef   string
}

// PersonUpdate patches a Person; nil fields are left unchanged.
type PersonUpdate struct {
	DisplayName *string
	GivenName   *string
	FamilyName  *string
	Notes       *string
}

// OrganizationInput creates a new Organization.
type OrganizationInput struct {
	Name      string
	Notes     string
	Source    string
	SourceRef string
}

// OrganizationUpdate patches an Organization; nil fields are left
// unchanged.
type OrganizationUpdate struct {
	Name  *string
	Notes *string
}

// ContactPointInput adds a contact point to a person or organization.
type ContactPointInput struct {
	Kind        ContactPointKind
	RawValue    string
	Label       string
	IsPreferred bool
	IsVerified  bool
}

// MembershipInput adds a person to an organization.
type MembershipInput struct {
	Role      string
	Title     string
	ValidFrom *time.Time
	ValidTo   *time.Time
}
