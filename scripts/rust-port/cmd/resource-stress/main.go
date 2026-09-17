// Command resource-stress exercises the RUST-006 resource
// limits against both the Go oracle and the Rust representative binary.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	maxMCPBytes       = 1 << 20
	maxNoteBytes      = 8 << 20
	maxBodyBytes      = 16 << 20
	httpHeaderTimeout = 5 * time.Second
	token             = "0123456789abcdef0123456789abcdef"
)

type response struct {
	status int
	body   []byte
}

type limitedOutput struct {
	bytes.Buffer
	overflow bool
}

func (b *limitedOutput) Write(data []byte) (int, error) {
	if b.Len() < maxMCPBytes {
		remaining := maxMCPBytes - b.Len()
		if len(data) > remaining {
			_, _ = b.Buffer.Write(data[:remaining])
			b.overflow = true
			return len(data), nil
		}
	}
	if len(data) > 0 {
		b.overflow = true
	}
	return len(data), nil
}

func main() {
	goBinary := flag.String("go", "", "Go oracle symdesk binary")
	rustBinary := flag.String("rust", "", "Rust candidate symdesk binary")
	root := flag.String("root", "", "writable stress root")
	flag.Parse()
	if *goBinary == "" || *rustBinary == "" || *root == "" {
		fatal("--go, --rust, and --root are required")
	}
	if err := os.MkdirAll(*root, 0o700); err != nil {
		fatal("create stress root: %v", err)
	}
	for _, candidate := range []struct {
		name string
		path string
	}{
		{name: "Go", path: *goBinary},
		{name: "Rust", path: *rustBinary},
	} {
		if err := runMCPStress(candidate.name, candidate.path, *root); err != nil {
			fatal("%s MCP stress: %v", candidate.name, err)
		}
		if err := runHTTPStress(candidate.name, candidate.path, *root); err != nil {
			fatal("%s HTTP stress: %v", candidate.name, err)
		}
	}
	fmt.Println("PASS SEC-003 stress slice: MCP frame/line/response bounds, HTTP snapshot/file bounds, slow-peer deadline, and process cleanup")
}

func runMCPStress(name, binary, root string) error {
	for _, tc := range []struct {
		name  string
		input []byte
	}{
		{name: "line", input: append(bytes.Repeat([]byte{'x'}, maxMCPBytes+1), '\n')},
		{name: "framed", input: []byte(fmt.Sprintf("Content-Length: %d\r\n\r\n", maxMCPBytes+1))},
	} {
		testRoot, err := os.MkdirTemp(root, "mcp-")
		if err != nil {
			return err
		}
		defer func() {
			if cleanupErr := os.RemoveAll(testRoot); cleanupErr != nil {
				fatal("remove MCP stress root: %v", cleanupErr)
			}
		}() // #nosec G304 -- test root was created above
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		cmd := exec.CommandContext(ctx, binary, "mcp") // #nosec G204 -- explicit test operand
		cmd.Env = isolatedEnv(testRoot, filepath.Join(testRoot, "vault"))
		cmd.Stdin = bytes.NewReader(tc.input)
		var stdout, stderr limitedOutput
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		started := time.Now()
		err = cmd.Run()
		elapsed := time.Since(started)
		timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
		cancel()
		if timedOut {
			return fmt.Errorf("%s did not reject %s input within 3s", name, tc.name)
		}
		if err == nil {
			return fmt.Errorf("%s accepted oversized %s input", name, tc.name)
		}
		if stdout.overflow || stderr.overflow {
			return fmt.Errorf("%s oversized %s output exceeded 1 MiB (stdout=%d stderr=%d)", name, tc.name, stdout.Len(), stderr.Len())
		}
		if elapsed >= 3*time.Second {
			return fmt.Errorf("%s oversized %s input took %s", name, tc.name, elapsed)
		}
	}
	return runMCPPartialCancellation(name, binary, root)
}

