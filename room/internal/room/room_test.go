package room

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/danieljustus/symaira-room/internal/event"
	"github.com/danieljustus/symaira-room/internal/identity"
)

func TestInitRoom(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tempDir)

	id, err := identity.Generate("founder")
	if err != nil {
		t.Fatalf("failed to generate identity: %v", err)
	}

	roomDir := filepath.Join(tempDir, "my-room")
	cfg, err := Init(roomDir, "Test Room", id)
	if err != nil {
		t.Fatalf("failed to init room: %v", err)
	}

	if !strings.HasPrefix(cfg.ID, "rm_") {
		t.Errorf("expected room ID prefix rm_, got %s", cfg.ID)
	}

	// Verify room.toml exists
	roomTomlPath := filepath.Join(roomDir, "room.toml")
	if _, err := os.Stat(roomTomlPath); err != nil {
		t.Errorf("expected room.toml to exist: %v", err)
	}

	// Verify .gitignore excludes .symroom/
	gitIgnorePath := filepath.Join(roomDir, ".gitignore")
	data, err := os.ReadFile(gitIgnorePath)
	if err != nil {
		t.Fatalf("failed to read .gitignore: %v", err)
	}
	if !strings.Contains(string(data), ".symroom/") {
		t.Errorf("expected .gitignore to contain .symroom/")
	}

	// Verify journal/<owner-id>.jsonl exists and room.created verifies against root_pubkey
	journalPath := filepath.Join(roomDir, "journal", id.MemberID+".jsonl")
	jData, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatalf("failed to read journal file: %v", err)
	}

	ev, err := event.UnmarshalJSONLine(jData)
	if err != nil {
		t.Fatalf("failed to unmarshal room.created event: %v", err)
	}

	if ev.Kind != event.KindRoomCreated {
		t.Errorf("expected event kind room.created, got %s", ev.Kind)
	}

	rawRootPubkey := strings.TrimPrefix(cfg.RootPubkey, "ed25519:")
	pubBytes, err := hex.DecodeString(rawRootPubkey)
	if err != nil {
		t.Fatalf("invalid root pubkey hex: %v", err)
	}

	if err := ev.VerifySignature(ed25519.PublicKey(pubBytes)); err != nil {
		t.Errorf("room.created failed verification against root_pubkey: %v", err)
	}
}

func TestInitRoomNameSpecialCharsRoundTrip(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tempDir)

	// Identity names are user-controlled and flow into .symroom/local.toml,
	// so they must be TOML-escaped as well.
	id, err := identity.Generate("founder \"quoted\"\n#hash")
	if err != nil {
		t.Fatalf("failed to generate identity: %v", err)
	}

	roomName := "He said \"hello\"\n# not a comment"
	roomDir := filepath.Join(tempDir, "special-room")
	cfg, err := Init(roomDir, roomName, id)
	if err != nil {
		t.Fatalf("failed to init room: %v", err)
	}

	// room.toml must be valid TOML and load back to the exact values Init returned.
	var parsedRoom RoomConfig
	if _, err := toml.DecodeFile(filepath.Join(roomDir, "room.toml"), &parsedRoom); err != nil {
		t.Fatalf("room.toml could not be parsed as TOML: %v", err)
	}
	if parsedRoom.SchemaVersion != cfg.SchemaVersion {
		t.Errorf("schema_version round-trip = %d, want %d", parsedRoom.SchemaVersion, cfg.SchemaVersion)
	}
	if parsedRoom.ID != cfg.ID {
		t.Errorf("id round-trip = %q, want %q", parsedRoom.ID, cfg.ID)
	}
	if parsedRoom.Created != cfg.Created {
		t.Errorf("created round-trip = %q, want %q", parsedRoom.Created, cfg.Created)
	}
	if parsedRoom.RootPubkey != cfg.RootPubkey {
		t.Errorf("root_pubkey round-trip = %q, want %q", parsedRoom.RootPubkey, cfg.RootPubkey)
	}
	if parsedRoom.RootEvent != cfg.RootEvent {
		t.Errorf("root_event round-trip = %q, want %q", parsedRoom.RootEvent, cfg.RootEvent)
	}

	// The CLI's room.toml reader must still parse the encoded file.
	readCfg, err := ReadRoomConfig(roomDir)
	if err != nil {
		t.Fatalf("ReadRoomConfig failed: %v", err)
	}
	if readCfg.ID != cfg.ID || readCfg.RootEvent != cfg.RootEvent {
		t.Errorf("ReadRoomConfig round-trip = %+v, want id %q and root_event %q", readCfg, cfg.ID, cfg.RootEvent)
	}

	// .symroom/local.toml must be valid TOML and round-trip the identity name exactly.
	var parsedLocal LocalConfig
	if _, err := toml.DecodeFile(filepath.Join(roomDir, ".symroom", "local.toml"), &parsedLocal); err != nil {
		t.Fatalf("local.toml could not be parsed as TOML: %v", err)
	}
	if parsedLocal.Identity != id.Name {
		t.Errorf("identity round-trip = %q, want %q", parsedLocal.Identity, id.Name)
	}
	if parsedLocal.ArtifactRoot != "" {
		t.Errorf("artifact_root round-trip = %q, want empty", parsedLocal.ArtifactRoot)
	}

	// The room name itself round-trips exactly through the room.created event body.
	journalPath := filepath.Join(roomDir, "journal", id.MemberID+".jsonl")
	jData, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatalf("failed to read journal file: %v", err)
	}
	ev, err := event.UnmarshalJSONLine(jData)
	if err != nil {
		t.Fatalf("failed to unmarshal room.created event: %v", err)
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(ev.Body, &body); err != nil {
		t.Fatalf("failed to unmarshal event body: %v", err)
	}
	if body.Name != roomName {
		t.Errorf("room name round-trip = %q, want %q", body.Name, roomName)
	}
}

func TestInitRefusesNonEmptyDir(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tempDir)

	id, err := identity.Generate("founder")
	if err != nil {
		t.Fatalf("failed to generate identity: %v", err)
	}

	roomDir := filepath.Join(tempDir, "existing-room")
	if err := os.MkdirAll(roomDir, 0755); err != nil {
		t.Fatalf("failed to mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(roomDir, "some-file.txt"), []byte("data"), 0644); err != nil {
		t.Fatalf("failed to write dummy file: %v", err)
	}

	_, err = Init(roomDir, "Test Room", id)
	if err == nil {
		t.Fatalf("expected error initializing non-empty dir, got nil")
	}
	if err != ErrNotEmpty {
		t.Errorf("expected ErrNotEmpty, got %v", err)
	}
}
