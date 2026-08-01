package selfhost

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWorkerRejectsNonBareServerURLs(t *testing.T) {
	for _, serverURL := range []string{
		"server.local:8787",
		"https://user:secret@server.local",
		"https://server.local/api",
		"https://server.local?token=secret",
	} {
		if _, err := NewWorker(WorkerConfig{ServerURL: serverURL, Token: testToken}); err == nil {
			t.Fatalf("expected URL %q to be rejected", serverURL)
		}
	}
}

func TestNewWorkerValidation(t *testing.T) {
	tests := []struct {
		name    string
		cfg     WorkerConfig
		wantErr bool
	}{
		{"valid minimal config", WorkerConfig{ServerURL: "http://server:8787", Token: "01234567890123456789012345678901"}, false},
		{"short token", WorkerConfig{ServerURL: "http://server:8787", Token: "short"}, true},
		{"empty token", WorkerConfig{ServerURL: "http://server:8787", Token: ""}, true},
		{"invalid server URL", WorkerConfig{ServerURL: "not-a-url", Token: testToken}, true},
		{"valid with Ollama", WorkerConfig{ServerURL: "http://server:8787", Token: testToken, Engine: "ollama", OllamaURL: "http://ollama:11434", OllamaModel: "gemma3"}, false},
		{"invalid Ollama URL", WorkerConfig{ServerURL: "http://server:8787", Token: testToken, OllamaURL: "not-a-url"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewWorker(tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewWorker() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestWorkerRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	w, err := NewWorker(WorkerConfig{ServerURL: server.URL, Token: testToken})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := w.request(context.Background(), http.MethodGet, "/test", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestResponseError(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		want       string
	}{
		{"JSON error body", http.StatusBadRequest, `{"error":"invalid input"}`, "invalid input"},
		{"plain text body", http.StatusInternalServerError, "server error", "HTTP 500: server error"},
		{"empty body", http.StatusNotFound, "", "HTTP 404: "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &http.Response{
				StatusCode: tt.statusCode,
				Body:       io.NopCloser(strings.NewReader(tt.body)),
			}
			got := responseError(resp)
			if got.Error() != tt.want {
				t.Errorf("responseError() = %q, want %q", got.Error(), tt.want)
			}
		})
	}
}

func TestWorkerLease(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/worker/lease" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"job1","original_name":"doc.png","content_type":"image/png"}`))
	}))
	defer server.Close()

	w, err := NewWorker(WorkerConfig{ServerURL: server.URL, Token: testToken})
	if err != nil {
		t.Fatal(err)
	}

	job, err := w.lease(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if job.ID != "job1" {
		t.Errorf("expected job ID job1, got %s", job.ID)
	}
}

func TestWorkerLeaseNoPending(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	w, err := NewWorker(WorkerConfig{ServerURL: server.URL, Token: testToken})
	if err != nil {
		t.Fatal(err)
	}

	_, err = w.lease(context.Background())
	if !errors.Is(err, ErrNoPendingJob) {
		t.Errorf("expected ErrNoPendingJob, got %v", err)
	}
}

func TestWorkerComplete(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/worker/complete" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	w, err := NewWorker(WorkerConfig{ServerURL: server.URL, Token: testToken})
	if err != nil {
		t.Fatal(err)
	}

	if err := w.complete(context.Background(), "job1", "text", "tesseract", ""); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerCompleteServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"processing failed"}`))
	}))
	defer server.Close()

	w, err := NewWorker(WorkerConfig{ServerURL: server.URL, Token: testToken})
	if err != nil {
		t.Fatal(err)
	}

	err = w.complete(context.Background(), "job1", "text", "tesseract", "")
	if err == nil {
		t.Fatal("expected error from complete")
	}
}

func TestWorkerFail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/worker/fail" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	w, err := NewWorker(WorkerConfig{ServerURL: server.URL, Token: testToken})
	if err != nil {
		t.Fatal(err)
	}

	if err := w.fail(context.Background(), "job1", "download failed", true); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerRunOnce_FullLifecycle(t *testing.T) {
	// Mock server that handles lease, input, and fail (because tesseract is not installed)
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		switch {
		case r.URL.Path == "/api/v1/worker/lease" && r.Method == http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"job1","original_name":"doc.png","content_type":"image/png"}`))
		case r.URL.Path == "/api/v1/worker/input" && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("fake-image-data"))
		case r.URL.Path == "/api/v1/worker/fail" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	w, err := NewWorker(WorkerConfig{ServerURL: server.URL, Token: testToken, Engine: "tesseract"})
	if err != nil {
		t.Fatal(err)
	}

	// process will fail because tesseract is not installed, but the download+cleanup+fail HTTP
	// path should still be exercised
	processed, err := w.RunOnce(context.Background())
	if err == nil {
		t.Fatal("expected error (tesseract not installed)")
	}
	if !processed {
		t.Error("expected processed=true (a job was attempted)")
	}
	if callCount < 2 {
		t.Errorf("expected at least 2 HTTP calls (lease+input), got %d", callCount)
	}
}

func TestWorkerRunOnce_NoPendingJob(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	w, err := NewWorker(WorkerConfig{ServerURL: server.URL, Token: testToken})
	if err != nil {
		t.Fatal(err)
	}

	processed, err := w.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if processed {
		t.Error("expected processed=false for no pending job")
	}
}

func TestWorkerRunOnce_LeaseError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	w, err := NewWorker(WorkerConfig{ServerURL: server.URL, Token: testToken})
	if err != nil {
		t.Fatal(err)
	}

	_, err = w.RunOnce(context.Background())
	if err == nil {
		t.Fatal("expected error from lease failure")
	}
}

func TestWorkerRunOnce_DownloadFail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/worker/lease" && r.Method == http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"job1","original_name":"doc.png","content_type":"image/png"}`))
		case r.URL.Path == "/api/v1/worker/input" && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"storage error"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	w, err := NewWorker(WorkerConfig{ServerURL: server.URL, Token: testToken})
	if err != nil {
		t.Fatal(err)
	}

	_, err = w.RunOnce(context.Background())
	if err == nil {
		t.Fatal("expected error from download failure")
	}
}

func TestWorkerProcessEngineDispatch(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	worker, err := NewWorker(WorkerConfig{ServerURL: "http://server:8787", Token: testToken, Engine: "invalid"})
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = worker.process(context.Background(), "input.png")
	if err == nil || !strings.Contains(err.Error(), "unsupported OCR engine") {
		t.Fatalf("expected explicit unsupported-engine error, got %v", err)
	}
}

func TestWorkerRun_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	w, err := NewWorker(WorkerConfig{ServerURL: server.URL, Token: testToken, PollEvery: time.Second})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err = w.Run(ctx)
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestWorkerRun_OnceMode(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		switch {
		case r.URL.Path == "/api/v1/worker/lease" && r.Method == http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"job1","original_name":"doc.png","content_type":"image/png"}`))
		case r.URL.Path == "/api/v1/worker/input" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte("data"))
		case r.URL.Path == "/api/v1/worker/fail" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	w, err := NewWorker(WorkerConfig{ServerURL: server.URL, Token: testToken, Engine: "tesseract", Once: true})
	if err != nil {
		t.Fatal(err)
	}

	// process will fail because tesseract is not installed, but Once mode should still return
	// the error (not loop forever)
	if err := w.Run(context.Background()); err == nil {
		t.Fatal("expected error from process (tesseract not installed)")
	}
}
