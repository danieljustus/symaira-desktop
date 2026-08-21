package relationship

import (
	"context"
	"testing"
	"time"

	"github.com/danieljustus/symaira-relate/internal/domain/relationship"
	"github.com/danieljustus/symaira-relate/internal/errs"
)

func TestAddOrganizationFollowUp_ListAndFilter(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	acme := f.org(t, "Acme")
	globex := f.org(t, "Globex")

	dueSoon := time.Now().Add(24 * time.Hour)
	dueLate := time.Now().Add(72 * time.Hour)
	if _, err := f.relationships.AddOrganizationFollowUp(ctx, acme, relationship.FollowUpInput{DueAt: dueSoon, Notes: "follow up soon"}); err != nil {
		t.Fatalf("AddOrganizationFollowUp(soon) error = %v", err)
	}
	if _, err := f.relationships.AddOrganizationFollowUp(ctx, acme, relationship.FollowUpInput{DueAt: dueLate, Notes: "follow up later"}); err != nil {
		t.Fatalf("AddOrganizationFollowUp(late) error = %v", err)
	}
	// A follow-up on another organization must not leak into this list.
	if _, err := f.relationships.AddOrganizationFollowUp(ctx, globex, relationship.FollowUpInput{DueAt: dueSoon, Notes: "globex"}); err != nil {
		t.Fatalf("AddOrganizationFollowUp(globex) error = %v", err)
	}

	all, err := f.relationships.ListOrganizationFollowUps(ctx, acme, FollowUpFilterAll)
	if err != nil {
		t.Fatalf("ListOrganizationFollowUps(all) error = %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("len(all) = %d, want 2", len(all))
	}
	if !all[0].DueAt.Before(all[1].DueAt) {
		t.Errorf("follow-ups not ordered by due_at: %v then %v", all[0].DueAt, all[1].DueAt)
	}

	open, err := f.relationships.ListOrganizationFollowUps(ctx, acme, FollowUpFilterOpen)
	if err != nil {
		t.Fatalf("ListOrganizationFollowUps(open) error = %v", err)
	}
	if len(open) != 2 {
		t.Fatalf("len(open) = %d, want 2", len(open))
	}
}

func TestAddOrganizationFollowUp_CompleteAndCancel(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	acme := f.org(t, "Acme")

	fu, err := f.relationships.AddOrganizationFollowUp(ctx, acme, relationship.FollowUpInput{DueAt: time.Now().Add(24 * time.Hour), Notes: "call back"})
	if err != nil {
		t.Fatalf("AddOrganizationFollowUp() error = %v", err)
	}

	done, err := f.relationships.CompleteFollowUp(ctx, fu.ID)
	if err != nil {
		t.Fatalf("CompleteFollowUp() error = %v", err)
	}
	if done.Status != relationship.FollowUpCompleted || done.CompletedAt == nil {
		t.Errorf("completed follow-up = %+v, want status=completed with CompletedAt", done)
	}

	// Completing again is a conflict, not a silent no-op.
	_, err = f.relationships.CompleteFollowUp(ctx, fu.ID)
	if errs.KindOf(err) != errs.KindConflict {
		t.Fatalf("second CompleteFollowUp() error kind = %v, want conflict (err=%v)", errs.KindOf(err), err)
	}

	// Open filter now excludes it.
	open, err := f.relationships.ListOrganizationFollowUps(ctx, acme, FollowUpFilterOpen)
	if err != nil {
		t.Fatalf("ListOrganizationFollowUps(open) error = %v", err)
	}
	if len(open) != 0 {
		t.Fatalf("len(open) after complete = %d, want 0", len(open))
	}

	// Cancellation path on a fresh follow-up.
	fu2, err := f.relationships.AddOrganizationFollowUp(ctx, acme, relationship.FollowUpInput{DueAt: time.Now().Add(24 * time.Hour)})
	if err != nil {
		t.Fatalf("AddOrganizationFollowUp(2) error = %v", err)
	}
	cancelled, err := f.relationships.CancelFollowUp(ctx, fu2.ID)
	if err != nil {
		t.Fatalf("CancelFollowUp() error = %v", err)
	}
	if cancelled.Status != relationship.FollowUpCancelled || cancelled.CancelledAt == nil {
		t.Errorf("cancelled follow-up = %+v, want status=cancelled with CancelledAt", cancelled)
	}
}

func TestAddOrganizationFollowUp_UnknownOrganizationRejected(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	_, err := f.relationships.AddOrganizationFollowUp(ctx, "does-not-exist", relationship.FollowUpInput{DueAt: time.Now().Add(24 * time.Hour)})
	if errs.KindOf(err) != errs.KindInvalid {
		t.Fatalf("AddOrganizationFollowUp(unknown org) error kind = %v, want invalid (err=%v)", errs.KindOf(err), err)
	}
}

func TestAddOrganizationFollowUp_RequiresDueDate(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	acme := f.org(t, "Acme")

	_, err := f.relationships.AddOrganizationFollowUp(ctx, acme, relationship.FollowUpInput{})
	if errs.KindOf(err) != errs.KindInvalid {
		t.Fatalf("AddOrganizationFollowUp(no due date) error kind = %v, want invalid (err=%v)", errs.KindOf(err), err)
	}
}

func TestOrganizationInteractions_AddListLast(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	acme := f.org(t, "Acme")
	globex := f.org(t, "Globex")

	first := time.Now().Add(-48 * time.Hour)
	second := time.Now().Add(-24 * time.Hour)
	if _, err := f.relationships.AddOrganizationInteraction(ctx, acme, relationship.InteractionInput{Kind: relationship.InteractionCall, OccurredAt: first, Summary: "intro call"}); err != nil {
		t.Fatalf("AddOrganizationInteraction(first) error = %v", err)
	}
	if _, err := f.relationships.AddOrganizationInteraction(ctx, acme, relationship.InteractionInput{Kind: relationship.InteractionEmail, OccurredAt: second, Summary: "follow-up email"}); err != nil {
		t.Fatalf("AddOrganizationInteraction(second) error = %v", err)
	}
	// Another organization's interaction must not leak.
	if _, err := f.relationships.AddOrganizationInteraction(ctx, globex, relationship.InteractionInput{Kind: relationship.InteractionNote, OccurredAt: second, Summary: "globex note"}); err != nil {
		t.Fatalf("AddOrganizationInteraction(globex) error = %v", err)
	}

	list, err := f.relationships.ListOrganizationInteractions(ctx, acme)
	if err != nil {
		t.Fatalf("ListOrganizationInteractions() error = %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len(list) = %d, want 2", len(list))
	}
	if !list[0].OccurredAt.Before(list[1].OccurredAt) {
		t.Errorf("interactions not ordered oldest-first: %v then %v", list[0].OccurredAt, list[1].OccurredAt)
	}

	last, err := f.relationships.LastOrganizationInteraction(ctx, acme)
	if err != nil {
		t.Fatalf("LastOrganizationInteraction() error = %v", err)
	}
	if last == nil || last.Summary != "follow-up email" {
		t.Fatalf("LastOrganizationInteraction() = %+v, want the most recent email", last)
	}
}

func TestLastOrganizationInteraction_None(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	acme := f.org(t, "Acme")

	last, err := f.relationships.LastOrganizationInteraction(ctx, acme)
	if err != nil {
		t.Fatalf("LastOrganizationInteraction() error = %v", err)
	}
	if last != nil {
		t.Fatalf("LastOrganizationInteraction() = %+v, want nil", last)
	}
}

func TestAddOrganizationInteraction_RejectsInvalidInput(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	acme := f.org(t, "Acme")

	// Unknown kind.
	_, err := f.relationships.AddOrganizationInteraction(ctx, acme, relationship.InteractionInput{Kind: relationship.InteractionKind("bogus"), Summary: "x"})
	if errs.KindOf(err) != errs.KindInvalid {
		t.Fatalf("unknown kind error kind = %v, want invalid (err=%v)", errs.KindOf(err), err)
	}
	// Empty summary.
	_, err = f.relationships.AddOrganizationInteraction(ctx, acme, relationship.InteractionInput{Kind: relationship.InteractionNote})
	if errs.KindOf(err) != errs.KindInvalid {
		t.Fatalf("empty summary error kind = %v, want invalid (err=%v)", errs.KindOf(err), err)
	}
	// External-reference kind requires external_ref.
	_, err = f.relationships.AddOrganizationInteraction(ctx, acme, relationship.InteractionInput{Kind: relationship.InteractionExternalReference, Summary: "ref"})
	if errs.KindOf(err) != errs.KindInvalid {
		t.Fatalf("external_reference without ref error kind = %v, want invalid (err=%v)", errs.KindOf(err), err)
	}
	// Unknown organization.
	_, err = f.relationships.AddOrganizationInteraction(ctx, "does-not-exist", relationship.InteractionInput{Kind: relationship.InteractionNote, Summary: "x"})
	if errs.KindOf(err) != errs.KindInvalid {
		t.Fatalf("unknown org error kind = %v, want invalid (err=%v)", errs.KindOf(err), err)
	}
}

func TestListIncomingToOrganization(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	alice := f.person(t, "Alice")
	acme := f.org(t, "Acme")

	if _, err := f.relationships.AddRelationship(ctx, relationship.RelationshipInput{FromPersonID: alice, ToOrganizationID: acme, Type: "vendor-contact"}); err != nil {
		t.Fatalf("AddRelationship() error = %v", err)
	}
	// A person-to-person relationship must not appear in the org's
	// incoming list.
	bob := f.person(t, "Bob")
	if _, err := f.relationships.AddRelationship(ctx, relationship.RelationshipInput{FromPersonID: bob, ToPersonID: alice, Type: "friend"}); err != nil {
		t.Fatalf("AddRelationship(person-person) error = %v", err)
	}

	incoming, err := f.relationships.ListIncomingToOrganization(ctx, acme)
	if err != nil {
		t.Fatalf("ListIncomingToOrganization() error = %v", err)
	}
	if len(incoming) != 1 {
		t.Fatalf("len(incoming) = %d, want 1", len(incoming))
	}
	if incoming[0].ToOrganizationID != acme || incoming[0].FromPersonID != alice {
		t.Errorf("incoming[0] = %+v, want from Alice to Acme", incoming[0])
	}
}

func TestOrganizationTimeline_MergesInteractionsAndFollowUpsNewestFirst(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	acme := f.org(t, "Acme")

	old := time.Now().Add(-72 * time.Hour)
	mid := time.Now().Add(-48 * time.Hour)
	recent := time.Now().Add(24 * time.Hour)

	if _, err := f.relationships.AddOrganizationInteraction(ctx, acme, relationship.InteractionInput{Kind: relationship.InteractionNote, OccurredAt: old, Summary: "old note"}); err != nil {
		t.Fatalf("AddOrganizationInteraction(old) error = %v", err)
	}
	if _, err := f.relationships.AddOrganizationFollowUp(ctx, acme, relationship.FollowUpInput{DueAt: mid, Notes: "mid follow-up"}); err != nil {
		t.Fatalf("AddOrganizationFollowUp(mid) error = %v", err)
	}
	if _, err := f.relationships.AddOrganizationInteraction(ctx, acme, relationship.InteractionInput{Kind: relationship.InteractionEmail, OccurredAt: recent, Summary: "recent email"}); err != nil {
		t.Fatalf("AddOrganizationInteraction(recent) error = %v", err)
	}

	entries, err := f.relationships.OrganizationTimeline(ctx, acme)
	if err != nil {
		t.Fatalf("OrganizationTimeline() error = %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("len(entries) = %d, want 3", len(entries))
	}
	// Newest first.
	if !entries[0].At.After(entries[1].At) || !entries[1].At.After(entries[2].At) {
		t.Errorf("timeline not newest-first: %v, %v, %v", entries[0].At, entries[1].At, entries[2].At)
	}
	if entries[0].Kind != relationship.TimelineInteraction || entries[0].Interaction == nil || entries[0].Interaction.Summary != "recent email" {
		t.Errorf("entries[0] = %+v, want recent email interaction", entries[0])
	}
	if entries[1].Kind != relationship.TimelineFollowUp || entries[1].FollowUp == nil {
		t.Errorf("entries[1] = %+v, want follow-up", entries[1])
	}
	if entries[2].Kind != relationship.TimelineInteraction || entries[2].Interaction == nil || entries[2].Interaction.Summary != "old note" {
		t.Errorf("entries[2] = %+v, want old note interaction", entries[2])
	}
}

func TestOrganizationTimeline_Empty(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	acme := f.org(t, "Acme")

	entries, err := f.relationships.OrganizationTimeline(ctx, acme)
	if err != nil {
		t.Fatalf("OrganizationTimeline() error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("len(entries) = %d, want 0", len(entries))
	}
}
