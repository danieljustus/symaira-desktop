package selfhost

import (
	"bufio"
	"bytes"
	"encoding/json"
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
	defer response.Body.Close()
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
	// total). Delivery must not wait for the process to exit: the first
	// line has to arrive long before the last one, proving the response is
	// flushed incrementally rather than buffered until completion.
	if arrivals[0] > 150*time.Millisecond {
		t.Fatalf("first line arrived after %v; expected near-immediate delivery, not buffering until completion", arrivals[0])
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
	defer response.Body.Close()
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
		name   string
		method string
		path   string
		scope  tokenScope
		body   func() (io.Reader, string)
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
		{"status", http.MethodGet, "/api/v1/status", scopeAdmin, nil},
		{"snapshot", http.MethodGet, "/api/v1/snapshot", scopeAdmin, nil},
		{"get-file", http.MethodGet, "/api/v1/files?path=Hello.md", scopeAdmin, nil},
		{"put-file", http.MethodPut, "/api/v1/files?path=Hello.md", scopeAdmin, func() (io.Reader, string) {
			return strings.NewReader("# Hello"), "text/markdown"
		}},
		{"ingest", http.MethodPost, "/api/v1/ingest", scopeAdmin, func() (io.Reader, string) {
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
		{"jobs", http.MethodGet, "/api/v1/jobs", scopeAdmin, nil},
		{"jobs-retry", http.MethodPost, "/api/v1/jobs/retry?id=missing", scopeAdmin, nil},
		{"command", http.MethodPost, "/api/v1/command", scopeAdmin, jsonBody(commandRequest{Arguments: []string{"ls", "--json"}})},
		{"worker-lease", http.MethodPost, "/api/v1/worker/lease", scopeWorker, jsonBody(leaseRequest{WorkerID: "w1", Capabilities: []string{"ocr"}})},
		{"worker-input", http.MethodGet, "/api/v1/worker/input?id=missing", scopeWorker, nil},
		{"worker-complete", http.MethodPost, "/api/v1/worker/complete", scopeWorker, jsonBody(completionRequest{JobID: "missing", WorkerID: "w1", Text: "x", Engine: "tesseract"})},
		{"worker-fail", http.MethodPost, "/api/v1/worker/fail", scopeWorker, jsonBody(failRequest{JobID: "missing", WorkerID: "w1", Error: "x"})},
	}

	credentials := []struct {
		name    string
		token   string
		allowed func(scope tokenScope) bool
	}{
		{"admin token", testToken, func(tokenScope) bool { return true }},
		{"worker token", testWorkerToken, func(scope tokenScope) bool { return scope == scopeWorker }},
		{"no token", "", func(tokenScope) bool { return false }},
		{"wrong token", "0000000000000000000000000000wrong", func(tokenScope) bool { return false }},
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
				defer response.Body.Close()
				wantAllowed := cred.allowed(rt.scope)
				gotAllowed := response.StatusCode != http.StatusUnauthorized
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
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	return string(data)
}
