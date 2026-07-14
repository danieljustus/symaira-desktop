package selfhost

import (
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
