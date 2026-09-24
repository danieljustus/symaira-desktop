package room

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/danieljustus/symaira-desktop/internal/room/event"
	"github.com/danieljustus/symaira-desktop/internal/room/identity"
)

type roomInitFixture struct {
	SchemaVersion int               `json:"schema_version"`
	Files         map[string]string `json:"files"`
	Modes         map[string]string `json:"modes"`
	NonemptyError string            `json:"nonempty_error"`
	Preserved     string            `json:"preserved"`
}

const roomInitFixturePath = "../../../testdata/port/room/init.json"
const roomInitID = "rm_0123456789abcdef"
const roomInitEventID = "ev_0123456789abcdef0123"
const roomInitTimestamp = "2026-01-02T03:04:05.006Z"

func TestPortRoomInitContract(t *testing.T) {
	id := roomInitIdentity(t)
	dir := t.TempDir()
	if _, err := Init(dir, "Parity Room", id); err != nil {
		t.Fatal(err)
	}
	// Init's random identifiers and wall clock are the only variable inputs.
	// Replace them with fixed contract values and re-sign the resulting event.
	journalPath := filepath.Join(dir, "journal", id.MemberID+".jsonl")
	line, err := os.ReadFile(journalPath) //nolint:gosec // journalPath is constructed beneath the test's temporary room
	if err != nil {
		t.Fatal(err)
	}
	ev, err := event.UnmarshalJSONLine(line)
	if err != nil {
		t.Fatal(err)
	}
	ev.ID, ev.Room, ev.TS = roomInitEventID, roomInitID, roomInitTimestamp
	if err := ev.Sign(id); err != nil {
		t.Fatal(err)
	}
	line, err = ev.MarshalJSONLine()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journalPath, line, 0o600); err != nil { //nolint:gosec // journalPath is a test fixture beneath the temporary room root
		t.Fatal(err)
	}
	roomConfig := &RoomConfig{SchemaVersion: 1, ID: roomInitID, Created: roomInitTimestamp, RootPubkey: "ed25519:" + hex.EncodeToString(id.PublicKey), RootEvent: roomInitEventID}
	var roomTOML bytes.Buffer
	if err := toml.NewEncoder(&roomTOML).Encode(roomConfig); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "room.toml"), roomTOML.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	localConfig := LocalConfig{Identity: id.Name}
	var localTOML bytes.Buffer
	if err := toml.NewEncoder(&localTOML).Encode(localConfig); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".symroom", "local.toml"), localTOML.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	fixture := roomInitFixture{
		SchemaVersion: 1,
		Files: map[string]string{
			".gitignore":                        readRoomInitFile(t, dir, ".gitignore"),
			".symroom/local.toml":               readRoomInitFile(t, dir, ".symroom/local.toml"),
			"journal/" + id.MemberID + ".jsonl": string(line),
			"room.toml":                         roomTOML.String(),
		},
		Modes: map[string]string{
			".gitignore":                        roomInitMode(t, dir, ".gitignore"),
			".symroom":                          roomInitMode(t, dir, ".symroom"),
			".symroom/local.toml":               roomInitMode(t, dir, ".symroom/local.toml"),
			"journal":                           roomInitMode(t, dir, "journal"),
			"journal/" + id.MemberID + ".jsonl": roomInitMode(t, dir, "journal/"+id.MemberID+".jsonl"),
			"room.toml":                         roomInitMode(t, dir, "room.toml"),
		},
	}
	nonempty := filepath.Join(t.TempDir(), "room")
	if err := os.MkdirAll(nonempty, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nonempty, "keep"), []byte("preserve me"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = Init(nonempty, "Parity Room", id)
	if err == nil {
		t.Fatal("Init accepted a nonempty directory")
	}
	fixture.NonemptyError = err.Error()
	fixture.Preserved = readRoomInitFile(t, nonempty, "keep")

	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.WriteFile(roomInitFixturePath, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	current, err := os.ReadFile(roomInitFixturePath)
	if err != nil {
		t.Fatal(err)
	}
	want, err := normalizeRoomInitFixture(current, runtime.GOOS == "windows")
	if err != nil {
		t.Fatalf("normalize committed room init fixture: %v", err)
	}
	got, err := normalizeRoomInitFixture(encoded, runtime.GOOS == "windows")
	if err != nil {
		t.Fatalf("normalize generated room init fixture: %v", err)
	}
	if !bytes.Equal(want, got) {
		t.Fatal("room init fixture is stale; regenerate from the Go oracle")
	}
}

func TestNormalizeRoomInitFixtureIgnoresUnixModesOnlyOnWindows(t *testing.T) {
	committed := []byte("{\n  \"schema_version\": 1,\n  \"files\": {\n    \"room.toml\": \"schema_version = 1\\n\"\n  },\n  \"modes\": {\n    \"room.toml\": \"0644\"\n  },\n  \"nonempty_error\": \"room directory is not empty\",\n  \"preserved\": \"preserve me\",\n  \"future_contract\": {\n    \"value\": \"frozen\"\n  }\n}\n")
	generated := []byte("{\n  \"schema_version\": 1,\n  \"files\": {\n    \"room.toml\": \"schema_version = 1\\n\"\n  },\n  \"modes\": {\n    \"room.toml\": \"platform\"\n  },\n  \"nonempty_error\": \"room directory is not empty\",\n  \"preserved\": \"preserve me\",\n  \"future_contract\": {\n    \"value\": \"frozen\"\n  }\n}\n")

	want, err := normalizeRoomInitFixture(committed, true)
	if err != nil {
		t.Fatal(err)
	}
	got, err := normalizeRoomInitFixture(generated, true)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, got) {
		t.Fatal("Windows comparison should ignore only the unobservable POSIX modes")
	}
	windowsWant := want

	want, err = normalizeRoomInitFixture(committed, false)
	if err != nil {
		t.Fatal(err)
	}
	got, err = normalizeRoomInitFixture(generated, false)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(want, got) {
		t.Fatal("Unix comparison must retain the recorded POSIX modes")
	}

	generated = bytes.Replace(generated, []byte("frozen"), []byte("changed"), 1)
	got, err = normalizeRoomInitFixture(generated, true)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(windowsWant, got) {
		t.Fatal("Windows comparison must continue checking non-mode fixture data")
	}
}

func normalizeRoomInitFixture(data []byte, windows bool) ([]byte, error) {
	if !windows {
		return data, nil
	}
	var fixture map[string]json.RawMessage
	if err := json.Unmarshal(data, &fixture); err != nil {
		return nil, err
	}
	// Windows cannot observe the POSIX permission bits recorded by the fixture.
	// Keep every other field, including fields unknown to this test version.
	delete(fixture, "modes")
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func roomInitIdentity(t *testing.T) *identity.Identity {
	t.Helper()
	sum := sha256.Sum256([]byte("symroom-port-identity/room-init"))
	private := ed25519.NewKeyFromSeed(sum[:])
	public := private.Public().(ed25519.PublicKey)
	return &identity.Identity{Name: "room-init", MemberID: identity.ComputeMemberID(public), PublicKey: public, PrivateKey: private}
}

func readRoomInitFile(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // name is a fixed room-init fixture path
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func roomInitMode(t *testing.T, dir, name string) string {
	t.Helper()
	info, err := os.Stat(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		return "platform"
	}
	return fmt.Sprintf("%04o", info.Mode().Perm())
}
