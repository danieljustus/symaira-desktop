package service

import (
	"os"
	"path/filepath"
	"strings"
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

const mockSymmeetScript = `#!/bin/bash
case "$1" in
  capabilities)
    echo '{"tool":"symmeet","version":"1.0.0","schema_version":1,"artifact_schema_versions":[1],"export_formats":["markdown"]}'
    ;;
  meeting)
    if [ "$2" = "show" ]; then
      if [ "$3" = "missing" ]; then
        echo "unknown meeting id" >&2
        exit 2
      fi
      echo '{"schema_version":1,"meeting_id":"m1","source":"imported","created_at":"2026-07-01T10:00:00Z","updated_at":"2026-07-01T10:30:00Z","audio_tracks":[],"language":"en","job":{"job_id":"j1","state":"completed"}}'
    fi
    ;;
  speaker)
    if [ "$2" = "list" ]; then
      echo '{"meeting_id":"m1","speakers":["speaker_0"],"labels":{"speaker_0":"Alice"},"merged_speakers":{}}'
    fi
    ;;
  export)
    printf '%s' "$SYMMEET_TRANSCRIPT"
    ;;
esac
`

func TestMeetingImportSuccess(t *testing.T) {
	dir := t.TempDir()
	writeMockSymmeet(t, dir, mockSymmeetScript)
	withMockSymmeetPath(t, dir)
	t.Setenv("SYMMEET_TRANSCRIPT", "# Transcript\n\nAlice: Hello everyone.\n")

	svc := newTestService(t)
	path, err := svc.MeetingImport("m1")
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if path != filepath.Join("meetings", "meeting-m1.md") {
		t.Errorf("unexpected note path: %s", path)
	}

	content, err := os.ReadFile(filepath.Join(svc.VaultRoot, path)) //nolint:gosec // test reads its own temp vault fixture
	if err != nil {
		t.Fatal(err)
	}
	body := string(content)

	for _, want := range []string{
		"type: meeting",
		"meeting_id: m1",
		"started_at:",
		"duration_ms: 1800000",
		"language: en",
		"label: Alice",
		"speaker_0",
		"review_state: unreviewed",
		"<!-- symmeet-transcript:start -->",
		"Alice: Hello everyone.",
		"<!-- symmeet-transcript:end -->",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected note to contain %q, full content:\n%s", want, body)
		}
	}

	// Must also be indexed and searchable.
	results, err := svc.Search("Hello everyone")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Error("expected the imported meeting note to be searchable")
	}
}

func TestMeetingImportRequiresID(t *testing.T) {
	svc := newTestService(t)
	if _, err := svc.MeetingImport("  "); err == nil {
		t.Error("expected error for blank meeting id")
	}
}

func TestMeetingImportSymmeetUnavailable(t *testing.T) {
	dir := t.TempDir()
	withMockSymmeetPath(t, dir) // empty dir: symmeet is not on PATH

	svc := newTestService(t)
	_, err := svc.MeetingImport("m1")
	if err != ErrSymmeetUnavailable {
		t.Errorf("expected ErrSymmeetUnavailable, got %v", err)
	}
}

func TestMeetingImportIncompatibleSchema(t *testing.T) {
	dir := t.TempDir()
	writeMockSymmeet(t, dir, `#!/bin/bash
if [ "$1" = "capabilities" ]; then
  echo '{"tool":"symmeet","version":"2.0.0","schema_version":2,"artifact_schema_versions":[2],"export_formats":["markdown"]}'
fi
`)
	withMockSymmeetPath(t, dir)

	svc := newTestService(t)
	_, err := svc.MeetingImport("m1")
	if err == nil || !strings.Contains(err.Error(), "artifact schema versions") {
		t.Errorf("expected artifact schema incompatibility error, got %v", err)
	}
}

func TestMeetingImportUnknownMeeting(t *testing.T) {
	dir := t.TempDir()
	writeMockSymmeet(t, dir, mockSymmeetScript)
	withMockSymmeetPath(t, dir)

	svc := newTestService(t)
	_, err := svc.MeetingImport("missing")
	if err == nil || !strings.Contains(err.Error(), "failed to load meeting") {
		t.Errorf("expected a load-failure error for an unknown meeting id, got %v", err)
	}
}

