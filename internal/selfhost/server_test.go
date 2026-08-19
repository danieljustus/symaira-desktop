package selfhost

import (
	"bufio"
	"bytes"
	cryptorand "crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testToken = "0123456789abcdef0123456789abcdef"

func TestServerAuthSnapshotAndSecureFiles(t *testing.T) {
	vaultRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(vaultRoot, "Hello.md"), []byte("---\ntitle: Hello\n---\nBody"), 0644); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(ServerConfig{VaultRoot: vaultRoot, Token: testToken, Version: "test", Executable: "/bin/false"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	response, err := http.Get(httpServer.URL + "/api/v1/status")
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.StatusCode)
	}
	response.Body.Close()

	response = authorized(t, http.MethodGet, httpServer.URL+"/api/v1/snapshot", nil, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("snapshot returned %d", response.StatusCode)
	}
	var snapshot struct {
		Notes []snapshotNote `json:"notes"`
	}
	if err := json.NewDecoder(response.Body).Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if len(snapshot.Notes) != 1 || snapshot.Notes[0].Path != "Hello.md" {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}

	request, err := http.NewRequest(http.MethodGet, httpServer.URL+"/api/v1/snapshot", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+testToken)
	request.Header.Set("If-None-Match", response.Header.Get("ETag"))
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusNotModified {
		t.Fatalf("expected cached snapshot to return 304, got %d", response.StatusCode)
	}
	response.Body.Close()

	if err := os.WriteFile(filepath.Join(vaultRoot, "Hello.md"), []byte("---\ntitle: Hello\n---\nChanged body"), 0644); err != nil {
		t.Fatal(err)
	}
	response = authorized(t, http.MethodGet, httpServer.URL+"/api/v1/snapshot", nil, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("changed snapshot returned %d", response.StatusCode)
	}
	response.Body.Close()

	response = authorized(t, http.MethodGet, httpServer.URL+"/api/v1/files?path=../outside", nil, "")
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected traversal rejection, got %d", response.StatusCode)
	}
	response.Body.Close()
}

// TestServerRestartReindexesCorrectlyFromAWarmSidecar exercises server.go's
// refreshIndex (now delegating to sidecar.DB.RefreshIndex, see issue #180)
// across a full server restart against the same vault: the second NewServer
// call reopens the same sidecar.db under VaultRoot/.symdesk and must still
// find the previously indexed note, whether the stat-based fast path skips
// it or falls back to a full parse. Detailed coverage of the fast path
// itself (skip on unchanged, re-index on change, same-size edits) lives in
// internal/sidecar's RefreshIndex tests.
func TestServerRestartReindexesCorrectlyFromAWarmSidecar(t *testing.T) {
	vaultRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(vaultRoot, "Hello.md"), []byte("---\ntitle: Hello\n---\nBody"), 0644); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(ServerConfig{VaultRoot: vaultRoot, Token: testToken, Version: "test", Executable: "/bin/false"})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := NewServer(ServerConfig{VaultRoot: vaultRoot, Token: testToken, Version: "test", Executable: "/bin/false"})
	if err != nil {
		t.Fatalf("restart failed: %v", err)
	}
	t.Cleanup(func() { _ = restarted.Close() })

	httpServer := httptest.NewServer(restarted.Handler())
	t.Cleanup(httpServer.Close)
	response := authorized(t, http.MethodGet, httpServer.URL+"/api/v1/snapshot", nil, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("snapshot after restart returned %d", response.StatusCode)
	}
	var snapshot struct {
		Notes []snapshotNote `json:"notes"`
	}
	if err := json.NewDecoder(response.Body).Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if len(snapshot.Notes) != 1 || snapshot.Notes[0].Path != "Hello.md" {
		t.Fatalf("unexpected snapshot after restart: %+v", snapshot)
	}
}

