package compose

import (
	"strings"
	"testing"
)

func TestHasSymmeetShorthand(t *testing.T) {
	dir := t.TempDir()
	writeMockTool(t, dir, "symmeet", `#!/bin/bash
echo '{"tool":"symmeet","version":"1.0.0","schema_version":1}'
`)
	withMockPath(t, dir)

	if ok, ver := HasSymmeet(); !ok || ver != "1.0.0" {
		t.Errorf("HasSymmeet: expected true/1.0.0, got %v/%s", ok, ver)
	}
}

func TestGetSymmeetCapabilitiesSuccess(t *testing.T) {
	dir := t.TempDir()
	writeMockTool(t, dir, "symmeet", `#!/bin/bash
if [ "$1" = "capabilities" ]; then
  echo '{"tool":"symmeet","version":"1.2.3","schema_version":1,"artifact_schema_versions":[1],"export_formats":["markdown","txt","json"]}'
fi
`)
	withMockPath(t, dir)

	caps, err := GetSymmeetCapabilities()
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if caps.Version != "1.2.3" || !caps.SupportsArtifactSchema() {
		t.Errorf("unexpected capabilities: %+v", caps)
	}
}

func TestGetSymmeetCapabilitiesIncompatibleSchema(t *testing.T) {
	dir := t.TempDir()
	writeMockTool(t, dir, "symmeet", `#!/bin/bash
echo '{"tool":"symmeet","version":"2.0.0","schema_version":2,"artifact_schema_versions":[2],"export_formats":["markdown"]}'
`)
	withMockPath(t, dir)

	caps, err := GetSymmeetCapabilities()
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if caps.SupportsArtifactSchema() {
		t.Errorf("expected schema version 1 to be unsupported by %v", caps.ArtifactSchemaVersions)
	}
}

func TestGetSymmeetCapabilitiesMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	writeMockTool(t, dir, "symmeet", `#!/bin/bash
echo 'not json'
`)
	withMockPath(t, dir)

	if _, err := GetSymmeetCapabilities(); err == nil || !strings.Contains(err.Error(), "unmarshal") {
		t.Errorf("expected unmarshal error, got %v", err)
	}
}

func TestGetSymmeetCapabilitiesTimeout(t *testing.T) {
	dir := t.TempDir()
	writeMockTool(t, dir, "symmeet", `#!/bin/bash
sleep 10
`)
	withMockPath(t, dir)

	_, err := GetSymmeetCapabilities()
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("expected timeout error, got %v", err)
	}
}

func TestListMeetingsSuccess(t *testing.T) {
	dir := t.TempDir()
	writeMockTool(t, dir, "symmeet", `#!/bin/bash
if [ "$1" = "meeting" ] && [ "$2" = "list" ]; then
  echo '{"meetings":[{"meeting_id":"00000000-0000-0000-0000-000000000001","source":"imported"}],"diagnostics":[]}'
fi
`)
	withMockPath(t, dir)

	meetings, err := ListMeetings()
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if len(meetings) != 1 || meetings[0].MeetingID != "00000000-0000-0000-0000-000000000001" {
		t.Errorf("unexpected meetings: %+v", meetings)
	}
}

func TestShowMeetingSuccessWithISOTimestamps(t *testing.T) {
	dir := t.TempDir()
	writeMockTool(t, dir, "symmeet", `#!/bin/bash
if [ "$1" = "meeting" ] && [ "$2" = "show" ]; then
  echo '{"schema_version":1,"meeting_id":"m1","source":"imported","created_at":"2026-07-01T10:00:00Z","updated_at":"2026-07-01T10:30:00Z","audio_tracks":[],"language":"en","job":{"job_id":"j1","state":"completed"}}'
fi
`)
	withMockPath(t, dir)

	manifest, err := ShowMeeting("m1")
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if manifest.MeetingID != "m1" || manifest.Language != "en" {
		t.Errorf("unexpected manifest: %+v", manifest)
	}
	wantDuration := manifest.UpdatedAt.Sub(manifest.CreatedAt.Time)
	if wantDuration != 30*60*1e9 {
		t.Errorf("expected 30m duration between timestamps, got %v", wantDuration)
	}
}

func TestShowMeetingAcceptsEpochTimestamps(t *testing.T) {
	dir := t.TempDir()
	writeMockTool(t, dir, "symmeet", `#!/bin/bash
echo '{"schema_version":1,"meeting_id":"m1","source":"imported","created_at":1751365200,"updated_at":1751367000,"audio_tracks":[],"language":"en"}'
`)
	withMockPath(t, dir)

	manifest, err := ShowMeeting("m1")
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if manifest.CreatedAt.IsZero() {
		t.Errorf("expected non-zero created_at parsed from epoch seconds")
	}
}

func TestShowMeetingUnsupportedSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	writeMockTool(t, dir, "symmeet", `#!/bin/bash
echo '{"schema_version":99,"meeting_id":"m1","source":"imported","created_at":"2026-07-01T10:00:00Z","updated_at":"2026-07-01T10:00:00Z"}'
`)
	withMockPath(t, dir)

	if _, err := ShowMeeting("m1"); err == nil || !strings.Contains(err.Error(), "unsupported meeting artifact schema version") {
		t.Errorf("expected unsupported schema version error, got %v", err)
	}
}

func TestShowMeetingNotFoundExitCode(t *testing.T) {
	dir := t.TempDir()
	writeMockTool(t, dir, "symmeet", `#!/bin/bash
echo "unknown meeting id" >&2
exit 2
`)
	withMockPath(t, dir)

	_, err := ShowMeeting("missing")
	if err == nil {
		t.Fatal("expected an error for an unknown meeting id")
	}
	symErr, ok := err.(*SymmeetError)
	if !ok {
		t.Fatalf("expected *SymmeetError, got %T: %v", err, err)
	}
	if !symErr.IsNotFound() {
		t.Errorf("expected IsNotFound()=true for exit code %d", symErr.ExitCode)
	}
}

func TestShowMeetingTrashedExitCode(t *testing.T) {
	dir := t.TempDir()
	writeMockTool(t, dir, "symmeet", `#!/bin/bash
echo "meeting is in trash" >&2
exit 3
`)
	withMockPath(t, dir)

	_, err := ShowMeeting("trashed")
	symErr, ok := err.(*SymmeetError)
	if !ok {
		t.Fatalf("expected *SymmeetError, got %T: %v", err, err)
	}
	if !symErr.IsPermissionDenied() {
		t.Errorf("expected IsPermissionDenied()=true for exit code %d", symErr.ExitCode)
	}
}

func TestShowMeetingTransientExitCode(t *testing.T) {
	dir := t.TempDir()
	writeMockTool(t, dir, "symmeet", `#!/bin/bash
echo "disk full" >&2
exit 1
`)
	withMockPath(t, dir)

	_, err := ShowMeeting("m1")
	symErr, ok := err.(*SymmeetError)
	if !ok {
		t.Fatalf("expected *SymmeetError, got %T: %v", err, err)
	}
	if !symErr.IsTransient() {
		t.Errorf("expected IsTransient()=true for exit code %d", symErr.ExitCode)
	}
	if !strings.Contains(symErr.Stderr, "disk full") {
		t.Errorf("expected stderr to be preserved, got %q", symErr.Stderr)
	}
}

func TestListSpeakersLabelsFallBackToID(t *testing.T) {
	dir := t.TempDir()
	writeMockTool(t, dir, "symmeet", `#!/bin/bash
echo '{"meeting_id":"m1","speakers":["speaker_0","speaker_1"],"labels":{"speaker_0":"Alice"},"merged_speakers":{}}'
`)
	withMockPath(t, dir)

	speakers, err := ListSpeakers("m1")
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if len(speakers) != 2 {
		t.Fatalf("expected 2 speakers, got %d", len(speakers))
	}
	byID := map[string]string{}
	for _, s := range speakers {
		byID[s.SpeakerID] = s.Label
	}
	if byID["speaker_0"] != "Alice" {
		t.Errorf("expected labeled speaker_0=Alice, got %q", byID["speaker_0"])
	}
	if byID["speaker_1"] != "speaker_1" {
		t.Errorf("expected unlabeled speaker_1 to fall back to its ID, got %q", byID["speaker_1"])
	}
}

func TestExportMeetingMarkdownSuccessAndFailure(t *testing.T) {
	dir := t.TempDir()
	writeMockTool(t, dir, "symmeet", `#!/bin/bash
if [ "$1" = "export" ]; then
  if [ "$EXPORT_FAIL" = "1" ]; then
    echo "export-boom" >&2
    exit 1
  fi
  echo "# Transcript"
fi
`)
	withMockPath(t, dir)

	out, err := ExportMeetingMarkdown("m1")
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if !strings.Contains(out, "# Transcript") {
		t.Errorf("unexpected transcript output: %q", out)
	}

	t.Setenv("EXPORT_FAIL", "1")
	if _, err := ExportMeetingMarkdown("m1"); err == nil || !strings.Contains(err.Error(), "export-boom") {
		t.Errorf("expected wrapped export failure, got %v", err)
	}
}
