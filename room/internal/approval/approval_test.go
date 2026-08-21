package approval

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-room/internal/event"
	"github.com/danieljustus/symaira-room/internal/identity"
	"github.com/danieljustus/symaira-room/internal/journal"
	"github.com/danieljustus/symaira-room/internal/members"
	"github.com/danieljustus/symaira-room/internal/room"
	"github.com/danieljustus/symaira-room/internal/run"
)

func TestApproveAndDenyWithScopeAndTTL(t *testing.T) {
	tempDir := t.TempDir()
	ownerID, err := identity.Generate("owner")
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}

	if _, err := room.Init(tempDir, "Test Room", ownerID); err != nil {
		t.Fatalf("Init room failed: %v", err)
	}

	// 1. Request a run
	evReq, err := run.Request(tempDir, "Deploy Service", "deploy.md", "shell", ownerID)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	var bReq struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(evReq.Body, &bReq); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	runID := bReq.RunID

	// 2. Approve run with scope and TTL
	_, err = Approve(tempDir, runID, "deploy:staging", 30*time.Minute, ownerID)
	if err != nil {
		t.Fatalf("Approve failed: %v", err)
	}

	rApp, err := run.Get(tempDir, runID)
	if err != nil {
		t.Fatalf("Get run after approve failed: %v", err)
	}
	if rApp.State != run.StateApproved {
		t.Errorf("expected state approved, got %s", rApp.State)
	}
	if rApp.Scope != "deploy:staging" {
		t.Errorf("expected scope deploy:staging, got %s", rApp.Scope)
	}
}

func TestAgentApprovalForbidden(t *testing.T) {
	tempDir := t.TempDir()
	ownerID, _ := identity.Generate("owner")
	if _, err := room.Init(tempDir, "Test Room", ownerID); err != nil {
		t.Fatalf("Init room failed: %v", err)
	}

	agentID, _ := identity.Generate("bot_worker")

	// Add agent to room with role agent
	agentBody, _ := json.Marshal(map[string]string{
		"id":         agentID.MemberID,
		"public_key": hex.EncodeToString(agentID.PublicKey),
		"role":       string(members.RoleAgent),
		"kind":       string(members.KindAgent),
	})
	j := journal.New(filepath.Join(tempDir, "journal"))
	evAdd := &event.Event{
		V:      event.CurrentVersion,
		ID:     "ev_add_agent",
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

	evReq, _ := run.Request(tempDir, "Bot Run", "", "", ownerID)
	var bBot struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(evReq.Body, &bBot); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}

	// Agent attempts to approve -> refused
	_, err := Approve(tempDir, bBot.RunID, "all", 10*time.Minute, agentID)
	if err == nil {
		t.Fatalf("expected ErrAgentApprovalForbidden, got nil")
	}
	if err != ErrAgentApprovalForbidden {
		t.Errorf("expected ErrAgentApprovalForbidden, got %v", err)
	}
}

func TestExpiredApprovalBlocksStart(t *testing.T) {
	tempDir := t.TempDir()
	ownerID, _ := identity.Generate("owner")
	if _, err := room.Init(tempDir, "Test Room", ownerID); err != nil {
		t.Fatalf("Init room failed: %v", err)
	}

	evReq, _ := run.Request(tempDir, "Fast Expire Run", "", "", ownerID)
	var bFast struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(evReq.Body, &bFast); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}

	// Approve with negative TTL -> immediately expired
	if _, err := Approve(tempDir, bFast.RunID, "all", -1*time.Minute, ownerID); err != nil {
		t.Fatalf("Approve failed: %v", err)
	}

	_, err := run.Start(tempDir, bFast.RunID, ownerID)
	if err == nil {
		t.Fatalf("expected error starting expired run, got nil")
	}
}

// addMember appends a member.added event signed by the owner, mirroring how
// agents are added in TestAgentApprovalForbidden.
func addMember(t *testing.T, roomDir string, ownerID, memberID *identity.Identity, role members.Role, kind members.MemberKind) {
	t.Helper()
	body, err := json.Marshal(map[string]string{
		"id":         memberID.MemberID,
		"public_key": hex.EncodeToString(memberID.PublicKey),
		"role":       string(role),
		"kind":       string(kind),
	})
	if err != nil {
		t.Fatalf("marshal member body: %v", err)
	}
	j := journal.New(filepath.Join(roomDir, "journal"))
	ev := &event.Event{
		V:      event.CurrentVersion,
		ID:     "ev_add_" + memberID.MemberID,
		Room:   "rm_test",
		Author: ownerID.MemberID,
		Kind:   event.KindMemberAdded,
		Body:   json.RawMessage(body),
	}
	if err := j.PrepareEvent(ev); err != nil {
		t.Fatalf("PrepareEvent failed: %v", err)
	}
	if err := ev.Sign(ownerID); err != nil {
		t.Fatalf("Sign failed: %v", err)
	}
	if err := j.Append(ev); err != nil {
		t.Fatalf("Append failed: %v", err)
	}
}

