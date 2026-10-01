package approval

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/room/identity"
)

func TestApproveAndDenyRejectMissingRoomConfig(t *testing.T) {
	roomDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(roomDir, "journal"), 0o700); err != nil {
		t.Fatal(err)
	}
	signer, err := identity.Generate("config-test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Approve(roomDir, "run-test", "room", time.Minute, signer); err == nil {
		t.Fatal("Approve accepted a room with missing room.toml")
	}
	if _, err := Deny(roomDir, "run-test", "reason", signer); err == nil {
		t.Fatal("Deny accepted a room with missing room.toml")
	}
}
