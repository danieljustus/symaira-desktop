package run

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/room/event"
	"github.com/danieljustus/symaira-desktop/internal/room/identity"
	"github.com/danieljustus/symaira-desktop/internal/room/journal"
	"github.com/danieljustus/symaira-desktop/internal/room/room"
)

func TestListAndGetFailClosedForInvalidRootAnchors(t *testing.T) {
	tests := []struct {
		name       string
		configText func(room.RoomConfig, *identity.Identity) string
	}{
		{name: "missing root fields", configText: func(cfg room.RoomConfig, _ *identity.Identity) string { return fmt.Sprintf("id = %q\n", cfg.ID) }},
		{name: "empty root event", configText: func(cfg room.RoomConfig, _ *identity.Identity) string {
			return roomTestConfig(cfg.ID, "", cfg.RootPubkey)
		}},
		{name: "wrong root event", configText: func(cfg room.RoomConfig, _ *identity.Identity) string {
			return roomTestConfig(cfg.ID, "ev_wrong_root", cfg.RootPubkey)
		}},
		{name: "missing root key", configText: func(cfg room.RoomConfig, _ *identity.Identity) string {
			return roomTestConfig(cfg.ID, cfg.RootEvent, "")
		}},
		{name: "malformed root key", configText: func(cfg room.RoomConfig, _ *identity.Identity) string {
			return roomTestConfig(cfg.ID, cfg.RootEvent, "ed25519:invalid")
		}},
		{name: "mismatched root key", configText: func(cfg room.RoomConfig, other *identity.Identity) string {
			return roomTestConfig(cfg.ID, cfg.RootEvent, "ed25519:"+hex.EncodeToString(other.PublicKey))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			roomDir := t.TempDir()
			owner, err := identity.Generate("anchor-owner")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := room.Init(roomDir, "auth room", owner); err != nil {
				t.Fatal(err)
			}
			cfg, err := room.ReadRoomConfig(roomDir)
			if err != nil {
				t.Fatal(err)
			}
			request, err := Request(roomDir, "approval target", "", "", owner)
			if err != nil {
				t.Fatal(err)
			}
			var body struct {
				RunID string `json:"run_id"`
			}
			if err := json.Unmarshal(request.Body, &body); err != nil {
				t.Fatal(err)
			}
			appendRunApprovalForAuthTest(t, roomDir, cfg.ID, body.RunID, owner)
			other, err := identity.Generate("wrong-anchor")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(roomDir, "room.toml"), []byte(tt.configText(*cfg, other)), 0o600); err != nil {
				t.Fatal(err)
			}

			runs, err := List(roomDir, false)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(runs) != 1 || runs[0].ID != body.RunID || runs[0].State != StateRequested {
				t.Fatalf("List with invalid root anchor = %+v, want the unapproved run", runs)
			}
			got, err := Get(roomDir, body.RunID)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got.State != StateRequested {
				t.Fatalf("Get with invalid root anchor state = %q, want requested", got.State)
			}
		})
	}
}

