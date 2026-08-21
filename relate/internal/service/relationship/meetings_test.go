package relationship

import (
	"context"
	"testing"
	"time"

	"github.com/danieljustus/symaira-relate/internal/domain/relationship"
	"github.com/danieljustus/symaira-relate/internal/errs"
)

func TestImportPersonMeeting_CreatesInteraction(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	alice := f.person(t, "Alice")

	occurred := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	interaction, created, err := f.relationships.ImportPersonMeeting(ctx, alice, MeetingImportInput{
		MeetingID: "meet-123", Title: "Kickoff", OccurredAt: occurred,
	})
	if err != nil {
		t.Fatalf("ImportPersonMeeting() error = %v", err)
	}
	if !created {
		t.Error("created = false on first import, want true")
	}
	if interaction.Kind != relationship.InteractionMeeting {
		t.Errorf("Kind = %q, want meeting", interaction.Kind)
	}
	if interaction.ExternalRef != "meet-123" {
		t.Errorf("ExternalRef = %q, want meet-123", interaction.ExternalRef)
	}
	if interaction.Summary != "Kickoff" {
		t.Errorf("Summary = %q, want Kickoff", interaction.Summary)
	}
	if !interaction.OccurredAt.Equal(occurred) {
		t.Errorf("OccurredAt = %v, want %v", interaction.OccurredAt, occurred)
	}

	list, err := f.relationships.ListPersonInteractions(ctx, alice)
	if err != nil {
		t.Fatalf("ListPersonInteractions() error = %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("len(list) = %d, want 1", len(list))
	}
}

// TestImportPersonMeeting_ReImport_IsIdempotent is the core acceptance
// criterion: re-importing the same meeting id for the same person must
// return the existing interaction, not create a second one.
func TestImportPersonMeeting_ReImport_IsIdempotent(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	alice := f.person(t, "Alice")

	first, created1, err := f.relationships.ImportPersonMeeting(ctx, alice, MeetingImportInput{MeetingID: "meet-123", Title: "Kickoff"})
	if err != nil {
		t.Fatalf("first ImportPersonMeeting() error = %v", err)
	}
	if !created1 {
		t.Error("first import: created = false, want true")
	}

	second, created2, err := f.relationships.ImportPersonMeeting(ctx, alice, MeetingImportInput{MeetingID: "meet-123", Title: "Kickoff (retry)"})
	if err != nil {
		t.Fatalf("second ImportPersonMeeting() error = %v", err)
	}
	if created2 {
		t.Error("second import: created = true, want false (idempotent)")
	}
	if second.ID != first.ID {
		t.Errorf("second import returned a different interaction: %s != %s", second.ID, first.ID)
	}
	if second.Summary != "Kickoff" {
		t.Errorf("second import's returned Summary = %q, want the original %q (not overwritten)", second.Summary, "Kickoff")
	}

	list, err := f.relationships.ListPersonInteractions(ctx, alice)
	if err != nil {
		t.Fatalf("ListPersonInteractions() error = %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("len(list) = %d after re-import, want 1 (no duplicate)", len(list))
	}
}

func TestImportPersonMeeting_DifferentPeople_SameMeetingID_BothCreated(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	alice := f.person(t, "Alice")
	bob := f.person(t, "Bob")

	if _, created, err := f.relationships.ImportPersonMeeting(ctx, alice, MeetingImportInput{MeetingID: "meet-999", Title: "Sync"}); err != nil || !created {
		t.Fatalf("alice import: created=%v err=%v", created, err)
	}
	if _, created, err := f.relationships.ImportPersonMeeting(ctx, bob, MeetingImportInput{MeetingID: "meet-999", Title: "Sync"}); err != nil || !created {
		t.Fatalf("bob import: created=%v err=%v", created, err)
	}
}

func TestImportOrganizationMeeting_CreatesInteraction(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	acme := f.org(t, "Acme")

	interaction, created, err := f.relationships.ImportOrganizationMeeting(ctx, acme, MeetingImportInput{MeetingID: "meet-acme-1", Title: "QBR"})
	if err != nil {
		t.Fatalf("ImportOrganizationMeeting() error = %v", err)
	}
	if !created || interaction.ExternalRef != "meet-acme-1" {
		t.Errorf("interaction = %+v created = %v", interaction, created)
	}
}

func TestImportPersonMeeting_EmptyMeetingID_IsInvalid(t *testing.T) {
	f := newFixture(t)
	alice := f.person(t, "Alice")
	_, _, err := f.relationships.ImportPersonMeeting(context.Background(), alice, MeetingImportInput{MeetingID: "", Title: "No id"})
	if errs.KindOf(err) != errs.KindInvalid {
		t.Errorf("kind = %v, want invalid", errs.KindOf(err))
	}
}

func TestImportPersonMeeting_NoTitleOrSummary_FallsBackToDefault(t *testing.T) {
	f := newFixture(t)
	alice := f.person(t, "Alice")
	interaction, _, err := f.relationships.ImportPersonMeeting(context.Background(), alice, MeetingImportInput{MeetingID: "meet-bare"})
	if err != nil {
		t.Fatalf("ImportPersonMeeting() error = %v", err)
	}
	if interaction.Summary == "" {
		t.Error("Summary is empty, want a non-empty fallback")
	}
}
