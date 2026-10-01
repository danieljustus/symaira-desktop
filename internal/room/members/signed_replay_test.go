package members

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/room/event"
	"github.com/danieljustus/symaira-desktop/internal/room/identity"
)

func TestApplySignedEventWithRootRejectsInvalidConfiguredAnchors(t *testing.T) {
	owner, err := identity.Generate("root-owner")
	if err != nil {
		t.Fatal(err)
	}
	other, err := identity.Generate("other-root")
	if err != nil {
		t.Fatal(err)
	}
	root := signedMembershipEvent(t, "room.created", "root-event", owner, map[string]string{
		"name": "room", "public_key": hex.EncodeToString(owner.PublicKey),
	})

	tests := []struct {
		name      string
		rootEvent string
		rootKey   string
		wantError string
	}{
		{name: "valid configured root", rootEvent: root.ID, rootKey: "ed25519:" + hex.EncodeToString(owner.PublicKey)},
		{name: "empty root event", rootEvent: "", rootKey: "ed25519:" + hex.EncodeToString(owner.PublicKey), wantError: "configured root event"},
		{name: "wrong root event", rootEvent: "another-event", rootKey: "ed25519:" + hex.EncodeToString(owner.PublicKey), wantError: "configured root event"},
		{name: "missing root key", rootEvent: root.ID, wantError: "configured root pubkey"},
		{name: "malformed root key", rootEvent: root.ID, rootKey: "ed25519:not-hex", wantError: "configured root pubkey"},
		{name: "wrong root key", rootEvent: root.ID, rootKey: "ed25519:" + hex.EncodeToString(other.PublicKey), wantError: "does not match configured root pubkey"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := NewState()
			err := state.ApplySignedEventWithRoot(root, tt.rootEvent, tt.rootKey)
			if tt.wantError == "" {
				if err != nil {
					t.Fatalf("ApplySignedEventWithRoot: %v", err)
				}
				if state.Members[owner.MemberID] == nil || !state.RoomCreated {
					t.Fatal("valid configured root was not applied")
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("ApplySignedEventWithRoot error = %v, want substring %q", err, tt.wantError)
			}
			if len(state.Members) != 0 || state.RoomCreated {
				t.Fatal("invalid configured root changed membership state")
			}
		})
	}
}

func TestApplySignedEventRejectsSecondRootAfterOwnerRemoval(t *testing.T) {
	owner, err := identity.Generate("root-owner")
	if err != nil {
		t.Fatal(err)
	}
	attacker, err := identity.Generate("replacement-root")
	if err != nil {
		t.Fatal(err)
	}
	state := NewState()
	root := signedMembershipEvent(t, event.KindRoomCreated, "root-event", owner, map[string]string{
		"name": "room", "public_key": hex.EncodeToString(owner.PublicKey),
	})
	if err := state.ApplySignedEvent(root); err != nil {
		t.Fatalf("apply initial root: %v", err)
	}
	removed := signedMembershipEvent(t, event.KindMemberRemoved, "owner-removed", owner, map[string]string{"id": owner.MemberID})
	if err := state.ApplySignedEvent(removed); err != nil {
		t.Fatalf("apply owner self-removal: %v", err)
	}
	replacement := signedMembershipEvent(t, event.KindRoomCreated, "replacement-root-event", attacker, map[string]string{
		"name": "takeover", "public_key": hex.EncodeToString(attacker.PublicKey),
	})
	if err := state.ApplySignedEvent(replacement); err == nil {
		t.Fatal("accepted room.created after the original owner removed themself")
	}
	if !state.RoomCreated || len(state.Members) != 0 {
		t.Fatalf("replacement root changed state: created=%v members=%v", state.RoomCreated, state.Members)
	}
}

func signedMembershipEvent(t *testing.T, kind, id string, signer *identity.Identity, body any) *event.Event {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	ev := &event.Event{
		V: event.CurrentVersion, ID: id, Room: "room-test", Author: signer.MemberID,
		Kind: kind, Body: encoded,
	}
	if err := ev.Sign(signer); err != nil {
		t.Fatalf("sign %s: %v", id, err)
	}
	return ev
}