// requestRun creates a room, requests a run and returns its run ID.
func requestRun(t *testing.T, roomDir string, ownerID *identity.Identity) string {
	t.Helper()
	if _, err := room.Init(roomDir, "Test Room", ownerID); err != nil {
		t.Fatalf("Init room failed: %v", err)
	}
	evReq, err := run.Request(roomDir, "Deny Target Run", "", "", ownerID)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	var bReq struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(evReq.Body, &bReq); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	return bReq.RunID
}

func TestDenyRequestedRun(t *testing.T) {
	tempDir := t.TempDir()
	ownerID, err := identity.Generate("owner")
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}
	runID := requestRun(t, tempDir, ownerID)

	ev, err := Deny(tempDir, runID, "not needed right now", ownerID)
	if err != nil {
		t.Fatalf("Deny failed: %v", err)
	}
	if ev.Kind != event.KindRunDenied {
		t.Errorf("expected KindRunDenied, got %s", ev.Kind)
	}

	var body struct {
		RunID      string `json:"run_id"`
		ApprovalID string `json:"approval_id"`
		Reason     string `json:"reason"`
	}
	if err := json.Unmarshal(ev.Body, &body); err != nil {
		t.Fatalf("unmarshal deny body: %v", err)
	}
	if body.RunID != runID {
		t.Errorf("expected run_id %s, got %s", runID, body.RunID)
	}
	if body.Reason != "not needed right now" {
		t.Errorf("expected reason in body, got %q", body.Reason)
	}
	if !strings.HasPrefix(body.ApprovalID, "app_") {
		t.Errorf("expected approval_id prefix app_, got %s", body.ApprovalID)
	}

	r, err := run.Get(tempDir, runID)
	if err != nil {
		t.Fatalf("Get run after deny failed: %v", err)
	}
	if r.State != run.StateDenied {
		t.Errorf("expected state denied, got %s", r.State)
	}
	if r.Error != "not needed right now" {
		t.Errorf("expected reason projected onto run.Error, got %q", r.Error)
	}
}

func TestDenyNonRequestedRunFails(t *testing.T) {
	tempDir := t.TempDir()
	ownerID, err := identity.Generate("owner")
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}
	runID := requestRun(t, tempDir, ownerID)

	if _, err := Approve(tempDir, runID, "all", 5*time.Minute, ownerID); err != nil {
		t.Fatalf("Approve failed: %v", err)
	}

	_, err = Deny(tempDir, runID, "too late", ownerID)
	if err == nil {
		t.Fatal("expected error denying an approved run, got nil")
	}
	if !errors.Is(err, run.ErrInvalidTransition) {
		t.Errorf("expected ErrInvalidTransition, got %v", err)
	}
	if !strings.Contains(err.Error(), "cannot deny run in state 'approved'") {
		t.Errorf("expected state context in error, got %v", err)
	}
}

func TestDenyUnknownRun(t *testing.T) {
	tempDir := t.TempDir()
	ownerID, err := identity.Generate("owner")
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}
	if _, err := room.Init(tempDir, "Test Room", ownerID); err != nil {
		t.Fatalf("Init room failed: %v", err)
	}

	_, err = Deny(tempDir, "run_does_not_exist", "nope", ownerID)
	if !errors.Is(err, run.ErrRunNotFound) {
		t.Errorf("expected ErrRunNotFound, got %v", err)
	}
}

// TestDenyPermitsAgentAndObserverMembers documents current Deny behavior:
// unlike Approve (which rejects agent identities via ErrAgentApprovalForbidden),
// Deny performs no member-role gate, so agent and observer members can sign a
// denial. Issue #66 asked to verify which roles trigger rejection; reading the
// code shows none do for Deny.
func TestDenyPermitsAgentAndObserverMembers(t *testing.T) {
	tests := []struct {
		name string
		role members.Role
		kind members.MemberKind
	}{
		{name: "agent", role: members.RoleAgent, kind: members.KindAgent},
		{name: "observer", role: members.RoleObserver, kind: members.KindHuman},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			ownerID, err := identity.Generate("owner")
			if err != nil {
				t.Fatalf("generate identity: %v", err)
			}
			actorID, err := identity.Generate("actor-" + tt.name)
			if err != nil {
				t.Fatalf("generate actor identity: %v", err)
			}
			runID := requestRun(t, tempDir, ownerID)
			addMember(t, tempDir, ownerID, actorID, tt.role, tt.kind)

			ev, err := Deny(tempDir, runID, "blocked by "+tt.name, actorID)
			if err != nil {
				t.Fatalf("Deny by %s member failed: %v", tt.name, err)
			}
			if ev.Kind != event.KindRunDenied {
				t.Errorf("expected KindRunDenied, got %s", ev.Kind)
			}
			if ev.Author != actorID.MemberID {
				t.Errorf("expected author %s, got %s", actorID.MemberID, ev.Author)
			}

			r, err := run.Get(tempDir, runID)
			if err != nil {
				t.Fatalf("Get run failed: %v", err)
			}
			if r.State != run.StateDenied {
				t.Errorf("expected state denied, got %s", r.State)
			}
		})
	}
}