func TestSnapshotSkipsRescanWhenVaultUnchanged(t *testing.T) {
	vaultRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(vaultRoot, "Hello.md"), []byte("---\ntitle: Hello\n---\nBody"), 0644); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(ServerConfig{VaultRoot: vaultRoot, Token: testToken, Version: "test", Executable: "/bin/false"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	if server.vaultWatcher == nil {
		t.Skip("vault change watcher unavailable in this environment")
	}
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	response := authorized(t, http.MethodGet, httpServer.URL+"/api/v1/snapshot", nil, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("first snapshot returned %d", response.StatusCode)
	}
	firstETag := response.Header.Get("ETag")
	response.Body.Close()

	if server.snapshotDirty.Load() {
		t.Fatal("expected the snapshot to be marked clean right after a successful computation")
	}

	response = authorized(t, http.MethodGet, httpServer.URL+"/api/v1/snapshot", nil, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("second snapshot returned %d", response.StatusCode)
	}
	if response.Header.Get("ETag") != firstETag {
		t.Fatalf("etag changed with no vault modification: %q -> %q", firstETag, response.Header.Get("ETag"))
	}
	response.Body.Close()
	if server.snapshotDirty.Load() {
		t.Fatal("an unchanged-vault request must not mark the snapshot dirty")
	}

	if err := os.WriteFile(filepath.Join(vaultRoot, "Hello.md"), []byte("---\ntitle: Hello\n---\nChanged body"), 0644); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !server.snapshotDirty.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !server.snapshotDirty.Load() {
		t.Fatal("expected the vault watcher to mark the snapshot dirty after an external edit")
	}

	response = authorized(t, http.MethodGet, httpServer.URL+"/api/v1/snapshot", nil, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("post-edit snapshot returned %d", response.StatusCode)
	}
	if response.Header.Get("ETag") == firstETag {
		t.Fatal("expected a fresh etag after the vault changed")
	}
	response.Body.Close()
}

func TestServerRootedFilesystemRejectsEscapes(t *testing.T) {
	vaultRoot := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.md"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(vaultRoot, "escape")); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	server, err := NewServer(ServerConfig{VaultRoot: vaultRoot, Token: testToken, Version: "test", Executable: "/bin/false"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	for _, path := range []string{"../outside.md", "/etc/passwd", `escape\\secret.md`, ".symdesk/server/sidecar.db"} {
		response := authorized(t, http.MethodGet, httpServer.URL+"/api/v1/files?path="+path, nil, "")
		if response.StatusCode != http.StatusBadRequest {
			response.Body.Close()
			t.Fatalf("expected %q to be rejected with 400, got %d", path, response.StatusCode)
		}
		response.Body.Close()
	}

	response := authorized(t, http.MethodGet, httpServer.URL+"/api/v1/files?path=escape/secret.md", nil, "")
	if response.StatusCode != http.StatusNotFound {
		response.Body.Close()
		t.Fatalf("expected symlink escape to be unavailable, got %d", response.StatusCode)
	}
	response.Body.Close()

	response = authorized(t, http.MethodPut, httpServer.URL+"/api/v1/files?path=escape/new.md", strings.NewReader("outside write"), "text/markdown")
	if response.StatusCode == http.StatusOK {
		response.Body.Close()
		t.Fatal("expected write through escaping symlink to fail")
	}
	response.Body.Close()
	if _, err := os.Stat(filepath.Join(outside, "new.md")); !os.IsNotExist(err) {
		t.Fatalf("write escaped the vault root: %v", err)
	}

	response = authorized(t, http.MethodPut, httpServer.URL+"/api/v1/files?path=folder/new.md", strings.NewReader("# Rooted"), "text/markdown")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected valid nested write to succeed, got %d: %s", response.StatusCode, readBody(response))
	}
	response.Body.Close()
	written, err := os.ReadFile(filepath.Join(vaultRoot, "folder", "new.md"))
	if err != nil || string(written) != "# Rooted" {
		t.Fatalf("unexpected rooted write: %q, %v", written, err)
	}
}

