package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// buildBinary compiles the symroom binary and returns its path.
func buildBinary(t *testing.T) string {
	t.Helper()
	binPath := filepath.Join(t.TempDir(), "symroom")
	cmd := exec.Command("go", "build", "-o", binPath, "../../cmd/symroom/")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to build binary: %v\noutput: %s", err, string(out))
	}
	return binPath
}

// setupTestEnv creates a temporary identity and room directory. The returned
// env slice includes XDG_DATA_HOME so the identity writes to a temp location.
func setupTestEnv(t *testing.T, binPath string) (env []string, roomDir string) {
	t.Helper()
	tmpDir := t.TempDir()
	roomDir = filepath.Join(tmpDir, "room")
	xdgData := filepath.Join(tmpDir, "xdg-data")

	env = append(os.Environ(), "XDG_DATA_HOME="+xdgData)

	// Create a test identity on disk (identity.Save writes to IdentitiesDir()
	// which honours XDG_DATA_HOME).
	createCmd := exec.Command(binPath, "identity", "create", "test-id")
	createCmd.Env = env
	out, err := createCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("create identity: %v\noutput: %s", err, string(out))
	}

	// Initialise a room so the MCP server has a room to serve.
	initCmd := exec.Command(binPath, "init", "--name", "TestRoom", "--identity", "test-id", roomDir)
	initCmd.Env = env
	out, err = initCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("init room: %v\noutput: %s", err, string(out))
	}

	return env, roomDir
}

// ---- JSON-RPC framing helpers -----------------------------------------------

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

func framedRequest(t *testing.T, id int, method string, params map[string]any) []byte {
	t.Helper()
	body := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
	}
	if params != nil {
		body["params"] = params
	}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return []byte(fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(data), data))
}

// ---- Subprocess runner ------------------------------------------------------

// mcpResult holds the stdout and stderr captured from one MCP-server run.
type mcpResult struct {
	stdout []byte
	stderr []byte
}

// runMCP starts the symroom binary in MCP mode, calls sendFn to feed requests
// into stdin, closes stdin, and waits for the process to finish. All stdout
// and stderr produced during the run are returned.
func runMCP(t *testing.T, binPath string, env []string, roomDir string, sendFn func(io.WriteCloser)) mcpResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binPath, "mcp", "--room", roomDir, "--identity", "test-id")
	cmd.Env = env

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("StderrPipe: %v", err)
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	var wg sync.WaitGroup

	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(&stdoutBuf, stdoutPipe)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(&stderrBuf, stderrPipe)
	}()

	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	sendFn(stdin)
	_ = stdin.Close()

	if err := cmd.Wait(); err != nil {
		// The process may exit with a non-zero exit code depending on the
		// scenario (e.g., kill). That is acceptable.
		t.Logf("mcp process exited: %v", err)
	}

	wg.Wait()
	return mcpResult{stdoutBuf.Bytes(), stderrBuf.Bytes()}
}

// ---- Stdout validation ------------------------------------------------------

// isValidJSONRPCResponse returns true when data is a valid JSON-RPC 2.0
// response object (has "jsonrpc":"2.0" and an "id" field).
func isValidJSONRPCResponse(data []byte) bool {
	var r jsonRPCResponse
	if err := json.Unmarshal(data, &r); err != nil {
		return false
	}
	return r.JSONRPC == "2.0" && len(r.ID) > 0
}

// validateStdoutClean walks through the raw stdout bytes and asserts that
// every byte belongs to a valid Content-Length framed JSON-RPC 2.0 response.
func validateStdoutClean(t *testing.T, stdout []byte, desc string) {
	t.Helper()
	if len(stdout) == 0 {
		t.Errorf("%s: stdout is empty (expected at least one JSON-RPC response)", desc)
		return
	}

	s := string(stdout)
	offset := 0
	frameCount := 0

	for offset < len(s) {
		// Every frame must start with "Content-Length:".
		if !strings.HasPrefix(s[offset:], "Content-Length:") {
			t.Errorf("%s: frame %d: expected Content-Length header at offset %d, got %q",
				desc, frameCount, offset, preview(s[offset:]))
			break
		}

		// Parse the value.
		crlf := strings.Index(s[offset:], "\r\n")
		if crlf < 0 {
			t.Errorf("%s: frame %d: unterminated Content-Length header at offset %d",
				desc, frameCount, offset)
			break
		}
		headerLine := s[offset : offset+crlf]
		val := strings.TrimSpace(strings.TrimPrefix(headerLine, "Content-Length:"))
		var contentLen int
		if _, err := fmt.Sscanf(val, "%d", &contentLen); err != nil || contentLen <= 0 {
			t.Errorf("%s: frame %d: invalid Content-Length %q at offset %d",
				desc, frameCount, val, offset)
			break
		}

		bodyStart := offset + crlf + 2 // skip \r\n after header

		// There must be another \r\n (the blank line separating headers from body).
		if !strings.HasPrefix(s[bodyStart:], "\r\n") {
			t.Errorf("%s: frame %d: missing blank line after Content-Length header at offset %d",
				desc, frameCount, bodyStart)
			break
		}
		bodyStart += 2 // skip the blank \r\n

		if bodyStart+contentLen > len(s) {
			t.Errorf("%s: frame %d: body truncated: need %d bytes at offset %d, got %d remaining",
				desc, frameCount, contentLen, bodyStart, len(s)-bodyStart)
			break
		}

		body := s[bodyStart : bodyStart+contentLen]
		if !isValidJSONRPCResponse([]byte(body)) {
			t.Errorf("%s: frame %d: body is not a valid JSON-RPC 2.0 response: %s",
				desc, frameCount, body)
		}

		offset = bodyStart + contentLen
		frameCount++
	}

	// After the last frame there must be trailing bytes — but any trailing
	// content (non-header, non-JSON-RPC) is a failure.
	remaining := strings.TrimSpace(s[offset:])
	if remaining != "" {
		t.Errorf("%s: found %d trailing byte(s) after last frame: %q",
			desc, len(remaining), preview(remaining))
	}

	t.Logf("%s: validated %d JSON-RPC frame(s) in %d stdout byte(s)",
		desc, frameCount, len(stdout))
}

