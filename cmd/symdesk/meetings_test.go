package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/compose"
	"github.com/danieljustus/symaira-desktop/internal/config"
)

const mockSymmeetScriptForCLI = `#!/bin/bash
case "$1" in
  capabilities)
    echo '{"tool":"symmeet","version":"1.0.0","schema_version":1,"artifact_schema_versions":[1],"export_formats":["markdown"]}'
    ;;
  meeting)
    if [ "$2" = "show" ]; then
      echo '{"schema_version":1,"meeting_id":"m1","source":"imported","created_at":"2026-07-01T10:00:00Z","updated_at":"2026-07-01T10:30:00Z","audio_tracks":[],"language":"en"}'
    fi
    ;;
  speaker)
    echo '{"meeting_id":"m1","speakers":[],"labels":{},"merged_speakers":{}}'
    ;;
  export)
    printf '# Transcript\n\nHello.\n'
    ;;
esac
`

func setupMeetingVault(t *testing.T) string {
	t.Helper()
	vaultDir := t.TempDir()
	origCfg := cfg
	cfg = &config.Config{Vault: vaultDir}
	t.Cleanup(func() { cfg = origCfg })

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "symmeet"), []byte(mockSymmeetScriptForCLI), 0755); err != nil { //nolint:gosec // test fixture must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	compose.ResetCache()
	t.Cleanup(compose.ResetCache)

	return vaultDir
}

func TestMeetingImportListShowCLI(t *testing.T) {
	setupMeetingVault(t)

	out, err := execRootCapture(t, "", "meeting", "import", "m1", "--json")
	if err != nil {
		t.Fatalf("import failed: %v (output: %s)", err, out)
	}
	var importResult map[string]string
	if err := json.Unmarshal([]byte(out), &importResult); err != nil {
		t.Fatalf("failed to parse import output %q: %v", out, err)
	}
	path := importResult["path"]
	if path == "" {
		t.Fatal("expected a non-empty imported note path")
	}

	listOut, err := execRootCapture(t, "", "meeting", "list", "--json")
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if !strings.Contains(listOut, "m1") {
		t.Errorf("expected meeting list output to contain the meeting id, got %q", listOut)
	}

	showOut, err := execRootCapture(t, "", "meeting", "show", path, "--json")
	if err != nil {
		t.Fatalf("show failed: %v", err)
	}
	if !strings.Contains(showOut, "meeting_id") {
		t.Errorf("expected show output to contain frontmatter, got %q", showOut)
	}
}

func TestMeetingRefreshPreviewCLI(t *testing.T) {
	setupMeetingVault(t)

	out, err := execRootCapture(t, "", "meeting", "import", "m1", "--json")
	if err != nil {
		t.Fatalf("import failed: %v (output: %s)", err, out)
	}
	var importResult map[string]string
	if err := json.Unmarshal([]byte(out), &importResult); err != nil {
		t.Fatal(err)
	}
	path := importResult["path"]

	refreshOut, err := execRootCapture(t, "", "meeting", "refresh", path, "--json")
	if err != nil {
		t.Fatalf("refresh failed: %v", err)
	}
	var refreshResult struct {
		Changed bool `json:"changed"`
		Applied bool `json:"applied"`
	}
	if err := json.Unmarshal([]byte(refreshOut), &refreshResult); err != nil {
		t.Fatalf("failed to parse refresh output %q: %v", refreshOut, err)
	}
	if refreshResult.Applied {
		t.Error("expected refresh without --apply to not apply changes")
	}
}

func TestMeetingImportRejectsMissingArg(t *testing.T) {
	setupMeetingVault(t)
	if _, err := execRootCapture(t, "", "meeting", "import"); err == nil {
		t.Error("expected an error for missing meeting id argument")
	}
}