func TestDistributedIngestLifecycle(t *testing.T) {
	vaultRoot := t.TempDir()
	server, err := NewServer(ServerConfig{VaultRoot: vaultRoot, Token: testToken, Version: "test", Executable: "/bin/false"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	var upload bytes.Buffer
	writer := multipart.NewWriter(&upload)
	part, err := writer.CreateFormFile("file", "invoice.png")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte("fake image"))
	_ = writer.Close()
	response := authorized(t, http.MethodPost, httpServer.URL+"/api/v1/ingest", &upload, writer.FormDataContentType())
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("ingest returned %d: %s", response.StatusCode, readBody(response))
	}
	var job Job
	if err := json.NewDecoder(response.Body).Decode(&job); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()

	leaseBody, _ := json.Marshal(map[string]any{"worker_id": "macbook", "capabilities": []string{"ocr"}})
	response = authorized(t, http.MethodPost, httpServer.URL+"/api/v1/worker/lease", bytes.NewReader(leaseBody), "application/json")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("lease returned %d", response.StatusCode)
	}
	response.Body.Close()

	completeBody, _ := json.Marshal(map[string]string{
		"job_id": job.ID, "worker_id": "macbook", "text": "Invoice total: 42 EUR", "engine": "ollama", "model": "gemma3",
	})
	response = authorized(t, http.MethodPost, httpServer.URL+"/api/v1/worker/complete", bytes.NewReader(completeBody), "application/json")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("complete returned %d: %s", response.StatusCode, readBody(response))
	}
	var completed Job
	if err := json.NewDecoder(response.Body).Decode(&completed); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if completed.Status != "completed" || completed.NotePath == "" {
		t.Fatalf("unexpected completed job: %+v", completed)
	}
	note, err := os.ReadFile(filepath.Join(vaultRoot, filepath.FromSlash(completed.NotePath)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(note), "Invoice total: 42 EUR") || !strings.Contains(string(note), "archive_path:") {
		t.Fatalf("completed note is incomplete: %s", note)
	}
}

// fakeSymdeskScript writes an executable shell script standing in for the
// symdesk binary, so streaming tests do not depend on a real AI backend.
func fakeSymdeskScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-symdesk.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestHandleCommandStreamsAskIncrementally(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh unavailable")
	}
	script := fakeSymdeskScript(t, `echo '{"type":"answer","text":"first"}'
sleep 0.2
echo '{"type":"answer","text":"second"}'
sleep 0.2
echo '{"type":"done"}'
`)
	vaultRoot := t.TempDir()
	server, err := NewServer(ServerConfig{VaultRoot: vaultRoot, Token: testToken, Version: "test", Executable: script})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	body, err := json.Marshal(commandRequest{Arguments: []string{"ask", "what is this vault about"}})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, httpServer.URL+"/api/v1/command", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+testToken)
	request.Header.Set("Content-Type", "application/json")

	start := time.Now()
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.StatusCode, readBody(response))
	}
	if contentType := response.Header.Get("Content-Type"); contentType != "application/x-ndjson" {
		t.Fatalf("expected application/x-ndjson, got %q", contentType)
	}

	var lines []string
	var arrivals []time.Duration
	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		arrivals = append(arrivals, time.Since(start))
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(lines) != 3 {
		t.Fatalf("expected 3 streamed lines, got %d: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "first") || !strings.Contains(lines[1], "second") || !strings.Contains(lines[2], "done") {
		t.Fatalf("unexpected streamed lines: %v", lines)
	}
	// The subprocess sleeps 200ms between each of its three writes (~400ms
	// total). Use a relative assertion — the first line must arrive at least
	// one sleep before the last — which proves the response is flushed
	// incrementally without depending on absolute machine speed.
	if arrivals[0] > arrivals[2]-150*time.Millisecond {
		t.Fatalf("first line arrived after %v but the last after %v; expected incremental delivery, not buffering until completion", arrivals[0], arrivals[2])
	}
	if arrivals[2] < 300*time.Millisecond {
		t.Fatalf("last line arrived after only %v; expected it to follow both sleeps", arrivals[2])
	}
}