func runMCPPartialCancellation(name, binary, root string) error {
	testRoot, err := os.MkdirTemp(root, "mcp-partial-")
	if err != nil {
		return err
	}
	defer func() {
		if cleanupErr := os.RemoveAll(testRoot); cleanupErr != nil {
			fatal("remove partial-MCP stress root: %v", cleanupErr)
		}
	}() // #nosec G304 -- test root was created above
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "mcp") // #nosec G204 -- explicit test operand
	cmd.Env = isolatedEnv(testRoot, filepath.Join(testRoot, "vault"))
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	var stdout, stderr limitedOutput
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	waited := false
	defer func() {
		if !waited {
			_ = stdin.Close()
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	if _, err := io.WriteString(stdin, "Content-Length: 100\r\n\r\n{"); err != nil {
		return err
	}
	time.Sleep(100 * time.Millisecond)
	if err := stdin.Close(); err != nil {
		return err
	}
	err = cmd.Wait()
	waited = true
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%s did not finish after peer cancellation within 2s", name)
	}
	if err == nil {
		return fmt.Errorf("%s accepted truncated MCP frame", name)
	}
	if stdout.overflow || stderr.overflow {
		return fmt.Errorf("%s truncated-frame output exceeded 1 MiB", name)
	}
	return nil
}

