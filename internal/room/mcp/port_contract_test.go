package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

const mcpParityFixture = "../../../testdata/port/room/mcp-parity.json"

func TestSymRoomMCPRepresentativeOracle(t *testing.T) {
	requests := []any{
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "room_journal_tail", "arguments": map[string]any{"limit": 2}}},
		map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": map[string]any{"name": "missing_tool", "arguments": map[string]any{}}},
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
	if err := NewServer(t.TempDir(), nil, "").ServeIO(context.Background(), &input, &output); err != nil {
		t.Fatal(err)
	}
	responses, err := decodeFrames(output.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	doc := struct {
		SchemaVersion int              `json:"schema_version"`
		Oracle        inventory.Oracle `json:"oracle"`
		Cases         []map[string]any `json:"cases"`
	}{SchemaVersion: 1, Oracle: inventory.Oracle{Commit: "745c08e8144971c61133c5d0e5d61c7ce405aad2", Release: "post-v0.12.2-security-880"}}
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
