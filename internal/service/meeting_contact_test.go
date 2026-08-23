package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/contacts"
)

// adaRef is the one contact the fake store knows.
var adaRef = &contacts.Ref{
	Provider:      contacts.Provider,
	SchemaVersion: contacts.SchemaVersion,
	ID:            "c-ada",
	Kind:          "person",
	DisplayName:   "Ada Lovelace",
}

// withContactStore points the in-process contact seam at a fake store that
// knows exactly the given references. Every other ID resolves to
// ErrContactNotFound, which is how an erased contact behaves.
func withContactStore(t *testing.T, refs ...*contacts.Ref) {
	t.Helper()
	known := make(map[string]*contacts.Ref, len(refs))
	for _, r := range refs {
		known[r.ID] = r
	}
	prevAvailable, prevResolve := contacts.AvailableFunc, contacts.ResolveFunc
	contacts.AvailableFunc = func(context.Context) bool { return true }
	contacts.ResolveFunc = func(_ context.Context, id string) (*contacts.Ref, error) {
		if r, ok := known[id]; ok {
			clone := *r
			return &clone, nil
		}
		return nil, contacts.ErrContactNotFound
	}
	t.Cleanup(func() {
		contacts.AvailableFunc, contacts.ResolveFunc = prevAvailable, prevResolve
	})
}

// withoutContactStore simulates an unreadable local contact store.
func withoutContactStore(t *testing.T) {
	t.Helper()
	prevAvailable := contacts.AvailableFunc
	contacts.AvailableFunc = func(context.Context) bool { return false }
	t.Cleanup(func() { contacts.AvailableFunc = prevAvailable })
}

func importMeetingWithContactStore(t *testing.T) (*Service, string) {
	t.Helper()
	withContactStore(t, adaRef)

	svc := newTestService(t)
	relPath := "meetings/meeting-m1.md"
	writeMeetingNoteFixture(t, svc, relPath, meetingNoteUnknownFieldsFixture)
	return svc, relPath
}

const meetingNoteUnknownFieldsFixture = `---
type: meeting
title: Meeting 2026-07-21 10:00
created: 2026-07-21T10:00:00Z
tags:
  - meeting
meeting_id: m-fixture
started_at: 2026-07-21T10:00:00Z
x_future_top: keep-me
participants:
  - label: Alice
    speaker_ids:
      - speaker_0
    entity_id: e-alice
    nickname: Al
symmeet_source:
  artifact_schema_version: 1
  review_state: reviewed
---

<!-- symmeet-transcript:start -->
Alice: Hello everyone.
<!-- symmeet-transcript:end -->
`

func writeMeetingNoteFixture(t *testing.T, svc *Service, relPath, content string) {
	t.Helper()
	abs := filepath.Join(svc.VaultRoot, relPath)
	if err := os.MkdirAll(filepath.Dir(abs), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0600); err != nil { //nolint:gosec // test fixture mirrors meeting-note permissions
		t.Fatal(err)
	}
}

func participantFromDoc(t *testing.T, svc *Service, notePath string) map[string]interface{} {
	t.Helper()
	doc, err := svc.MeetingShow(notePath)
	if err != nil {
		t.Fatal(err)
	}
	participants, ok := doc.Frontmatter["participants"].([]interface{})
	if !ok || len(participants) != 1 {
		t.Fatalf("expected 1 participant in frontmatter, got %+v", doc.Frontmatter["participants"])
	}
	p, _ := participants[0].(map[string]interface{})
	if p == nil {
		t.Fatalf("participant entry is not a map: %+v", participants[0])
	}
	return p
}

