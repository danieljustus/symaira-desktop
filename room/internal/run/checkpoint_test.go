package run

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/danieljustus/symaira-room/internal/event"
	"github.com/danieljustus/symaira-room/internal/identity"
	"github.com/danieljustus/symaira-room/internal/journal"
	"github.com/danieljustus/symaira-room/internal/members"
	"github.com/danieljustus/symaira-room/internal/room"
)

func TestCheckpointLifecycle(t *testing.T) {
	tempDir := t.TempDir()
	ownerID, _ := identity.Generate("owner")
	if _, err := room.Init(tempDir, "Test Room", ownerID); err != nil {
		t.Fatalf("Init room failed: %v", err)
	}

	evReq, _ := Request(tempDir, "Checkpoint Task", "", "", ownerID)
	var bReq struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(evReq.Body, &bReq); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	runID := bReq.RunID

	// 1. Request checkpoint
	evChkReq, err := RequestCheckpoint(tempDir, runID, "Should we proceed with db migration?", ownerID)
	if err != nil {
		t.Fatalf("RequestCheckpoint failed: %v", err)
	}
	var bChk struct {
		CheckpointID string `json:"checkpoint_id"`
	}
	if err := json.Unmarshal(evChkReq.Body, &bChk); err != nil {
		t.Fatalf("unmarshal checkpoint body: %v", err)
	}
	chkID := bChk.CheckpointID

	// 2. Verify checkpoint appears in run show
	r, err := Get(tempDir, runID)
	if err != nil {
		t.Fatalf("Get run failed: %v", err)
	}
	if len(r.Checkpoints) != 1 {
		t.Fatalf("expected 1 checkpoint, got %d", len(r.Checkpoints))
	}
	if r.Checkpoints[0].State != "requested" {
		t.Errorf("expected state requested, got %s", r.Checkpoints[0].State)
	}

	// 3. Resolve checkpoint asynchronously
	go func() {
		time.Sleep(50 * time.Millisecond)
		if _, err := ResolveCheckpoint(tempDir, chkID, "Yes, proceed with migration", ownerID); err != nil {
			t.Errorf("ResolveCheckpoint failed: %v", err)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resolvedChk, err := WaitCheckpoint(ctx, tempDir, chkID, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("WaitCheckpoint failed: %v", err)
	}
	if resolvedChk.Answer != "Yes, proceed with migration" {
		t.Errorf("unexpected answer: %s", resolvedChk.Answer)
	}
}

func TestAgentCannotResolveCheckpoint(t *testing.T) {
	tempDir := t.TempDir()
	ownerID, _ := identity.Generate("owner")
	if _, err := room.Init(tempDir, "Test Room", ownerID); err != nil {
		t.Fatalf("Init room failed: %v", err)
	}

	agentID, _ := identity.Generate("bot_worker")

	// Add agent member
	agentBody, _ := json.Marshal(map[string]string{
		"id":         agentID.MemberID,
		"public_key": hex.EncodeToString(agentID.PublicKey),
		"role":       string(members.RoleAgent),
		"kind":       string(members.KindAgent),
	})
	j := journal.New(filepath.Join(tempDir, "journal"))
	evAdd := &event.Event{
		V:      event.CurrentVersion,
		ID:     "ev_add_agent_chk",
		Room:   "rm_test",
		Author: ownerID.MemberID,
		Kind:   event.KindMemberAdded,
		Body:   json.RawMessage(agentBody),
	}
	if err := j.PrepareEvent(evAdd); err != nil {
		t.Fatalf("PrepareEvent failed: %v", err)
	}
	if err := evAdd.Sign(ownerID); err != nil {
		t.Fatalf("Sign failed: %v", err)
	}
	if err := j.Append(evAdd); err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	evReq, _ := Request(tempDir, "Bot Checkpoint Task", "", "", ownerID)
	var bReq struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(evReq.Body, &bReq); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}

	evChk, _ := RequestCheckpoint(tempDir, bReq.RunID, "Can agent resolve this?", agentID)
	var bChk struct {
		CheckpointID string `json:"checkpoint_id"`
	}
	if err := json.Unmarshal(evChk.Body, &bChk); err != nil {
		t.Fatalf("unmarshal checkpoint body: %v", err)
	}

	// Agent tries to resolve -> forbidden
	_, err := ResolveCheckpoint(tempDir, bChk.CheckpointID, "Self approved", agentID)
	if err == nil {
		t.Fatalf("expected ErrAgentCheckpointResolveForbidden, got nil")
	}
	if err != ErrAgentCheckpointResolveForbidden {
		t.Errorf("expected ErrAgentCheckpointResolveForbidden, got %v", err)
	}
}