func TestMeetingListAndShow(t *testing.T) {
	dir := t.TempDir()
	writeMockSymmeet(t, dir, mockSymmeetScript)
	withMockSymmeetPath(t, dir)
	t.Setenv("SYMMEET_TRANSCRIPT", "# Transcript\n\nAlice: Hello.\n")

	svc := newTestService(t)
	path, err := svc.MeetingImport("m1")
	if err != nil {
		t.Fatal(err)
	}

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
	if summaries[0].DurationMS != 1800000 {
		t.Errorf("expected duration_ms=1800000, got %d", summaries[0].DurationMS)
	}

	doc, err := svc.MeetingShow(path)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Frontmatter["meeting_id"] != "m1" {
		t.Errorf("unexpected frontmatter: %+v", doc.Frontmatter)
	}
}

func TestAvailableMeetingsExcludesImported(t *testing.T) {
	dir := t.TempDir()
	writeMockSymmeet(t, dir, `#!/bin/bash
case "$1" in
  capabilities)
    echo '{"tool":"symmeet","version":"1.0.0","schema_version":1,"artifact_schema_versions":[1],"export_formats":["markdown"]}'
    ;;
  meeting)
    if [ "$2" = "list" ]; then
      echo '{"meetings":[{"meeting_id":"m1","source":"imported"},{"meeting_id":"m2","source":"recorded"}],"diagnostics":[]}'
    elif [ "$2" = "show" ]; then
      echo '{"schema_version":1,"meeting_id":"m1","source":"imported","created_at":"2026-07-01T10:00:00Z","updated_at":"2026-07-01T10:30:00Z","audio_tracks":[],"language":"en"}'
    fi
    ;;
  speaker)
    echo '{"meeting_id":"m1","speakers":[],"labels":{},"merged_speakers":{}}'
    ;;
  export)
    printf '%s' "$SYMMEET_TRANSCRIPT"
    ;;
esac
`)
	withMockSymmeetPath(t, dir)
	t.Setenv("SYMMEET_TRANSCRIPT", "# Transcript\n")

	svc := newTestService(t)
	if _, err := svc.MeetingImport("m1"); err != nil {
		t.Fatal(err)
	}

	available, err := svc.AvailableMeetings()
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if len(available) != 1 || available[0].MeetingID != "m2" {
		t.Errorf("expected only unimported m2, got %+v", available)
	}
}

