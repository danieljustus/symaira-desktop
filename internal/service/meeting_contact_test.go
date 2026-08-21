package service

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/compose"
)

func writeMockSymrelateBin(t *testing.T, dir, script string) {
	t.Helper()
	path := filepath.Join(dir, "symrelate")
	if err := os.WriteFile(path, []byte(script), 0755); err != nil { //nolint:gosec // test fixture must be executable
		t.Fatal(err)
	}
}

func withMockSymrelatePath(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	compose.ResetCache()
	t.Cleanup(compose.ResetCache)
}

const mockSymrelateRefScript = `#!/bin/bash
if [ "$1" = "contact" ] && [ "$2" = "ref" ]; then
  case "$3" in
    c-ada)
      echo '{"provider":"symrelate","schema_version":1,"id":"c-ada","kind":"person","display_name":"Ada Lovelace","future_flag":true}'
      exit 0 ;;
    *)
      echo 'symrelate: contact.GetRef: contact not found' >&2
      exit 1 ;;
  esac
fi
`

const mockSymrelateSmuggleScript = `#!/bin/bash
if [ "$1" = "contact" ] && [ "$2" = "ref" ]; then
  echo '{"provider":"symrelate","schema_version":1,"id":"c-ada","kind":"person","display_name":"Ada Lovelace","future_flag":true,"email":"ada@private.example","phone":"+1 555 000 1111","address":"12 Secret Lane","notes":"secret board notes","transcript_path":"/Users/ada/private/t.md"}'
  exit 0
fi
`

const mockSymrelateV2Script = `#!/bin/bash
if [ "$1" = "contact" ] && [ "$2" = "ref" ]; then
  echo '{"provider":"symrelate","schema_version":2,"id":"c-ada","kind":"person","display_name":"Ada Lovelace"}'
  exit 0
fi
`

func importMeetingWithSymrelate(t *testing.T, dir, symrelateScript string) (*Service, string) {
	t.Helper()
	writeMockSymrelateBin(t, dir, symrelateScript)
	withMockSymrelatePath(t, dir)

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
	dir := t.TempDir()
	svc, path := importMeetingWithSymrelate(t, dir, mockSymrelateRefScript)

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
	if ref["future_flag"] != true {
		t.Errorf("unknown additive field future_flag must be preserved, got %+v", ref)
	}
	for _, banned := range []string{"email", "phone", "address", "notes", "contact_points"} {
		if _, found := ref[banned]; found {
			t.Errorf("contact_ref must never contain %q", banned)
		}
	}
}

