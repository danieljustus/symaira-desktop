package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	relationshipsvc "github.com/danieljustus/symaira-relate/internal/service/relationship"
)

// testdata/symmeet/meeting-complete/manifest.json is a verbatim copy of
// symaira-meet's published synthetic fixture
// (Tests/Fixtures/integration/meeting-complete/manifest.json,
// symaira-meet#19) — the real contract this package now parses instead of
// the earlier stopgap shape.

func TestApplyMeetingFixture_RealManifest_SetsMeetingIDAndOccurredAt(t *testing.T) {
	in := &relationshipsvc.MeetingImportInput{}
	status, err := applyMeetingFixture(filepath.Join("testdata", "symmeet", "meeting-complete"), in)
	if err != nil {
		t.Fatalf("applyMeetingFixture() error = %v", err)
	}
	if in.MeetingID != "00000000-0000-0000-0000-000000000001" {
		t.Errorf("MeetingID = %q, want the fixture's meeting_id", in.MeetingID)
	}
	wantOccurred := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
	if !in.OccurredAt.Equal(wantOccurred) {
		t.Errorf("OccurredAt = %v, want %v (fixture created_at)", in.OccurredAt, wantOccurred)
	}
	if status.Source != "imported" || status.ConsentStatus != "required" || status.RetentionPolicy != "keep" {
		t.Errorf("status = %+v, want source=imported consent=required retention=keep", status)
	}
	if in.Title != "" || in.Summary != "" {
		t.Errorf("Title/Summary = %q/%q, want empty — the real manifest carries neither", in.Title, in.Summary)
	}
}

func TestApplyMeetingFixture_ExplicitFieldsAreNotOverridden(t *testing.T) {
	preset := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	in := &relationshipsvc.MeetingImportInput{MeetingID: "explicit-id", OccurredAt: preset}
	if _, err := applyMeetingFixture(filepath.Join("testdata", "symmeet", "meeting-complete"), in); err != nil {
		t.Fatalf("applyMeetingFixture() error = %v", err)
	}
	if in.MeetingID != "explicit-id" {
		t.Errorf("MeetingID = %q, want the explicitly-set id to survive", in.MeetingID)
	}
	if !in.OccurredAt.Equal(preset) {
		t.Errorf("OccurredAt = %v, want the explicitly-set time to survive", in.OccurredAt)
	}
}

func TestApplyMeetingFixture_MissingManifest_ReturnsError(t *testing.T) {
	in := &relationshipsvc.MeetingImportInput{}
	if _, err := applyMeetingFixture(t.TempDir(), in); err == nil {
		t.Error("applyMeetingFixture() error = nil, want an error for a directory with no manifest.json")
	}
}

func TestApplyMeetingFixture_MalformedJSON_IsRejected(t *testing.T) {
	dir := writeManifest(t, "not json")
	in := &relationshipsvc.MeetingImportInput{}
	if _, err := applyMeetingFixture(dir, in); err == nil {
		t.Error("applyMeetingFixture() error = nil, want an error for malformed manifest.json")
	}
}

func TestApplyMeetingFixture_UnsupportedSchemaVersion_IsRejected(t *testing.T) {
	dir := writeManifest(t, `{"schema_version":2,"meeting_id":"m1","created_at":"2026-01-01T00:00:00Z"}`)
	in := &relationshipsvc.MeetingImportInput{}
	if _, err := applyMeetingFixture(dir, in); err == nil {
		t.Error("applyMeetingFixture() error = nil, want an error for an unsupported schema_version")
	}
}

// TestApplyMeetingFixture_RetentionRestrictedState_StillSucceeds covers a
// manifest whose retention policy marks the underlying SymMeet artifact
// for deletion — Relate only ever stores the opaque meeting id, so import
// must succeed and accurately report the restrictive policy rather than
// depending on the artifact surviving.
func TestApplyMeetingFixture_RetentionRestrictedState_StillSucceeds(t *testing.T) {
	dir := writeManifest(t, `{
		"schema_version": 1,
		"meeting_id": "m-restricted",
		"source": "imported",
		"created_at": "2026-01-01T00:00:00Z",
		"consent": {"status": "authorized"},
		"retention": {"policy": "delete_after_export"}
	}`)
	in := &relationshipsvc.MeetingImportInput{}
	status, err := applyMeetingFixture(dir, in)
	if err != nil {
		t.Fatalf("applyMeetingFixture() error = %v, want success even under a restrictive retention policy", err)
	}
	if status.RetentionPolicy != "delete_after_export" {
		t.Errorf("RetentionPolicy = %q, want delete_after_export", status.RetentionPolicy)
	}
	if in.MeetingID != "m-restricted" {
		t.Errorf("MeetingID = %q, want m-restricted", in.MeetingID)
	}
}

func writeManifest(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write manifest.json: %v", err)
	}
	return dir
}
