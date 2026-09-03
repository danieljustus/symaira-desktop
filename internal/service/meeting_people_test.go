package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/compose"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

func writeMockSymmemory(t *testing.T, dir, script string) {
	t.Helper()
	path := filepath.Join(dir, "symmemory")
	if err := os.WriteFile(path, []byte(script), 0700); err != nil { //nolint:gosec // test fixture must be executable
		t.Fatal(err)
	}
}

func withMockSymmemoryPath(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	compose.ResetCache()
	t.Cleanup(compose.ResetCache)
}

const mockSymmemoryEntityListScript = `#!/bin/bash
if [ "$1" = "entity" ] && [ "$2" = "list" ]; then
  echo '[{"id":"e-alice","name":"Alice Example","type":"person","aliases":["Ali"],"description":""}]'
fi
`

const meetingNoteM1Fixture = `---
type: meeting
title: Meeting 2026-07-01 10:00
created: 2026-07-01T10:00:00Z
tags:
  - meeting
meeting_id: m1
started_at: 2026-07-01T10:00:00Z
symmeet_source:
  artifact_schema_version: 1
  review_state: reviewed
participants:
  - label: Alice
    speaker_ids:
      - speaker_0
    entity_id: e-alice
---

<!-- symmeet-transcript:start -->
Alice: Hello everyone.
<!-- symmeet-transcript:end -->
`

const meetingNoteUnconfirmedFixture = `---
type: meeting
title: Meeting 2026-07-01 10:00
created: 2026-07-01T10:00:00Z
tags:
  - meeting
meeting_id: m1
started_at: 2026-07-01T10:00:00Z
symmeet_source:
  artifact_schema_version: 1
  review_state: reviewed
participants:
  - label: Alice
    speaker_ids:
      - speaker_0
---

<!-- symmeet-transcript:start -->
Alice: Hello everyone.
<!-- symmeet-transcript:end -->
`

func importFixtureMeeting(t *testing.T, dir string) (*Service, string) {
	t.Helper()
	svc := newTestService(t)
	relPath := "meetings/meeting-m1.md"
	writeMeetingNoteFixture(t, svc, relPath, meetingNoteM1Fixture)
	return svc, relPath
}

func TestResolveParticipantCandidatesSuccess(t *testing.T) {
	dir := t.TempDir()
	writeMockSymmemory(t, dir, mockSymmemoryEntityListScript)
	withMockSymmemoryPath(t, dir)

	svc := newTestService(t)
	candidates, err := svc.ResolveParticipantCandidates("Alice Example")
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if len(candidates) != 1 || candidates[0].EntityID != "e-alice" {
		t.Errorf("unexpected candidates: %+v", candidates)
	}
}

func TestResolveParticipantCandidatesSymmemoryUnavailable(t *testing.T) {
	// A bare-bones PATH, not a prepended one: the real symmemory installed
	// on the dev machine must not leak into this "unavailable" scenario.
	t.Setenv("PATH", "/usr/bin:/bin")
	compose.ResetCache()
	t.Cleanup(compose.ResetCache)

	svc := newTestService(t)
	if _, err := svc.ResolveParticipantCandidates("Alice Example"); err != ErrSymmemoryUnavailable {
		t.Errorf("expected ErrSymmemoryUnavailable, got %v", err)
	}
}

func TestConfirmParticipantSetsEntityID(t *testing.T) {
	dir := t.TempDir()
	svc, path := importFixtureMeeting(t, dir)

	if err := svc.ConfirmParticipant(path, "speaker_0", "e-alice"); err != nil {
		t.Fatalf("expected success, got %v", err)
	}

	doc, err := svc.MeetingShow(path)
	if err != nil {
		t.Fatal(err)
	}
	participants, ok := doc.Frontmatter["participants"].([]interface{})
	if !ok || len(participants) != 1 {
		t.Fatalf("expected 1 participant in frontmatter, got %+v", doc.Frontmatter["participants"])
	}
	p, _ := participants[0].(map[string]interface{})
	if p["entity_id"] != "e-alice" {
		t.Errorf("expected entity_id e-alice, got %+v", p)
	}
	// speaker_id must be preserved unchanged alongside the new entity_id.
	if label, _ := p["label"].(string); label != "Alice" {
		t.Errorf("expected label preserved as Alice, got %+v", p)
	}
}