func TestLinkParticipantContactRetainsEntityIDAndSetsRef(t *testing.T) {
	svc, path := importMeetingWithContactStore(t)

	if err := svc.ConfirmParticipant(path, "speaker_0", "e-alice"); err != nil {
		t.Fatalf("ConfirmParticipant() error = %v", err)
	}
	if _, err := svc.LinkParticipantContact(path, "speaker_0", "c-ada"); err != nil {
		t.Fatalf("LinkParticipantContact() error = %v", err)
	}

	p := participantFromDoc(t, svc, path)
	if p["entity_id"] != "e-alice" {
		t.Errorf("entity_id must survive linking a contact ref, got %+v", p)
	}
	ref, ok := p["contact_ref"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected contact_ref map, got %+v", p)
	}
	want := map[string]interface{}{"provider": "symrelate", "id": "c-ada", "kind": "person", "display_name": "Ada Lovelace"}
	for k, v := range want {
		if ref[k] != v {
			t.Errorf("contact_ref[%s] = %v, want %v", k, ref[k], v)
		}
	}
	if ref["schema_version"] != 1 {
		t.Errorf("contact_ref schema_version = %v, want 1", ref["schema_version"])
	}
	for _, banned := range []string{"email", "phone", "address", "notes", "contact_points"} {
		if _, found := ref[banned]; found {
			t.Errorf("contact_ref must never contain %q", banned)
		}
	}
}

func TestLinkParticipantContactPreservesUnknownFields(t *testing.T) {
	withContactStore(t, adaRef)

	svc := newTestService(t)
	writeMeetingNoteFixture(t, svc, "meetings/meeting-m-fixture.md", meetingNoteUnknownFieldsFixture)
	path := "meetings/meeting-m-fixture.md"

	if _, err := svc.LinkParticipantContact(path, "speaker_0", "c-ada"); err != nil {
		t.Fatalf("LinkParticipantContact() error = %v", err)
	}

	doc, err := svc.MeetingShow(path)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Frontmatter["x_future_top"] != "keep-me" {
		t.Errorf("unknown top-level field lost on link: %+v", doc.Frontmatter)
	}
	p := participantFromDoc(t, svc, path)
	if p["nickname"] != "Al" {
		t.Errorf("unknown participant field lost on link: %+v", p)
	}
	if p["entity_id"] != "e-alice" {
		t.Errorf("entity_id lost on link: %+v", p)
	}
	if _, ok := p["contact_ref"].(map[string]interface{}); !ok {
		t.Fatalf("contact_ref missing after link: %+v", p)
	}

	if err := svc.UnlinkParticipantContact(path, "speaker_0"); err != nil {
		t.Fatalf("UnlinkParticipantContact() error = %v", err)
	}
	p = participantFromDoc(t, svc, path)
	if _, found := p["contact_ref"]; found {
		t.Errorf("contact_ref must be removed on unlink: %+v", p)
	}
	if p["nickname"] != "Al" || p["entity_id"] != "e-alice" {
		t.Errorf("unlink must keep entity mapping and unknown fields: %+v", p)
	}
	doc, err = svc.MeetingShow(path)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Frontmatter["x_future_top"] != "keep-me" {
		t.Errorf("unknown top-level field lost on unlink: %+v", doc.Frontmatter)
	}
	if !strings.Contains(doc.Body, "Alice: Hello everyone.") {
		t.Error("transcript body must survive link/unlink")
	}
}