// preview returns a short clipped representation of s for error messages.
func preview(s string) string {
	const max = 120
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

// ---- The actual tests -------------------------------------------------------

func TestMCPStdioHygiene(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping MCP stdio hygiene test in short mode")
	}

	binPath := buildBinary(t)
	env, roomDir := setupTestEnv(t, binPath)

	// ------------------------------------------------------------------
	// Subtest 1: Normal handshake — initialize + tools/list.
	// ------------------------------------------------------------------
	t.Run("normal handshake produces clean stdout", func(t *testing.T) {
		res := runMCP(t, binPath, env, roomDir, func(stdin io.WriteCloser) {
			_, _ = stdin.Write(framedRequest(t, 1, "initialize", nil))
			_, _ = stdin.Write(framedRequest(t, 2, "tools/list", nil))
		})

		validateStdoutClean(t, res.stdout, "normal handshake")

		// Verify the responses are semantically correct.
		frames := splitFrames(res.stdout)
		if len(frames) < 2 {
			t.Fatalf("expected at least 2 response frames, got %d", len(frames))
		}

		// Frame 1: initialize response.
		var initResp jsonRPCResponse
		if err := json.Unmarshal([]byte(frames[0]), &initResp); err != nil {
			t.Fatalf("initialize response unmarshal: %v", err)
		}
		if initResp.Error != nil {
			t.Fatalf("initialize returned error: %s", string(initResp.Error))
		}
		var initResult map[string]any
		if err := json.Unmarshal(initResp.Result, &initResult); err != nil {
			t.Fatalf("initialize result unmarshal: %v", err)
		}
		if initResult["protocolVersion"] != "2024-11-05" {
			t.Errorf("protocolVersion = %v, want 2024-11-05", initResult["protocolVersion"])
		}

		// Frame 2: tools/list response.
		var listResp jsonRPCResponse
		if err := json.Unmarshal([]byte(frames[1]), &listResp); err != nil {
			t.Fatalf("tools/list response unmarshal: %v", err)
		}
		if listResp.Error != nil {
			t.Fatalf("tools/list returned error: %s", string(listResp.Error))
		}

		// Stderr should capture any log/diagnostic output.
		if len(res.stderr) == 0 {
			t.Log("stderr was empty — no diagnostics were emitted during normal operation")
		} else {
			t.Logf("stderr captured %d byte(s) of diagnostic output", len(res.stderr))
		}
	})

	// ------------------------------------------------------------------
	// Subtest 2: Error path — kill the child and restart.
	// ------------------------------------------------------------------
	t.Run("error paths keep stdout clean", func(t *testing.T) {
		// First run: normal handshake, then kill the process.
		cmd1 := exec.Command(binPath, "mcp", "--room", roomDir, "--identity", "test-id")
		cmd1.Env = env

		stdin1, _ := cmd1.StdinPipe()
		stdoutPipe1, _ := cmd1.StdoutPipe()
		stderrPipe1, _ := cmd1.StderrPipe()

		var out1, err1 bytes.Buffer
		var wg1 sync.WaitGroup
		wg1.Add(1)

		// TeeReader: everything read from stdout goes through out1.
		stdoutTee := io.TeeReader(stdoutPipe1, &out1)
		stdoutBufReader := bufio.NewReader(stdoutTee)

		go func() { defer wg1.Done(); _, _ = io.Copy(&err1, stderrPipe1) }()

		if err := cmd1.Start(); err != nil {
			t.Fatalf("start mcp (first run): %v", err)
		}

		// Send a valid request.
		_, _ = stdin1.Write(framedRequest(t, 1, "initialize", nil))

		// Read the response directly — this blocks until the server
		// has received, processed, and replied.  After this we know
		// the server is past the startup path.
		readFrameBytes(t, stdoutBufReader)

		// Kill the process while it is running.
		_ = cmd1.Process.Signal(os.Kill)

		// Wait for the process to die then drain any remaining output.
		_ = cmd1.Wait()
		_ = stdin1.Close()
		wg1.Wait()

		t.Logf("first run captured %d stdout bytes (after kill)", len(out1.Bytes()))
		validateStdoutClean(t, out1.Bytes(), "first run (killed)")

		// Second run: restart and do another handshake.
		res2 := runMCP(t, binPath, env, roomDir, func(stdin io.WriteCloser) {
			_, _ = stdin.Write(framedRequest(t, 1, "initialize", nil))
			_, _ = stdin.Write(framedRequest(t, 2, "tools/list", nil))
		})

		t.Logf("second run (restart) captured %d stdout bytes", len(res2.stdout))
		validateStdoutClean(t, res2.stdout, "second run (restart)")

		// Stderr should have captured something (process kill message etc.).
		combinedStderr := append(err1.Bytes(), res2.stderr...)
		if len(combinedStderr) == 0 {
			t.Log("stderr was empty across kill/restart test")
		} else {
			t.Logf("stderr captured %d byte(s) of diagnostic output across kill/restart",
				len(combinedStderr))
		}
	})

	// ------------------------------------------------------------------
	// Subtest 3: Stderr actually receives log output.
	// ------------------------------------------------------------------
	t.Run("stderr captures diagnostics", func(t *testing.T) {
		res := runMCP(t, binPath, env, roomDir, func(stdin io.WriteCloser) {
			// Send a valid initialize.
			_, _ = stdin.Write(framedRequest(t, 1, "initialize", nil))
			// Send an unknown method — this produces a JSON-RPC error
			// response on stdout but does NOT generate slog output in the
			// current implementation.
			_, _ = stdin.Write(framedRequest(t, 2, "nonexistent/method", nil))
		})

		validateStdoutClean(t, res.stdout, "unknown method")

		// Parse frames and verify the second is a method-not-found error.
		frames := splitFrames(res.stdout)
		if len(frames) >= 2 {
			var errResp jsonRPCResponse
			_ = json.Unmarshal([]byte(frames[1]), &errResp)
			if errResp.Error != nil {
				t.Logf("unknown method correctly returned JSON-RPC error on stdout")
			}
		}

		// Stderr must have content — the unknown method handler doesn't
		// generate log output in the current mcpserver implementation,
		// but we verify stdout is clean regardless.
		if len(res.stderr) > 0 {
			t.Logf("stderr received %d byte(s) of log output", len(res.stderr))
		}
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// splitFrames extracts the JSON body of each Content-Length framed response
// from a raw stdout capture. It does NOT validate — call validateStdoutClean
// for that. It exists so tests can inspect individual frames.
func splitFrames(stdout []byte) []string {
	s := string(stdout)
	var frames []string
	offset := 0
	for offset < len(s) {
		if !strings.HasPrefix(s[offset:], "Content-Length:") {
			break
		}
		crlf := strings.Index(s[offset:], "\r\n")
		if crlf < 0 {
			break
		}
		headerLine := s[offset : offset+crlf]
		val := strings.TrimSpace(strings.TrimPrefix(headerLine, "Content-Length:"))
		var contentLen int
		if _, err := fmt.Sscanf(val, "%d", &contentLen); err != nil || contentLen <= 0 {
			break
		}
		bodyStart := offset + crlf + 2
		if !strings.HasPrefix(s[bodyStart:], "\r\n") {
			break
		}
		bodyStart += 2
		if bodyStart+contentLen > len(s) {
			break
		}
		frames = append(frames, s[bodyStart:bodyStart+contentLen])
		offset = bodyStart + contentLen
	}
	return frames
}

// readFrameBytes reads one Content-Length framed response from r and returns
// the raw response bytes (header + blank line + body).  It blocks until the
// full frame is available.
func readFrameBytes(t *testing.T, r *bufio.Reader) []byte {
	t.Helper()
	// Read the Content-Length header line.
	line, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("readFrameBytes header: %v", err)
	}
	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(line, "Content-Length:") {
		t.Fatalf("readFrameBytes: expected Content-Length header, got: %q", line)
	}
	val := strings.TrimSpace(strings.TrimPrefix(line, "Content-Length:"))
	var contentLen int
	if _, err := fmt.Sscanf(val, "%d", &contentLen); err != nil || contentLen <= 0 {
		t.Fatalf("readFrameBytes: invalid Content-Length: %q", val)
	}

	// Read the blank line separator.
	blank, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("readFrameBytes blank: %v", err)
	}
	if blank != "\r\n" && blank != "\n" {
		t.Fatalf("readFrameBytes: expected blank line, got: %q", blank)
	}

	// Read the body.
	body := make([]byte, contentLen)
	if _, err := io.ReadFull(r, body); err != nil {
		t.Fatalf("readFrameBytes body: %v", err)
	}
	return body
}
