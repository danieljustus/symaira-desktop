package service

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/compose"
)

// mockSymmeetReviewScript extends the base mock with structured segment
// export and speaker mutation commands, recording every speaker mutation
// into $SYMMEET_CALL_LOG for assertion.
const mockSymmeetReviewScript = `#!/bin/bash
case "$1" in
  capabilities)
    echo '{"tool":"symmeet","version":"1.0.0","schema_version":1,"artifact_schema_versions":[1],"export_formats":["markdown","json"]}'
    ;;
  meeting)
    if [ "$2" = "show" ]; then
      echo '{"schema_version":1,"meeting_id":"m1","source":"imported","created_at":"2026-07-01T10:00:00Z","updated_at":"2026-07-01T10:30:00Z","audio_tracks":[],"language":"en","job":{"job_id":"j1","state":"completed"}}'
    fi
    ;;
  speaker)
    echo "$@" >> "$SYMMEET_CALL_LOG"
    if [ "$2" = "list" ]; then
      echo '{"meeting_id":"m1","speakers":["speaker_0","speaker_1"],"labels":{"speaker_0":"Alice"},"merged_speakers":{}}'
    else
      echo '{"meeting_id":"m1","status":"ok"}'
    fi
    ;;
  export)
    for arg in "$@"; do
      if [ "$arg" = "json" ]; then
        echo '{"schema_version":1,"meeting_id":"m1","segment_count":2,"segments":[{"segment_id":"seg-1","track_id":"t1","speaker_id":"speaker_0","start_ms":0,"end_ms":1500,"engine_text":"Hello everyone.","revision":"engine"},{"segment_id":"seg-2","track_id":"t1","speaker_id":"speaker_1","start_ms":1500,"end_ms":4000,"engine_text":"Hi Alice.","edited_text":"Hi, Alice!","revision":"user_corrected"}]}'
        exit 0
      fi
    done
    printf '%s' "$SYMMEET_TRANSCRIPT"
    ;;
esac
`

func importReviewFixtureMeeting(t *testing.T, dir string) (*Service, string) {
	t.Helper()
	writeMockSymmeet(t, dir, mockSymmeetReviewScript)
	withMockSymmeetPath(t, dir)
	t.Setenv("SYMMEET_TRANSCRIPT", "# Transcript\n\nAlice: Hello everyone.\n")
	t.Setenv("SYMMEET_CALL_LOG", dir+"/calls.log")

	svc := newTestService(t)
	path, err := svc.MeetingImport("m1")
	if err != nil {
		t.Fatal(err)
	}
	return svc, path
}

func readCallLog(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(os.Getenv("SYMMEET_CALL_LOG"))
	if err != nil {
		t.Fatalf("no speaker call recorded: %v", err)
	}
	return string(data)
}

func TestMeetingSegmentsReturnsTimeCodedSegments(t *testing.T) {
	svc, path := importReviewFixtureMeeting(t, t.TempDir())

	segments, err := svc.MeetingSegments(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(segments))
	}
	if segments[0].SegmentID != "seg-1" || segments[0].StartMS != 0 || segments[0].EndMS != 1500 {
		t.Errorf("unexpected first segment: %+v", segments[0])
	}
	if segments[1].EditedText != "Hi, Alice!" || segments[1].Revision != "user_corrected" {
		t.Errorf("expected edited second segment, got %+v", segments[1])
	}
}

func TestMeetingSegmentsSymmeetUnavailable(t *testing.T) {
	svc, path := importReviewFixtureMeeting(t, t.TempDir())

	// Drop symmeet off PATH after import: segments must fail with the
	// sentinel error, not corrupt or misreport the note.
	t.Setenv("PATH", "/usr/bin:/bin")
	compose.ResetCache()
	if _, err := svc.MeetingSegments(path); !errors.Is(err, ErrSymmeetUnavailable) {
		t.Fatalf("expected ErrSymmeetUnavailable, got %v", err)
	}
}

func TestMeetingSpeakersListsLabels(t *testing.T) {
	svc, path := importReviewFixtureMeeting(t, t.TempDir())

	speakers, err := svc.MeetingSpeakers(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(speakers) != 2 {
		t.Fatalf("expected 2 speakers, got %d", len(speakers))
	}
	if speakers[0].SpeakerID != "speaker_0" || speakers[0].Label != "Alice" {
		t.Errorf("unexpected first speaker: %+v", speakers[0])
	}
	if speakers[1].Label != "speaker_1" {
		t.Errorf("expected unlabeled speaker to fall back to its ID, got %+v", speakers[1])
	}
}

func TestMeetingSpeakerMutationsInvokeSymmeet(t *testing.T) {
	svc, path := importReviewFixtureMeeting(t, t.TempDir())

	if err := svc.MeetingSpeakerLabel(path, "speaker_1", "Bob"); err != nil {
		t.Fatal(err)
	}
	if err := svc.MeetingSpeakerMerge(path, "speaker_1", "speaker_0"); err != nil {
		t.Fatal(err)
	}
	if err := svc.MeetingSpeakerSplit(path, "speaker_0", "seg-2"); err != nil {
		t.Fatal(err)
	}
	if err := svc.MeetingSpeakerReset(path); err != nil {
		t.Fatal(err)
	}

	log := readCallLog(t)
	for _, want := range []string{
		"speaker label m1 speaker_1 Bob --json",
		"speaker merge m1 speaker_1 speaker_0 --json",
		"speaker split m1 speaker_0 --segment seg-2 --json",
		"speaker reset m1 --json",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("expected symmeet call %q, log:\n%s", want, log)
		}
	}
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
	dir := t.TempDir()
	svc, path := importReviewFixtureMeeting(t, dir)

	// Review state is SymDesk's own; marking reviewed must not need
	// symmeet. Remove the mock symmeet (leaving the rest of PATH, which
	// re-indexing still needs) and mark reviewed.
	if err := os.Remove(dir + "/symmeet"); err != nil {
		t.Fatal(err)
	}
	compose.ResetCache()
	if err := svc.MeetingMarkReviewed(path); err != nil {
		t.Fatal(err)
	}
}

func TestMeetingSegmentsRejectsNonMeetingNote(t *testing.T) {
	svc, _ := importReviewFixtureMeeting(t, t.TempDir())

	if err := os.WriteFile(svc.VaultRoot+"/plain.md", []byte("---\ntitle: Plain\n---\nBody\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.MeetingSegments("plain.md"); err == nil || !strings.Contains(err.Error(), "not a meeting note") {
		t.Fatalf("expected not-a-meeting-note error, got %v", err)
	}
}
