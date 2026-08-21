package service

import (
	"os"
	"testing"
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
	svc := newTestService(t)
	relPath := "meetings/meeting-m1.md"
	writeMeetingNoteFixture(t, svc, relPath, meetingNoteUnknownFieldsFixture)
	return svc, relPath
}

func readCallLog(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(os.Getenv("SYMMEET_CALL_LOG")) //nolint:gosec // test-owned temp path
	if err != nil {
		t.Fatalf("no speaker call recorded: %v", err)
	}
	return string(data)
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