func TestHandleCommandStreamTruncatesOversizedOutput(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh unavailable")
	}
	// Print well over maxCommandBytes (32 MiB) worth of NDJSON lines.
	script := fakeSymdeskScript(t, `awk 'BEGIN{for (i=0;i<450000;i++) print "{\"type\":\"answer\",\"text\":\"0123456789012345678901234567890123456789012345678901234567890123456789\"}"}'
`)
	vaultRoot := t.TempDir()
	server, err := NewServer(ServerConfig{VaultRoot: vaultRoot, Token: testToken, Version: "test", Executable: script})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	body, err := json.Marshal(commandRequest{Arguments: []string{"transform", "summarize"}})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, httpServer.URL+"/api/v1/command", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+testToken)
	request.Header.Set("Content-Type", "application/json")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (status already committed once streaming starts), got %d", response.StatusCode)
	}
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(data)) > maxCommandBytes+(1<<20) {
		t.Fatalf("streamed body was not bounded: got %d bytes", len(data))
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	last := lines[len(lines)-1]
	if !strings.Contains(last, `"type":"error"`) || !strings.Contains(last, "32 MiB") {
		t.Fatalf("expected a trailing bounded-error NDJSON line, got: %s", last)
	}
}

// TestHandleCommandScrubsServerTokensFromSubprocessEnv guards against
// SYMDESK_SERVER_TOKEN/SYMDESK_WORKER_TOKEN leaking into the environment of
// a remotely spawned symdesk subprocess (issue #175): a future command that
// echoes its environment (e.g. a diagnostics extension of `doctor`) must not
// be able to hand server credentials back to an authenticated remote client.
func TestHandleCommandScrubsServerTokensFromSubprocessEnv(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh unavailable")
	}
	t.Setenv("SYMDESK_SERVER_TOKEN", "leaked-server-token-0123456789")
	t.Setenv("SYMDESK_WORKER_TOKEN", "leaked-worker-token-0123456789")
	script := fakeSymdeskScript(t, "env\n")
	vaultRoot := t.TempDir()
	server, err := NewServer(ServerConfig{VaultRoot: vaultRoot, Token: testToken, Version: "test", Executable: script})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	body, err := json.Marshal(commandRequest{Arguments: []string{"ls"}})
	if err != nil {
		t.Fatal(err)
	}
	response := authorized(t, http.MethodPost, httpServer.URL+"/api/v1/command", bytes.NewReader(body), "application/json")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("command returned %d: %s", response.StatusCode, readBody(response))
	}
	output := readBody(response)
	if strings.Contains(output, "leaked-server-token-0123456789") {
		t.Fatalf("SYMDESK_SERVER_TOKEN leaked into subprocess environment: %s", output)
	}
	if strings.Contains(output, "leaked-worker-token-0123456789") {
		t.Fatalf("SYMDESK_WORKER_TOKEN leaked into subprocess environment: %s", output)
	}
	if !strings.Contains(output, "SYMDESK_SIDECAR=") {
		t.Fatalf("expected SYMDESK_SIDECAR to be set in subprocess environment: %s", output)
	}
	if !strings.Contains(output, "PATH=") {
		t.Fatalf("expected the subprocess to still inherit PATH: %s", output)
	}
}