func TestListGetAndApprovalRejectMalformedRoomConfig(t *testing.T) {
	roomDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(roomDir, "journal"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := List(roomDir, false); err == nil {
		t.Fatal("List accepted a room with missing room.toml")
	}
	if _, err := Get(roomDir, "run-missing"); err == nil {
		t.Fatal("Get accepted a room with missing room.toml")
	}
}

func TestConfiguredProjectionRejectsUnauthorizedMemberInjectionAndRotatedOrRevokedKeys(t *testing.T) {
	owner, err := identity.Generate("projection-owner")
	if err != nil {
		t.Fatal(err)
	}
	member, err := identity.Generate("projection-member")
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := identity.Generate("projection-member-rotated")
	if err != nil {
		t.Fatal(err)
	}
	attacker, err := identity.Generate("projection-attacker")
	if err != nil {
		t.Fatal(err)
	}
	roomID, rootID := "room-auth-test", "room-auth-root"
	config := &room.RoomConfig{ID: roomID, RootEvent: rootID, RootPubkey: "ed25519:" + hex.EncodeToString(owner.PublicKey)}
	events := []*event.Event{
		authRunEvent(t, roomID, rootID, event.KindRoomCreated, owner, map[string]string{"name": "room", "public_key": hex.EncodeToString(owner.PublicKey)}),
		authRunEvent(t, roomID, "member-added", event.KindMemberAdded, owner, map[string]string{"id": member.MemberID, "public_key": hex.EncodeToString(member.PublicKey), "role": "member", "kind": "human"}),
		authRunEvent(t, roomID, "request-old-key", event.KindRunRequested, owner, map[string]string{"run_id": "old-key", "title": "old key"}),
		authRunEvent(t, roomID, "member-rotated", event.KindMemberAdded, owner, map[string]string{"id": member.MemberID, "public_key": hex.EncodeToString(rotated.PublicKey), "role": "member", "kind": "human"}),
		authRunEvent(t, roomID, "approve-old-key", event.KindRunApproved, member, map[string]string{"run_id": "old-key", "approval_id": "old", "scope": "room"}),
		authRunEvent(t, roomID, "request-current-key", event.KindRunRequested, owner, map[string]string{"run_id": "current-key", "title": "current key"}),
		authRunEventAs(t, roomID, "approve-current-key", event.KindRunApproved, member, rotated, map[string]string{"run_id": "current-key", "approval_id": "current", "scope": "room"}),
		authRunEvent(t, roomID, "member-revoked", event.KindMemberRemoved, owner, map[string]string{"id": member.MemberID}),
		authRunEvent(t, roomID, "request-revoked-key", event.KindRunRequested, owner, map[string]string{"run_id": "revoked-key", "title": "revoked key"}),
		authRunEventAs(t, roomID, "approve-revoked-key", event.KindRunApproved, member, rotated, map[string]string{"run_id": "revoked-key", "approval_id": "revoked", "scope": "room"}),
		authRunEvent(t, roomID, "request-injected-member", event.KindRunRequested, owner, map[string]string{"run_id": "injected-member", "title": "injected member"}),
		authRunEventAs(t, roomID, "forged-member-added", event.KindMemberAdded, owner, attacker, map[string]string{"id": attacker.MemberID, "public_key": hex.EncodeToString(attacker.PublicKey), "role": "member", "kind": "human"}),
		authRunEvent(t, roomID, "approve-injected-member", event.KindRunApproved, attacker, map[string]string{"run_id": "injected-member", "approval_id": "injected", "scope": "room"}),
	}
	got := ProjectRunsInConfiguredRoom(events, config)
	for runID, want := range map[string]State{
		"old-key":         StateRequested,
		"current-key":     StateApproved,
		"revoked-key":     StateRequested,
		"injected-member": StateRequested,
	} {
		if got[runID] == nil || got[runID].State != want {
			t.Errorf("run %q state = %v, want %q", runID, got[runID], want)
		}
	}
}

func appendRunApprovalForAuthTest(t *testing.T, roomDir, roomID, runID string, signer *identity.Identity) {
	t.Helper()
	body, err := json.Marshal(map[string]string{"run_id": runID, "approval_id": "approval-auth-test", "scope": "room"})
	if err != nil {
		t.Fatal(err)
	}
	j := journal.New(filepath.Join(roomDir, "journal"))
	ev := &event.Event{V: event.CurrentVersion, ID: "ev_auth_test_approval", Room: roomID, Author: signer.MemberID, Kind: event.KindRunApproved, Body: body}
	if err := j.PrepareEvent(ev); err != nil {
		t.Fatal(err)
	}
	if err := ev.Sign(signer); err != nil {
		t.Fatal(err)
	}
	if err := j.Append(ev); err != nil {
		t.Fatal(err)
	}
}

func authRunEvent(t *testing.T, roomID, id, kind string, signer *identity.Identity, body any) *event.Event {
	return authRunEventAs(t, roomID, id, kind, signer, signer, body)
}

func authRunEventAs(t *testing.T, roomID, id, kind string, author, signer *identity.Identity, body any) *event.Event {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	ev := &event.Event{V: event.CurrentVersion, ID: id, Room: roomID, Author: author.MemberID, Kind: kind, Body: encoded}
	if err := ev.Sign(signer); err != nil {
		t.Fatalf("sign %s: %v", id, err)
	}
	return ev
}

func roomTestConfig(roomID, rootEvent, rootPubkey string) string {
	return fmt.Sprintf("id = %q\nroot_event = %q\nroot_pubkey = %q\n", roomID, rootEvent, rootPubkey)
}
