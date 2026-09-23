package room

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/room/event"
	"github.com/danieljustus/symaira-desktop/internal/room/identity"
	"github.com/danieljustus/symaira-desktop/internal/room/members"
)

const surrogateFixturePath = "testdata/port/room/surrogate.json"

type surrogateVector struct {
	ID        string `json:"id"`
	Line      string `json:"line"`
	Canonical string `json:"canonical"`
	Verified  bool   `json:"verified"`
	Members   int    `json:"members"`
	Name      string `json:"name"`
	ParseOK   bool   `json:"parse_ok"`
}

type surrogateFixture struct {
	SchemaVersion int               `json:"schema_version"`
	SourceHashes  map[string]string `json:"source_hashes"`
	Cases         []surrogateVector `json:"cases"`
}

func TestPortRoomSurrogateContract(t *testing.T) {
	seed := sha256.Sum256([]byte("symroom-port-surrogate-v1"))
	private := ed25519.NewKeyFromSeed(seed[:])
	public := private.Public().(ed25519.PublicKey)
	signer := &identity.Identity{MemberID: identity.ComputeMemberID(public), PublicKey: public, PrivateKey: private}
	shapes := []struct{ id, nameJSON, extra string }{
		{"signed-unpaired-surrogate", `"Room"`, `,"\ud800":0`},
		{"signed-low-surrogate", `"Room"`, `,"\udc00":0`},
		{"signed-pair", `"\ud83d\ude00"`, ``},
		{"signed-unpaired-value", `"\ud800"`, ``},
		{"escaped-literal", `"\\ud800"`, ``},
	}
	vectors := make([]surrogateVector, 0, len(shapes)+1)
	for _, shape := range shapes {
		body := json.RawMessage(`{"name":` + shape.nameJSON + `,"public_key":"` + hex.EncodeToString(public) + `"` + shape.extra + `}`)
		original := &event.Event{V: 1, ID: shape.id, Room: "r", Author: signer.MemberID, Kind: event.KindRoomCreated, Body: body}
		if err := original.Sign(signer); err != nil {
			t.Fatal(err)
		}
		canonical, err := event.CanonicalBytes(original)
		if err != nil {
			t.Fatal(err)
		}
		line, err := original.MarshalJSONLine()
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := event.UnmarshalJSONLine(line)
		if err != nil {
			t.Fatalf("%s: Go parser: %v", shape.id, err)
		}
		if err := parsed.VerifySignature(public); err != nil {
			t.Fatalf("%s: signed raw bytes: %v", shape.id, err)
		}
		state := members.NewState()
		if err := state.ApplyEvent(parsed); err != nil {
			t.Fatalf("%s: Go projection: %v", shape.id, err)
		}
		if len(state.Members) != 1 {
			t.Fatalf("%s: expected one projected member, got %d", shape.id, len(state.Members))
		}
		vectors = append(vectors, surrogateVector{
			ID: shape.id, Line: string(line), Canonical: string(canonical),
			Verified: true, Members: len(state.Members), Name: state.Members[signer.MemberID].Name, ParseOK: true,
		})
	}
	// Unlike a lone surrogate, a malformed Unicode escape is not valid JSON.
	broken := []byte(`{"body":{"\ud80x":0}}`)
	if _, err := event.UnmarshalJSONLine(broken); err == nil {
		t.Fatal("malformed Unicode escape accepted")
	}
	vectors = append(vectors, surrogateVector{ID: "malformed-escape", Line: string(broken), ParseOK: false})
	fixture := surrogateFixture{
		SchemaVersion: 1,
		SourceHashes: map[string]string{
			"internal/room/event/event.go":     roomFileSHA256(t, "internal/room/event/event.go"),
			"internal/room/members/members.go": roomFileSHA256(t, "internal/room/members/members.go"),
		},
		Cases: vectors,
	}
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	path := filepath.Join(roomRepoRoot(t), surrogateFixturePath)
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.WriteFile(path, encoded, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != string(encoded) {
		t.Fatal("Go surrogate fixture drift: regenerate explicitly")
	}
}