func TestSubprocessEnvExcludesTokensAndKeepsSidecar(t *testing.T) {
	t.Setenv("SYMDESK_SERVER_TOKEN", "leaked-server-token-0123456789")
	t.Setenv("SYMDESK_WORKER_TOKEN", "leaked-worker-token-0123456789")
	t.Setenv("SYMDESK_LLM_PROVIDER", "anthropic")

	server := &Server{cfg: ServerConfig{VaultRoot: t.TempDir()}}
	env := server.subprocessEnv()

	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if name == "SYMDESK_SERVER_TOKEN" || name == "SYMDESK_WORKER_TOKEN" {
			t.Fatalf("expected %s to be excluded from subprocess environment, got %v", name, env)
		}
	}
	var sawSidecar, sawProvider bool
	for _, kv := range env {
		if strings.HasPrefix(kv, "SYMDESK_SIDECAR=") {
			sawSidecar = true
		}
		if kv == "SYMDESK_LLM_PROVIDER=anthropic" {
			sawProvider = true
		}
	}
	if !sawSidecar {
		t.Fatalf("expected SYMDESK_SIDECAR to be present: %v", env)
	}
	if !sawProvider {
		t.Fatalf("expected unrelated env vars such as SYMDESK_LLM_PROVIDER to pass through: %v", env)
	}
}

func TestRemoteCommandAllowlist(t *testing.T) {
	for _, args := range [][]string{{"serve"}, {"ingest", "/etc/passwd"}, {"export", "--output", "/tmp/x"}, {"ls", "--vault", "/tmp"}} {
		if err := validateRemoteCommand(args); err == nil {
			t.Fatalf("expected rejection for %v", args)
		}
	}
	for _, args := range [][]string{{"ls", "--json"}, {"docs", "list", "--json"}, {"doc", "status", "a.md", "done"}} {
		if err := validateRemoteCommand(args); err != nil {
			t.Fatalf("expected %v to be allowed: %v", args, err)
		}
	}
}

func TestJobLeaseExpires(t *testing.T) {
	store, err := NewJobStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.Create("archive/a.pdf", "a.pdf", "application/pdf")
	if err != nil {
		t.Fatal(err)
	}
	leased, err := store.Lease("first", []string{"ocr"}, -time.Second)
	if err != nil || leased.ID != job.ID {
		t.Fatalf("initial lease failed: %+v %v", leased, err)
	}
	leased, err = store.Lease("second", []string{"ocr"}, time.Minute)
	if err != nil || leased.WorkerID != "second" {
		t.Fatalf("expired lease was not recovered: %+v %v", leased, err)
	}
}

const testWorkerToken = "fedcba9876543210fedcba9876543210"

