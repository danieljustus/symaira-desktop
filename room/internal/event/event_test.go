package event

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/danieljustus/symaira-room/internal/identity"
)

func TestCanonicalEncodingAndSigning(t *testing.T) {
	id, err := identity.Generate("alice")
	if err != nil {
		t.Fatalf("failed to generate identity: %v", err)
	}

	fixedTime := time.Date(2026, 7, 27, 18, 52, 3, 123000000, time.UTC)
	ev := &Event{
		V:       CurrentVersion,
		ID:      "ev_01h8abcdef0123456789",
		Room:    "rm_01h8room00000000000",
		Author:  id.MemberID,
		Seq:     42,
		Prev:    "sha256:7c1f000000000000000000000000000000000000000000000000000000000000",
		Lamport: 128,
		TS:      FormatTimestamp(fixedTime),
		Kind:    KindRunApproved,
		Body:    json.RawMessage(`{}`),
	}

	if err := ev.Sign(id); err != nil {
		t.Fatalf("failed to sign event: %v", err)
	}

	// Verify signature passes
	if err := ev.VerifySignature(id.PublicKey); err != nil {
		t.Fatalf("signature verification failed: %v", err)
	}

	// Single flipped byte in any field fails verification
	tampered := *ev
	tampered.Seq = 43
	if err := tampered.VerifySignature(id.PublicKey); err == nil {
		t.Errorf("expected signature verification to fail on tampered seq")
	}

	tampered2 := *ev
	tampered2.TS = "2026-07-27T18:52:03.124Z"
	if err := tampered2.VerifySignature(id.PublicKey); err == nil {
		t.Errorf("expected signature verification to fail on tampered ts")
	}
}

func TestGoldenEncodingsForAllKinds(t *testing.T) {
	id, err := identity.Generate("bob")
	if err != nil {
		t.Fatalf("failed to generate identity: %v", err)
	}

	for kind := range KnownKinds {
		ev := &Event{
			V:       CurrentVersion,
			ID:      "ev_sample",
			Room:    "rm_sample",
			Author:  id.MemberID,
			Seq:     1,
			Prev:    "sha256:0000",
			Lamport: 1,
			TS:      "2026-07-27T18:52:03.123Z",
			Kind:    kind,
			Body:    json.RawMessage(`{}`),
		}

		line1, err := ev.MarshalJSONLine()
		if err != nil {
			t.Fatalf("failed to marshal kind %s: %v", kind, err)
		}

		line2, err := ev.MarshalJSONLine()
		if err != nil {
			t.Fatalf("failed to marshal kind %s: %v", kind, err)
		}

		if string(line1) != string(line2) {
			t.Errorf("encoding for kind %s was not byte-identical across runs", kind)
		}

		parsed, err := UnmarshalJSONLine(line1)
		if err != nil {
			t.Fatalf("failed to unmarshal JSON line for kind %s: %v", kind, err)
		}

		if parsed.Kind != kind {
			t.Errorf("expected kind %s, got %s", kind, parsed.Kind)
		}
	}
}

func TestKnownKindsRegistry(t *testing.T) {
	kinds := []string{
		KindRoomCreated, KindRoomRenamed, KindPolicyChanged,
		KindMemberAdded, KindMemberRemoved, KindMemberRoleChanged,
		KindNotePosted, KindDecisionRecorded, KindArtifactLinked,
		KindArtifactUnlinked, KindArtifactChanged, KindRunRequested,
		KindRunApproved, KindRunDenied, KindRunStarted,
		KindRunFinished, KindRunFailed, KindRunCancelled,
		KindCheckpointReq, KindCheckpointResolved,
	}

	for _, k := range kinds {
		if !KnownKinds[k] {
			t.Errorf("expected kind %s to be in KnownKinds registry", k)
		}
	}
}
