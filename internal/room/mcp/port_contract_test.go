package mcp

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/room/event"
	"github.com/danieljustus/symaira-desktop/internal/room/identity"
	"github.com/danieljustus/symaira-desktop/internal/room/journal"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

const mcpParityFixture = "../../../testdata/port/room/mcp-parity.json"

func TestSymRoomMCPRepresentativeOracle(t *testing.T) {
	roomDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(roomDir, "room.toml"), []byte("schema_version = 1\nid = \"rm_fixture\"\ncreated = \"2026-09-01T00:00:00Z\"\nroot_pubkey = \"ed25519:fixture\"\nroot_event = \"ev_fixture\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	artifact := []byte("artifact-content")
	if err := os.WriteFile(filepath.Join(roomDir, "known.txt"), artifact, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(artifact)
	journalDir := filepath.Join(roomDir, "journal")
	if err := os.MkdirAll(journalDir, 0o700); err != nil {
		t.Fatal(err)
	}
	seed := sha256.Sum256([]byte("symroom-mcp-fixture-signing-seed"))
	privateKey := ed25519.NewKeyFromSeed(seed[:])
	publicKey := privateKey.Public().(ed25519.PublicKey)
	id := &identity.Identity{Name: "fixture", MemberID: identity.ComputeMemberID(publicKey), PublicKey: publicKey, PrivateKey: privateKey}
	j := journal.New(journalDir)
	appendSigned := func(eventID, kind, body string) {
		t.Helper()
		ev := &event.Event{V: event.CurrentVersion, ID: eventID, Room: "rm_fixture", Author: id.MemberID, TS: "2026-09-01T00:00:00.000Z", Kind: kind, Body: json.RawMessage(body)}
		if err := j.PrepareEvent(ev); err != nil {
			t.Fatal(err)
		}
		if err := ev.Sign(id); err != nil {
			t.Fatal(err)
		}
		if err := j.Append(ev); err != nil {
			t.Fatal(err)
		}
	}
	appendSigned("ev_fixture", event.KindArtifactLinked, fmt.Sprintf(`{"artifact_id":"art_fixture","path":"known.txt","sha256":"%s","title":"Known"}`, hex.EncodeToString(digest[:])))
	appendSigned("ev_run_request", event.KindRunRequested, `{"run_id":"run_fixture","title":"Fixture"}`)
	appendSigned("ev_run_denied", event.KindRunDenied, `{"run_id":"run_fixture","reason":"outside policy"}`)
	requests := []any{
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "room_journal_tail", "arguments": map[string]any{"limit": 2}}},
		map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": map[string]any{"name": "missing_tool", "arguments": map[string]any{}}},
		map[string]any{"jsonrpc": "2.0", "id": 4, "method": "tools/call", "params": map[string]any{"name": "room_status", "arguments": map[string]any{}}},
		map[string]any{"jsonrpc": "2.0", "id": 5, "method": "tools/call", "params": map[string]any{"name": "room_artifact_list", "arguments": map[string]any{}}},
		map[string]any{"jsonrpc": "2.0", "id": 6, "method": "tools/call", "params": map[string]any{"name": "room_run_wait", "arguments": map[string]any{"run_id": "run_fixture", "timeout_seconds": 0.02}}},
		map[string]any{"jsonrpc": "2.0", "id": 7, "method": "tools/call", "params": map[string]any{"name": "room_run_wait", "arguments": map[string]any{"run_id": "run_missing", "timeout_seconds": 0.02}}},
	}
	var input bytes.Buffer
	for _, request := range requests {
		body, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&input, "Content-Length: %d\r\n\r\n%s", len(body), body)
	}
	var output bytes.Buffer
	if err := NewServer(roomDir, nil, roomDir).ServeIO(context.Background(), &input, &output); err != nil {
		t.Fatal(err)
	}
	responses, err := decodeFrames(output.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	doc := struct {
		SchemaVersion int              `json:"schema_version"`
		Oracle        inventory.Oracle `json:"oracle"`
		JournalLines  []string         `json:"journal_lines"`
		Cases         []map[string]any `json:"cases"`
	}{SchemaVersion: 1, Oracle: inventory.Oracle{Commit: "745c08e8144971c61133c5d0e5d61c7ce405aad2", Release: "post-v0.12.2-security-880"}}
	journalBytes, err := os.ReadFile(filepath.Join(journalDir, id.MemberID+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	doc.JournalLines = strings.Split(strings.TrimSuffix(string(journalBytes), "\n"), "\n")
	responseByID := make(map[int]any, len(responses))
	for _, response := range responses {
		message := response.(map[string]any)
		responseByID[int(message["id"].(float64))] = response
	}
	for i, request := range requests {
		doc.Cases = append(doc.Cases, map[string]any{"request": request, "response": responseByID[i+1]})
	}
	content, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	content = append(content, '\n')
	path := filepath.Clean(mcpParityFixture)
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read oracle fixture %s: %v", path, err)
	}
	if !bytes.Equal(content, want) {
		t.Fatalf("MCP oracle fixture drifted; run PORT_GENERATE=1 go test ./internal/room/mcp -run '^TestSymRoomMCPRepresentativeOracle$'")
	}
}

func decodeFrames(data []byte) ([]any, error) {
	var result []any
	for len(data) > 0 {
		const prefix = "Content-Length: "
		if !bytes.HasPrefix(data, []byte(prefix)) {
			return nil, fmt.Errorf("invalid frame prefix %q", data)
		}
		end := bytes.Index(data, []byte("\r\n\r\n"))
		if end < 0 {
			return nil, fmt.Errorf("frame header not terminated")
		}
		var length int
		if _, err := fmt.Sscanf(string(data[:end]), prefix+"%d", &length); err != nil {
			return nil, err
		}
		start := end + 4
		if length < 0 || start+length > len(data) {
			return nil, fmt.Errorf("truncated frame")
		}
		var value any
		if err := json.Unmarshal(data[start:start+length], &value); err != nil {
			return nil, err
		}
		result = append(result, value)
		data = data[start+length:]
	}
	return result, nil
}
