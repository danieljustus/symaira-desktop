package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-corekit/mcpserver"

	"github.com/danieljustus/symaira-ingest/internal/extract"
	"github.com/danieljustus/symaira-ingest/internal/store"
)

type fakeEngine struct{}

func (fakeEngine) Extract(ctx context.Context, path string, kind extract.Kind) (*extract.Result, error) {
	return &extract.Result{Text: "ocr-result", MIME: string(kind), Engine: "fake"}, nil
}

func TestRegister_IngestFile(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	vault := filepath.Join(dir, "vault")
	if err := os.MkdirAll(vault, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "scan.png")
	if err := os.WriteFile(src, []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, 0o644); err != nil {
		t.Fatal(err)
	}

	server := mcpserver.New("symingest", "0.1.0")
	Register(server, st, fakeEngine{}, vault, filepath.Join(dir, "archive"))

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	defer outR.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = server.ServeIO(ctx, inR, outW)
	}()

	// initialize
	writeFramed(t, inW, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
	})
	readResp(t, outR)

	// tools/call ingest_file
	writeFramed(t, inW, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "ingest_file",
			"arguments": map[string]any{"path": src},
		},
	})
	resp := readResp(t, outR)

	if id, ok := resp.ID.(float64); !ok || id != 2 {
		t.Fatalf("response id = %v, want 2", resp.ID)
	}
	raw := parseToolText(t, resp)
	if raw["status"] != "success" {
		t.Fatalf("status = %v, want success", raw["status"])
	}
	if raw["text_length"] != float64(len("ocr-result")) {
		t.Fatalf("text_length = %v", raw["text_length"])
	}
}

func writeFramed(t *testing.T, w io.WriteCloser, req map[string]any) {
	t.Helper()
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	_, err = w.Write([]byte("Content-Length: " + itoa(len(data)) + "\r\n\r\n" + string(data)))
	if err != nil {
		t.Fatal(err)
	}
}

func readResp(t *testing.T, r io.ReadCloser) jsonRPCResponse {
	t.Helper()
	br := bufio.NewReader(r)
	// read Content-Length header
	var length int
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		if strings.HasPrefix(line, "Content-Length:") {
			length = atoi(strings.TrimSpace(strings.TrimPrefix(line, "Content-Length:")))
		}
	}
	if length == 0 {
		t.Fatal("missing Content-Length")
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(br, body); err != nil {
		t.Fatal(err)
	}
	var resp jsonRPCResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

type jsonRPCResponse struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Result  any    `json:"result"`
	Error   any    `json:"error"`
}

func itoa(n int) string {
	return strconv.Itoa(n)
}

func atoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

// parseToolText extracts the JSON-encoded result from an MCP tool response's
// content[0].text field. The handler returns JSON-encoded strings per the MCP
// spec (TextContent.text must be a string), so this helper unmarshals them.
func parseToolText(t *testing.T, resp jsonRPCResponse) map[string]any {
	t.Helper()
	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T, want map", resp.Result)
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("missing content in result: %v", result)
	}
	first, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("content[0] type = %T", content[0])
	}
	text, ok := first["text"].(string)
	if !ok {
		t.Fatalf("content text type = %T, want string", first["text"])
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		t.Fatalf("failed to unmarshal tool text: %v", err)
	}
	return raw
}

func TestRegister_IngestFile_InvalidInput(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	server := mcpserver.New("symingest", "0.1.0")
	Register(server, st, fakeEngine{}, filepath.Join(dir, "vault"), filepath.Join(dir, "archive"))

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	defer outR.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = server.ServeIO(ctx, inR, outW)
	}()

	writeFramed(t, inW, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
	})
	readResp(t, outR)

	writeFramed(t, inW, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "ingest_file",
			"arguments": "not-a-json-object",
		},
	})
	resp := readResp(t, outR)
	if resp.Error != nil {
		return
	}
	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T, want map", resp.Result)
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("missing content in result: %v", result)
	}
	first, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("content[0] type = %T", content[0])
	}
	text, ok := first["text"].(string)
	if !ok {
		t.Fatalf("content text type = %T", first["text"])
	}
	if !strings.Contains(text, "invalid") && !strings.Contains(text, "error") {
		t.Fatalf("expected error message, got: %s", text)
	}
}