func TestLinkParticipantContactPreservesUnknownFields(t *testing.T) {
	dir := t.TempDir()
	writeMockSymrelateBin(t, dir, mockSymrelateRefScript)
	withMockSymrelatePath(t, dir)

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

func TestLinkParticipantContactFixtureContainsNoPrivateData(t *testing.T) {
	dir := t.TempDir()
	svc, path := importMeetingWithSymrelate(t, dir, mockSymrelateSmuggleScript)

	if _, err := svc.LinkParticipantContact(path, "speaker_0", "c-ada"); err != nil {
		t.Fatalf("LinkParticipantContact() error = %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(svc.VaultRoot, path)) //nolint:gosec // test reads its own temp vault fixture
	if err != nil {
		t.Fatal(err)
	}
	content := string(raw)
	for _, leak := range []string{"ada@private.example", "+1 555 000 1111", "12 Secret Lane", "secret board notes", "/Users/ada"} {
		if strings.Contains(content, leak) {
			t.Errorf("vault file contains smuggled private data %q:\n%s", leak, content)
		}
	}
	fm := content
	if idx := strings.Index(content[4:], "---"); idx != -1 {
		fm = content[:idx+4]
	}
	for _, bannedKey := range []string{"email:", "phone:", "address:", "notes:", "_path:", "contact_point"} {
		if strings.Contains(fm, bannedKey) {
			t.Errorf("frontmatter contains forbidden key fragment %q:\n%s", bannedKey, fm)
		}
	}
	if !strings.Contains(fm, "future_flag") {
		t.Errorf("benign unknown field future_flag must be stored, frontmatter:\n%s", fm)
	}
}

func TestLinkParticipantContactSymrelateUnavailable(t *testing.T) {
	dir := t.TempDir()
	svc, path := importFixtureMeeting(t, dir)

	before, err := os.ReadFile(filepath.Join(svc.VaultRoot, path)) //nolint:gosec // test reads its own temp vault fixture
	if err != nil {
		t.Fatal(err)
	}

	// A bare-bones PATH: the real symrelate must not leak into this test.
	t.Setenv("PATH", "/usr/bin:/bin")
	compose.ResetCache()
	t.Cleanup(compose.ResetCache)

	_, err = svc.LinkParticipantContact(path, "speaker_0", "c-ada")
	if !errors.Is(err, ErrSymrelateUnavailable) {
		t.Fatalf("expected ErrSymrelateUnavailable, got %v", err)
	}

	after, err := os.ReadFile(filepath.Join(svc.VaultRoot, path)) //nolint:gosec // test reads its own temp vault fixture
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("note must be unchanged when symrelate is unavailable")
	}
}

func TestLinkParticipantContactIncompatibleSchema(t *testing.T) {
	dir := t.TempDir()
	svc, path := importMeetingWithSymrelate(t, dir, mockSymrelateV2Script)

	before, err := os.ReadFile(filepath.Join(svc.VaultRoot, path)) //nolint:gosec // test reads its own temp vault fixture
	if err != nil {
		t.Fatal(err)
	}

	_, err = svc.LinkParticipantContact(path, "speaker_0", "c-ada")
	if !errors.Is(err, compose.ErrContactRefIncompatible) {
		t.Fatalf("expected ErrContactRefIncompatible, got %v", err)
	}

	after, err := os.ReadFile(filepath.Join(svc.VaultRoot, path)) //nolint:gosec // test reads its own temp vault fixture
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("note must be unchanged for an incompatible reference")
	}
}

func TestLinkParticipantContactErasedContact(t *testing.T) {
	dir := t.TempDir()
	svc, path := importMeetingWithSymrelate(t, dir, mockSymrelateRefScript)

	_, err := svc.LinkParticipantContact(path, "speaker_0", "c-erased")
	if !errors.Is(err, compose.ErrContactNotFound) {
		t.Fatalf("expected ErrContactNotFound, got %v", err)
	}
}

func TestLinkParticipantContactUnknownSpeaker(t *testing.T) {
	dir := t.TempDir()
	svc, path := importMeetingWithSymrelate(t, dir, mockSymrelateRefScript)

	if _, err := svc.LinkParticipantContact(path, "speaker_99", "c-ada"); err == nil || !strings.Contains(err.Error(), "no participant with speaker id") {
		t.Errorf("expected an unknown-speaker error, got %v", err)
	}
}

func TestUnlinkParticipantContactRemovesRef(t *testing.T) {
	dir := t.TempDir()
	svc, path := importMeetingWithSymrelate(t, dir, mockSymrelateRefScript)

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
	dir := t.TempDir()
	svc, path := importMeetingWithSymrelate(t, dir, mockSymrelateRefScript)

	if _, err := svc.LinkParticipantContact(path, "speaker_0", "c-ada"); err != nil {
		t.Fatal(err)
	}

	// Relate disappears (or the contact was erased): reading, listing and
	// rendering the note must keep working off the stored reference alone.
	t.Setenv("PATH", "/usr/bin:/bin")
	compose.ResetCache()
	t.Cleanup(compose.ResetCache)

	doc, err := svc.MeetingShow(path)
	if err != nil {
		t.Fatalf("MeetingShow() with symrelate absent error = %v", err)
	}
	p, _ := doc.Frontmatter["participants"].([]interface{})
	entry, _ := p[0].(map[string]interface{})
	ref, ok := entry["contact_ref"].(map[string]interface{})
	if !ok || ref["display_name"] != "Ada Lovelace" {
		t.Errorf("stored contact_ref must render without symrelate: %+v", entry)
	}

	if _, err := svc.MeetingList(); err != nil {
		t.Fatalf("MeetingList() with symrelate absent error = %v", err)
	}
}

func TestResolveMeetingContactRef(t *testing.T) {
	dir := t.TempDir()
	writeMockSymrelateBin(t, dir, mockSymrelateRefScript)
	withMockSymrelatePath(t, dir)

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
	t.Setenv("PATH", "/usr/bin:/bin")
	compose.ResetCache()
	t.Cleanup(compose.ResetCache)

	svc := newTestService(t)
	if _, err := svc.ResolveMeetingContactRef("c-ada"); !errors.Is(err, ErrSymrelateUnavailable) {
		t.Fatalf("expected ErrSymrelateUnavailable, got %v", err)
	}
}