func TestConfirmParticipantPreservesBodyAndUnrelatedContent(t *testing.T) {
	dir := t.TempDir()
	svc, path := importFixtureMeeting(t, dir)
	absPath := filepath.Join(svc.VaultRoot, path)

	original, err := os.ReadFile(absPath) //nolint:gosec // test reads its own temp vault fixture
	if err != nil {
		t.Fatal(err)
	}
	withManualNote := string(original) + "\n## Follow-up\n\nSend the recap.\n"
	if err := os.WriteFile(absPath, []byte(withManualNote), 0600); err != nil { //nolint:gosec // test writes into its own temp vault fixture
		t.Fatal(err)
	}

	if err := svc.ConfirmParticipant(path, "speaker_0", "e-alice"); err != nil {
		t.Fatalf("expected success, got %v", err)
	}

	final, err := os.ReadFile(absPath) //nolint:gosec // test reads its own temp vault fixture
	if err != nil {
		t.Fatal(err)
	}
	finalStr := string(final)
	if !strings.Contains(finalStr, "## Follow-up") || !strings.Contains(finalStr, "Send the recap.") {
		t.Error("expected manual notes outside frontmatter to survive")
	}
	if !strings.Contains(finalStr, "Alice: Hello everyone.") {
		t.Error("expected the transcript to survive")
	}
}

func TestConfirmParticipantUnknownSpeakerID(t *testing.T) {
	dir := t.TempDir()
	svc, path := importFixtureMeeting(t, dir)

	if err := svc.ConfirmParticipant(path, "speaker_99", "e-alice"); err == nil || !strings.Contains(err.Error(), "no participant with speaker id") {
		t.Errorf("expected an unknown-speaker error, got %v", err)
	}
}

// Exercises the same stale-write guard MeetingRefresh uses: a write built
// from a doc read before a concurrent on-disk change must be rejected
// instead of silently clobbering the newer content.
func TestWriteMeetingFrontmatterConflictWhenChangedOnDisk(t *testing.T) {
	dir := t.TempDir()
	svc, path := importFixtureMeeting(t, dir)
	absPath := filepath.Join(svc.VaultRoot, path)

	doc, fm, err := svc.loadMeetingFrontmatter(path)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a concurrent edit landing after doc was read but before this
	// write runs.
	current, err := os.ReadFile(absPath) //nolint:gosec // test reads its own temp vault fixture
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absPath, append(current, []byte("\n## Concurrent edit\n")...), 0600); err != nil { //nolint:gosec // test writes into its own temp vault fixture
		t.Fatal(err)
	}

	fm.Participants[0].EntityID = "e-alice"
	err = svc.writeMeetingFrontmatter(path, doc, fm)
	if err == nil || !strings.Contains(err.Error(), "changed on disk since it was read") {
		t.Errorf("expected a stale-write conflict error, got %v", err)
	}

	// The concurrent edit must survive untouched — the rejected write must
	// not have partially applied.
	final, err := os.ReadFile(absPath) //nolint:gosec // test reads its own temp vault fixture
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(final), "## Concurrent edit") {
		t.Error("expected the concurrent edit to survive the rejected write")
	}
}

// A frontmatter-only concurrent change (e.g. a publish updating the
// symmeet_published_facts ledger via vault.SetFrontmatterValue) leaves the
// body untouched, so a body-suffix guard would miss it entirely and let the
// stale write silently revert the ledger update. The content-hash guard must
// catch this even though the body never changed.
func TestWriteMeetingFrontmatterConflictWhenFrontmatterOnlyChangedOnDisk(t *testing.T) {
	dir := t.TempDir()
	svc, path := importFixtureMeeting(t, dir)
	absPath := filepath.Join(svc.VaultRoot, path)

	doc, fm, err := svc.loadMeetingFrontmatter(path)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a concurrent publish writing to the published-facts ledger:
	// frontmatter changes, body bytes stay identical.
	if err := vault.SetFrontmatterValue(absPath, "symmeet_published_facts", []string{"h1", "h2"}); err != nil {
		t.Fatal(err)
	}

	fm.Participants[0].EntityID = "e-alice"
	err = svc.writeMeetingFrontmatter(path, doc, fm)
	if err == nil || !strings.Contains(err.Error(), "changed on disk since it was read") {
		t.Errorf("expected a stale-write conflict error, got %v", err)
	}

	// The concurrent ledger update must survive untouched.
	final, err := os.ReadFile(absPath) //nolint:gosec // test reads its own temp vault fixture
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(final), "symmeet_published_facts") {
		t.Error("expected the concurrent published-facts ledger update to survive the rejected write")
	}
}

