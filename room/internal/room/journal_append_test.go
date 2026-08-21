package room

import (
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/danieljustus/symaira-room/internal/event"
	"github.com/danieljustus/symaira-room/internal/identity"
	"github.com/danieljustus/symaira-room/internal/members"
)

func TestPostNoteAndRecordDecision(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tempDir)

	ownerID, err := identity.Generate("owner")
	if err != nil {
		t.Fatalf("failed to generate identity: %v", err)
	}

	roomDir := filepath.Join(tempDir, "test-room")
	_, err = Init(roomDir, "Test Room", ownerID)
	if err != nil {
		t.Fatalf("failed to init room: %v", err)
	}

	// 1. Post note
	noteEv, err := PostNote(roomDir, "This is a note", ownerID)
	if err != nil {
		t.Fatalf("failed to post note: %v", err)
	}

	if noteEv.Kind != event.KindNotePosted {
		t.Errorf("expected kind note.posted, got %s", noteEv.Kind)
	}

	// 2. Record decision
	decEv, err := RecordDecision(roomDir, "Use Go 1.26+", []string{"issue-1"}, ownerID)
	if err != nil {
		t.Fatalf("failed to record decision: %v", err)
	}

	if decEv.Kind != event.KindDecisionRecorded {
		t.Errorf("expected kind decision.recorded, got %s", decEv.Kind)
	}
}

func TestObserverRoleRefused(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tempDir)

	ownerID, _ := identity.Generate("owner")
	observerID, _ := identity.Generate("obs")

	roomDir := filepath.Join(tempDir, "test-room-obs")
	_, err := Init(roomDir, "Obs Room", ownerID)
	if err != nil {
		t.Fatalf("failed to init room: %v", err)
	}

	// Add observer to room journal
	bodyAdded, _ := json.Marshal(map[string]any{
		"id":         observerID.MemberID,
		"name":       "obs",
		"public_key": hex.EncodeToString(observerID.PublicKey),
		"role":       members.RoleObserver,
		"kind":       members.KindHuman,
	})
	evAdded := &event.Event{
		V:       1,
		ID:      GenerateEventID(),
		Room:    "rm_test",
		Author:  ownerID.MemberID,
		Seq:     2,
		Prev:    "sha256:0000",
		Lamport: 2,
		TS:      event.FormatTimestamp(time.Now()),
		Kind:    event.KindMemberAdded,
		Body:    bodyAdded,
	}
	if err := evAdded.Sign(ownerID); err != nil {
		t.Fatalf("sign member.added: %v", err)
	}
	if err := AppendEvent(roomDir, evAdded); err != nil {
		t.Fatalf("append member.added: %v", err)
	}

	// Attempt note posting as observer -> must be refused
	_, err = PostNote(roomDir, "Observer attempt", observerID)
	if err == nil {
		t.Fatalf("expected error posting note as observer, got nil")
	}
	if err != members.ErrObserverForbidden {
		t.Errorf("expected ErrObserverForbidden, got %v", err)
	}
}
