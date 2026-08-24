package service

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/vault"

	"github.com/danieljustus/symaira-desktop/internal/contacts"
)

// withNameLookup points the name side of the contact seam at a fake store.
func withNameLookup(t *testing.T, byName map[string][]contacts.Ref) {
	t.Helper()
	prev := contacts.FindByNameFunc
	contacts.FindByNameFunc = func(_ context.Context, name string) ([]contacts.Ref, error) {
		return byName[name], nil
	}
	t.Cleanup(func() { contacts.FindByNameFunc = prev })
}

// A name nobody carries is an answer, not a failure: the documents filed
// under it must still come back, so the app can show them as unresolved
// rather than as an error.
func TestResolveContactReferencesReturnsDocumentsForAnUnknownName(t *testing.T) {
	withContactStore(t)
	withNameLookup(t, nil)
	svc := newTestService(t)
	writeMeetingNoteFixture(t, svc, "docs/bill.md", "---\ntitle: Bill\ncorrespondent: Stadtwerke\n---\n\nbody\n")
	doc, err := vault.ParseFile(filepath.Join(svc.VaultRoot, "docs/bill.md"))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.IndexDocument(doc); err != nil {
		t.Fatal(err)
	}

	got, err := svc.ResolveContactReferences("Stadtwerke")
	if err != nil {
		t.Fatalf("ResolveContactReferences: %v", err)
	}
	if len(got.Refs) != 0 {
		t.Errorf("Refs = %+v, want none for an unknown name", got.Refs)
	}
	if len(got.Documents) != 1 || got.Documents[0].Correspondent != "Stadtwerke" {
		t.Errorf("Documents = %+v, want the one document filed under the name", got.Documents)
	}
	if !got.StoreAvailable {
		t.Error("StoreAvailable = false, want true when the store answered")
	}
}

// An unreadable store must be distinguishable from "nobody by that name":
// rendering the first as the second would claim an identity question was
// answered when it was not.
func TestResolveContactReferencesFlagsAnUnavailableStore(t *testing.T) {
	withoutContactStore(t)
	svc := newTestService(t)

	got, err := svc.ResolveContactReferences("Stadtwerke")
	if err != nil {
		t.Fatalf("ResolveContactReferences: %v", err)
	}
	if got.StoreAvailable {
		t.Error("StoreAvailable = true, want false when the store cannot be opened")
	}
	if len(got.Refs) != 0 {
		t.Errorf("Refs = %+v, want none when the store is unavailable", got.Refs)
	}
}

// A blank correspondent must not be resolved at all — an empty name would
// otherwise match every document with no correspondent.
func TestResolveContactReferencesIgnoresABlankName(t *testing.T) {
	svc := newTestService(t)
	called := false
	prev := contacts.FindByNameFunc
	contacts.FindByNameFunc = func(context.Context, string) ([]contacts.Ref, error) {
		called = true
		return nil, errors.New("must not be called")
	}
	t.Cleanup(func() { contacts.FindByNameFunc = prev })

	got, err := svc.ResolveContactReferences("   ")
	if err != nil {
		t.Fatalf("ResolveContactReferences: %v", err)
	}
	if called {
		t.Error("the contact store was queried for a blank name")
	}
	if len(got.Documents) != 0 {
		t.Errorf("Documents = %+v, want none for a blank name", got.Documents)
	}
}

// Meetings are matched by reviewed reference ID, never by name.
func TestResolveContactReferencesFindsMeetingsByReferenceID(t *testing.T) {
	withContactStore(t, adaRef)
	withNameLookup(t, map[string][]contacts.Ref{"Ada Lovelace": {*adaRef}})
	svc := newTestService(t)
	writeMeetingNoteFixture(t, svc, "meetings/meeting-m1.md", meetingNoteWithContactRefFixture)

	got, err := svc.ResolveContactReferences("Ada Lovelace")
	if err != nil {
		t.Fatalf("ResolveContactReferences: %v", err)
	}
	if len(got.Refs) != 1 || got.Refs[0].ID != adaRef.ID {
		t.Fatalf("Refs = %+v, want the resolved reference", got.Refs)
	}
	if len(got.Meetings) != 1 {
		t.Fatalf("Meetings = %+v, want the meeting linking that reference", got.Meetings)
	}
	if got.Meetings[0].Path != "meetings/meeting-m1.md" {
		t.Errorf("meeting path = %q", got.Meetings[0].Path)
	}
}

const meetingNoteWithContactRefFixture = `---
type: meeting
title: Design Review
created: 2026-07-21T10:00:00Z
meeting_id: m-fixture
started_at: 2026-07-21T10:00:00Z
participants:
  - label: Ada
    speaker_ids:
      - s1
    contact_ref:
      provider: symrelate
      schema_version: 1
      id: c-ada
      kind: person
      display_name: Ada Lovelace
---

body
`