// TestAuthorizationMatrix exercises every authenticated route against every
// credential class: the admin/client token (must always be accepted), the
// worker-scoped token (must be accepted only on worker routes), and no
// token / a wrong token (must always be rejected). It only asserts on
// whether the request cleared authentication (non-401) or was rejected
// (401) — business-level outcomes for malformed/missing resources are
// covered by the other tests above.
func TestAuthorizationMatrix(t *testing.T) {
	vaultRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(vaultRoot, "Hello.md"), []byte("---\ntitle: Hello\n---\nBody"), 0644); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(ServerConfig{
		VaultRoot: vaultRoot, Token: testToken, WorkerToken: testWorkerToken,
		Version: "test", Executable: "/bin/false",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	type route struct {
		name          string
		method        string
		path          string
		requiresAdmin bool
		body          func() (io.Reader, string)
	}
	jsonBody := func(value any) func() (io.Reader, string) {
		return func() (io.Reader, string) {
			data, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			return bytes.NewReader(data), "application/json"
		}
	}
	routes := []route{
		{"status", http.MethodGet, "/api/v1/status", false, nil},
		{"snapshot", http.MethodGet, "/api/v1/snapshot", false, nil},
		{"get-file", http.MethodGet, "/api/v1/files?path=Hello.md", false, nil},
		{"put-file", http.MethodPut, "/api/v1/files?path=Hello.md", false, func() (io.Reader, string) {
			return strings.NewReader("# Hello"), "text/markdown"
		}},
		{"ingest", http.MethodPost, "/api/v1/ingest", true, func() (io.Reader, string) {
			var upload bytes.Buffer
			writer := multipart.NewWriter(&upload)
			part, err := writer.CreateFormFile("file", "doc.png")
			if err != nil {
				t.Fatal(err)
			}
			_, _ = part.Write([]byte("fake image"))
			_ = writer.Close()
			return &upload, writer.FormDataContentType()
		}},
		{"jobs", http.MethodGet, "/api/v1/jobs", true, nil},
		{"jobs-retry", http.MethodPost, "/api/v1/jobs/retry?id=missing", true, nil},
		{"command", http.MethodPost, "/api/v1/command", true, jsonBody(commandRequest{Arguments: []string{"ls", "--json"}})},
		{"worker-lease", http.MethodPost, "/api/v1/worker/lease", false, jsonBody(leaseRequest{WorkerID: "w1", Capabilities: []string{"ocr"}})},
		{"worker-input", http.MethodGet, "/api/v1/worker/input?id=missing", false, nil},
		{"worker-complete", http.MethodPost, "/api/v1/worker/complete", false, jsonBody(completionRequest{JobID: "missing", WorkerID: "w1", Text: "x", Engine: "tesseract"})},
		{"worker-fail", http.MethodPost, "/api/v1/worker/fail", false, jsonBody(failRequest{JobID: "missing", WorkerID: "w1", Error: "x"})},
	}

	credentials := []struct {
		name    string
		token   string
		allowed func(requiresAdmin bool) bool
	}{
		{"admin token", testToken, func(bool) bool { return true }},
		{"worker token", testWorkerToken, func(requiresAdmin bool) bool { return !requiresAdmin }},
		{"no token", "", func(bool) bool { return false }},
		{"wrong token", "0000000000000000000000000000wrong", func(bool) bool { return false }},
	}

	for _, rt := range routes {
		for _, cred := range credentials {
			t.Run(rt.name+"/"+cred.name, func(t *testing.T) {
				var body io.Reader
				contentType := ""
				if rt.body != nil {
					body, contentType = rt.body()
				}
				request, err := http.NewRequest(rt.method, httpServer.URL+rt.path, body)
				if err != nil {
					t.Fatal(err)
				}
				if cred.token != "" {
					request.Header.Set("Authorization", "Bearer "+cred.token)
				}
				if contentType != "" {
					request.Header.Set("Content-Type", contentType)
				}
				response, err := http.DefaultClient.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = response.Body.Close() }()
				wantAllowed := cred.allowed(rt.requiresAdmin)
				gotAllowed := response.StatusCode != http.StatusUnauthorized && response.StatusCode != http.StatusForbidden && response.StatusCode != http.StatusTooManyRequests
				if gotAllowed != wantAllowed {
					t.Fatalf("%s with %s: expected allowed=%v, got status %d", rt.name, cred.name, wantAllowed, response.StatusCode)
				}
			})
		}
	}
}

func authorized(t *testing.T, method, url string, body io.Reader, contentType string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+testToken)
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func readBody(response *http.Response) string {
	defer func() { _ = response.Body.Close() }()
	data, _ := io.ReadAll(response.Body)
	return string(data)
}