func TestAvailableMeetingsSymmeetUnavailable(t *testing.T) {
	dir := t.TempDir()
	withMockSymmeetPath(t, dir) // empty dir: symmeet is not on PATH

	svc := newTestService(t)
	if _, err := svc.AvailableMeetings(); err != ErrSymmeetUnavailable {
		t.Errorf("expected ErrSymmeetUnavailable, got %v", err)
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

func TestMeetingRefreshPreviewDoesNotWrite(t *testing.T) {
	dir := t.TempDir()
	writeMockSymmeet(t, dir, mockSymmeetScript)
	withMockSymmeetPath(t, dir)
	t.Setenv("SYMMEET_TRANSCRIPT", "# Transcript\n\nAlice: Hello.\n")

	svc := newTestService(t)
	path, err := svc.MeetingImport("m1")
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(svc.VaultRoot, path)) //nolint:gosec // test reads its own temp vault fixture
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("SYMMEET_TRANSCRIPT", "# Transcript\n\nAlice: Hello, corrected.\n")
	result, err := svc.MeetingRefresh(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed {
		t.Error("expected the refreshed transcript to be reported as changed")
	}
	if result.Applied {
		t.Error("preview refresh must not apply")
	}
	if len(result.DiffLines) == 0 {
		t.Error("expected non-empty diff lines")
	}

	after, err := os.ReadFile(filepath.Join(svc.VaultRoot, path)) //nolint:gosec // test reads its own temp vault fixture
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("preview refresh must not modify the note on disk")
	}
}

func TestMeetingRefreshApplyPreservesContentOutsideMarkers(t *testing.T) {
	dir := t.TempDir()
	writeMockSymmeet(t, dir, mockSymmeetScript)
	withMockSymmeetPath(t, dir)
	t.Setenv("SYMMEET_TRANSCRIPT", "# Transcript\n\nAlice: Hello.\n")

	svc := newTestService(t)
	path, err := svc.MeetingImport("m1")
	if err != nil {
		t.Fatal(err)
	}
	absPath := filepath.Join(svc.VaultRoot, path)

	// Simulate the user adding their own notes after the transcript block,
	// and manually confirming a participant's entity_id in frontmatter.
	original, err := os.ReadFile(absPath) //nolint:gosec // test reads its own temp vault fixture
	if err != nil {
		t.Fatal(err)
	}
	withManualNote := string(original) + "\n## My follow-up notes\n\nCall Bob back.\n"
	withManualNote = strings.Replace(withManualNote, "review_state: unreviewed", "review_state: reviewed", 1)
	if err := os.WriteFile(absPath, []byte(withManualNote), 0600); err != nil { //nolint:gosec // test writes into its own temp vault fixture
		t.Fatal(err)
	}

	t.Setenv("SYMMEET_TRANSCRIPT", "# Transcript\n\nAlice: Hello, corrected.\n")
	result, err := svc.MeetingRefresh(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied {
		t.Fatal("expected refresh to apply")
	}

	final, err := os.ReadFile(absPath) //nolint:gosec // test reads its own temp vault fixture
	if err != nil {
		t.Fatal(err)
	}
	finalStr := string(final)
	if !strings.Contains(finalStr, "Alice: Hello, corrected.") {
		t.Error("expected the refreshed transcript text to be present")
	}
	if strings.Contains(finalStr, "Alice: Hello.\n\n<!-- symmeet-transcript:end -->") {
		t.Error("expected the old transcript text to be replaced")
	}
	if !strings.Contains(finalStr, "## My follow-up notes") || !strings.Contains(finalStr, "Call Bob back.") {
		t.Error("expected manual notes outside the transcript markers to survive refresh")
	}
	if !strings.Contains(finalStr, "review_state: reviewed") {
		t.Error("expected the manually confirmed review_state to survive refresh")
	}
}

func TestMeetingRefreshMissingMarkersErrors(t *testing.T) {
	dir := t.TempDir()
	writeMockSymmeet(t, dir, mockSymmeetScript)
	withMockSymmeetPath(t, dir)
	t.Setenv("SYMMEET_TRANSCRIPT", "# Transcript\n\nAlice: Hello.\n")

	svc := newTestService(t)
	path, err := svc.MeetingImport("m1")
	if err != nil {
		t.Fatal(err)
	}
	absPath := filepath.Join(svc.VaultRoot, path)
	if err := os.WriteFile(absPath, []byte("---\ntype: meeting\nmeeting_id: m1\ntitle: x\ncreated: x\ntags: []\nsymmeet_source:\n  review_state: unreviewed\n---\n\nNo markers here.\n"), 0600); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.MeetingRefresh(path, false); err == nil {
		t.Error("expected an error when transcript markers are missing")
	}
}

func TestMeetingRefreshSymmeetUnavailable(t *testing.T) {
	dir := t.TempDir()
	writeMockSymmeet(t, dir, mockSymmeetScript)
	withMockSymmeetPath(t, dir)
	t.Setenv("SYMMEET_TRANSCRIPT", "# Transcript\n\nAlice: Hello.\n")

	svc := newTestService(t)
	path, err := svc.MeetingImport("m1")
	if err != nil {
		t.Fatal(err)
	}

	// Remove symmeet from PATH before refreshing.
	t.Setenv("PATH", "/usr/bin:/bin")
	compose.ResetCache()
	if _, err := svc.MeetingRefresh(path, false); err != ErrSymmeetUnavailable {
		t.Errorf("expected ErrSymmeetUnavailable, got %v", err)
	}
}

func TestDiffLines(t *testing.T) {
	old := "a\nb\nc"
	updated := "a\nx\nc"
	diff := diffLines(old, updated)
	joined := strings.Join(diff, "|")
	if !strings.Contains(joined, "-"+" b") {
		t.Errorf("expected removed line 'b', got %v", diff)
	}
	if !strings.Contains(joined, "+"+" x") {
		t.Errorf("expected added line 'x', got %v", diff)
	}
	if !strings.Contains(joined, "  a") || !strings.Contains(joined, "  c") {
		t.Errorf("expected unchanged lines 'a' and 'c', got %v", diff)
	}
}

func TestDiffLinesIdentical(t *testing.T) {
	diff := diffLines("same\ntext", "same\ntext")
	for _, line := range diff {
		if strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") {
			t.Errorf("expected no additions/removals for identical text, got %v", diff)
		}
	}
}
