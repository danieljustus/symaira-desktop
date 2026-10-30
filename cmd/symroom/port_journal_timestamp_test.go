package main

import (
	"bytes"
	"encoding/json"
	"strings"
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
	observed := map[string]int{}
	check := func(initial, final []string) {
		seeded := map[string]bool{}
		for _, content := range initial {
			for _, line := range strings.Split(content, "\n") {
				var ev map[string]any
				if json.Unmarshal([]byte(line), &ev) == nil {
					if id, ok := ev["id"].(string); ok {
						seeded[id] = true
					}
				}
			}
		}
		for _, content := range final {
			for _, line := range strings.Split(content, "\n") {
				var ev map[string]any
				if json.Unmarshal([]byte(line), &ev) != nil {
					continue
				}
				kind, _ := ev["kind"].(string)
				if kind == "" || goStampsJournalKind(kind) {
					continue
				}
				id, _ := ev["id"].(string)
				if seeded[id] {
					continue
				}
				if ev["ts"] != "" {
					t.Fatalf("Go CLI %s event has ts=%v, want empty", kind, ev["ts"])
				}
				observed[kind]++
			}
		}
	}
	for _, c := range approval.Cases {
		var initial, final []string
		for _, f := range c.InitialJournal {
			initial = append(initial, f.Content)
		}
		for _, f := range c.FinalJournal {
			final = append(final, f.Content)
		}
		check(initial, final)
	}
	for _, c := range artifacts.Cases {
		var initial, final []string
		for _, f := range c.InitialJournal {
			initial = append(initial, f.Content)
		}
		for _, f := range c.FinalFiles {
			if strings.HasSuffix(f.Name, ".jsonl") {
				final = append(final, f.Content)
			}
		}
		check(initial, final)
	}
	for _, c := range checkpoints.Cases {
		var initial, final []string
		for _, f := range c.InitialJournal {
			initial = append(initial, f.Content)
		}
		for _, f := range c.FinalFiles {
			if strings.HasSuffix(f.Name, ".jsonl") {
				final = append(final, f.Content)
			}
		}
		check(initial, final)
	}
	for _, kind := range []string{"run.approved", "run.denied", "artifact.linked", "artifact.unlinked", "checkpoint.requested", "checkpoint.resolved"} {
		if observed[kind] == 0 {
			t.Fatalf("no real Go CLI %s event exercised", kind)
		}
	}
}
