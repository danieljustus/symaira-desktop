package room

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/danieljustus/symaira-room/internal/event"
	"github.com/danieljustus/symaira-room/internal/identity"
	"github.com/danieljustus/symaira-room/internal/members"
)

// AddMember appends a signed member.added event to the room journal. Only the
// room owner may add members; any other caller (including non-members) is
// rejected with members.ErrUnauthorizedOwnerAction and nothing is persisted.
func AddMember(roomDir, name, pubKeyHex string, role members.Role, kind members.MemberKind, id *identity.Identity) (*event.Event, error) {
	pubBytes, err := hex.DecodeString(pubKeyHex)
	if err != nil {
		return nil, fmt.Errorf("invalid member public key hex: %w", err)
	}
	if len(pubBytes) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid member public key: expected %d bytes, got %d", ed25519.PublicKeySize, len(pubBytes))
	}
	if !validRole(role) {
		return nil, members.ErrInvalidRole
	}
	if !validKind(kind) {
		return nil, members.ErrInvalidKind
	}

	bodyMap := map[string]string{
		"id":         identity.ComputeMemberID(pubBytes),
		"name":       name,
		"public_key": pubKeyHex,
		"role":       string(role),
		"kind":       string(kind),
	}
	bodyBytes, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, fmt.Errorf("marshal member.added body: %w", err)
	}

	stats, err := requireOwner(roomDir, id)
	if err != nil {
		return nil, err
	}
	return appendSignedMemberEvent(roomDir, event.KindMemberAdded, bodyBytes, stats, id)
}

// RemoveMember appends a signed member.removed event to the room journal.
// Only the room owner may remove members.
func RemoveMember(roomDir, memberID string, id *identity.Identity) (*event.Event, error) {
	stats, err := requireOwner(roomDir, id)
	if err != nil {
		return nil, err
	}
	if _, exists := stats.MemberState.Members[memberID]; !exists {
		return nil, members.ErrMemberNotFound
	}

	bodyBytes, err := json.Marshal(map[string]string{"id": memberID})
	if err != nil {
		return nil, fmt.Errorf("marshal member.removed body: %w", err)
	}
	return appendSignedMemberEvent(roomDir, event.KindMemberRemoved, bodyBytes, stats, id)
}

// SetMemberRole appends a signed member.role_changed event to the room
// journal. Only the room owner may change member roles.
func SetMemberRole(roomDir, memberID string, role members.Role, id *identity.Identity) (*event.Event, error) {
	if !validRole(role) {
		return nil, members.ErrInvalidRole
	}

	stats, err := requireOwner(roomDir, id)
	if err != nil {
		return nil, err
	}
	if _, exists := stats.MemberState.Members[memberID]; !exists {
		return nil, members.ErrMemberNotFound
	}

	bodyBytes, err := json.Marshal(map[string]string{"id": memberID, "role": string(role)})
	if err != nil {
		return nil, fmt.Errorf("marshal member.role_changed body: %w", err)
	}
	return appendSignedMemberEvent(roomDir, event.KindMemberRoleChanged, bodyBytes, stats, id)
}

// ListMembers returns the materialized member view (id, name, public key,
// role, kind) replayed from the room journal.
func ListMembers(roomDir string) (*members.State, error) {
	stats, err := ReadJournalStats(roomDir)
	if err != nil {
		return nil, err
	}
	return stats.MemberState, nil
}

// requireOwner loads the merged journal state and verifies that the caller is
// a room owner.
func requireOwner(roomDir string, id *identity.Identity) (*JournalStats, error) {
	stats, err := ReadJournalStats(roomDir)
	if err != nil {
		return nil, err
	}
	caller, exists := stats.MemberState.Members[id.MemberID]
	if !exists || caller.Role != members.RoleOwner {
		return nil, members.ErrUnauthorizedOwnerAction
	}
	return stats, nil
}

// appendSignedMemberEvent builds a member event chained to the caller's
// journal file, signs it with the caller identity and appends it.
func appendSignedMemberEvent(roomDir, kind string, body []byte, stats *JournalStats, id *identity.Identity) (*event.Event, error) {
	roomCfg, err := ReadRoomConfig(roomDir)
	if err != nil {
		return nil, err
	}

	lastSeq, prevHash, err := GetAuthorStats(roomDir, id.MemberID)
	if err != nil {
		return nil, err
	}

	ev := &event.Event{
		V:       event.CurrentVersion,
		ID:      GenerateEventID(),
		Room:    roomCfg.ID,
		Author:  id.MemberID,
		Seq:     lastSeq + 1,
		Prev:    prevHash,
		Lamport: stats.MaxLamport + 1,
		TS:      event.FormatTimestamp(time.Now()),
		Kind:    kind,
		Body:    json.RawMessage(body),
	}

	if err := ev.Sign(id); err != nil {
		return nil, fmt.Errorf("sign %s event: %w", kind, err)
	}
	if err := AppendEvent(roomDir, ev); err != nil {
		return nil, fmt.Errorf("append %s event: %w", kind, err)
	}
	return ev, nil
}

func validRole(role members.Role) bool {
	switch role {
	case members.RoleOwner, members.RoleMember, members.RoleAgent, members.RoleObserver:
		return true
	}
	return false
}

func validKind(kind members.MemberKind) bool {
	switch kind {
	case members.KindHuman, members.KindAgent:
		return true
	}
	return false
}
