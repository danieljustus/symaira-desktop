package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMeetingListAndShow(t *testing.T) {
	svc := newTestService(t)
	path := "meetings/meeting-m1.md"
	writeMeetingNoteFixture(t, svc, path, meetingNoteM1Fixture)

	// A non-meeting note must not appear in the meeting list.
	if _, err := svc.NoteNew("Regular Note", "just a note", ""); err != nil {
		t.Fatal(err)
	}

	summaries, err := svc.MeetingList()
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 {
		t.Fatalf("expected exactly 1 meeting note, got %d: %+v", len(summaries), summaries)
	}
	if summaries[0].MeetingID != "m1" || summaries[0].Path != path {
		t.Errorf("unexpected summary: %+v", summaries[0])
	}
	// The vault-based note carries no duration (that came from the symmeet
	// artifact manifest via MeetingImport, which is gone); 0 is correct now.
	if summaries[0].DurationMS != 0 {
		t.Errorf("expected duration_ms=0 for a vault-based meeting note, got %d", summaries[0].DurationMS)
	}

	doc, err := svc.MeetingShow(path)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Frontmatter["meeting_id"] != "m1" {
		t.Errorf("unexpected frontmatter: %+v", doc.Frontmatter)
	}
}

func TestMeetingShowRejectsNonMeetingNote(t *testing.T) {
	svc := newTestService(t)
	path, err := svc.NoteNew("Not A Meeting", "body", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.MeetingShow(path); err == nil {
		t.Error("expected an error for a non-meeting note")
	}
}

// TestMeetingListDetailedReportsMalformedFiles verifies that one unreadable
// meeting candidate remains actionable instead of disappearing from the list.
func TestMeetingListDetailedReportsMalformedFiles(t *testing.T) {
	svc := newTestService(t)
	path := filepath.Join(svc.VaultRoot, "meetings", "broken.md")
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("---\ntitle: [broken\n---\nbody\n"), 0600); err != nil {
		t.Fatal(err)
	}

	result, err := svc.MeetingListDetailed()
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Failures) != 1 {
		t.Fatalf("expected one list failure, got %d: %+v", len(result.Failures), result.Failures)
	}
	if result.Failures[0].Path != filepath.Join("meetings", "broken.md") {
		t.Errorf("failure path = %q, want meetings/broken.md", result.Failures[0].Path)
	}
	if result.Failures[0].Message == "" {
		t.Error("failure message must explain why the file could not be read")
	}
}

// TestMeetingListOnEmptyVaultMarshalsAsEmptyArray guards against a
// regression where `symdesk meeting list --json` on a vault with zero
// meeting notes produced the JSON literal `null` (Go's zero value for a nil
// slice) instead of `[]`. Swift's Decodable expects a top-level array and
// cannot open an unkeyed container on `null`, so this crashed the Meetings
// tab with a raw decode error on every fresh vault.
func TestMeetingListOnEmptyVaultMarshalsAsEmptyArray(t *testing.T) {
	svc := newTestService(t)

	summaries, err := svc.MeetingList()
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 0 {
		t.Fatalf("expected zero meeting notes in a fresh vault, got %d: %+v", len(summaries), summaries)
	}

	b, err := json.Marshal(summaries)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "[]" {
		t.Errorf("expected MeetingList() to marshal as JSON '[]' for an empty vault, got %q", string(b))
	}
}