func TestConfirmParticipantNormalFollowUpSucceeds(t *testing.T) {
	dir := t.TempDir()
	svc, path := importFixtureMeeting(t, dir)

	if err := svc.ConfirmParticipant(path, "speaker_0", "e-alice"); err != nil {
		t.Fatal(err)
	}
	if err := svc.ConfirmParticipant(path, "speaker_0", "e-alice-2"); err != nil {
		t.Fatalf("expected a normal follow-up confirm to succeed, got %v", err)
	}
}

// mockSymmemoryPersonScript answers entity show for an existing person and
// records entity add calls, for the create-new-person flow.
const mockSymmemoryPersonScript = `#!/bin/bash
if [ "$1" = "entity" ] && [ "$2" = "show" ]; then
  if [ "$3" = "Carol New" ]; then
    if [ -f "$SYMMEMORY_CREATED" ]; then
      echo '{"id":"e-carol","name":"Carol New","type":"person","aliases":[],"description":""}'
      exit 0
    fi
    echo "entity not found" >&2
    exit 1
  fi
  echo '{"id":"e-alice","name":"Alice Example","type":"person","aliases":[],"description":""}'
elif [ "$1" = "entity" ] && [ "$2" = "add" ]; then
  touch "$SYMMEMORY_CREATED"
  echo '{"id":"e-carol","name":"Carol New","type":"person"}'
fi
`

func TestConfirmParticipantNewPersonCreatesAndLinks(t *testing.T) {
	dir := t.TempDir()
	svc, path := importFixtureMeeting(t, dir)
	writeMockSymmemory(t, dir, mockSymmemoryPersonScript)
	withMockSymmemoryPath(t, dir)
	t.Setenv("SYMMEMORY_CREATED", dir+"/created")

	entityID, err := svc.ConfirmParticipantNewPerson(path, "speaker_0", "Carol New")
	if err != nil {
		t.Fatal(err)
	}
	if entityID != "e-carol" {
		t.Errorf("expected e-carol, got %q", entityID)
	}
	if _, err := os.Stat(dir + "/created"); err != nil {
		t.Error("expected the person entity to be created in Memory")
	}

	doc, err := svc.MeetingShow(path)
	if err != nil {
		t.Fatal(err)
	}
	participants, _ := doc.Frontmatter["participants"].([]interface{})
	if len(participants) == 0 {
		t.Fatal("expected participants in frontmatter")
	}
	first, _ := participants[0].(map[string]interface{})
	if first["entity_id"] != "e-carol" {
		t.Errorf("expected linked entity_id e-carol, got %v", first["entity_id"])
	}
}

func TestConfirmParticipantNewPersonRejectsEmptyName(t *testing.T) {
	dir := t.TempDir()
	svc, path := importFixtureMeeting(t, dir)
	writeMockSymmemory(t, dir, mockSymmemoryPersonScript)
	withMockSymmemoryPath(t, dir)

	if _, err := svc.ConfirmParticipantNewPerson(path, "speaker_0", "   "); err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Errorf("expected name-required error, got %v", err)
	}
}

func TestConfirmParticipantNewPersonSymmemoryUnavailable(t *testing.T) {
	dir := t.TempDir()
	svc, path := importFixtureMeeting(t, dir)

	t.Setenv("PATH", "/usr/bin:/bin")
	compose.ResetCache()
	t.Cleanup(compose.ResetCache)
	if _, err := svc.ConfirmParticipantNewPerson(path, "speaker_0", "Carol New"); err != ErrSymmemoryUnavailable {
		t.Errorf("expected ErrSymmemoryUnavailable, got %v", err)
	}
}