func TestJobStoreFailAndRetry(t *testing.T) {
	dir := t.TempDir()
	store, err := NewJobStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	job, err := store.Create("input.pdf", "Invoice.pdf", "application/pdf")
	if err != nil {
		t.Fatal(err)
	}

	leased, err := store.Lease("w1", []string{"ocr"}, 10*time.Minute)
	if err != nil || leased == nil {
		t.Fatalf("failed to lease job: %v", err)
	}

	// Test Fail with wrong worker
	if _, err := store.Fail(job.ID, "w2", "error msg", false); err == nil {
		t.Error("expected error when failing with wrong workerID")
	}

	// Test Fail with retry=true
	failedWithRetry, err := store.Fail(job.ID, "w1", "temporary error", true)
	if err != nil {
		t.Fatalf("failed to fail job: %v", err)
	}
	if failedWithRetry.Status != "pending" || failedWithRetry.WorkerID != "" {
		t.Errorf("expected status 'pending' and empty workerID, got status %q worker %q", failedWithRetry.Status, failedWithRetry.WorkerID)
	}

	// Re-lease and Fail with retry=false
	leased, err = store.Lease("w1", []string{"ocr"}, 10*time.Minute)
	if err != nil || leased == nil {
		t.Fatalf("failed to re-lease job: %v", err)
	}
	failedFinal, err := store.Fail(job.ID, "w1", "permanent error", false)
	if err != nil {
		t.Fatalf("failed to fail job finally: %v", err)
	}
	if failedFinal.Status != "failed" || failedFinal.Error != "permanent error" {
		t.Errorf("expected status 'failed', got %q", failedFinal.Status)
	}

	// Test Retry on failed job
	retried, err := store.Retry(job.ID)
	if err != nil {
		t.Fatalf("failed to retry job: %v", err)
	}
	if retried.Status != "pending" || retried.Error != "" {
		t.Errorf("expected status 'pending' and empty error, got status %q error %q", retried.Status, retried.Error)
	}

	// Test Retry on non-failed job
	if _, err := store.Retry(job.ID); err == nil {
		t.Error("expected error when retrying non-failed job")
	}
}

func TestHandleWorkerInputAndFailRetryEndpoints(t *testing.T) {
	vaultRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(vaultRoot, "doc.txt"), []byte("Document content"), 0644); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(ServerConfig{VaultRoot: vaultRoot, Token: testToken, Version: "test", Executable: "/bin/false"})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	// 1. handleWorkerInput with missing job
	res := authorized(t, http.MethodGet, ts.URL+"/api/v1/worker/input?id=missing", nil, "")
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for missing input job, got %d", res.StatusCode)
	}
	res.Body.Close()

	// 2. Create and lease job
	job, err := server.jobs.Create("doc.txt", "doc.txt", "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	leased, err := server.jobs.Lease("worker-1", []string{"ocr"}, 10*time.Minute)
	if err != nil || leased == nil {
		t.Fatalf("failed to lease job: %v", err)
	}

	// 3. handleWorkerInput for leased job
	res = authorized(t, http.MethodGet, ts.URL+"/api/v1/worker/input?id="+job.ID, nil, "")
	if res.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for valid worker input, got %d", res.StatusCode)
	}
	res.Body.Close()

	// 4. /api/v1/worker/fail endpoint
	failPayload := `{"job_id":"` + job.ID + `","worker_id":"worker-1","error":"ocr failure","retry":false}`
	res = authorized(t, http.MethodPost, ts.URL+"/api/v1/worker/fail", strings.NewReader(failPayload), "application/json")
	if res.StatusCode != http.StatusOK {
		t.Errorf("expected 200 from worker fail endpoint, got %d", res.StatusCode)
	}
	res.Body.Close()

	// 5. /api/v1/jobs/retry endpoint
	res = authorized(t, http.MethodPost, ts.URL+"/api/v1/jobs/retry?id="+job.ID, nil, "")
	if res.StatusCode != http.StatusOK {
		t.Errorf("expected 200 from queue retry endpoint, got %d", res.StatusCode)
	}
	res.Body.Close()

	// 6. /api/v1/jobs/retry on non-failed job returns 409 StatusConflict
	res = authorized(t, http.MethodPost, ts.URL+"/api/v1/jobs/retry?id="+job.ID, nil, "")
	if res.StatusCode != http.StatusConflict {
		t.Errorf("expected 409 when retrying non-failed job, got %d", res.StatusCode)
	}
	res.Body.Close()
}

