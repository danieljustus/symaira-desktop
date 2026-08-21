package journal

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-room/internal/event"
	"github.com/danieljustus/symaira-room/internal/identity"
	"github.com/danieljustus/symaira-room/internal/members"
)

func createTestRoom(t *testing.T, dir string) (*identity.Identity, *Journal) {
	t.Helper()
	ownerID, err := identity.Generate("founder")
	if err != nil {
		t.Fatalf("generate owner: %v", err)
	}

	j := New(filepath.Join(dir, "journal"))

	bodyCreated, _ := json.Marshal(map[string]string{
		"name":       "Test Room",
		"public_key": hex.EncodeToString(ownerID.PublicKey),
	})
	evCreated := &event.Event{
		V:       1,
		ID:      "ev_root",
		Room:    "rm_test",
		Author:  ownerID.MemberID,
		Seq:     1,
		Prev:    "sha256:0000000000000000000000000000000000000000000000000000000000000000",
		Lamport: 1,
		TS:      event.FormatTimestamp(time.Now()),
		Kind:    event.KindRoomCreated,
		Body:    json.RawMessage(bodyCreated),
	}
	if err := evCreated.Sign(ownerID); err != nil {
		t.Fatalf("sign room.created: %v", err)
	}
	if err := j.Append(evCreated); err != nil {
		t.Fatalf("append room.created: %v", err)
	}

	return ownerID, j
}

func TestVerifyCleanJournal(t *testing.T) {
	tempDir := t.TempDir()
	_, j := createTestRoom(t, tempDir)

	report, err := j.Verify()
	if err != nil {
		t.Fatalf("verify failed: %v", err)
	}
	if !report.Valid {
		t.Errorf("expected clean journal to be valid, got findings: %v", report.Findings)
	}
}

func TestVerifyModifiedEvent(t *testing.T) {
	tempDir := t.TempDir()
	ownerID, j := createTestRoom(t, tempDir)

	// Append note
	evNote := &event.Event{
		V:      1,
		ID:     "ev_note",
		Room:   "rm_test",
		Author: ownerID.MemberID,
		Kind:   event.KindNotePosted,
		Body:   json.RawMessage(`{"text":"hello"}`),
	}
	if err := evNote.Sign(ownerID); err != nil {
		t.Fatalf("sign note event: %v", err)
	}
	if err := j.Append(evNote); err != nil {
		t.Fatalf("append note event: %v", err)
	}

	// Tamper note line in file
	segPath := j.SegmentPath(ownerID.MemberID)
	data, _ := os.ReadFile(segPath)
	lines := strings.Split(string(data), "\n")
	lines[1] = strings.Replace(lines[1], `"hello"`, `"tampered"`, 1)
	if err := os.WriteFile(segPath, []byte(strings.Join(lines, "\n")), 0644); err != nil {
		t.Fatalf("write tampered segment: %v", err)
	}

	report, err := j.Verify()
	if err != nil {
		t.Fatalf("verify error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected invalid report for tampered event")
	}
	found := false
	for _, f := range report.Findings {
		if f.Code == CodeSignatureInvalid || f.Code == CodeChainBroken {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected signature_invalid or chain_broken finding, got %v", report.Findings)
	}
}

func TestVerifyAgentApprovalForbidden(t *testing.T) {
	tempDir := t.TempDir()
	ownerID, j := createTestRoom(t, tempDir)

	agentID, _ := identity.Generate("bot")

	// 1. Add agent to room
	bodyAdded, _ := json.Marshal(map[string]any{
		"id":         agentID.MemberID,
		"name":       "bot",
		"public_key": hex.EncodeToString(agentID.PublicKey),
		"role":       members.RoleAgent,
		"kind":       members.KindAgent,
	})
	evAdded := &event.Event{
		V:      1,
		ID:     "ev_add_agent",
		Room:   "rm_test",
		Author: ownerID.MemberID,
		Kind:   event.KindMemberAdded,
		Body:   json.RawMessage(bodyAdded),
	}
	if err := evAdded.Sign(ownerID); err != nil {
		t.Fatalf("sign member.added: %v", err)
	}
	if err := j.Append(evAdded); err != nil {
		t.Fatalf("append member.added: %v", err)
	}

	// 2. Agent signs approval event (FORBIDDEN)
	evApprove := &event.Event{
		V:      1,
		ID:     "ev_agent_approve",
		Room:   "rm_test",
		Author: agentID.MemberID,
		Kind:   event.KindRunApproved,
		Body:   json.RawMessage(`{}`),
	}
	if err := evApprove.Sign(agentID); err != nil {
		t.Fatalf("sign approval event: %v", err)
	}
	if err := j.Append(evApprove); err != nil {
		t.Fatalf("append approval event: %v", err)
	}

	report, err := j.Verify()
	if err != nil {
		t.Fatalf("verify error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected invalid report for agent approval")
	}

	found := false
	for _, f := range report.Findings {
		if f.Code == CodeAgentApprovalForbidden {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected agent_approval_forbidden finding, got %v", report.Findings)
	}
}

func TestVerifyForkDetection(t *testing.T) {
	tempDir := t.TempDir()
	ownerID, j := createTestRoom(t, tempDir)

	// Create two different events with same seq=2 for owner
	evA := &event.Event{V: 1, ID: "ev_fork_a", Room: "rm_test", Author: ownerID.MemberID, Seq: 2, Prev: "sha256:00", Kind: event.KindNotePosted, Body: json.RawMessage(`{"text":"a"}`)}
	if err := evA.Sign(ownerID); err != nil {
		t.Fatalf("sign fork event A: %v", err)
	}

	evB := &event.Event{V: 1, ID: "ev_fork_b", Room: "rm_test", Author: ownerID.MemberID, Seq: 2, Prev: "sha256:00", Kind: event.KindNotePosted, Body: json.RawMessage(`{"text":"b"}`)}
	if err := evB.Sign(ownerID); err != nil {
		t.Fatalf("sign fork event B: %v", err)
	}

	lineA, _ := evA.MarshalJSONLine()
	lineB, _ := evB.MarshalJSONLine()

	// Write both lines into segment
	segPath := j.SegmentPath(ownerID.MemberID)
	f, _ := os.OpenFile(segPath, os.O_WRONLY|os.O_APPEND, 0644)
	if _, err := f.Write(lineA); err != nil {
		t.Fatalf("write fork line A: %v", err)
	}
	if _, err := f.Write(lineB); err != nil {
		t.Fatalf("write fork line B: %v", err)
	}
	_ = f.Close()

	report, err := j.Verify()
	if err != nil {
		t.Fatalf("verify error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected invalid report for fork detection")
	}

	found := false
	for _, f := range report.Findings {
		if f.Code == CodeForkDetected || f.Code == CodeSeqMismatch {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected fork_detected finding, got %v", report.Findings)
	}
}
