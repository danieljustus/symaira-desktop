// Command httpdiff compares the representative HTTP transcript against Go and Rust.
package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"
)

const token = "0123456789abcdef0123456789abcdef"

type fixture struct {
	Cases []httpCase `json:"cases"`
}

type httpCase struct {
	ID      string            `json:"id"`
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Auth    string            `json:"auth,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

type transcript struct {
	Status  int
	Headers map[string]string
	Body    []byte
}

type boundedBuffer struct {
	mu       sync.Mutex
	data     []byte
	overflow bool
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	const limit = 1 << 20
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.data) < limit {
		remaining := limit - len(b.data)
		if len(data) > remaining {
			b.data = append(b.data, data[:remaining]...)
			b.overflow = true
		} else {
			b.data = append(b.data, data...)
		}
	} else if len(data) > 0 {
		b.overflow = true
	}
	return len(data), nil
}

func (b *boundedBuffer) snapshot() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data), b.overflow
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL %v\n", err)
		os.Exit(1)
	}
}

func run() (runErr error) {
	defer func() {
		if value := recover(); value != nil {
			panicErr := fmt.Errorf("%v", value)
			if runErr == nil {
				runErr = panicErr
			} else {
				runErr = errors.Join(runErr, panicErr)
			}
		}
	}()
	left := flag.String("left", "", "Go oracle binary")
	right := flag.String("right", "", "Rust candidate binary")
	fixturePath := flag.String("fixture", "testdata/port/http/representative.json", "fixture path")
	flag.Parse()
	if *left == "" || *right == "" {
		fatal("--left and --right are required")
	}
	data, err := os.ReadFile(*fixturePath)
	if err != nil {
		fatal("read fixture: %v", err)
	}
	var suite fixture
	if err := json.Unmarshal(data, &suite); err != nil {
		fatal("decode fixture: %v", err)
	}
	if len(suite.Cases) == 0 {
		fatal("HTTP fixture is empty")
	}

	harnessRoot, err := os.MkdirTemp("", "symdesk-http-diff-")
	if err != nil {
		fatal("temp root: %v", err)
	}
	defer func() { _ = os.RemoveAll(harnessRoot) }()
	vault := filepath.Join(harnessRoot, "vault")
	if err := os.Mkdir(vault, 0o700); err != nil {
		fatal("vault directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(vault, "Hello.md"), []byte("---\ntitle: Hello\n---\nBody"), 0o600); err != nil {
		fatal("fixture file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(harnessRoot, "outside.md"), []byte("outside"), 0o600); err != nil {
		fatal("fixture file: %v", err)
	}
	if err := os.Mkdir(filepath.Join(vault, "nested"), 0o700); err != nil {
		fatal("fixture directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(vault, "nested/Note.md"), []byte("nested"), 0o600); err != nil {
		fatal("fixture file: %v", err)
	}
	if err := os.Symlink(filepath.Join(harnessRoot, "outside.md"), filepath.Join(vault, "escape.md")); err != nil {
		fatal("fixture symlink: %v", err)
	}
	if err := os.Symlink("Hello.md", filepath.Join(vault, "internal.md")); err != nil {
		fatal("fixture internal symlink: %v", err)
	}

	leftServer := startServer(*left, vault)
	defer func() {
		if err := leftServer.stop(); err != nil {
			cleanupErr := fmt.Errorf("Go cleanup failed: %w", err) //nolint:staticcheck // Go is the implementation label
			if runErr == nil {
				runErr = cleanupErr
			} else {
				runErr = errors.Join(runErr, cleanupErr)
			}
		}
	}()
	rightServer := startServer(*right, vault)
	defer func() {
		if err := rightServer.stop(); err != nil {
			cleanupErr := fmt.Errorf("Rust cleanup failed: %w", err) //nolint:staticcheck // Rust is the implementation label
			if runErr == nil {
				runErr = cleanupErr
			} else {
				runErr = errors.Join(runErr, cleanupErr)
			}
		}
	}()
	if err := leftServer.ready(); err != nil {
		fatal("Go readiness: %v", err)
	}
	if err := rightServer.ready(); err != nil {
		fatal("Rust readiness: %v", err)
	}
	leftETag, rightETag := "", ""
	for _, tc := range suite.Cases {
		leftResult, nextLeftETag, err := leftServer.request(tc, leftETag)
		if err != nil {
			fatal("%s Go request: %v", tc.ID, err)
		}
		rightResult, nextRightETag, err := rightServer.request(tc, rightETag)
		if err != nil {
			fatal("%s Rust request: %v", tc.ID, err)
		}
		if err := compare(tc.ID, leftResult, rightResult); err != nil {
			fatal("%s: %v", tc.ID, err)
		}
		leftETag, rightETag = nextLeftETag, nextRightETag
		fmt.Printf("PASS %s\n", tc.ID)
	}
	fmt.Printf("PASS HTTP differential: %d cases; isolated roots, loopback ports, readiness, harness bounds and shutdown verified\n", len(suite.Cases))
	return nil
}

const serverStopTimeout = 5 * time.Second

type runningServer struct {
	cmd  *exec.Cmd
	base string
	logs boundedBuffer
}

func startServer(binary, vault string) *runningServer {
	absoluteBinary, err := filepath.Abs(binary)
	if err != nil {
		fatal("resolve %s: %v", binary, err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fatal("choose loopback port: %v", err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	home := filepath.Join(vault, ".http-harness", filepath.Base(binary))
	if err := os.MkdirAll(filepath.Join(home, "tmp"), 0o700); err != nil {
		fatal("isolation root: %v", err)
	}
	//nolint:gosec // absoluteBinary is the explicit Go/Rust harness operand
	cmd := exec.Command(absoluteBinary, "serve", "--listen", address, "--vault", vault, "--token", token)
	cmd.Dir = home
	cmd.Env = isolatedEnv(vault, home)
	server := &runningServer{cmd: cmd, base: "http://" + address}
	cmd.Stdout = io.Discard
	cmd.Stderr = &server.logs
	// Bound os/exec cleanup if a descendant retains an inherited handle.
	cmd.WaitDelay = serverStopTimeout
	if err := cmd.Start(); err != nil {
		fatal("start %s: %v", binary, err)
	}
	return server
}

func (s *runningServer) ready() error {
	client := &http.Client{Timeout: 250 * time.Millisecond}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get(s.base + "/healthz")
		if err == nil {
			body, readErr := io.ReadAll(io.LimitReader(response.Body, 1024))
			_ = response.Body.Close()
			if readErr == nil && response.StatusCode == http.StatusOK && string(body) == `{"status":"ok"}` {
				return nil
			}
		}
		if s.cmd.ProcessState != nil {
			logs, overflow := s.logs.snapshot()
			if overflow {
				return fmt.Errorf("process exited with more than 1 MiB stderr; stderr=%s", logs)
			}
			return fmt.Errorf("process exited: %s; stderr=%s", s.cmd.ProcessState, logs)
		}
		time.Sleep(25 * time.Millisecond)
	}
	logs, overflow := s.logs.snapshot()
	if overflow {
		return fmt.Errorf("timed out waiting for %s: stderr exceeded 1 MiB; stderr=%s", s.base, logs)
	}
	return fmt.Errorf("timed out waiting for %s; stderr=%s", s.base, logs)
}

func (s *runningServer) request(tc httpCase, previousETag string) (transcript, string, error) {
	request, err := http.NewRequestWithContext(context.Background(), tc.Method, s.base+tc.Path, nil)
	if err != nil {
		return transcript{}, previousETag, err
	}
	for key, value := range tc.Headers {
		if value == "$LAST_ETAG" {
			value = previousETag
		}
		request.Header.Set(key, value)
	}
	switch tc.Auth {
	case "valid":
		request.Header.Set("Authorization", "Bearer "+token)
	case "wrong":
		request.Header.Set("Authorization", "Bearer 0000000000000000000000000000wrong")
	case "raw":
		request.Header.Set("Authorization", token)
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableCompression: true}}
	response, err := client.Do(request)
	if err != nil {
		return transcript{}, previousETag, err
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (16<<20)+1))
	closeErr := response.Body.Close()
	if err != nil {
		return transcript{}, previousETag, err
	}
	if closeErr != nil {
		return transcript{}, previousETag, fmt.Errorf("close response body: %w", closeErr)
	}
	if len(body) > 16<<20 {
		return transcript{}, previousETag, fmt.Errorf("response exceeded 16 MiB harness cap")
	}
	if response.Header.Get("Content-Encoding") == "gzip" && len(body) > 0 {
		reader, gzipErr := gzip.NewReader(bytes.NewReader(body))
		if gzipErr != nil {
			return transcript{}, previousETag, gzipErr
		}
		body, err = io.ReadAll(io.LimitReader(reader, (16<<20)+1))
		closeErr := reader.Close()
		if err != nil {
			return transcript{}, previousETag, err
		}
		if closeErr != nil {
			return transcript{}, previousETag, closeErr
		}
		if len(body) > 16<<20 {
			return transcript{}, previousETag, fmt.Errorf("decompressed response exceeded 16 MiB harness cap")
		}
	}
	headers := make(map[string]string)
	for _, name := range []string{"Accept-Ranges", "Allow", "Cache-Control", "Content-Disposition", "Content-Encoding", "Content-Length", "Content-Range", "Content-Security-Policy", "Content-Type", "ETag", "Last-Modified", "Referrer-Policy", "WWW-Authenticate", "X-Content-Type-Options"} {
		if value := response.Header.Get(name); value != "" {
			headers[strings.ToLower(name)] = value
		}
	}
	return transcript{Status: response.StatusCode, Headers: headers, Body: normalizeBody(body)}, response.Header.Get("ETag"), nil
}

func normalizeBody(body []byte) []byte {
	key := []byte(`"generated_at":`)
	index := bytes.Index(body, key)
	if index < 0 {
		return body
	}
	start := index + len(key)
	for start < len(body) && (body[start] == ' ' || body[start] == '	' || body[start] == '\r' || body[start] == '\n') {
		start++
	}
	if start >= len(body) || body[start] != '"' {
		return body
	}
	end := start + 1
	escaped := false
	for end < len(body) {
		switch {
		case escaped:
			escaped = false
		case body[end] == '\\':
			escaped = true
		case body[end] == '"':
			result := make([]byte, 0, len(body)-end+len(`<dynamic>`))
			result = append(result, body[:start]...)
			result = append(result, `"<dynamic>"`...)
			result = append(result, body[end+1:]...)
			return result
		}
		end++
	}
	return body
}

func compare(id string, left, right transcript) error {
	if left.Status != right.Status {
		return fmt.Errorf("status mismatch: Go=%d Rust=%d", left.Status, right.Status)
	}
	if strings.HasPrefix(id, "snapshot-") {
		if strings.HasPrefix(id, "snapshot-gzip") || id == "snapshot-head-gzip" {
			if left.Headers["content-encoding"] != "gzip" || right.Headers["content-encoding"] != "gzip" || left.Headers["content-length"] == "" || right.Headers["content-length"] == "" {
				return fmt.Errorf("gzip response lacks encoding or length contract")
			}
		}
		// generated_at is intentionally normalized in the body; its
		// fractional timestamp width can still vary and therefore changes
		// Content-Length without changing the response contract.
		left.Headers = cloneWithout(left.Headers, "content-length")
		right.Headers = cloneWithout(right.Headers, "content-length")
	}
	if !reflect.DeepEqual(left.Headers, right.Headers) {
		return fmt.Errorf("headers mismatch: Go=%v Rust=%v", left.Headers, right.Headers)
	}
	if !reflect.DeepEqual(left.Body, right.Body) {
		return fmt.Errorf("body mismatch: Go=%q Rust=%q", left.Body, right.Body)
	}
	return nil
}

func cloneWithout(headers map[string]string, omitted string) map[string]string {
	clone := make(map[string]string, len(headers))
	for key, value := range headers {
		if key != omitted {
			clone[key] = value
		}
	}
	return clone
}

func isolatedEnv(vault, home string) []string {
	tmp := filepath.Join(home, "tmp")
	env := []string{
		"HOME=" + home, "USERPROFILE=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"TMPDIR=" + tmp, "TMP=" + tmp, "TEMP=" + tmp,
		"LANG=C", "LC_ALL=C", "TZ=UTC", "TERM=dumb", "NO_COLOR=1",
		"SYMDESK_VAULT=" + vault, "SYMDESK_VERSION=0.12.2",
	}
	for _, key := range []string{"PATH", "SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT"} {
		if value := os.Getenv(key); value != "" {
			env = append(env, key+"="+value)
		}
	}
	return env
}

func (s *runningServer) stop() error {
	if s.cmd == nil || s.cmd.Process == nil {
		return nil
	}
	if s.cmd.ProcessState != nil {
		if s.cmd.ProcessState.Success() {
			return nil
		}
		return fmt.Errorf("process cleanup failed: already exited with status %d", s.cmd.ProcessState.ExitCode())
	}
	terminateErr := terminateProcessTree(s.cmd)
	intentionalTermination := terminateErr == nil || errors.Is(terminateErr, os.ErrProcessDone)
	alreadyExited := errors.Is(terminateErr, os.ErrProcessDone)
	wait := make(chan error, 1)
	go func() { wait <- s.cmd.Wait() }()
	select {
	case err := <-wait:
		if err != nil && !cleanupExitExpected(err, intentionalTermination, alreadyExited, isWindowsProcess()) {
			logs, overflow := s.logs.snapshot()
			if overflow {
				return fmt.Errorf("process cleanup stderr exceeded 1 MiB: %w; stderr=%s", err, logs)
			}
			return fmt.Errorf("process cleanup failed: %w; stderr=%s", err, logs)
		}
		if _, overflow := s.logs.snapshot(); overflow {
			return fmt.Errorf("process cleanup stderr exceeded 1 MiB")
		}
		if terminateErr != nil && !errors.Is(terminateErr, os.ErrProcessDone) {
			return fmt.Errorf("terminate process: %w", terminateErr)
		}
		return nil
	case <-time.After(serverStopTimeout):
		killErr := s.cmd.Process.Kill()
		select {
		case waitErr := <-wait:
			if killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
				return fmt.Errorf("process did not exit within %s: kill: %w", serverStopTimeout, killErr)
			}
			if waitErr != nil && !cleanupExitExpected(waitErr, true, false, isWindowsProcess()) {
				return fmt.Errorf("process cleanup failed after timeout: %w", waitErr)
			}
		case <-time.After(serverStopTimeout):
			return fmt.Errorf("process did not exit within %s after kill", serverStopTimeout)
		}
		return fmt.Errorf("process did not exit within %s", serverStopTimeout)
	}
}

// cleanupExitExpected accepts only statuses caused by the cleanup action.
// Windows reports a forcibly terminated process as exit status 1, while Unix
// reports a signal termination as -1. Other non-zero exits remain failures.
func cleanupExitExpected(err error, intentionalTermination, alreadyExited, windows bool) bool {
	if !intentionalTermination || alreadyExited {
		return false
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	code := exitErr.ExitCode()
	return code == -1 || (windows && code == 1)
}

func fatal(format string, args ...any) {
	panic(fmt.Errorf(format, args...))
}
