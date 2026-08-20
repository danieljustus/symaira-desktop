package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-corekit/mcpserver"

	"github.com/danieljustus/symaira-ingest/internal/store"
)

// callTool drives one initialize + one tools/call round trip. On success it
// returns the JSON-decoded tool result and isError=false. On failure the
// handler's error text is plain (not JSON), so it returns a map with that
// text under "error" and isError=true.
func callTool(t *testing.T, server *mcpserver.Server, name string, args map[string]any) (map[string]any, bool) {
	t.Helper()

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
			"name":      name,
			"arguments": args,
		},
	})
	resp := readResp(t, outR)
	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T, want map", resp.Result)
	}
	isError, _ := result["isError"].(bool)
	if isError {
		content, ok := result["content"].([]any)
		if !ok || len(content) == 0 {
			t.Fatalf("missing content in error result: %v", result)
		}
		first, ok := content[0].(map[string]any)
		if !ok {
			t.Fatalf("content[0] type = %T", content[0])
		}
		text, _ := first["text"].(string)
		return map[string]any{"error": text}, true
	}
	return parseToolText(t, resp), false
}

// callToolRaw is callTool but accepts arbitrary "arguments" values, so a
// malformed (non-object) payload can be sent to exercise the handler's own
// json.Unmarshal error branch.
func callToolRaw(t *testing.T, server *mcpserver.Server, name string, args any) (map[string]any, bool) {
	t.Helper()

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
			"name":      name,
			"arguments": args,
		},
	})
	resp := readResp(t, outR)
	if resp.Error != nil {
		return map[string]any{"error": resp.Error}, true
	}
	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T, want map", resp.Result)
	}
	isError, _ := result["isError"].(bool)
	if isError {
		content, ok := result["content"].([]any)
		if !ok || len(content) == 0 {
			t.Fatalf("missing content in error result: %v", result)
		}
		first, ok := content[0].(map[string]any)
		if !ok {
			t.Fatalf("content[0] type = %T", content[0])
		}
		text, _ := first["text"].(string)
		return map[string]any{"error": text}, true
	}
	return parseToolText(t, resp), false
}

func seedJob(t *testing.T, st *store.Store, sourcePath string) int64 {
	t.Helper()
	sum := sha256.Sum256([]byte(sourcePath))
	doc, _, err := st.CreateOrGet(context.Background(), sourcePath, hex.EncodeToString(sum[:]), "text/plain")
	if err != nil {
		t.Fatalf("CreateOrGet: %v", err)
	}
	job, err := st.EnqueueJob(context.Background(), doc.ID, "ingest")
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	if err := st.FailJob(context.Background(), job.ID, "boom"); err != nil {
		t.Fatalf("FailJob: %v", err)
	}
	return job.ID
}

