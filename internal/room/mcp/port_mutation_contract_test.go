package mcp

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/room/identity"
)

const mcpMutationFixture = "../../../testdata/port/room/mcp-mutations.json"

func TestSymRoomMCPMutationOracle(t *testing.T) {
	roomDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(roomDir, "room.toml"), []byte("schema_version = 1\nid = \"rm_fixture\"\ncreated = \"2026-09-01T00:00:00Z\"\nroot_pubkey = \"ed25519:fixture\"\nroot_event = \"ev_fixture\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	seed := sha256.Sum256([]byte("symroom-mcp-mutation-seed"))
	privateKey := ed25519.NewKeyFromSeed(seed[:])
	publicKey := privateKey.Public().(ed25519.PublicKey)
	id := &identity.Identity{Name: "fixture", MemberID: identity.ComputeMemberID(publicKey), PublicKey: publicKey, PrivateKey: privateKey}
	_, file, _, _ := runtime.Caller(0)
	artifactPath := filepath.Clean(filepath.Join(filepath.Dir(file), "../../..", "testdata/port/room/mcp-artifact.txt"))
	if contents, err := os.ReadFile(artifactPath); err != nil || strings.TrimSpace(string(contents)) != "artifact-content" {
		t.Fatalf("artifact fixture: %v", err)
	}
	requests := []any{
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "room_note_post", "arguments": map[string]any{"text": "hello from MCP"}}},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "room_artifact_link", "arguments": map[string]any{"path": "${ARTIFACT}", "title": "Fixture artifact"}}},
		map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": map[string]any{"name": "room_run_request", "arguments": map[string]any{"title": "Fixture run", "plan_file": "plan.md", "adapter": "local"}}},
		map[string]any{"jsonrpc": "2.0", "id": 4, "method": "tools/call", "params": map[string]any{"name": "room_checkpoint_request", "arguments": map[string]any{"run_id": "run_fixture", "question": "Continue?"}}},
	}
	fixtureRequests := make([]any, 0, len(requests))
	responses := make([]any, 0, len(requests))
	for _, request := range requests {
		fixtureRequestBytes, _ := json.Marshal(request)
		var fixtureRequest any
		_ = json.Unmarshal(fixtureRequestBytes, &fixtureRequest)
		fixtureRequests = append(fixtureRequests, fixtureRequest)
		requestMap := request.(map[string]any)
		params := requestMap["params"].(map[string]any)
		args := params["arguments"].(map[string]any)
		if args["path"] == "${ARTIFACT}" {
			args["path"] = artifactPath
		}
		body, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		var input, output bytes.Buffer
		fmt.Fprintf(&input, "Content-Length: %d\r\n\r\n%s", len(body), body)
		if err := NewServer(roomDir, id, filepath.Dir(artifactPath)).ServeIO(context.Background(), &input, &output); err != nil {
			t.Fatal(err)
		}
		decoded, err := decodeFrames(output.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		responses = append(responses, decoded[0])
	}
	doc := map[string]any{"schema_version": 1, "oracle": map[string]any{"commit": "745c08e8144971c61133c5d0e5d61c7ce405aad2", "release": "post-v0.12.2-security-880"}}
	cases := make([]map[string]any, 0, len(requests))
	for i := range fixtureRequests {
		response := responses[i].(map[string]any)
		result := response["result"].(map[string]any)
		content := result["content"].([]any)[0].(map[string]any)
		var event map[string]any
		if err := json.Unmarshal([]byte(content["text"].(string)), &event); err != nil {
			t.Fatalf("case %d event: %v", i, err)
		}
		delete(event, "ts")
		delete(event, "sig")
		delete(event, "prev")
		if event["kind"] == "note.posted" {
			event["id"] = "ev_<generated>"
		}
		body := event["body"].(map[string]any)
		if event["kind"] == "checkpoint.requested" {
			event["id"] = "ev_<generated>"
			body["checkpoint_id"] = "chk_<generated>"
		}
		normalized, _ := json.Marshal(event)
		content["text"] = string(normalized)
		cases = append(cases, map[string]any{"request": fixtureRequests[i], "response": response})
	}
	doc["cases"] = cases
	journalBytes, err := os.ReadFile(filepath.Join(roomDir, "journal", id.MemberID+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	content, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	content = append(content, '\n')
	path := filepath.Clean(mcpMutationFixture)
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
		t.Fatalf("Go mutation behavior differs from mcp-mutations.json; generated %d bytes, fixture %d bytes", len(content), len(want))
	}
	if len(strings.Split(strings.TrimSuffix(string(journalBytes), "\n"), "\n")) != 4 {
		t.Fatal("expected all four mutations to append signed journal events")
	}
}
