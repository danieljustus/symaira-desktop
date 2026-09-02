package service

import (
	"testing"
)

func importReviewFixtureMeeting(t *testing.T, dir string) (*Service, string) {
	t.Helper()
	svc := newTestService(t)
	relPath := "meetings/meeting-m1.md"
	writeMeetingNoteFixture(t, svc, relPath, meetingNoteUnknownFieldsFixture)
	return svc, relPath
}

func TestMeetingMarkReviewedSetsStateAndSnapshotsHistory(t *testing.T) {
	svc, path := importReviewFixtureMeeting(t, t.TempDir())

	if err := svc.MeetingMarkReviewed(path); err != nil {
		t.Fatal(err)
	}

	doc, err := svc.MeetingShow(path)
	if err != nil {
		t.Fatal(err)
	}
	src, _ := doc.Frontmatter["symmeet_source"].(map[string]interface{})
	if src["review_state"] != "reviewed" {
		t.Errorf("expected review_state reviewed, got %v", src["review_state"])
	}

	// The pre-save content must be recoverable from history.
	entries, err := svc.HistoryList(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Error("expected a history snapshot from the review save")
	}
}

func TestMeetingMarkReviewedWorksWithSymmeetAbsent(t *testing.T) {
	svc, path := importReviewFixtureMeeting(t, t.TempDir())

	// Review state is SymDesk's own; marking reviewed must not need
	// symmeet at all (no artifact-backed operations are involved).
	if err := svc.MeetingMarkReviewed(path); err != nil {
		t.Fatal(err)
	}
}