func TestRegister_IngestFile_NoVault(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	server := mcpserver.New("symingest", "0.1.0")
	Register(server, st, fakeEngine{}, "", "")

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	defer outR.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = server.ServeIO(ctx, inR, outW)
	}()

	writeFramed(t, inW, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
	})
	readResp(t, outR)

	writeFramed(t, inW, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "ingest_file",
			"arguments": map[string]any{"path": "/nonexistent/file.txt"},
		},
	})
	resp := readResp(t, outR)
	if resp.Error != nil {
		return
	}
	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T, want map", resp.Result)
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("missing content in result: %v", result)
	}
	first, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("content[0] type = %T", content[0])
	}
	text, ok := first["text"].(string)
	if !ok {
		t.Fatalf("content text type = %T", first["text"])
	}
	if !strings.Contains(text, "vault") && !strings.Contains(text, "error") {
		t.Fatalf("expected vault error message, got: %s", text)
	}
}

func TestRegister_IngestFile_Duplicate(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	vault := filepath.Join(dir, "vault")
	if err := os.MkdirAll(vault, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "scan.png")
	if err := os.WriteFile(src, []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, 0o644); err != nil {
		t.Fatal(err)
	}

	server := mcpserver.New("symingest", "0.1.0")
	Register(server, st, fakeEngine{}, vault, filepath.Join(dir, "archive"))

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	defer outR.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = server.ServeIO(ctx, inR, outW)
	}()

	writeFramed(t, inW, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
	})
	readResp(t, outR)

	writeFramed(t, inW, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "ingest_file",
			"arguments": map[string]any{"path": src},
		},
	})
	readResp(t, outR)

	writeFramed(t, inW, map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "ingest_file",
			"arguments": map[string]any{"path": src},
		},
	})
	resp := readResp(t, outR)
	if resp.Error != nil {
		t.Fatalf("expected no error for duplicate, got: %v", resp.Error)
	}
	raw := parseToolText(t, resp)
	if raw["status"] != "duplicate" {
		t.Fatalf("status = %v, want duplicate", raw["status"])
	}
}

func TestRegister_RetryJob_InvalidInput(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	server := mcpserver.New("symingest", "0.1.0")
	Register(server, st, fakeEngine{}, filepath.Join(dir, "vault"), filepath.Join(dir, "archive"))

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	defer outR.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = server.ServeIO(ctx, inR, outW)
	}()

	writeFramed(t, inW, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
	})
	readResp(t, outR)

	writeFramed(t, inW, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "retry_job",
			"arguments": "not-a-json-object",
		},
	})
	resp := readResp(t, outR)
	if resp.Error != nil {
		return
	}
	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T, want map", resp.Result)
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("missing content in result: %v", result)
	}
	first, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("content[0] type = %T", content[0])
	}
	text, ok := first["text"].(string)
	if !ok {
		t.Fatalf("content text type = %T", first["text"])
	}
	if !strings.Contains(text, "invalid") && !strings.Contains(text, "error") {
		t.Fatalf("expected error message, got: %s", text)
	}
}

func TestRegister_AddRule_InvalidInput(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	server := mcpserver.New("symingest", "0.1.0")
	Register(server, st, fakeEngine{}, filepath.Join(dir, "vault"), filepath.Join(dir, "archive"))

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	defer outR.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = server.ServeIO(ctx, inR, outW)
	}()

	writeFramed(t, inW, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
	})
	readResp(t, outR)

	writeFramed(t, inW, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "add_rule",
			"arguments": "not-a-json-object",
		},
	})
	resp := readResp(t, outR)
	if resp.Error != nil {
		return
	}
	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T, want map", resp.Result)
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("missing content in result: %v", result)
	}
	first, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("content[0] type = %T", content[0])
	}
	text, ok := first["text"].(string)
	if !ok {
		t.Fatalf("content text type = %T", first["text"])
	}
	if !strings.Contains(text, "invalid") && !strings.Contains(text, "error") {
		t.Fatalf("expected error message, got: %s", text)
	}
}