// TestPerUserSnapshotCacheStaysUnderByteBudget proves the per-user snapshot
// cache is bounded by total bytes with LRU eviction rather than by entry
// count: caching many distinct non-admin users against a synthetic vault
// never lets the cache's resident size exceed the configured budget, and
// older entries are evicted to make room for newer ones instead of the
// whole cache being flushed at once.
func TestPerUserSnapshotCacheStaysUnderByteBudget(t *testing.T) {
	vaultRoot := t.TempDir()
	// A handful of reasonably sized, high-entropy notes so each user's
	// filtered-and-gzipped snapshot has a non-trivial, gzip-resistant size
	// (repetitive text would compress away to almost nothing and never
	// exercise eviction).
	for i := 0; i < 5; i++ {
		raw := make([]byte, 6000)
		if _, err := cryptorand.Read(raw); err != nil {
			t.Fatal(err)
		}
		body := base64.StdEncoding.EncodeToString(raw)
		name := fmt.Sprintf("note-%d.md", i)
		if err := os.WriteFile(filepath.Join(vaultRoot, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}

	server, err := NewServer(ServerConfig{VaultRoot: vaultRoot, Token: testToken, Version: "test", Executable: "/bin/false"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })

	// Shrink the budget so eviction kicks in well before all N users would
	// otherwise fit, without needing a huge synthetic vault. Each user's
	// filtered-and-gzipped snapshot here runs ~30KB (high-entropy content
	// barely compresses), so this budget comfortably holds a handful of
	// users but not all 20 requested below.
	const budget = 100 << 10 // 100KB
	server.perUserCacheBudget = budget

	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)

	const n = 20
	tokens := make([]string, n)
	for i := 0; i < n; i++ {
		token, err := server.perm.UserAdd(fmt.Sprintf("user-%d", i), "user")
		if err != nil {
			t.Fatal(err)
		}
		tokens[i] = token
	}

	for i, token := range tokens {
		req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/snapshot", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != http.StatusOK {
			t.Fatalf("snapshot request %d returned %d", i, res.StatusCode)
		}
		res.Body.Close()

		server.perUserCacheMu.Lock()
		resident := server.perUserCacheBytes
		entries := len(server.perUserCache)
		server.perUserCacheMu.Unlock()
		if resident > budget {
			t.Fatalf("after %d users, cache holds %d bytes > budget %d", i+1, resident, budget)
		}
		if entries > n {
			t.Fatalf("cache has more entries (%d) than users requested (%d)", entries, i+1)
		}
	}

	// After all N requests the cache must have evicted down to (well) under
	// N entries — the old "flush everything past 64" policy would instead
	// have kept growing (or periodically flushed to zero) regardless of
	// actual memory footprint.
	server.perUserCacheMu.Lock()
	finalEntries := len(server.perUserCache)
	finalBytes := server.perUserCacheBytes
	server.perUserCacheMu.Unlock()
	if finalEntries >= n {
		t.Fatalf("expected LRU eviction to keep the cache well under %d entries, got %d", n, finalEntries)
	}
	if finalBytes > budget {
		t.Fatalf("final cache size %d exceeds budget %d", finalBytes, budget)
	}
}

// TestSnapshotIfNoneMatchShortCircuitsForNonAdmin proves a non-admin user's
// repeat poll against an unchanged vault still gets a 304 from the
// gzip-only per-user cache, exactly like the admin path.
func TestSnapshotIfNoneMatchShortCircuitsForNonAdmin(t *testing.T) {
	vaultRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(vaultRoot, "Hello.md"), []byte("World"), 0644); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(ServerConfig{VaultRoot: vaultRoot, Token: testToken, Version: "test", Executable: "/bin/false"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)

	userToken, err := server.perm.UserAdd("reader", "user")
	if err != nil {
		t.Fatal(err)
	}

	get := func() *http.Response {
		req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/snapshot", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+userToken)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	first := get()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first snapshot returned %d", first.StatusCode)
	}
	etag := first.Header.Get("ETag")
	first.Body.Close()
	if etag == "" {
		t.Fatal("expected an ETag on the first response")
	}

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/snapshot", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+userToken)
	req.Header.Set("If-None-Match", etag)
	second, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Body.Close()
	if second.StatusCode != http.StatusNotModified {
		t.Fatalf("expected cached snapshot to return 304 for non-admin user, got %d", second.StatusCode)
	}
}
