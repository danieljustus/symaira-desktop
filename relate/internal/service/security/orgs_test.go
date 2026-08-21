package security

import (
	"context"
	"strings"
	"testing"
	"time"

	contactdomain "github.com/danieljustus/symaira-relate/internal/domain/contact"
	relationshipdomain "github.com/danieljustus/symaira-relate/internal/domain/relationship"
	"github.com/danieljustus/symaira-relate/internal/errs"
	contactsvc "github.com/danieljustus/symaira-relate/internal/service/contact"
	relationshipsvc "github.com/danieljustus/symaira-relate/internal/service/relationship"
	"github.com/danieljustus/symaira-relate/internal/storage/sqlite"
)

// orgEraseFixture wires contact, relationship and security services
// against one in-memory database so an organization can carry the full
// child-data surface (contact points, aliases, memberships, relationships,
// interactions, follow-ups) before being erased.
type orgEraseFixture struct {
	contacts      *contactsvc.Service
	relationships *relationshipsvc.Service
	security      *Service
}

func newOrgEraseFixture(t *testing.T) *orgEraseFixture {
	t.Helper()
	db, err := sqlite.OpenMemory(context.Background())
	if err != nil {
		t.Fatalf("OpenMemory() error = %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return &orgEraseFixture{
		contacts:      contactsvc.New(db),
		relationships: relationshipsvc.New(db),
		security:      New(db),
	}
}

func (f *orgEraseFixture) person(t *testing.T, name string) string {
	t.Helper()
	p, err := f.contacts.CreatePerson(context.Background(), contactdomain.PersonInput{DisplayName: name})
	if err != nil {
		t.Fatalf("CreatePerson() error = %v", err)
	}
	return p.ID
}

func (f *orgEraseFixture) org(t *testing.T, name string) string {
	t.Helper()
	o, err := f.contacts.CreateOrganization(context.Background(), contactdomain.OrganizationInput{Name: name})
	if err != nil {
		t.Fatalf("CreateOrganization() error = %v", err)
	}
	return o.ID
}

func TestEraseOrganization_RemovesLinkedDataAndRecordsAudit(t *testing.T) {
	ctx := context.Background()
	f := newOrgEraseFixture(t)

	orgID := f.org(t, "Acme")
	alice := f.person(t, "Alice")

	// Contact point, alias, classification and tag on the org.
	if _, err := f.contacts.AddOrganizationContactPoint(ctx, orgID, contactdomain.ContactPointInput{Kind: contactdomain.ContactPointEmail, RawValue: "acme@example.com"}); err != nil {
		t.Fatalf("AddOrganizationContactPoint() error = %v", err)
	}
	if err := f.contacts.AddOrganizationAlias(ctx, orgID, "ACME Inc"); err != nil {
		t.Fatalf("AddOrganizationAlias() error = %v", err)
	}
	if err := f.contacts.SetOrganizationClassification(ctx, orgID, contactdomain.ClassificationBusiness); err != nil {
		t.Fatalf("SetOrganizationClassification() error = %v", err)
	}
	// Membership, relationship, interaction and follow-up.
	if _, err := f.contacts.AddMembership(ctx, alice, orgID, contactdomain.MembershipInput{Role: "employee"}); err != nil {
		t.Fatalf("AddMembership() error = %v", err)
	}
	if _, err := f.relationships.AddRelationship(ctx, relationshipdomain.RelationshipInput{FromPersonID: alice, ToOrganizationID: orgID, Type: "vendor-contact"}); err != nil {
		t.Fatalf("AddRelationship() error = %v", err)
	}
	if _, err := f.relationships.AddOrganizationInteraction(ctx, orgID, relationshipdomain.InteractionInput{Kind: relationshipdomain.InteractionNote, OccurredAt: time.Now(), Summary: "intro"}); err != nil {
		t.Fatalf("AddOrganizationInteraction() error = %v", err)
	}
	if _, err := f.relationships.AddOrganizationFollowUp(ctx, orgID, relationshipdomain.FollowUpInput{DueAt: time.Now().Add(24 * time.Hour), Notes: "follow up"}); err != nil {
		t.Fatalf("AddOrganizationFollowUp() error = %v", err)
	}

	summary, err := f.security.EraseOrganization(ctx, orgID)
	if err != nil {
		t.Fatalf("EraseOrganization() error = %v", err)
	}
	if summary.ContactPointsRemoved != 1 {
		t.Errorf("ContactPointsRemoved = %d, want 1", summary.ContactPointsRemoved)
	}
	if summary.AliasesRemoved != 1 {
		t.Errorf("AliasesRemoved = %d, want 1", summary.AliasesRemoved)
	}
	if summary.MembershipsRemoved != 1 {
		t.Errorf("MembershipsRemoved = %d, want 1", summary.MembershipsRemoved)
	}
	if summary.RelationshipsRemoved != 1 {
		t.Errorf("RelationshipsRemoved = %d, want 1", summary.RelationshipsRemoved)
	}
	if summary.InteractionsRemoved != 1 {
		t.Errorf("InteractionsRemoved = %d, want 1", summary.InteractionsRemoved)
	}
	if summary.FollowUpsRemoved != 1 {
		t.Errorf("FollowUpsRemoved = %d, want 1", summary.FollowUpsRemoved)
	}

	// The organization and every child row must be gone; the member
	// person survives.
	if _, err := f.contacts.GetOrganization(ctx, orgID); errs.KindOf(err) != errs.KindNotFound {
		t.Fatalf("GetOrganization() after erase: kind = %v, want not_found", errs.KindOf(err))
	}
	if _, err := f.contacts.GetPerson(ctx, alice); err != nil {
		t.Fatalf("person lost during organization erase: %v", err)
	}
	var orphanCount int
	for _, table := range []string{"contact_points", "aliases", "entity_tags", "entity_classifications", "organization_memberships", "interactions", "follow_ups"} {
		if err := f.security.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE organization_id = ?", orgID).Scan(&orphanCount); err != nil {
			t.Fatalf("query %s error = %v", table, err)
		}
		if orphanCount != 0 {
			t.Errorf("table %s has %d orphaned rows for erased organization", table, orphanCount)
		}
	}
	var relCount int
	if err := f.security.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM relationships WHERE to_organization_id = ?", orgID).Scan(&relCount); err != nil {
		t.Fatalf("query relationships error = %v", err)
	}
	if relCount != 0 {
		t.Errorf("relationships has %d orphaned rows for erased organization", relCount)
	}

	// Audit trail: one event, counts only — never names or emails.
	events, err := f.security.ListAuditEvents(ctx)
	if err != nil {
		t.Fatalf("ListAuditEvents() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	ev := events[0]
	if ev.Operation != "erase_contact" || ev.EntityID != orgID {
		t.Errorf("audit event = %+v, want erase_contact for %s", ev, orgID)
	}
	if !strings.Contains(ev.Detail, "contact_points=1") || !strings.Contains(ev.Detail, "memberships=1") {
		t.Errorf("audit detail missing counts: %q", ev.Detail)
	}
	if strings.Contains(ev.Detail, "Acme") || strings.Contains(ev.Detail, "acme@example.com") {
		t.Errorf("audit detail leaked contact data: %q", ev.Detail)
	}
}

func TestEraseOrganization_NotFound(t *testing.T) {
	f := newOrgEraseFixture(t)
	_, err := f.security.EraseOrganization(context.Background(), "does-not-exist")
	if errs.KindOf(err) != errs.KindNotFound {
		t.Fatalf("EraseOrganization() kind = %v, want not_found", errs.KindOf(err))
	}
}

func TestEraseOrganization_BareOrganizationRecordsAuditZeroCounts(t *testing.T) {
	ctx := context.Background()
	f := newOrgEraseFixture(t)
	orgID := f.org(t, "Empty Org")

	summary, err := f.security.EraseOrganization(ctx, orgID)
	if err != nil {
		t.Fatalf("EraseOrganization() error = %v", err)
	}
	if summary.ContactPointsRemoved != 0 || summary.AliasesRemoved != 0 || summary.MembershipsRemoved != 0 ||
		summary.RelationshipsRemoved != 0 || summary.InteractionsRemoved != 0 || summary.FollowUpsRemoved != 0 {
		t.Errorf("bare org summary = %+v, want all-zero counts", summary)
	}

	events, err := f.security.ListAuditEvents(ctx)
	if err != nil {
		t.Fatalf("ListAuditEvents() error = %v", err)
	}
	if len(events) != 1 || events[0].EntityID != orgID {
		t.Fatalf("audit events = %+v, want exactly one for the erased org", events)
	}
}
