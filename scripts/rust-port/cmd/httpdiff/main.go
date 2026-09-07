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
	"runtime"
	"strings"
	"syscall"
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

type boundedBuffer struct{ data []byte }

func (b *boundedBuffer) Write(data []byte) (int, error) {
	const limit = 1 << 20
	if len(b.data) < limit {
		remaining := limit - len(b.data)
		if len(data) > remaining {
			b.data = append(b.data, data[:remaining]...)
		} else {
			b.data = append(b.data, data...)
		}
	}
	return len(data), nil
}

func main() {
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

	leftServer := startServer(*left, vault)
	rightServer := startServer(*right, vault)
	defer leftServer.stop()
	defer rightServer.stop()
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
	if err := leftServer.stop(); err != nil {
		fatal("Go graceful shutdown: %v", err)
	}
	if err := rightServer.stop(); err != nil {
		fatal("Rust graceful shutdown: %v", err)
	}
	fmt.Printf("PASS HTTP differential: %d cases; isolated roots, loopback ports, readiness, bounds and shutdown verified\n", len(suite.Cases))
}

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
	cmd := exec.Command(absoluteBinary, "serve", "--listen", address, "--vault", vault, "--token", token)
	cmd.Dir = home
	cmd.Env = isolatedEnv(vault, home)
	server := &runningServer{cmd: cmd, base: "http://" + address}
	cmd.Stdout = io.Discard
	cmd.Stderr = &server.logs
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
			return fmt.Errorf("process exited: %s; stderr=%s", s.cmd.ProcessState, s.logs.data)
		}
		time.Sleep(25 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for %s; stderr=%s", s.base, s.logs.data)
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
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, (16<<20)+1))
	if err != nil {
		return transcript{}, previousETag, err
	}
	if len(body) > 16<<20 {
		return transcript{}, previousETag, fmt.Errorf("response exceeded 16 MiB harness cap")
	}
	if response.Header.Get("Content-Encoding") == "gzip" {
		reader, gzipErr := gzip.NewReader(bytes.NewReader(body))
		if gzipErr != nil {
			return transcript{}, previousETag, gzipErr
		}
		body, err = io.ReadAll(io.LimitReader(reader, (16<<20)+1))
		_ = reader.Close()
		if err != nil {
			return transcript{}, previousETag, err
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
	if !reflect.DeepEqual(left.Headers, right.Headers) {
		return fmt.Errorf("headers mismatch: Go=%v Rust=%v", left.Headers, right.Headers)
	}
	if !reflect.DeepEqual(left.Body, right.Body) {
		return fmt.Errorf("body mismatch: Go=%q Rust=%q", left.Body, right.Body)
	}
	return nil
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
	if s.cmd == nil || s.cmd.Process == nil || s.cmd.ProcessState != nil {
		return nil
	}
	if runtime.GOOS == "windows" {
		_ = s.cmd.Process.Kill()
	} else if err := s.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		_ = s.cmd.Process.Kill()
	}
	wait := make(chan error, 1)
	go func() { wait <- s.cmd.Wait() }()
	select {
	case err := <-wait:
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) && exitErr.ExitCode() == 0 {
				return nil
			}
		}
		return nil
	case <-time.After(5 * time.Second):
		_ = s.cmd.Process.Kill()
		<-wait
		return fmt.Errorf("process did not exit within 5 seconds; stderr=%s", s.logs.data)
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "FAIL "+format+"\n", args...)
	os.Exit(1)
}
