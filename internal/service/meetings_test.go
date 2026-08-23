package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/compose"
)

func writeMockSymmeet(t *testing.T, dir, script string) {
	t.Helper()
	path := filepath.Join(dir, "symmeet")
	if err := os.WriteFile(path, []byte(script), 0755); err != nil { //nolint:gosec // test fixture must be executable
		t.Fatal(err)
	}
}

func withMockSymmeetPath(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, "symmeet")); os.IsNotExist(err) {
		t.Setenv("PATH", dir)
	} else {
		t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	compose.ResetCache()
	t.Cleanup(compose.ResetCache)
}

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