func TestRegister_RetryJob_Success(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	jobID := seedJob(t, st, filepath.Join(dir, "doc.txt"))

	server := mcpserver.New("symingest", "0.1.0")
	Register(server, st, fakeEngine{}, filepath.Join(dir, "vault"), filepath.Join(dir, "archive"))

	raw, isError := callTool(t, server, "retry_job", map[string]any{"job_id": jobID})
	if isError {
		t.Fatalf("expected success, got error result: %v", raw)
	}
	if raw["status"] != "success" {
		t.Fatalf("status = %v, want success", raw["status"])
	}

	jobs, err := st.ListJobs(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != 1 || jobs[0].Status != "pending" {
		t.Fatalf("expected job to be pending, got %+v", jobs)
	}
}

func TestRegister_StartWatch_Success_AlreadyWatching(t *testing.T) {
	t.Cleanup(StopAllWatchers)

	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	watchDir := filepath.Join(dir, "inbox")
	if err := os.MkdirAll(watchDir, 0o755); err != nil {
		t.Fatal(err)
	}
	vault := filepath.Join(dir, "vault")
	if err := os.MkdirAll(vault, 0o755); err != nil {
		t.Fatal(err)
	}

	server := mcpserver.New("symingest", "0.1.0")
	Register(server, st, fakeEngine{}, vault, filepath.Join(dir, "archive"))

	raw, isError := callTool(t, server, "start_watch", map[string]any{"directory": watchDir})
	if isError {
		t.Fatalf("expected success, got error result: %v", raw)
	}
	if raw["status"] != "success" {
		t.Fatalf("status = %v, want success", raw["status"])
	}

	raw2, isError2 := callTool(t, server, "start_watch", map[string]any{"directory": watchDir})
	if !isError2 {
		t.Fatalf("expected error for already-watched directory, got: %v", raw2)
	}
}

func TestRegister_RetryJob_UnknownJobID(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	server := mcpserver.New("symingest", "0.1.0")
	Register(server, st, fakeEngine{}, filepath.Join(dir, "vault"), filepath.Join(dir, "archive"))

	raw, isError := callTool(t, server, "retry_job", map[string]any{"job_id": 999999})
	if !isError {
		t.Fatalf("expected error for unknown job ID, got: %v", raw)
	}
}

func TestRegister_StartWatch_MalformedArgs(t *testing.T) {
	t.Cleanup(StopAllWatchers)

	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	server := mcpserver.New("symingest", "0.1.0")
	Register(server, st, fakeEngine{}, filepath.Join(dir, "vault"), filepath.Join(dir, "archive"))

	raw, isError := callToolRaw(t, server, "start_watch", "not-a-json-object")
	if !isError {
		t.Fatalf("expected error for malformed arguments, got: %v", raw)
	}
}

func TestRegister_StartWatch_DefaultArchiveFromHome(t *testing.T) {
	t.Cleanup(StopAllWatchers)

	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	watchDir := filepath.Join(dir, "inbox")
	if err := os.MkdirAll(watchDir, 0o755); err != nil {
		t.Fatal(err)
	}
	vault := filepath.Join(dir, "vault")
	if err := os.MkdirAll(vault, 0o755); err != nil {
		t.Fatal(err)
	}

	// Empty default archive forces the handler to fall back to
	// $HOME/.local/share/symingest/archive.
	server := mcpserver.New("symingest", "0.1.0")
	Register(server, st, fakeEngine{}, vault, "")

	raw, isError := callTool(t, server, "start_watch", map[string]any{"directory": watchDir})
	if isError {
		t.Fatalf("expected success, got error result: %v", raw)
	}
}

func TestRegister_StartWatch_NonexistentDirectory(t *testing.T) {
	t.Cleanup(StopAllWatchers)

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

	server := mcpserver.New("symingest", "0.1.0")
	Register(server, st, fakeEngine{}, vault, filepath.Join(dir, "archive"))

	raw, isError := callTool(t, server, "start_watch", map[string]any{"directory": filepath.Join(dir, "does-not-exist")})
	if !isError {
		t.Fatalf("expected error for nonexistent directory, got: %v", raw)
	}
}

func TestRegister_StopWatch_MalformedArgs(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	server := mcpserver.New("symingest", "0.1.0")
	Register(server, st, fakeEngine{}, filepath.Join(dir, "vault"), filepath.Join(dir, "archive"))

	raw, isError := callToolRaw(t, server, "stop_watch", "not-a-json-object")
	if !isError {
		t.Fatalf("expected error for malformed arguments, got: %v", raw)
	}
}

func TestRegister_StartWatch_NoVault(t *testing.T) {
	t.Cleanup(StopAllWatchers)

	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	watchDir := filepath.Join(dir, "inbox")
	if err := os.MkdirAll(watchDir, 0o755); err != nil {
		t.Fatal(err)
	}

	server := mcpserver.New("symingest", "0.1.0")
	Register(server, st, fakeEngine{}, "", filepath.Join(dir, "archive"))

	raw, isError := callTool(t, server, "start_watch", map[string]any{"directory": watchDir})
	if !isError {
		t.Fatalf("expected error for missing vault, got: %v", raw)
	}
}

func TestRegister_StopWatch_Success_NotWatching(t *testing.T) {
	t.Cleanup(StopAllWatchers)

	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	watchDir := filepath.Join(dir, "inbox")
	if err := os.MkdirAll(watchDir, 0o755); err != nil {
		t.Fatal(err)
	}
	vault := filepath.Join(dir, "vault")
	if err := os.MkdirAll(vault, 0o755); err != nil {
		t.Fatal(err)
	}

	server := mcpserver.New("symingest", "0.1.0")
	Register(server, st, fakeEngine{}, vault, filepath.Join(dir, "archive"))

	startRaw, startErr := callTool(t, server, "start_watch", map[string]any{"directory": watchDir})
	if startErr {
		t.Fatalf("expected start_watch success, got error: %v", startRaw)
	}

	stopRaw, stopErr := callTool(t, server, "stop_watch", map[string]any{"directory": watchDir})
	if stopErr {
		t.Fatalf("expected stop_watch success, got error: %v", stopRaw)
	}
	if stopRaw["status"] != "success" {
		t.Fatalf("status = %v, want success", stopRaw["status"])
	}

	stopRaw2, stopErr2 := callTool(t, server, "stop_watch", map[string]any{"directory": watchDir})
	if !stopErr2 {
		t.Fatalf("expected error for not-watched directory, got: %v", stopRaw2)
	}
}