func TestRegister_DeleteRule_InvalidInput(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	server := mcpserver.New("symingest", "0.1.0")
	Register(server, st, fakeEngine{}, filepath.Join(dir, "vault"), filepath.Join(dir, "archive"))

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	defer outR.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = server.ServeIO(ctx, inR, outW)
	}()

	writeFramed(t, inW, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
	})
	readResp(t, outR)

	writeFramed(t, inW, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "delete_rule",
			"arguments": "not-a-json-object",
		},
	})
	resp := readResp(t, outR)
	if resp.Error != nil {
		return
	}
	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T, want map", resp.Result)
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("missing content in result: %v", result)
	}
	first, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("content[0] type = %T", content[0])
	}
	text, ok := first["text"].(string)
	if !ok {
		t.Fatalf("content text type = %T", first["text"])
	}
	if !strings.Contains(text, "invalid") && !strings.Contains(text, "error") {
		t.Fatalf("expected error message, got: %s", text)
	}
}

func TestRegister_DeleteRule_NotFound(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	server := mcpserver.New("symingest", "0.1.0")
	Register(server, st, fakeEngine{}, filepath.Join(dir, "vault"), filepath.Join(dir, "archive"))

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	defer outR.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = server.ServeIO(ctx, inR, outW)
	}()

	writeFramed(t, inW, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
	})
	readResp(t, outR)

	writeFramed(t, inW, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "delete_rule",
			"arguments": map[string]any{"rule_id": 99999},
		},
	})
	resp := readResp(t, outR)
	if resp.Error != nil {
		return
	}
	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T, want map", resp.Result)
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("missing content in result: %v", result)
	}
	first, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("content[0] type = %T", content[0])
	}
	text, ok := first["text"].(string)
	if !ok {
		t.Fatalf("content text type = %T", first["text"])
	}
	if !strings.Contains(text, "not found") && !strings.Contains(text, "error") {
		t.Fatalf("expected not found error, got: %s", text)
	}
}

func TestRegister_ListJobs_Empty(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	server := mcpserver.New("symingest", "0.1.0")
	Register(server, st, fakeEngine{}, filepath.Join(dir, "vault"), filepath.Join(dir, "archive"))

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	defer outR.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = server.ServeIO(ctx, inR, outW)
	}()

	writeFramed(t, inW, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
	})
	readResp(t, outR)

	writeFramed(t, inW, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "list_jobs",
			"arguments": map[string]any{},
		},
	})
	resp := readResp(t, outR)
	if resp.Error != nil {
		t.Fatalf("expected no error, got: %v", resp.Error)
	}
	raw := parseToolText(t, resp)
	if raw["status"] != "success" {
		t.Fatalf("status = %v, want success", raw["status"])
	}
}