func TestMeetingAvailableCLI(t *testing.T) {
	vaultDir := t.TempDir()
	origCfg := cfg
	cfg = &config.Config{Vault: vaultDir}
	t.Cleanup(func() { cfg = origCfg })

	dir := t.TempDir()
	script := `#!/bin/bash
case "$1" in
  capabilities)
    echo '{"tool":"symmeet","version":"1.0.0","schema_version":1,"artifact_schema_versions":[1],"export_formats":["markdown"]}'
    ;;
  meeting)
    if [ "$2" = "list" ]; then
      echo '{"meetings":[{"meeting_id":"m1","source":"recorded"}],"diagnostics":[]}'
    fi
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "symmeet"), []byte(script), 0755); err != nil { //nolint:gosec // test fixture must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	compose.ResetCache()
	t.Cleanup(compose.ResetCache)

	out, err := execRootCapture(t, "", "meeting", "available", "--json")
	if err != nil {
		t.Fatalf("available failed: %v (output: %s)", err, out)
	}
	if !strings.Contains(out, "m1") {
		t.Errorf("expected available output to contain the unimported meeting id, got %q", out)
	}
}

func TestMeetingAvailableCLISymmeetUnavailable(t *testing.T) {
	vaultDir := t.TempDir()
	origCfg := cfg
	cfg = &config.Config{Vault: vaultDir}
	t.Cleanup(func() { cfg = origCfg })

	t.Setenv("PATH", "/usr/bin:/bin")
	compose.ResetCache()
	t.Cleanup(compose.ResetCache)

	if _, err := execRootCapture(t, "", "meeting", "available", "--json"); err == nil {
		t.Error("expected an error when symmeet is not on PATH")
	}
}

const mockSymmeetReviewScriptForCLI = `#!/bin/bash
case "$1" in
  capabilities)
    echo '{"tool":"symmeet","version":"1.0.0","schema_version":1,"artifact_schema_versions":[1],"export_formats":["markdown","json"]}'
    ;;
  meeting)
    if [ "$2" = "show" ]; then
      echo '{"schema_version":1,"meeting_id":"m1","source":"imported","created_at":"2026-07-01T10:00:00Z","updated_at":"2026-07-01T10:30:00Z","audio_tracks":[],"language":"en"}'
    fi
    ;;
  speaker)
    if [ "$2" = "list" ]; then
      echo '{"meeting_id":"m1","speakers":["speaker_0"],"labels":{"speaker_0":"Alice"},"merged_speakers":{}}'
    else
      echo '{"meeting_id":"m1","status":"ok"}'
    fi
    ;;
  export)
    for arg in "$@"; do
      if [ "$arg" = "json" ]; then
        echo '{"schema_version":1,"meeting_id":"m1","segment_count":1,"segments":[{"segment_id":"seg-1","track_id":"t1","speaker_id":"speaker_0","start_ms":0,"end_ms":1200,"engine_text":"Hello.","revision":"engine"}]}'
        exit 0
      fi
    done
    printf '# Transcript\n\nHello.\n'
    ;;
esac
`

func setupReviewMeetingVault(t *testing.T) string {
	t.Helper()
	vaultDir := t.TempDir()
	origCfg := cfg
	cfg = &config.Config{Vault: vaultDir}
	t.Cleanup(func() { cfg = origCfg })

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "symmeet"), []byte(mockSymmeetReviewScriptForCLI), 0755); err != nil { //nolint:gosec // test fixture must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	compose.ResetCache()
	t.Cleanup(compose.ResetCache)

	return vaultDir
}

func importMeetingViaCLI(t *testing.T) string {
	t.Helper()
	out, err := execRootCapture(t, "", "meeting", "import", "m1", "--json")
	if err != nil {
		t.Fatalf("import failed: %v (output: %s)", err, out)
	}
	var importResult map[string]string
	if err := json.Unmarshal([]byte(out), &importResult); err != nil {
		t.Fatalf("failed to parse import output %q: %v", out, err)
	}
	return importResult["path"]
}

func TestMeetingSegmentsAndSpeakersCLI(t *testing.T) {
	setupReviewMeetingVault(t)
	path := importMeetingViaCLI(t)

	segOut, err := execRootCapture(t, "", "meeting", "segments", path, "--json")
	if err != nil {
		t.Fatalf("segments failed: %v (output: %s)", err, segOut)
	}
	if !strings.Contains(segOut, "seg-1") || !strings.Contains(segOut, "start_ms") {
		t.Errorf("expected segment JSON, got %q", segOut)
	}

	spkOut, err := execRootCapture(t, "", "meeting", "speakers", path, "--json")
	if err != nil {
		t.Fatalf("speakers failed: %v (output: %s)", err, spkOut)
	}
	if !strings.Contains(spkOut, "speaker_0") || !strings.Contains(spkOut, "Alice") {
		t.Errorf("expected speaker JSON, got %q", spkOut)
	}
}

func TestMeetingSpeakerLabelAndReviewCLI(t *testing.T) {
	setupReviewMeetingVault(t)
	path := importMeetingViaCLI(t)

	labelOut, err := execRootCapture(t, "", "meeting", "speaker", "label", path, "speaker_0", "Bob", "--json")
	if err != nil {
		t.Fatalf("speaker label failed: %v (output: %s)", err, labelOut)
	}
	if !strings.Contains(labelOut, "labeled") {
		t.Errorf("expected labeled status, got %q", labelOut)
	}

	reviewOut, err := execRootCapture(t, "", "meeting", "review", path, "--json")
	if err != nil {
		t.Fatalf("review failed: %v (output: %s)", err, reviewOut)
	}
	if !strings.Contains(reviewOut, "reviewed") {
		t.Errorf("expected reviewed state, got %q", reviewOut)
	}

	showOut, err := execRootCapture(t, "", "meeting", "show", path, "--json")
	if err != nil {
		t.Fatalf("show failed: %v", err)
	}
	if !strings.Contains(showOut, "reviewed") {
		t.Errorf("expected show output to reflect reviewed state, got %q", showOut)
	}
}

func TestMeetingSpeakerSplitRequiresSegmentCLI(t *testing.T) {
	setupReviewMeetingVault(t)
	path := importMeetingViaCLI(t)

	if _, err := execRootCapture(t, "", "meeting", "speaker", "split", path, "speaker_0", "--json"); err == nil || !strings.Contains(err.Error(), "--segment is required") {
		t.Errorf("expected --segment requirement error, got %v", err)
	}
}
