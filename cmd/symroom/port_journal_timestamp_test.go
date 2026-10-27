package main

import (
	"bytes"
	"encoding/json"
	"testing"
)

// These are the Go constructors which explicitly assign wall-clock TS.
func goStampsJournalKind(kind any) bool {
	switch kind {
	case "room.created", "member.added", "note.posted", "decision.recorded":
		return true
	}
	return false
}

func TestJournalTimestampNormalizersPreserveEmptyTS(t *testing.T) {
	for _, kind := range []string{"run.requested", "run.started", "run.cancelled", "run.approved", "run.denied", "artifact.linked", "artifact.unlinked", "checkpoint.requested", "checkpoint.resolved"} {
		for _, normalize := range []func([]byte) ([]byte, error){normalizeArtifactJournal, normalizeCheckpointJournal, func(b []byte) ([]byte, error) { return normalizeRunApprovalCLIEvent(b, false) }} {
			raw, _ := json.Marshal(map[string]any{"kind": kind, "ts": "", "sig": "x", "body": map[string]any{}})
			baseline, err := normalize(raw)
			if err != nil {
				t.Fatal(err)
			}
			changed := bytes.Replace(raw, []byte(`"ts":""`), []byte(`"ts":"2026-01-01T00:00:00Z"`), 1)
			mutant, err := normalize(changed)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(baseline, mutant) || !bytes.Contains(baseline, []byte(`"ts":""`)) {
				t.Fatalf("%s: timestamp mismatch was masked", kind)
			}
		}
	}
}

func TestGoCLIJournalTimestamps(t *testing.T) {
	// Exercise the actual Go CLI generators without updating committed fixtures.
	root := noteCLIRoot(t)
	approval, err := makeRunApprovalCLIContract(t, root)
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := makeArtifactCLIContract(t, root)
	if err != nil {
		t.Fatal(err)
	}
	checkpoints, err := makeCheckpointCLIContract(t, root)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal([]any{approval, artifacts, checkpoints})
	if err != nil {
		t.Fatal(err)
	}
	// The constructors preserve empty TS; prove real recorded events reach this assertion.
	if !bytes.Contains(raw, []byte(`\"ts\":\"\"`)) {
		t.Fatal("no empty-ts Go CLI journal event was exercised")
	}
}