func TestRegister_Rules(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	server := mcpserver.New("symingest", "0.1.0")
	Register(server, st, fakeEngine{}, filepath.Join(dir, "vault"), filepath.Join(dir, "archive"))

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	defer outR.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = server.ServeIO(ctx, inR, outW)
	}()

	// initialize
	writeFramed(t, inW, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
	})
	readResp(t, outR)

	// tools/call add_rule
	writeFramed(t, inW, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "add_rule",
			"arguments": map[string]any{
				"pattern": "test-pattern",
				"kind":    "category",
				"value":   "test-value",
			},
		},
	})
	resp := readResp(t, outR)
	raw := parseToolText(t, resp)
	if raw["status"] != "success" {
		t.Fatalf("status = %v, want success", raw["status"])
	}
	rule, _ := raw["rule"].(map[string]any)
	if rule["pattern"] != "test-pattern" || rule["kind"] != "category" || rule["value"] != "test-value" {
		t.Fatalf("unexpected rule result: %v", rule)
	}

	// tools/call list_rules
	writeFramed(t, inW, map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "list_rules",
			"arguments": map[string]any{},
		},
	})
	resp = readResp(t, outR)
	raw = parseToolText(t, resp)
	if raw["status"] != "success" {
		t.Fatalf("status = %v, want success", raw["status"])
	}
	rules, _ := raw["rules"].([]any)
	if len(rules) != 1 {
		t.Fatalf("len(rules) = %d, want 1", len(rules))
	}

	// tools/call delete_rule
	ruleID := int64(rule["id"].(float64))
	writeFramed(t, inW, map[string]any{
		"jsonrpc": "2.0",
		"id":      4,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "delete_rule",
			"arguments": map[string]any{
				"rule_id": ruleID,
			},
		},
	})
	resp = readResp(t, outR)
	raw = parseToolText(t, resp)
	if raw["status"] != "success" {
		t.Fatalf("status = %v, want success", raw["status"])
	}
}

// paperlessFixtureServer serves a single Paperless-ngx document fixture.
func paperlessFixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/documents/":
			json.NewEncoder(w).Encode(map[string]any{
				"count": 1,
				"results": []map[string]any{
					{"id": 1, "title": "MCP Override Doc", "created_date": "2026-01-15", "file_type": ".txt"},
				},
				"next": nil,
			})
		case "/api/documents/1/download/":
			w.Write([]byte("mcp override test content"))
		case "/api/tags/", "/api/correspondents/", "/api/document_types/", "/api/storage_paths/":
			json.NewEncoder(w).Encode(map[string]any{"count": 0, "results": []any{}, "next": nil})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRegister_ImportPaperless_NoVaultWithoutOverride(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// No default vault configured for the server.
	server := mcpserver.New("symingest", "0.1.0")
	Register(server, st, fakeEngine{}, "", "")

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	defer outR.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = server.ServeIO(ctx, inR, outW) }()

	writeFramed(t, inW, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"})
	readResp(t, outR)

	writeFramed(t, inW, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "import_paperless",
			"arguments": map[string]any{"base_url": "http://example.invalid", "token": "x"},
		},
	})
	resp := readResp(t, outR)
	if resp.Error != nil {
		return
	}
	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T, want map", resp.Result)
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("missing content in result: %v", result)
	}
	first, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("content[0] type = %T", content[0])
	}
	text, ok := first["text"].(string)
	if !ok {
		t.Fatalf("content text type = %T", first["text"])
	}
	if !strings.Contains(text, "vault") {
		t.Fatalf("expected vault error message, got: %s", text)
	}
}

func TestRegister_ImportPaperless_PathOverrides(t *testing.T) {
	srv := paperlessFixtureServer(t)

	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// The MCP server itself has no default vault/archive configured, so a
	// successful import here proves the per-call overrides are used.
	server := mcpserver.New("symingest", "0.1.0")
	Register(server, st, fakeEngine{}, "", "")

	overrideVault := filepath.Join(dir, "override-vault")
	overrideArchive := filepath.Join(dir, "override-archive")
	overrideDB := filepath.Join(dir, "override.db")

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	defer outR.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = server.ServeIO(ctx, inR, outW) }()

	writeFramed(t, inW, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"})
	readResp(t, outR)

	writeFramed(t, inW, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "import_paperless",
			"arguments": map[string]any{
				"base_url":     srv.URL,
				"token":        "test-token",
				"vault_path":   overrideVault,
				"archive_path": overrideArchive,
				"db_path":      overrideDB,
			},
		},
	})
	resp := readResp(t, outR)
	raw := parseToolText(t, resp)
	if raw["status"] != "success" {
		t.Fatalf("status = %v, want success (full response: %v)", raw["status"], raw)
	}
	if raw["imported"] != float64(1) {
		t.Fatalf("imported = %v, want 1", raw["imported"])
	}

	notes, err := filepath.Glob(filepath.Join(overrideVault, "*.md"))
	if err != nil || len(notes) != 1 {
		t.Fatalf("expected 1 note in override vault, got %v (err=%v)", notes, err)
	}
	archived, _ := filepath.Glob(filepath.Join(overrideArchive, "*"))
	if len(archived) != 1 {
		t.Fatalf("expected 1 archived file in override archive, got %v", archived)
	}
	if _, err := os.Stat(overrideDB); err != nil {
		t.Fatalf("expected override db_path to be created: %v", err)
	}

	// The shared default store must remain untouched by the override.
	defaultDocs, err := st.ListJobs(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListJobs on default store: %v", err)
	}
	if len(defaultDocs) != 0 {
		t.Fatalf("expected the default store to stay empty, got %d jobs", len(defaultDocs))
	}
}

