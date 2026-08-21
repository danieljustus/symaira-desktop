package relationship

import (
	"context"
	"testing"
	"time"

	contactdomain "github.com/danieljustus/symaira-relate/internal/domain/contact"
	"github.com/danieljustus/symaira-relate/internal/domain/relationship"
	"github.com/danieljustus/symaira-relate/internal/errs"
	contactsvc "github.com/danieljustus/symaira-relate/internal/service/contact"
	"github.com/danieljustus/symaira-relate/internal/storage/sqlite"
)

// testFixture wires both the contact and relationship services against the
// same in-memory database, since relationships/interactions/follow-ups
// reference persons and organizations created via the contact service.
type testFixture struct {
	contacts      *contactsvc.Service
	relationships *Service
}

func newFixture(t *testing.T) *testFixture {
	t.Helper()
	db, err := sqlite.OpenMemory(context.Background())
	if err != nil {
		t.Fatalf("OpenMemory() error = %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return &testFixture{contacts: contactsvc.New(db), relationships: New(db)}
}

func (f *testFixture) person(t *testing.T, name string) string {
	t.Helper()
	p, err := f.contacts.CreatePerson(context.Background(), contactdomain.PersonInput{DisplayName: name})
	if err != nil {
		t.Fatalf("CreatePerson(%s) error = %v", name, err)
	}
	return p.ID
}

func (f *testFixture) org(t *testing.T, name string) string {
	t.Helper()
	o, err := f.contacts.CreateOrganization(context.Background(), contactdomain.OrganizationInput{Name: name})
	if err != nil {
		t.Fatalf("CreateOrganization(%s) error = %v", name, err)
	}
	return o.ID
}

func TestAddRelationship_PersonToPersonWithTypeAndTimestamps(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	alice := f.person(t, "Alice")
	bob := f.person(t, "Bob")

	rel, err := f.relationships.AddRelationship(ctx, relationship.RelationshipInput{
		FromPersonID: alice, ToPersonID: bob, Type: "colleague",
	})
	if err != nil {
		t.Fatalf("AddRelationship() error = %v", err)
	}
	if rel.Type != "colleague" || rel.ToPersonID != bob {
		t.Errorf("rel = %+v, want type=colleague to=%s", rel, bob)
	}
	if rel.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero")
	}
}

func TestAddRelationship_PersonToOrganization(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	alice := f.person(t, "Alice")
	acme := f.org(t, "Acme")

	rel, err := f.relationships.AddRelationship(ctx, relationship.RelationshipInput{
		FromPersonID: alice, ToOrganizationID: acme, Type: "vendor-contact",
	})
	if err != nil {
		t.Fatalf("AddRelationship() error = %v", err)
	}
	if rel.ToOrganizationID != acme || rel.ToPersonID != "" {
		t.Errorf("rel = %+v, want to-organization=%s and empty to-person", rel, acme)
	}
}

func TestAddRelationship_RetryDoesNotDuplicate(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	alice := f.person(t, "Alice")
	bob := f.person(t, "Bob")

	in := relationship.RelationshipInput{FromPersonID: alice, ToPersonID: bob, Type: "friend"}
	first, err := f.relationships.AddRelationship(ctx, in)
	if err != nil {
		t.Fatalf("first AddRelationship() error = %v", err)
	}
	second, err := f.relationships.AddRelationship(ctx, in)
	if err != nil {
		t.Fatalf("retried AddRelationship() error = %v", err)
	}
	if first.ID != second.ID {
		t.Errorf("retry created a new row: first=%s second=%s", first.ID, second.ID)
	}

	list, err := f.relationships.ListOutgoingFromPerson(ctx, alice)
	if err != nil {
		t.Fatalf("ListOutgoingFromPerson() error = %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("len(list) = %d, want 1 (no duplicate row)", len(list))
	}
}

func TestAddRelationship_RejectsMissingOrAmbiguousTarget(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	alice := f.person(t, "Alice")
	bob := f.person(t, "Bob")
	acme := f.org(t, "Acme")

	if _, err := f.relationships.AddRelationship(ctx, relationship.RelationshipInput{FromPersonID: alice, Type: "x"}); errs.KindOf(err) != errs.KindInvalid {
		t.Errorf("no target: error kind = %v, want invalid", errs.KindOf(err))
	}
	if _, err := f.relationships.AddRelationship(ctx, relationship.RelationshipInput{FromPersonID: alice, ToPersonID: bob, ToOrganizationID: acme, Type: "x"}); errs.KindOf(err) != errs.KindInvalid {
		t.Errorf("both targets: error kind = %v, want invalid", errs.KindOf(err))
	}
}

func TestAddRelationship_RejectsSelfReference(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	alice := f.person(t, "Alice")

	_, err := f.relationships.AddRelationship(ctx, relationship.RelationshipInput{FromPersonID: alice, ToPersonID: alice, Type: "self"})
	if errs.KindOf(err) != errs.KindInvalid {
		t.Fatalf("self-reference error kind = %v, want invalid (err=%v)", errs.KindOf(err), err)
	}
}

func TestListIncoming_FindsWhoKnowsWhom(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	alice := f.person(t, "Alice")
	bob := f.person(t, "Bob")

	if _, err := f.relationships.AddRelationship(ctx, relationship.RelationshipInput{FromPersonID: alice, ToPersonID: bob, Type: "manager"}); err != nil {
		t.Fatalf("AddRelationship() error = %v", err)
	}

	incoming, err := f.relationships.ListIncomingToPerson(ctx, bob)
	if err != nil {
		t.Fatalf("ListIncomingToPerson() error = %v", err)
	}
	if len(incoming) != 1 || incoming[0].FromPersonID != alice {
		t.Fatalf("incoming = %+v, want one relationship from %s", incoming, alice)
	}
}

func TestInteraction_ExternalReferenceRequiresRef(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	alice := f.person(t, "Alice")

	_, err := f.relationships.AddPersonInteraction(ctx, alice, relationship.InteractionInput{
		Kind: relationship.InteractionExternalReference, Summary: "shared meeting notes",
	})
	if errs.KindOf(err) != errs.KindInvalid {
		t.Fatalf("missing external_ref: error kind = %v, want invalid (err=%v)", errs.KindOf(err), err)
	}

	i, err := f.relationships.AddPersonInteraction(ctx, alice, relationship.InteractionInput{
		Kind: relationship.InteractionExternalReference, Summary: "shared meeting notes", ExternalRef: "symmeet:abc123",
	})
	if err != nil {
		t.Fatalf("AddPersonInteraction() with ref error = %v", err)
	}
	if i.ExternalRef != "symmeet:abc123" {
		t.Errorf("ExternalRef = %q, want symmeet:abc123", i.ExternalRef)
	}
}

func TestInteraction_DoesNotCopyArtifactContent(t *testing.T) {
	// The domain type only has a Summary + ExternalRef — there is no
	// transcript/attachment field to accidentally populate.
	ctx := context.Background()
	f := newFixture(t)
	alice := f.person(t, "Alice")

	i, err := f.relationships.AddPersonInteraction(ctx, alice, relationship.InteractionInput{
		Kind: relationship.InteractionMeeting, Summary: "Quarterly check-in",
	})
	if err != nil {
		t.Fatalf("AddPersonInteraction() error = %v", err)
	}
	if i.ExternalRef != "" {
		t.Errorf("ExternalRef = %q, want empty for a plain meeting note", i.ExternalRef)
	}
}

func TestLastPersonInteraction(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	alice := f.person(t, "Alice")

	if last, err := f.relationships.LastPersonInteraction(ctx, alice); err != nil || last != nil {
		t.Fatalf("LastPersonInteraction() with no interactions = %+v, err = %v", last, err)
	}

	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if _, err := f.relationships.AddPersonInteraction(ctx, alice, relationship.InteractionInput{Kind: relationship.InteractionNote, Summary: "old note", OccurredAt: older}); err != nil {
		t.Fatalf("AddPersonInteraction(older) error = %v", err)
	}
	if _, err := f.relationships.AddPersonInteraction(ctx, alice, relationship.InteractionInput{Kind: relationship.InteractionNote, Summary: "new note", OccurredAt: newer}); err != nil {
		t.Fatalf("AddPersonInteraction(newer) error = %v", err)
	}

	last, err := f.relationships.LastPersonInteraction(ctx, alice)
	if err != nil {
		t.Fatalf("LastPersonInteraction() error = %v", err)
	}
	if last == nil || last.Summary != "new note" {
		t.Fatalf("last = %+v, want the newer interaction", last)
	}
}

func TestFollowUp_CompleteAndCancelAreTerminal(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	alice := f.person(t, "Alice")

	fu, err := f.relationships.AddPersonFollowUp(ctx, alice, relationship.FollowUpInput{DueAt: time.Now().Add(24 * time.Hour)})
	if err != nil {
		t.Fatalf("AddPersonFollowUp() error = %v", err)
	}

	completed, err := f.relationships.CompleteFollowUp(ctx, fu.ID)
	if err != nil {
		t.Fatalf("CompleteFollowUp() error = %v", err)
	}
	if completed.Status != relationship.FollowUpCompleted || completed.CompletedAt == nil {
		t.Fatalf("completed = %+v, want status=completed with CompletedAt set", completed)
	}

	// Completing again must not silently succeed or reschedule anything.
	if _, err := f.relationships.CompleteFollowUp(ctx, fu.ID); errs.KindOf(err) != errs.KindConflict {
		t.Fatalf("re-complete error kind = %v, want conflict (err=%v)", errs.KindOf(err), err)
	}
	if _, err := f.relationships.CancelFollowUp(ctx, fu.ID); errs.KindOf(err) != errs.KindConflict {
		t.Fatalf("cancel-after-complete error kind = %v, want conflict (err=%v)", errs.KindOf(err), err)
	}
}

func TestFollowUp_OverdueAndUpcomingFiltering(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	alice := f.person(t, "Alice")

	past := time.Now().Add(-48 * time.Hour)
	future := time.Now().Add(48 * time.Hour)

	overdueFU, err := f.relationships.AddPersonFollowUp(ctx, alice, relationship.FollowUpInput{DueAt: past, Notes: "overdue"})
	if err != nil {
		t.Fatalf("AddPersonFollowUp(overdue) error = %v", err)
	}
	upcomingFU, err := f.relationships.AddPersonFollowUp(ctx, alice, relationship.FollowUpInput{DueAt: future, Notes: "upcoming"})
	if err != nil {
		t.Fatalf("AddPersonFollowUp(upcoming) error = %v", err)
	}
	// A completed follow-up must never show up as overdue even if its due
	// date is in the past.
	completedFU, err := f.relationships.AddPersonFollowUp(ctx, alice, relationship.FollowUpInput{DueAt: past, Notes: "done already"})
	if err != nil {
		t.Fatalf("AddPersonFollowUp(completed) error = %v", err)
	}
	if _, err := f.relationships.CompleteFollowUp(ctx, completedFU.ID); err != nil {
		t.Fatalf("CompleteFollowUp() error = %v", err)
	}

	overdue, err := f.relationships.ListPersonFollowUps(ctx, alice, FollowUpFilterOverdue)
	if err != nil {
		t.Fatalf("ListPersonFollowUps(overdue) error = %v", err)
	}
	if len(overdue) != 1 || overdue[0].ID != overdueFU.ID {
		t.Fatalf("overdue = %+v, want exactly %s", overdue, overdueFU.ID)
	}

	upcoming, err := f.relationships.ListPersonFollowUps(ctx, alice, FollowUpFilterUpcoming)
	if err != nil {
		t.Fatalf("ListPersonFollowUps(upcoming) error = %v", err)
	}
	if len(upcoming) != 1 || upcoming[0].ID != upcomingFU.ID {
		t.Fatalf("upcoming = %+v, want exactly %s", upcoming, upcomingFU.ID)
	}

	all, err := f.relationships.ListPersonFollowUps(ctx, alice, FollowUpFilterAll)
	if err != nil {
		t.Fatalf("ListPersonFollowUps(all) error = %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("len(all) = %d, want 3", len(all))
	}
}

func TestPersonTimeline_CombinesAndOrdersDeterministically(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	alice := f.person(t, "Alice")

	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	if _, err := f.relationships.AddPersonInteraction(ctx, alice, relationship.InteractionInput{Kind: relationship.InteractionCall, Summary: "first call", OccurredAt: t1}); err != nil {
		t.Fatalf("AddPersonInteraction() error = %v", err)
	}
	if _, err := f.relationships.AddPersonFollowUp(ctx, alice, relationship.FollowUpInput{DueAt: t2, Notes: "mid follow-up"}); err != nil {
		t.Fatalf("AddPersonFollowUp() error = %v", err)
	}
	if _, err := f.relationships.AddPersonInteraction(ctx, alice, relationship.InteractionInput{Kind: relationship.InteractionEmail, Summary: "latest email", OccurredAt: t3}); err != nil {
		t.Fatalf("AddPersonInteraction() error = %v", err)
	}

	timeline, err := f.relationships.PersonTimeline(ctx, alice)
	if err != nil {
		t.Fatalf("PersonTimeline() error = %v", err)
	}
	if len(timeline) != 3 {
		t.Fatalf("len(timeline) = %d, want 3", len(timeline))
	}
	// Most recent first.
	if !timeline[0].At.Equal(t3) || !timeline[1].At.Equal(t2) || !timeline[2].At.Equal(t1) {
		t.Fatalf("timeline order = [%v, %v, %v], want [%v, %v, %v]", timeline[0].At, timeline[1].At, timeline[2].At, t3, t2, t1)
	}

	// Re-running must produce the same order (deterministic).
	timeline2, err := f.relationships.PersonTimeline(ctx, alice)
	if err != nil {
		t.Fatalf("second PersonTimeline() error = %v", err)
	}
	for i := range timeline {
		if !timeline[i].At.Equal(timeline2[i].At) || timeline[i].Kind != timeline2[i].Kind {
			t.Fatalf("timeline not deterministic at index %d: %+v vs %+v", i, timeline[i], timeline2[i])
		}
	}
}

func TestFollowUp_TimezoneBoundary(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	alice := f.person(t, "Alice")

	// 23:30 in UTC+9 is the previous day in UTC — due_at must round-trip
	// through storage without shifting which side of "now" it lands on.
	loc := time.FixedZone("UTC+9", 9*60*60)
	due := time.Date(2026, 5, 1, 23, 30, 0, 0, loc)

	fu, err := f.relationships.AddPersonFollowUp(ctx, alice, relationship.FollowUpInput{DueAt: due})
	if err != nil {
		t.Fatalf("AddPersonFollowUp() error = %v", err)
	}

	all, err := f.relationships.ListPersonFollowUps(ctx, alice, FollowUpFilterAll)
	if err != nil {
		t.Fatalf("ListPersonFollowUps() error = %v", err)
	}
	if len(all) != 1 || !all[0].DueAt.Equal(due) {
		t.Fatalf("DueAt = %v, want %v (fu id %s)", all[0].DueAt, due, fu.ID)
	}
}
