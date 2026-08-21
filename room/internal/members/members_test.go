package members

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/danieljustus/symaira-room/internal/event"
	"github.com/danieljustus/symaira-room/internal/identity"
)

func TestRoleMatrixPermissions(t *testing.T) {
	matrixTests := []struct {
		role    Role
		action  Action
		allowed bool
	}{
		// Owner
		{RoleOwner, ActionManageMembers, true},
		{RoleOwner, ActionApprove, true},
		{RoleOwner, ActionPostLink, true},
		{RoleOwner, ActionRequestRun, true},
		// Member
		{RoleMember, ActionManageMembers, false},
		{RoleMember, ActionApprove, true},
		{RoleMember, ActionPostLink, true},
		{RoleMember, ActionRequestRun, true},
		// Agent
		{RoleAgent, ActionManageMembers, false},
		{RoleAgent, ActionApprove, false}, // HARD INVARIANT
		{RoleAgent, ActionPostLink, true},
		{RoleAgent, ActionRequestRun, true},
		// Observer
		{RoleObserver, ActionManageMembers, false},
		{RoleObserver, ActionApprove, false},
		{RoleObserver, ActionPostLink, false},
		{RoleObserver, ActionRequestRun, false},
	}

	for _, tt := range matrixTests {
		m := &Member{Role: tt.role}
		got := m.CanPerform(tt.action)
		if got != tt.allowed {
			t.Errorf("role %s action %s: got allowed=%v, want %v", tt.role, tt.action, got, tt.allowed)
		}
	}
}

func TestMembershipJournalProjection(t *testing.T) {
	ownerID, _ := identity.Generate("owner")
	agentID, _ := identity.Generate("bot")

	state := NewState()

	// 1. room.created
	bodyCreated, _ := json.Marshal(map[string]string{
		"name":       "Room Alpha",
		"public_key": hex.EncodeToString(ownerID.PublicKey),
	})
	evCreated := &event.Event{
		V:      1,
		ID:     "ev_1",
		Room:   "rm_1",
		Author: ownerID.MemberID,
		Kind:   event.KindRoomCreated,
		Body:   bodyCreated,
	}

	if err := state.ApplyEvent(evCreated); err != nil {
		t.Fatalf("failed to apply room.created: %v", err)
	}

	if state.Members[ownerID.MemberID] == nil || state.Members[ownerID.MemberID].Role != RoleOwner {
		t.Fatalf("expected owner member with RoleOwner")
	}

	// 2. member.added (agent added by owner)
	bodyAdded, _ := json.Marshal(map[string]any{
		"id":         agentID.MemberID,
		"name":       "bot",
		"public_key": hex.EncodeToString(agentID.PublicKey),
		"role":       RoleAgent,
		"kind":       KindAgent,
	})
	evAdded := &event.Event{
		V:      1,
		ID:     "ev_2",
		Room:   "rm_1",
		Author: ownerID.MemberID,
		Kind:   event.KindMemberAdded,
		Body:   bodyAdded,
	}

	if err := state.ApplyEvent(evAdded); err != nil {
		t.Fatalf("failed to apply member.added: %v", err)
	}

	if state.Members[agentID.MemberID] == nil || state.Members[agentID.MemberID].Role != RoleAgent {
		t.Fatalf("expected agent member with RoleAgent")
	}

	// 3. Reject member.added signed by non-owner (agent)
	nonOwnerAdded := &event.Event{
		V:      1,
		ID:     "ev_3",
		Room:   "rm_1",
		Author: agentID.MemberID,
		Kind:   event.KindMemberAdded,
		Body:   bodyAdded,
	}
	if err := state.ApplyEvent(nonOwnerAdded); err != ErrUnauthorizedOwnerAction {
		t.Errorf("expected ErrUnauthorizedOwnerAction, got %v", err)
	}

	// 4. Hard invariant: approval event signed by agent is rejected
	evApprove := &event.Event{
		V:      1,
		ID:     "ev_4",
		Room:   "rm_1",
		Author: agentID.MemberID,
		Kind:   event.KindRunApproved,
		Body:   json.RawMessage(`{}`),
	}
	if err := state.ApplyEvent(evApprove); err != ErrAgentApprovalForbidden {
		t.Errorf("expected ErrAgentApprovalForbidden for agent approval, got %v", err)
	}
}

func TestVerifyMemberPublicKey(t *testing.T) {
	id, _ := identity.Generate("user1")
	m := &Member{
		ID:        id.MemberID,
		PublicKey: id.PublicKey,
		Role:      RoleMember,
	}
	if len(m.PublicKey) != ed25519.PublicKeySize {
		t.Errorf("expected valid ed25519 public key")
	}
}

func TestApplyEventTransitionsAndValidation(t *testing.T) {
	ownerID, err := identity.Generate("owner")
	if err != nil {
		t.Fatalf("generate owner: %v", err)
	}
	targetID, err := identity.Generate("target")
	if err != nil {
		t.Fatalf("generate target: %v", err)
	}
	state := NewState()
	apply := func(kind string, author string, body any) error {
		t.Helper()
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal %s: %v", kind, err)
		}
		return state.ApplyEvent(&event.Event{Kind: kind, Author: author, Body: data})
	}

	if err := apply(event.KindRoomCreated, ownerID.MemberID, map[string]string{
		"name": "Room", "public_key": hex.EncodeToString(ownerID.PublicKey),
	}); err != nil {
		t.Fatalf("room.created: %v", err)
	}
	if err := state.ApplyEvent(&event.Event{Kind: event.KindRoomCreated, Author: ownerID.MemberID, Body: json.RawMessage(`{`)}); err == nil {
		t.Fatal("malformed room.created should fail")
	}

	if err := apply(event.KindMemberAdded, ownerID.MemberID, map[string]any{
		"id": targetID.MemberID, "name": "target", "public_key": hex.EncodeToString(targetID.PublicKey),
		"role": RoleObserver, "kind": KindHuman,
	}); err != nil {
		t.Fatalf("member.added: %v", err)
	}
	if err := apply(event.KindRunApproved, targetID.MemberID, map[string]string{}); err != ErrObserverForbidden {
		t.Errorf("observer approval error = %v, want %v", err, ErrObserverForbidden)
	}
	if err := apply(event.KindRunApproved, "mem_missing", map[string]string{}); err != ErrMemberNotFound {
		t.Errorf("unknown approval error = %v, want %v", err, ErrMemberNotFound)
	}

	if err := apply(event.KindMemberRoleChanged, ownerID.MemberID, map[string]any{
		"id": targetID.MemberID, "role": RoleMember,
	}); err != nil {
		t.Fatalf("member.role_changed: %v", err)
	}
	if got := state.Members[targetID.MemberID].Role; got != RoleMember {
		t.Errorf("target role = %s, want %s", got, RoleMember)
	}
	if err := apply(event.KindRunApproved, targetID.MemberID, map[string]string{}); err != nil {
		t.Fatalf("member approval: %v", err)
	}

	if err := apply(event.KindMemberRemoved, ownerID.MemberID, map[string]string{"id": targetID.MemberID}); err != nil {
		t.Fatalf("member.removed: %v", err)
	}
	if _, ok := state.Members[targetID.MemberID]; ok {
		t.Fatal("removed member still present")
	}
	if err := apply(event.KindMemberAdded, ownerID.MemberID, map[string]any{"id": "bad", "public_key": "not-hex"}); err == nil {
		t.Fatal("invalid member public key should fail")
	}
}