func TestLinkParticipantContactStoresOnlyReferenceFields(t *testing.T) {
	svc, path := importMeetingWithContactStore(t)

	if _, err := svc.LinkParticipantContact(path, "speaker_0", "c-ada"); err != nil {
		t.Fatalf("LinkParticipantContact() error = %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(svc.VaultRoot, path)) //nolint:gosec // test reads its own temp vault fixture
	if err != nil {
		t.Fatal(err)
	}
	content := string(raw)
	fm := content
	if idx := strings.Index(content[4:], "---"); idx != -1 {
		fm = content[:idx+4]
	}
	// The reference type has no field that could carry a contact point, so
	// this asserts the stored shape rather than a filter: a regression that
	// widened the type would show up here.
	for _, bannedKey := range []string{"email:", "phone:", "address:", "notes:", "_path:", "contact_point"} {
		if strings.Contains(fm, bannedKey) {
			t.Errorf("frontmatter contains forbidden key fragment %q:\n%s", bannedKey, fm)
		}
	}
	if !strings.Contains(fm, "id: c-ada") || !strings.Contains(fm, "kind: person") {
		t.Errorf("frontmatter is missing the reference identity:\n%s", fm)
	}
}

func TestLinkParticipantContactStoreUnavailable(t *testing.T) {
	dir := t.TempDir()
	svc, path := importFixtureMeeting(t, dir)

	before, err := os.ReadFile(filepath.Join(svc.VaultRoot, path)) //nolint:gosec // test reads its own temp vault fixture
	if err != nil {
		t.Fatal(err)
	}

	withoutContactStore(t)

	_, err = svc.LinkParticipantContact(path, "speaker_0", "c-ada")
	if !errors.Is(err, ErrContactStoreUnavailable) {
		t.Fatalf("expected ErrContactStoreUnavailable, got %v", err)
	}

	after, err := os.ReadFile(filepath.Join(svc.VaultRoot, path)) //nolint:gosec // test reads its own temp vault fixture
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("note must be unchanged when the contact store is unavailable")
	}
}

func TestLinkParticipantContactErasedContact(t *testing.T) {
	svc, path := importMeetingWithContactStore(t)

	_, err := svc.LinkParticipantContact(path, "speaker_0", "c-erased")
	if !errors.Is(err, contacts.ErrContactNotFound) {
		t.Fatalf("expected ErrContactNotFound, got %v", err)
	}
}

func TestLinkParticipantContactUnknownSpeaker(t *testing.T) {
	svc, path := importMeetingWithContactStore(t)

	if _, err := svc.LinkParticipantContact(path, "speaker_99", "c-ada"); err == nil || !strings.Contains(err.Error(), "no participant with speaker id") {
		t.Errorf("expected an unknown-speaker error, got %v", err)
	}
}

func TestUnlinkParticipantContactRemovesRef(t *testing.T) {
	svc, path := importMeetingWithContactStore(t)

	if err := svc.ConfirmParticipant(path, "speaker_0", "e-alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.LinkParticipantContact(path, "speaker_0", "c-ada"); err != nil {
		t.Fatal(err)
	}
	if err := svc.UnlinkParticipantContact(path, "speaker_0"); err != nil {
		t.Fatalf("UnlinkParticipantContact() error = %v", err)
	}

	p := participantFromDoc(t, svc, path)
	if _, found := p["contact_ref"]; found {
		t.Errorf("contact_ref still present after unlink: %+v", p)
	}
	if p["entity_id"] != "e-alice" {
		t.Errorf("entity_id must survive unlink: %+v", p)
	}
}

func TestMeetingReadToleratesUnresolvableContactRef(t *testing.T) {
	svc, path := importMeetingWithContactStore(t)

	if _, err := svc.LinkParticipantContact(path, "speaker_0", "c-ada"); err != nil {
		t.Fatal(err)
	}

	// The store becomes unreadable (or the contact was erased): reading,
	// listing and rendering the note must keep working off the stored
	// reference alone.
	withoutContactStore(t)

	doc, err := svc.MeetingShow(path)
	if err != nil {
		t.Fatalf("MeetingShow() with the contact store unavailable error = %v", err)
	}
	p, _ := doc.Frontmatter["participants"].([]interface{})
	entry, _ := p[0].(map[string]interface{})
	ref, ok := entry["contact_ref"].(map[string]interface{})
	if !ok || ref["display_name"] != "Ada Lovelace" {
		t.Errorf("stored contact_ref must render without the store: %+v", entry)
	}

	if _, err := svc.MeetingList(); err != nil {
		t.Fatalf("MeetingList() with the contact store unavailable error = %v", err)
	}
}

func TestResolveMeetingContactRef(t *testing.T) {
	withContactStore(t, adaRef)

	svc := newTestService(t)
	ref, err := svc.ResolveMeetingContactRef("c-ada")
	if err != nil {
		t.Fatalf("ResolveMeetingContactRef() error = %v", err)
	}
	if ref.DisplayName != "Ada Lovelace" || ref.Kind != "person" {
		t.Errorf("unexpected ref: %+v", ref)
	}
}

func TestResolveMeetingContactRefUnavailable(t *testing.T) {
	withoutContactStore(t)

	svc := newTestService(t)
	if _, err := svc.ResolveMeetingContactRef("c-ada"); !errors.Is(err, ErrContactStoreUnavailable) {
		t.Fatalf("expected ErrContactStoreUnavailable, got %v", err)
	}
}