func runHTTPStress(name, binary, root string) error {
	testRoot, err := os.MkdirTemp(root, "http-")
	if err != nil {
		return err
	}
	defer func() {
		if cleanupErr := os.RemoveAll(testRoot); cleanupErr != nil {
			fatal("remove HTTP stress root: %v", cleanupErr)
		}
	}() // #nosec G304 -- test root was created above
	vault := filepath.Join(testRoot, "vault")
	if err := os.Mkdir(vault, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(vault, "small.md"), []byte("small note\n"), 0o600); err != nil {
		return err
	}
	large, err := os.OpenFile(filepath.Join(vault, "large.md"), os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- fixed file beneath fresh test vault
	if err != nil {
		return err
	}
	if err := large.Truncate(maxNoteBytes + 1); err != nil {
		_ = large.Close()
		return err
	}
	if err := large.Close(); err != nil {
		return err
	}

	server, err := startServer(binary, vault, testRoot)
	if err != nil {
		return err
	}
	defer func() { _ = server.stop() }()
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{DisableCompression: true}}
	if err := server.ready(client); err != nil {
		return err
	}

	snapshot, err := server.request(client, http.MethodGet, "/api/v1/snapshot", nil, true)
	if err != nil {
		return err
	}
	if snapshot.status != http.StatusOK {
		return fmt.Errorf("snapshot status=%d", snapshot.status)
	}
	if len(snapshot.body) > maxBodyBytes {
		return fmt.Errorf("snapshot exceeded harness body cap: %d", len(snapshot.body))
	}
	if bytes.Contains(snapshot.body, []byte(`"large.md"`)) {
		return errors.New("oversized Markdown file was included in snapshot")
	}

	file, err := server.request(client, http.MethodGet, "/api/v1/files?path=small.md", nil, true)
	if err != nil {
		return err
	}
	if file.status != http.StatusOK || !bytes.Equal(file.body, []byte("small note\n")) {
		return fmt.Errorf("small file response status=%d body=%q", file.status, file.body)
	}
	if strings.EqualFold(name, "rust") {
		for _, fileName := range []string{"aggregate-a.md", "aggregate-b.md"} {
			if err := createSparseFile(filepath.Join(vault, fileName), maxNoteBytes); err != nil {
				return err
			}
		}
		deadline := time.Now().Add(3 * time.Second)
		for {
			aggregate, err := server.request(client, http.MethodGet, "/api/v1/snapshot", nil, true)
			if err != nil {
				return err
			}
			if aggregate.status == http.StatusRequestEntityTooLarge {
				break
			}
			if aggregate.status != http.StatusOK {
				return fmt.Errorf("aggregate snapshot status=%d", aggregate.status)
			}
			if time.Now().After(deadline) {
				return errors.New("aggregate snapshot limit was not enforced")
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	if err := slowPeerCancellation(name, server.address); err != nil {
		return err
	}
	if err := server.stop(); err != nil {
		return err
	}
	return nil
}

func createSparseFile(path string, size int64) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- fixed caller-generated path beneath fresh test vault
	if err != nil {
		return err
	}
	if err := file.Truncate(size); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

type runningServer struct {
	cmd     *exec.Cmd
	address string
	logs    limitedOutput
}

func startServer(binary, vault, root string) (*runningServer, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	address := listener.Addr().String()
	_ = listener.Close()
	cmd := exec.Command(binary, "serve", "--listen", address, "--vault", vault, "--token", token) // #nosec G204 -- explicit test operand
	cmd.Dir = root
	cmd.Env = isolatedEnv(root, vault)
	server := &runningServer{cmd: cmd, address: "http://" + address}
	cmd.Stdout = io.Discard
	cmd.Stderr = &server.logs
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return server, nil
}

func (s *runningServer) ready(client *http.Client) error {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		result, err := s.request(client, http.MethodGet, "/healthz", nil, false)
		if err == nil && result.status == http.StatusOK && bytes.Equal(result.body, []byte(`{"status":"ok"}`)) {
			return nil
		}
		if s.cmd.ProcessState != nil {
			return fmt.Errorf("server exited: %s; stderr=%s", s.cmd.ProcessState, s.logs.String())
		}
		time.Sleep(25 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for %s; stderr=%s", s.address, s.logs.String())
}

func (s *runningServer) request(client *http.Client, method, path string, body []byte, auth bool) (response, error) {
	request, err := http.NewRequest(method, s.address+path, bytes.NewReader(body))
	if err != nil {
		return response{}, err
	}
	if auth {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if method == http.MethodPut {
		request.Header.Set("Content-Type", "text/markdown")
	}
	result, err := client.Do(request)
	if err != nil {
		return response{}, err
	}
	defer func() { _ = result.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(result.Body, maxBodyBytes+1))
	if err != nil {
		return response{}, err
	}
	if len(data) > maxBodyBytes {
		return response{}, fmt.Errorf("%s %s response exceeded 16 MiB harness cap (status=%d)", method, path, result.StatusCode)
	}
	return response{status: result.StatusCode, body: data}, nil
}

func (s *runningServer) stop() error {
	if s == nil || s.cmd == nil || s.cmd.Process == nil || s.cmd.ProcessState != nil {
		return nil
	}
	if runtime.GOOS != "windows" {
		if err := s.cmd.Process.Signal(os.Interrupt); err != nil {
			_ = s.cmd.Process.Kill()
		}
	} else if err := s.cmd.Process.Kill(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- s.cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil && runtime.GOOS == "windows" {
			return nil
		}
		return nil
	case <-time.After(3 * time.Second):
		_ = s.cmd.Process.Kill()
		return fmt.Errorf("server did not exit within 3s")
	}
}

func slowPeerCancellation(name, address string) error {
	conn, err := net.DialTimeout("tcp", strings.TrimPrefix(address, "http://"), time.Second)
	if err != nil {
		return err
	}
	started := time.Now()
	if _, err := io.WriteString(conn, "GET /healthz HTTP/1.1\r\nHost: "); err != nil {
		_ = conn.Close()
		return err
	}
	timeout := httpHeaderTimeout
	if strings.EqualFold(name, "go") {
		timeout = 10 * time.Second
	}
	if err := conn.SetReadDeadline(time.Now().Add(timeout + 2*time.Second)); err != nil {
		_ = conn.Close()
		return err
	}
	var response [1]byte
	read, readErr := conn.Read(response[:])
	_ = conn.Close()
	if readErr == nil || read > 0 {
		return errors.New("server kept incomplete HTTP headers alive past the deadline")
	}
	if timeoutErr, ok := readErr.(net.Error); ok && timeoutErr.Timeout() {
		return errors.New("server did not close incomplete HTTP headers before the deadline")
	}
	if elapsed := time.Since(started); elapsed < timeout-500*time.Millisecond {
		return fmt.Errorf("server closed incomplete headers too early after %s", elapsed)
	}
	client := &http.Client{Timeout: 2 * time.Second}
	result, err := (&runningServer{address: address}).request(client, http.MethodGet, "/healthz", nil, false)
	if err != nil {
		return fmt.Errorf("health after slow-peer cancellation: %w", err)
	}
	if result.status != http.StatusOK {
		return fmt.Errorf("health after slow-peer cancellation status=%d", result.status)
	}
	return nil
}

func isolatedEnv(root, vault string) []string {
	home := filepath.Join(root, "home")
	tmp := filepath.Join(root, "tmp")
	for _, dir := range []string{home, tmp, filepath.Join(home, ".config"), filepath.Join(home, ".local", "share"), filepath.Join(home, ".cache")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			fatal("create isolated directory: %v", err)
		}
	}
	return []string{
		"HOME=" + home,
		"USERPROFILE=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"TMPDIR=" + tmp,
		"TMP=" + tmp,
		"TEMP=" + tmp,
		"LANG=C",
		"LC_ALL=C",
		"TZ=UTC",
		"TERM=dumb",
		"NO_COLOR=1",
		"SYMDESK_VAULT=" + vault,
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "FAIL %s\n", fmt.Sprintf(format, args...))
	os.Exit(1)
}
