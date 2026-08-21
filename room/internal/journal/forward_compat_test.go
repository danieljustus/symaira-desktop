package journal

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/danieljustus/symaira-room/internal/event"
)

func TestForwardCompatibilityUnknownEventKind(t *testing.T) {
	tempDir := t.TempDir()
	ownerID, j := createTestRoom(t, tempDir)

	// Create a synthetic unknown event kind from the future
	unknownKind := "future.quantum_checkpoint_v2"
	bodyBytes := json.RawMessage(`{"future_field":"value_123"}`)

	evUnknown := &event.Event{
		V:       1,
		ID:      "ev_future_1",
		Room:    "rm_test",
		Author:  ownerID.MemberID,
		Seq:     2,
		Prev:    "sha256:0000",
		Lamport: 2,
		TS:      event.FormatTimestamp(time.Now()),
		Kind:    unknownKind,
		Body:    bodyBytes,
	}

	if err := j.PrepareEvent(evUnknown); err != nil {
		t.Fatalf("failed to prepare unknown event: %v", err)
	}
	if err := evUnknown.Sign(ownerID); err != nil {
		t.Fatalf("failed to sign unknown event: %v", err)
	}

	rawLineBefore, err := evUnknown.MarshalJSONLine()
	if err != nil {
		t.Fatalf("failed to marshal unknown event line: %v", err)
	}

	if err := j.Append(evUnknown); err != nil {
		t.Fatalf("failed to append unknown event: %v", err)
	}

	// 1. Chain verification succeeds across unknown event
	if err := j.VerifyChain(ownerID.MemberID); err != nil {
		t.Fatalf("chain verification failed across unknown event: %v", err)
	}

	// 2. Journal verification succeeds
	report, err := j.Verify()
	if err != nil {
		t.Fatalf("verify error: %v", err)
	}
	if !report.Valid {
		t.Fatalf("expected valid report despite unknown event, got findings: %v", report.Findings)
	}

	// 3. QueryLog includes the unknown event and formats kind + raw body
	res, err := j.QueryLog(LogFilter{Kind: unknownKind})
	if err != nil {
		t.Fatalf("QueryLog failed: %v", err)
	}
	if len(res.Events) != 1 {
		t.Fatalf("expected 1 event with unknown kind, got %d", len(res.Events))
	}

	readEv := res.Events[0]
	rawLineAfter, err := readEv.MarshalJSONLine()
	if err != nil {
		t.Fatalf("failed to re-marshal read event: %v", err)
	}

	// Byte-identical line verification
	if !bytes.Equal(bytes.TrimSpace(rawLineBefore), bytes.TrimSpace(rawLineAfter)) {
		t.Errorf("byte mismatch after re-serializing unknown event line:\nBefore: %s\nAfter:  %s", rawLineBefore, rawLineAfter)
	}

	// Human format includes kind + raw body
	formatted := FormatEventHuman(readEv)
	if !bytes.Contains([]byte(formatted), []byte(unknownKind)) {
		t.Errorf("human format should contain unknown kind %s, got: %s", unknownKind, formatted)
	}
}
