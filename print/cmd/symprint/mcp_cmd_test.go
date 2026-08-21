package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// mcpInitializeRequest is a single newline-delimited JSON-RPC initialize
// request, the "line mode" the MCP stdio transport accepts alongside
// Content-Length framing.
const mcpInitializeRequest = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n"

// runMCPCmd executes the mcp command with stdin backed by the given input.
// Closing the pipe writer sends EOF, which makes the stdio server return
// deterministically — the test can never hang on the server loop. It returns
// the captured stdout and stderr plus the command error.
func runMCPCmd(t *testing.T, stdin string) (stdout, stderr string, execErr error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	if _, err := w.WriteString(stdin); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close stdin writer: %v", err)
	}
	origStdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = origStdin }()

	cmd := newMCPCmd()
	stderr = captureStderr(t, func() {
		stdout = captureStdout(t, func() {
			execErr = cmd.ExecuteContext(context.Background())
		})
	})
	return stdout, stderr, execErr
}

// TestMCPCmd_EmptyOutputRoot_WarnsOnStderr covers the mcp command with an
// empty mcp.output_root (the config default): the startup warning must appear
// on stderr only, stdout must carry nothing but JSON-RPC responses, and the
// server must terminate cleanly on stdin EOF.
func TestMCPCmd_EmptyOutputRoot_WarnsOnStderr(t *testing.T) {
	resetGlobalState(t)
	stdout, stderr, execErr := runMCPCmd(t, mcpInitializeRequest)
	if execErr != nil {
		t.Fatalf("mcp command failed: %v", execErr)
	}
	if !strings.Contains(stderr, "warning: mcp.output_root is not set") {
		t.Errorf("stderr = %q, want the output_root startup warning", stderr)
	}
	if strings.Contains(stdout, "warning") {
		t.Errorf("warning leaked onto stdout: %q", stdout)
	}
	if !strings.Contains(stdout, `"serverInfo"`) {
		t.Errorf("stdout = %q, want a JSON-RPC initialize response", stdout)
	}
	// Zero stdio pollution: every non-empty stdout line is JSON-RPC.
	lines := 0
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if line == "" {
			continue
		}
		lines++
		if !json.Valid([]byte(line)) {
			t.Errorf("stdout line %q is not valid JSON-RPC", line)
		}
	}
	if lines == 0 {
		t.Error("expected at least one JSON-RPC response on stdout")
	}
}

// TestMCPCmd_OutputRootSet_NoWarning covers the configured-output-root branch:
// with SYMPRINT_MCP_OUTPUT_ROOT set, no startup warning may appear anywhere.
func TestMCPCmd_OutputRootSet_NoWarning(t *testing.T) {
	resetGlobalState(t)
	t.Setenv("SYMPRINT_MCP_OUTPUT_ROOT", t.TempDir())
	stdout, stderr, execErr := runMCPCmd(t, mcpInitializeRequest)
	if execErr != nil {
		t.Fatalf("mcp command failed: %v", execErr)
	}
	if strings.Contains(stderr, "output_root") {
		t.Errorf("stderr = %q, want no output_root warning when the root is configured", stderr)
	}
	if !strings.Contains(stdout, `"serverInfo"`) {
		t.Errorf("stdout = %q, want the initialize response", stdout)
	}
}
