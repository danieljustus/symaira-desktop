package room

import (
	"encoding/hex"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-room/internal/event"
	"github.com/danieljustus/symaira-room/internal/identity"
	"github.com/danieljustus/symaira-room/internal/journal"
	"github.com/danieljustus/symaira-room/internal/members"
)

func initMemberTestRoom(t *testing.T) (string, *identity.Identity) {
	t.Helper()
	tempDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tempDir)

	ownerID, err := identity.Generate("owner")
	if err != nil {
		t.Fatalf("failed to generate identity: %v", err)
	}

	roomDir := filepath.Join(tempDir, "test-room")
	if _, err := Init(roomDir, "Test Room", ownerID); err != nil {
		t.Fatalf("failed to init room: %v", err)
	}
	return roomDir, ownerID
}

func TestAddMemberPersistsEventAndListShowsMember(t *testing.T) {
	roomDir, ownerID := initMemberTestRoom(t)

	memberID, err := identity.Generate("bob")
	if err != nil {
		t.Fatalf("failed to generate identity: %v", err)
	}

	ev, err := AddMember(roomDir, "bob", hex.EncodeToString(memberID.PublicKey), members.RoleMember, members.KindHuman, ownerID)
	if err != nil {
		t.Fatalf("AddMember failed: %v", err)
	}
	if ev.Kind != event.KindMemberAdded {
		t.Errorf("expected kind %s, got %s", event.KindMemberAdded, ev.Kind)
	}
	if ev.Author != ownerID.MemberID {
		t.Errorf("expected author %s, got %s", ownerID.MemberID, ev.Author)
	}
	if ev.Sig == "" {
		t.Errorf("expected signed event, got empty signature")
	}

	// The event must land in the journal: a fresh replay shows the member.
	j := journal.New(filepath.Join(roomDir, "journal"))
	merged, err := j.MergeAll()
	if err != nil {
		t.Fatalf("MergeAll failed: %v", err)
	}
	found := false
	for _, e := range merged {
		if e.Kind == event.KindMemberAdded && e.Author == ownerID.MemberID {
			found = true
		}
	}
	if !found {
		t.Errorf("member.added event not found in journal")
	}

	// member list shows the added member plus the owner.
	state, err := ListMembers(roomDir)
	if err != nil {
		t.Fatalf("ListMembers failed: %v", err)
	}
	m, ok := state.Members[memberID.MemberID]
	if !ok {
		t.Fatalf("member %s not found after add", memberID.MemberID)
	}
	if m.Name != "bob" || m.Role != members.RoleMember || m.Kind != members.KindHuman {
		t.Errorf("unexpected member state: %+v", m)
	}
	if owner, ok := state.Members[ownerID.MemberID]; !ok || owner.Role != members.RoleOwner {
		t.Errorf("owner missing or not owner in member state: %+v", owner)
	}
	if len(state.Members) != 2 {
		t.Errorf("expected 2 members, got %d", len(state.Members))
	}
}

func TestAddMemberNonOwnerRejectedAndNothingPersisted(t *testing.T) {
	roomDir, ownerID := initMemberTestRoom(t)

	// A stranger (not a member at all) cannot add members.
	strangerID, err := identity.Generate("stranger")
	if err != nil {
		t.Fatalf("failed to generate identity: %v", err)
	}
	_, err = AddMember(roomDir, "stranger", hex.EncodeToString(strangerID.PublicKey), members.RoleMember, members.KindHuman, strangerID)
	if err != members.ErrUnauthorizedOwnerAction {
		t.Fatalf("expected ErrUnauthorizedOwnerAction for stranger, got %v", err)
	}

	// A regular member cannot add members either.
	memberID, err := identity.Generate("bob")
	if err != nil {
		t.Fatalf("failed to generate identity: %v", err)
	}
	if _, err := AddMember(roomDir, "bob", hex.EncodeToString(memberID.PublicKey), members.RoleMember, members.KindHuman, ownerID); err != nil {
		t.Fatalf("setup: AddMember failed: %v", err)
	}
	eveID, err := identity.Generate("eve")
	if err != nil {
		t.Fatalf("failed to generate identity: %v", err)
	}
	_, err = AddMember(roomDir, "eve", hex.EncodeToString(eveID.PublicKey), members.RoleMember, members.KindHuman, memberID)
	if err != members.ErrUnauthorizedOwnerAction {
		t.Fatalf("expected ErrUnauthorizedOwnerAction for member-role caller, got %v", err)
	}

	// Nothing was persisted for either rejected call.
	state, err := ListMembers(roomDir)
	if err != nil {
		t.Fatalf("ListMembers failed: %v", err)
	}
	if _, ok := state.Members[strangerID.MemberID]; ok {
		t.Errorf("stranger persisted despite rejection")
	}
	if _, ok := state.Members[eveID.MemberID]; ok {
		t.Errorf("eve persisted despite rejection")
	}
	if len(state.Members) != 2 {
		t.Errorf("expected exactly owner+bob in state, got %d", len(state.Members))
	}
}