func TestRegister_ImportPaperless_BoundedOptions(t *testing.T) {
	var listCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/documents/":
			listCalled = true
			w.WriteHeader(http.StatusInternalServerError)
		case "/api/documents/2/":
			json.NewEncoder(w).Encode(map[string]any{
				"id": 2, "title": "Bounded MCP Doc", "created_date": "2026-01-15", "file_type": ".txt",
				"original_file_name": "invoice.pdf",
				"storage_path":       map[string]any{"id": 9, "name": "Finance/Invoices"},
			})
		case "/api/documents/2/download/":
			w.Write([]byte("bounded mcp content"))
		case "/api/tags/", "/api/correspondents/", "/api/document_types/", "/api/storage_paths/":
			json.NewEncoder(w).Encode(map[string]any{"count": 0, "results": []any{}, "next": nil})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	server := mcpserver.New("symingest", "0.1.0")
	Register(server, st, fakeEngine{}, "", "")

	overrideVault := filepath.Join(dir, "override-vault")
	overrideArchive := filepath.Join(dir, "override-archive")
	reportPath := filepath.Join(dir, "report.json")

	raw, isError := callTool(t, server, "import_paperless", map[string]any{
		"base_url":               srv.URL,
		"token":                  "test-token",
		"vault_path":             overrideVault,
		"archive_path":           overrideArchive,
		"ids":                    []any{float64(2)},
		"limit":                  float64(1),
		"preserve_storage_paths": true,
		"report_path":            reportPath,
	})
	if isError {
		t.Fatalf("expected success, got error result: %v", raw)
	}
	if listCalled {
		t.Fatal("ids import must fetch documents individually instead of listing the archive")
	}
	selected, ok := raw["selected_ids"].([]any)
	if !ok || len(selected) != 1 || selected[0] != float64(2) {
		t.Fatalf("selected_ids = %v, want [2]", raw["selected_ids"])
	}
	wantNote := filepath.Join(overrideVault, "Finance", "Invoices", "invoice.md")
	if _, err := os.Stat(wantNote); err != nil {
		t.Fatalf("expected preserve_storage_paths note at %s: %v", wantNote, err)
	}
	if _, err := os.Stat(reportPath); err != nil {
		t.Fatalf("expected report_path to be written: %v", err)
	}
}

func TestResolveVaultArchive_DefaultArchiveHomeError(t *testing.T) {
	t.Setenv("HOME", "")
	_, _, err := resolveVaultArchive("/vault", "", "", "")
	if err == nil {
		t.Fatal("expected home-dir error for missing default archive base")
	}
	if !strings.Contains(err.Error(), "default archive") {
		t.Fatalf("error = %v, want mention of default archive", err)
	}
}

func TestStopAllWatchers(t *testing.T) {
	cancelled := false
	activeWatchers.Store("test-dir", &watcherEntry{
		cancel:  func() { cancelled = true },
		watcher: nil,
	})

	StopAllWatchers()

	if _, loaded := activeWatchers.Load("test-dir"); loaded {
		t.Fatal("expected watcher to be removed from activeWatchers")
	}
	if !cancelled {
		t.Fatal("expected cancel to be called")
	}
}
