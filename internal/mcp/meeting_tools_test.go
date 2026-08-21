package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/compose"
	"github.com/danieljustus/symaira-desktop/internal/service"
)

const mockSymmeetScriptForMCP = `#!/bin/bash
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

func withMockSymmeetOnPath(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "symmeet"), []byte(mockSymmeetScriptForMCP), 0755); err != nil { //nolint:gosec // test fixture must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	compose.ResetCache()
	t.Cleanup(compose.ResetCache)
}

func TestMeetingListAndGetTools(t *testing.T) {
	factory := testFactory(t)

	// Create a meeting note in the vault so list/get have something to return.
	svc, db, err := factory()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	abs := filepath.Join(svc.VaultRoot, "meetings")
	if err := os.MkdirAll(abs, 0750); err != nil {
		t.Fatal(err)
	}
	note := `---
type: meeting
title: Meeting 2026-07-21 10:00
created: 2026-07-21T10:00:00Z
tags:
  - meeting
meeting_id: m-fixture
started_at: 2026-07-21T10:00:00Z
symmeet_source:
  artifact_schema_version: 1
  review_state: reviewed
---

<!-- symmeet-transcript:start -->
Alice: Hello everyone.
<!-- symmeet-transcript:end -->
`
	if err := os.WriteFile(filepath.Join(svc.VaultRoot, "meetings/meeting-m1.md"), []byte(note), 0600); err != nil {
		t.Fatal(err)
	}

	listTool := newMeetingListTool(factory)
	listOut, err := listTool.Handler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	summaries, ok := listOut.([]service.MeetingNoteSummary)
	if !ok || len(summaries) != 1 {
		t.Errorf("unexpected meeting_list result: %#v", listOut)
	}

	getTool := newMeetingGetTool(factory)
	if _, err := getTool.Handler(context.Background(), json.RawMessage(`{"path":"meetings/meeting-m1.md"}`)); err != nil {
		t.Fatal(err)
	}
}

func TestMeetingGetToolRequiresPath(t *testing.T) {
	tool := newMeetingGetTool(testFactory(t))
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Error("expected an error for missing path")
	}
}