func TestAddMemberValidatesInput(t *testing.T) {
	roomDir, ownerID := initMemberTestRoom(t)

	memberID, err := identity.Generate("bob")
	if err != nil {
		t.Fatalf("failed to generate identity: %v", err)
	}
	pubHex := hex.EncodeToString(memberID.PublicKey)

	if _, err := AddMember(roomDir, "bob", pubHex, members.Role("admin"), members.KindHuman, ownerID); err != members.ErrInvalidRole {
		t.Errorf("expected ErrInvalidRole, got %v", err)
	}
	if _, err := AddMember(roomDir, "bob", pubHex, members.RoleMember, members.MemberKind("robot"), ownerID); err != members.ErrInvalidKind {
		t.Errorf("expected ErrInvalidKind, got %v", err)
	}
	if _, err := AddMember(roomDir, "bob", "zzz", members.RoleMember, members.KindHuman, ownerID); err == nil {
		t.Errorf("expected error for invalid pubkey hex, got nil")
	}
	if _, err := AddMember(roomDir, "bob", hex.EncodeToString([]byte{1, 2, 3}), members.RoleMember, members.KindHuman, ownerID); err == nil {
		t.Errorf("expected error for short pubkey, got nil")
	}

	state, err := ListMembers(roomDir)
	if err != nil {
		t.Fatalf("ListMembers failed: %v", err)
	}
	if len(state.Members) != 1 {
		t.Errorf("invalid adds must not persist: expected only owner, got %d members", len(state.Members))
	}
}

func TestRemoveMemberOwnerOnly(t *testing.T) {
	roomDir, ownerID := initMemberTestRoom(t)

	memberID, err := identity.Generate("bob")
	if err != nil {
		t.Fatalf("failed to generate identity: %v", err)
	}
	if _, err := AddMember(roomDir, "bob", hex.EncodeToString(memberID.PublicKey), members.RoleMember, members.KindHuman, ownerID); err != nil {
		t.Fatalf("setup: AddMember failed: %v", err)
	}

	// Non-owner rejected.
	_, err = RemoveMember(roomDir, memberID.MemberID, memberID)
	if err != members.ErrUnauthorizedOwnerAction {
		t.Fatalf("expected ErrUnauthorizedOwnerAction, got %v", err)
	}

	// Owner removes.
	ev, err := RemoveMember(roomDir, memberID.MemberID, ownerID)
	if err != nil {
		t.Fatalf("RemoveMember failed: %v", err)
	}
	if ev.Kind != event.KindMemberRemoved {
		t.Errorf("expected kind %s, got %s", event.KindMemberRemoved, ev.Kind)
	}

	state, err := ListMembers(roomDir)
	if err != nil {
		t.Fatalf("ListMembers failed: %v", err)
	}
	if _, ok := state.Members[memberID.MemberID]; ok {
		t.Errorf("member still present after remove")
	}
	if len(state.Members) != 1 {
		t.Errorf("expected only owner after remove, got %d members", len(state.Members))
	}

	// Removing a nonexistent member errors.
	if _, err := RemoveMember(roomDir, "mem_deadbeefdeadbeef", ownerID); err != members.ErrMemberNotFound {
		t.Errorf("expected ErrMemberNotFound, got %v", err)
	}
}

func TestSetMemberRoleOwnerOnlyAndValidatesRole(t *testing.T) {
	roomDir, ownerID := initMemberTestRoom(t)

	memberID, err := identity.Generate("bot")
	if err != nil {
		t.Fatalf("failed to generate identity: %v", err)
	}
	if _, err := AddMember(roomDir, "bot", hex.EncodeToString(memberID.PublicKey), members.RoleMember, members.KindAgent, ownerID); err != nil {
		t.Fatalf("setup: AddMember failed: %v", err)
	}

	// Invalid role rejected.
	if _, err := SetMemberRole(roomDir, memberID.MemberID, members.Role("admin"), ownerID); err != members.ErrInvalidRole {
		t.Errorf("expected ErrInvalidRole, got %v", err)
	}

	// Non-owner rejected.
	if _, err := SetMemberRole(roomDir, memberID.MemberID, members.RoleAgent, memberID); err != members.ErrUnauthorizedOwnerAction {
		t.Errorf("expected ErrUnauthorizedOwnerAction, got %v", err)
	}

	// Nonexistent member rejected.
	if _, err := SetMemberRole(roomDir, "mem_deadbeefdeadbeef", members.RoleAgent, ownerID); err != members.ErrMemberNotFound {
		t.Errorf("expected ErrMemberNotFound, got %v", err)
	}

	// Owner changes the role.
	ev, err := SetMemberRole(roomDir, memberID.MemberID, members.RoleAgent, ownerID)
	if err != nil {
		t.Fatalf("SetMemberRole failed: %v", err)
	}
	if ev.Kind != event.KindMemberRoleChanged {
		t.Errorf("expected kind %s, got %s", event.KindMemberRoleChanged, ev.Kind)
	}

	state, err := ListMembers(roomDir)
	if err != nil {
		t.Fatalf("ListMembers failed: %v", err)
	}
	if state.Members[memberID.MemberID].Role != members.RoleAgent {
		t.Errorf("expected role %s, got %s", members.RoleAgent, state.Members[memberID.MemberID].Role)
	}
}
