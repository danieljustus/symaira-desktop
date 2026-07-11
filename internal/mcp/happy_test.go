package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/service"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

// newTestFactory creates a real serviceFactory backed by a temp vault + sidecar.
func newTestFactory(t *testing.T) serviceFactory {
	t.Helper()
	vaultRoot := t.TempDir()
	return func() (*service.Service, *sidecar.DB, error) {
		db, err := sidecar.Open(filepath.Join(vaultRoot, "sidecar.db"))
		if err != nil {
			return nil, nil, err
		}
		return service.New(vaultRoot, db), db, nil
	}
}

// seedNote creates a test note via the service and returns its relative path.
func seedNote(t *testing.T, factory serviceFactory, title, content string) string {
	t.Helper()
	svc, db, err := factory()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	path, err := svc.NoteNew(title, content, "")
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// --- desk_status happy path ---

func TestStatusToolHappyPath(t *testing.T) {
	cfg := &config.Config{Vault: "/test/vault"}
	tool := newStatusTool(cfg, false)

	out, err := tool.Handler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	status, ok := out.(map[string]string)
	if !ok {
		t.Fatalf("expected map[string]string, got %T", out)
	}
	if status["version"] != ServerVersion {
		t.Errorf("expected version %q, got %q", ServerVersion, status["version"])
	}
	if status["vault"] != "/test/vault" {
		t.Errorf("expected vault '/test/vault', got %q", status["vault"])
	}
	if status["capabilities"] != "read_only" {
		t.Errorf("expected capabilities 'read_only', got %q", status["capabilities"])
	}
}

// --- desk_ls happy path ---

func TestLsToolHappyPath(t *testing.T) {
	factory := newTestFactory(t)
	seedNote(t, factory, "Hello World", "body text")

	tool := newLsTool(factory)
	out, err := tool.Handler(context.Background(), json.RawMessage(`{"dir":""}`))
	if err != nil {
		t.Fatal(err)
	}
	files, ok := out.([]map[string]interface{})
	if !ok {
		t.Fatalf("expected []map[string]interface{}, got %T", out)
	}
	if len(files) == 0 {
		t.Fatal("expected at least one file from ls")
	}
	var found bool
	for _, f := range files {
		if p, _ := f["path"].(string); strings.Contains(p, "Hello") {
			found = true
		}
	}
	if !found {
		t.Error("expected to find the seeded note in ls results")
	}
}

// --- desk_search happy path ---

func TestSearchToolHappyPath(t *testing.T) {
	factory := newTestFactory(t)
	seedNote(t, factory, "Searchable Note", "The quick brown fox jumps")

	tool := newSearchTool(factory)
	out, err := tool.Handler(context.Background(), json.RawMessage(`{"query":"quick brown"}`))
	if err != nil {
		t.Fatal(err)
	}
	response, ok := out.(service.SearchResponse)
	if !ok {
		t.Fatalf("expected service.SearchResponse, got %T", out)
	}
	if len(response.Results) == 0 {
		t.Fatal("expected at least one search result")
	}
}

func TestSearchToolReturnsSyntaxFallbackHint(t *testing.T) {
	factory := newTestFactory(t)
	tool := newSearchTool(factory)

	out, err := tool.Handler(context.Background(), json.RawMessage(`{"query":"tag:"}`))
	if err != nil {
		t.Fatal(err)
	}
	response, ok := out.(service.SearchResponse)
	if !ok {
		t.Fatalf("expected service.SearchResponse, got %T", out)
	}
	if response.Hint == "" {
		t.Fatal("expected syntax fallback hint")
	}
}

func TestSearchToolSupportsScopedOperators(t *testing.T) {
	factory := newTestFactory(t)
	svc, db, err := factory()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := svc.DB.IndexDocument(&vault.Document{
		Path:    filepath.Join(svc.VaultRoot, "finance", "invoice.md"),
		Title:   "Invoice",
		SHA256:  "invoice",
		Created: "2026-01-01T00:00:00Z",
		Status:  "open",
		Body:    "steuer",
		Frontmatter: map[string]interface{}{
			"tags":          []interface{}{"invoice"},
			"document_type": "invoice",
		},
	}); err != nil {
		t.Fatal(err)
	}

	tool := newSearchTool(factory)
	out, err := tool.Handler(context.Background(), json.RawMessage(`{"query":"tag:invoice path:finance type:invoice -status:paid steuer"}`))
	if err != nil {
		t.Fatal(err)
	}
	response, ok := out.(service.SearchResponse)
	if !ok {
		t.Fatalf("expected service.SearchResponse, got %T", out)
	}
	if len(response.Results) != 1 || response.Results[0]["title"] != "Invoice" {
		t.Fatalf("unexpected results: %#v", response.Results)
	}
}

// --- desk_note_new happy path ---

func TestNoteNewToolHappyPath(t *testing.T) {
	factory := newTestFactory(t)
	tool := newNoteNewTool(factory)

	in, _ := json.Marshal(map[string]string{"title": "Brand New Note", "content": "Fresh content"})
	out, err := tool.Handler(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	result, ok := out.(map[string]string)
	if !ok {
		t.Fatalf("expected map[string]string, got %T", out)
	}
	if result["path"] == "" {
		t.Error("expected non-empty path from note_new")
	}
}

// --- desk_docs happy path ---

func TestDocsToolHappyPath(t *testing.T) {
	factory := newTestFactory(t)
	tool := newDocsTool(factory)

	in, _ := json.Marshal(map[string]string{})
	out, err := tool.Handler(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	results, ok := out.([]service.DocsListResult)
	if !ok {
		t.Fatalf("expected []service.DocsListResult, got %T", out)
	}
	// Empty vault returns empty list — exercises the happy path.
	if results == nil {
		t.Error("expected non-nil results slice, got nil")
	}
}

func TestStatusToolReadOnlyCapabilities(t *testing.T) {
	cfg := &config.Config{Vault: "/test"}
	tool := newStatusTool(cfg, false)
	out, err := tool.Handler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	status := out.(map[string]string)
	if status["capabilities"] != "read_only" {
		t.Errorf("expected 'read_only', got %q", status["capabilities"])
	}
}

func TestStatusToolReadWriteCapabilities(t *testing.T) {
	cfg := &config.Config{Vault: "/test"}
	tool := newStatusTool(cfg, true)
	out, err := tool.Handler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	status := out.(map[string]string)
	if status["capabilities"] != "read_write" {
		t.Errorf("expected 'read_write', got %q", status["capabilities"])
	}
}

func TestStartServerReadOnlyOmitsMutatingTools(t *testing.T) {
	vaultRoot := t.TempDir()
	cfg := &config.Config{Vault: vaultRoot}

	origStdin, origStdout := os.Stdin, os.Stdout
	stdinR, stdinW, _ := os.Pipe()
	stdoutR, stdoutW, _ := os.Pipe()
	os.Stdin = stdinR
	os.Stdout = stdoutW

	errCh := make(chan error, 1)
	go func() {
		errCh <- StartServer(cfg, "test", false)
		stdoutW.Close()
	}()

	sendRequest := func(request map[string]any) map[string]any {
		t.Helper()
		reqBytes, _ := json.Marshal(request)
		reqBytes = append(reqBytes, '\n')
		stdinW.Write(reqBytes)
		decoder := json.NewDecoder(stdoutR)
		var resp map[string]any
		decoder.Decode(&resp)
		return resp
	}

	resp := sendRequest(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/list",
	})
	result := resp["result"].(map[string]any)
	tools := result["tools"].([]any)

	toolNames := make(map[string]bool)
	for _, tl := range tools {
		tool := tl.(map[string]any)
		toolNames[tool["name"].(string)] = true
	}

	for _, name := range []string{"desk_note_new", "desk_ingest", "doc_set_status", "desk_ingest_retry", "desk_clip"} {
		if toolNames[name] {
			t.Errorf("mutating tool %q should NOT be registered in read-only mode", name)
		}
	}
	for _, name := range []string{"desk_status", "desk_ls", "desk_search", "desk_docs"} {
		if !toolNames[name] {
			t.Errorf("read-only tool %q should be registered", name)
		}
	}

	stdinW.Close()
	select {
	case <-errCh:
	case <-time.After(5 * time.Second):
	}
	os.Stdin, os.Stdout = origStdin, origStdout
	stdinR.Close()
	stdoutR.Close()
}

func TestStartServerReadWriteIncludesMutatingTools(t *testing.T) {
	vaultRoot := t.TempDir()
	cfg := &config.Config{Vault: vaultRoot}

	origStdin, origStdout := os.Stdin, os.Stdout
	stdinR, stdinW, _ := os.Pipe()
	stdoutR, stdoutW, _ := os.Pipe()
	os.Stdin = stdinR
	os.Stdout = stdoutW

	errCh := make(chan error, 1)
	go func() {
		errCh <- StartServer(cfg, "test", true)
		stdoutW.Close()
	}()

	sendRequest := func(request map[string]any) map[string]any {
		t.Helper()
		reqBytes, _ := json.Marshal(request)
		reqBytes = append(reqBytes, '\n')
		stdinW.Write(reqBytes)
		decoder := json.NewDecoder(stdoutR)
		var resp map[string]any
		decoder.Decode(&resp)
		return resp
	}

	resp := sendRequest(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/list",
	})
	result := resp["result"].(map[string]any)
	tools := result["tools"].([]any)

	toolNames := make(map[string]bool)
	for _, tl := range tools {
		tool := tl.(map[string]any)
		toolNames[tool["name"].(string)] = true
	}

	for _, name := range []string{"desk_note_new", "desk_ingest", "doc_set_status", "desk_ingest_retry", "desk_clip"} {
		if !toolNames[name] {
			t.Errorf("mutating tool %q should be registered in write mode", name)
		}
	}

	stdinW.Close()
	select {
	case <-errCh:
	case <-time.After(5 * time.Second):
	}
	os.Stdin, os.Stdout = origStdin, origStdout
	stdinR.Close()
	stdoutR.Close()
}

// --- StartServer integration test ---
// Exercises the full StartServer code path (service factory, tool
// registration, ServeStdio) by temporarily replacing os.Stdin/os.Stdout
// with pipes.

func TestStartServerIntegration(t *testing.T) {
	vaultRoot := t.TempDir()
	cfg := &config.Config{Vault: vaultRoot}

	// Save original stdio and create pipes.
	origStdin, origStdout := os.Stdin, os.Stdout
	stdinR, stdinW, _ := os.Pipe()
	stdoutR, stdoutW, _ := os.Pipe()
	os.Stdin = stdinR
	os.Stdout = stdoutW

	errCh := make(chan error, 1)
	go func() {
		errCh <- StartServer(cfg, "test-version", false)
		stdoutW.Close()
	}()

	// Helper: send a JSON-RPC request and read the response line.
	sendRequest := func(request map[string]any) map[string]any {
		t.Helper()
		reqBytes, _ := json.Marshal(request)
		reqBytes = append(reqBytes, '\n')
		if _, err := stdinW.Write(reqBytes); err != nil {
			t.Fatalf("write request: %v", err)
		}
		decoder := json.NewDecoder(stdoutR)
		var resp map[string]any
		if err := decoder.Decode(&resp); err != nil {
			t.Fatalf("read response: %v", err)
		}
		return resp
	}

	// 1. List tools — confirms server started and tools registered.
	listResp := sendRequest(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/list",
	})
	result, ok := listResp["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result in tools/list, got %v", listResp)
	}
	tools, ok := result["tools"].([]any)
	if !ok || len(tools) == 0 {
		t.Fatalf("expected non-empty tools list, got %v", result)
	}

	// 2. desk_status — version + vault path.
	statusResp := sendRequest(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{"name": "desk_status", "arguments": map[string]any{}},
	})
	statusResult, ok := statusResp["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result in desk_status, got %v", statusResp)
	}
	contentArr, _ := statusResult["content"].([]any)
	firstContent, _ := contentArr[0].(map[string]any)
	// text is the tool result serialized as a JSON object.
	textObj, ok := firstContent["text"].(map[string]any)
	if !ok {
		t.Fatalf("expected text object in desk_status content, got %T: %v", firstContent["text"], firstContent["text"])
	}
	version, _ := textObj["version"].(string)
	vault, _ := textObj["vault"].(string)
	if version != "test-version" {
		t.Errorf("expected version 'test-version', got %q", version)
	}
	if vault != vaultRoot {
		t.Errorf("expected vault %q, got %q", vaultRoot, vault)
	}

	// 3. desk_ls — empty vault returns empty list.
	lsResp := sendRequest(map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]any{"name": "desk_ls", "arguments": map[string]any{"dir": ""}},
	})
	lsResult, ok := lsResp["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result in desk_ls, got %v", lsResp)
	}
	lsContent, ok := lsResult["content"].([]any)
	if !ok || len(lsContent) == 0 {
		t.Fatalf("expected content in desk_ls, got %v", lsResult)
	}

	// Shut down.
	stdinW.Close()
	select {
	case err := <-errCh:
		if err != nil && err != io.EOF && !strings.Contains(err.Error(), "EOF") {
			t.Errorf("unexpected server exit error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("server did not exit within 5s")
	}

	// Restore original stdio.
	os.Stdin, os.Stdout = origStdin, origStdout
	stdinR.Close()
	stdoutR.Close()
}

// --- Verify zero stdio pollution ---
// All stdout output must be valid JSON-RPC lines.

func TestStartServerZeroStdioPollution(t *testing.T) {
	vaultRoot := t.TempDir()
	cfg := &config.Config{Vault: vaultRoot}

	origStdin, origStdout := os.Stdin, os.Stdout
	stdinR, stdinW, _ := os.Pipe()
	stdoutR, stdoutW, _ := os.Pipe()
	os.Stdin = stdinR
	os.Stdout = stdoutW

	errCh := make(chan error, 1)
	go func() {
		errCh <- StartServer(cfg, "", false)
		stdoutW.Close()
	}()

	// Send one request and close stdin.
	req, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "desk_status", "arguments": map[string]any{}},
	})
	req = append(req, '\n')
	stdinW.Write(req)
	stdinW.Close()

	// Capture all stdout output.
	var stdoutBuf bytes.Buffer
	done := make(chan struct{})
	go func() {
		io.Copy(&stdoutBuf, stdoutR)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}

	// Every line must be valid JSON.
	output := stdoutBuf.String()
	for i, line := range strings.Split(strings.TrimSpace(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !json.Valid([]byte(line)) {
			t.Errorf("non-JSON line %d in stdout: %q", i, line)
		}
	}

	// Wait for server.
	select {
	case <-errCh:
	case <-time.After(5 * time.Second):
	}

	os.Stdin, os.Stdout = origStdin, origStdout
	stdinR.Close()
}
