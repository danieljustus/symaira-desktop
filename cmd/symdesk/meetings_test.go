package main

import (
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
	// `meeting import` was removed in 2b (the app now owns symmeet
	// import/review). The participant/publish CLI tests seed a vault-based
	// meeting note fixture directly into the vault the caller configured.
	vaultDir := cfg.Vault
	relPath := "meetings/meeting-m1.md"
	if err := os.MkdirAll(filepath.Join(vaultDir, "meetings"), 0750); err != nil {
		t.Fatal(err)
	}
	content := `---
type: meeting
title: Meeting 2026-07-01 10:00
created: 2026-07-01T10:00:00Z
tags:
  - meeting
meeting_id: m1
started_at: 2026-07-01T10:00:00Z
symmeet_source:
  artifact_schema_version: 1
  review_state: reviewed
participants:
  - label: Alice
    speaker_ids:
      - speaker_0
---

<!-- symmeet-transcript:start -->
Alice: Hello everyone.
<!-- symmeet-transcript:end -->
`
	if err := os.WriteFile(filepath.Join(vaultDir, relPath), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return relPath
}

const mockSymmemoryScriptForCLI = `#!/bin/bash
if [ "$1" = "entity" ] && [ "$2" = "list" ]; then
  echo '[{"id":"e-alice","name":"Alice Example","type":"person","aliases":["Alice"],"description":""}]'
elif [ "$1" = "entity" ] && [ "$2" = "show" ]; then
  if [ "$3" = "Carol New" ]; then
    echo '{"id":"e-carol","name":"Carol New","type":"person","aliases":[],"description":""}'
  else
    echo '{"id":"e-alice","name":"Alice Example","type":"person","aliases":["Alice"],"description":""}'
  fi
elif [ "$1" = "entity" ] && [ "$2" = "add" ]; then
  echo 'ok'
elif [ "$1" = "entity" ] && [ "$2" = "relate" ]; then
  echo 'ok'
elif [ "$1" = "set" ]; then
  echo '{"id":"mem-1","content":"decision","scope":"project","entities":["Meeting m1"]}'
fi
`

const mockSymrelateScriptForCLI = `#!/bin/bash
if [ "$1" = "contact" ] && [ "$2" = "ref" ]; then
  case "$3" in
    c-ada)
      echo '{"provider":"symrelate","schema_version":1,"id":"c-ada","kind":"person","display_name":"Ada Lovelace"}'
      exit 0 ;;
    *)
      echo 'symrelate: contact not found' >&2
      exit 1 ;;
  esac
fi
`

func installMockSymmemory(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "symmemory"), []byte(mockSymmemoryScriptForCLI), 0755); err != nil { //nolint:gosec // test fixture must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	compose.ResetCache()
	t.Cleanup(compose.ResetCache)
}

func installMockSymrelate(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "symrelate"), []byte(mockSymrelateScriptForCLI), 0755); err != nil { //nolint:gosec // test fixture must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	compose.ResetCache()
	t.Cleanup(compose.ResetCache)
}

func TestMeetingParticipantCandidatesAndConfirmCLI(t *testing.T) {
	setupReviewMeetingVault(t)
	path := importMeetingViaCLI(t)
	installMockSymmemory(t)

	candOut, err := execRootCapture(t, "", "meeting", "participant", "candidates", "Alice", "--json")
	if err != nil {
		t.Fatalf("candidates failed: %v (output: %s)", err, candOut)
	}
	if !strings.Contains(candOut, "e-alice") || !strings.Contains(candOut, "match_reason") {
		t.Errorf("expected candidate JSON with match reason, got %q", candOut)
	}

	confirmOut, err := execRootCapture(t, "", "meeting", "participant", "confirm", path, "speaker_0", "e-alice", "--json")
	if err != nil {
		t.Fatalf("confirm failed: %v (output: %s)", err, confirmOut)
	}
	if !strings.Contains(confirmOut, "confirmed") {
		t.Errorf("expected confirmed status, got %q", confirmOut)
	}

	unlinkOut, err := execRootCapture(t, "", "meeting", "participant", "confirm", path, "speaker_0", "--json")
	if err != nil {
		t.Fatalf("unlink failed: %v (output: %s)", err, unlinkOut)
	}
	if !strings.Contains(unlinkOut, "unlinked") {
		t.Errorf("expected unlinked status, got %q", unlinkOut)
	}
}

func TestMeetingParticipantCreateCLI(t *testing.T) {
	setupReviewMeetingVault(t)
	path := importMeetingViaCLI(t)
	installMockSymmemory(t)

	createOut, err := execRootCapture(t, "", "meeting", "participant", "create", path, "speaker_0", "Carol New", "--json")
	if err != nil {
		t.Fatalf("create failed: %v (output: %s)", err, createOut)
	}
	if !strings.Contains(createOut, "created") || !strings.Contains(createOut, "Carol New") {
		t.Errorf("expected created status with Carol New, got %q", createOut)
	}
}

func TestMeetingParticipantContactCommandsCLI(t *testing.T) {
	setupReviewMeetingVault(t)
	path := importMeetingViaCLI(t)
	installMockSymrelate(t)

	contactOut, err := execRootCapture(t, "", "meeting", "participant", "contact", "c-ada", "--json")
	if err != nil {
		t.Fatalf("contact failed: %v (output: %s)", err, contactOut)
	}
	if !strings.Contains(contactOut, "Ada Lovelace") {
		t.Errorf("expected contact info with Ada Lovelace, got %q", contactOut)
	}

	linkOut, err := execRootCapture(t, "", "meeting", "participant", "link-contact", path, "speaker_0", "c-ada", "--json")
	if err != nil {
		t.Fatalf("link-contact failed: %v (output: %s)", err, linkOut)
	}
	if !strings.Contains(linkOut, "linked") || !strings.Contains(linkOut, "c-ada") {
		t.Errorf("expected linked status for c-ada, got %q", linkOut)
	}

	unlinkOut, err := execRootCapture(t, "", "meeting", "participant", "unlink-contact", path, "speaker_0", "--json")
	if err != nil {
		t.Fatalf("unlink-contact failed: %v (output: %s)", err, unlinkOut)
	}
	if !strings.Contains(unlinkOut, "unlinked") {
		t.Errorf("expected unlinked status, got %q", unlinkOut)
	}
}

func TestMeetingPublishCLI(t *testing.T) {
	setupReviewMeetingVault(t)
	path := importMeetingViaCLI(t)
	installMockSymmemory(t)

	if _, err := execRootCapture(t, "", "meeting", "participant", "confirm", path, "speaker_0", "e-alice", "--json"); err != nil {
		t.Fatalf("confirm failed: %v", err)
	}

	pubOut, err := execRootCapture(t, "", "meeting", "publish", path, "--fact", "Decision: ship the beta.", "--json")
	if err != nil {
		t.Fatalf("publish failed: %v (output: %s)", err, pubOut)
	}
	if !strings.Contains(pubOut, "mem-1") || !strings.Contains(pubOut, "relations_created\":1") {
		t.Errorf("expected publish result with fact id and relation count, got %q", pubOut)
	}

	// Idempotency: re-applying the same fact must skip, not duplicate.
	pubOut2, err := execRootCapture(t, "", "meeting", "publish", path, "--fact", "Decision: ship the beta.", "--json")
	if err != nil {
		t.Fatalf("repeat publish failed: %v (output: %s)", err, pubOut2)
	}
	if !strings.Contains(pubOut2, "facts_skipped\":1") {
		t.Errorf("expected repeat apply to skip the fact, got %q", pubOut2)
	}
}
