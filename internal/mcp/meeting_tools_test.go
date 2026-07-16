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

func TestMeetingImportToolRequiresMeetingID(t *testing.T) {
	tool := newMeetingImportTool(testFactory(t))
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Error("expected an error for missing meeting_id")
	}
}

func TestMeetingImportToolAndFollowUpTools(t *testing.T) {
	withMockSymmeetOnPath(t)
	factory := testFactory(t)

	importTool := newMeetingImportTool(factory)
	out, err := importTool.Handler(context.Background(), json.RawMessage(`{"meeting_id":"m1"}`))
	if err != nil {
		t.Fatal(err)
	}
	path := out.(map[string]string)["path"]
	if path == "" {
		t.Fatal("expected a non-empty imported note path")
	}

	listTool := newMeetingListTool(factory)
	listOut, err := listTool.Handler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	summaries, ok := listOut.([]service.MeetingNoteSummary)
	if !ok || len(summaries) != 1 || summaries[0].Path != path {
		t.Errorf("unexpected meeting_list result: %#v", listOut)
	}

	getTool := newMeetingGetTool(factory)
	if _, err := getTool.Handler(context.Background(), json.RawMessage(`{"path":"`+path+`"}`)); err != nil {
		t.Fatal(err)
	}
}

func TestMeetingGetToolRequiresPath(t *testing.T) {
	tool := newMeetingGetTool(testFactory(t))
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Error("expected an error for missing path")
	}
}
