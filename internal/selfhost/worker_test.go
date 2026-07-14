package selfhost

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestOllamaVisionWorkerContract(t *testing.T) {
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			http.NotFound(w, r)
			return
		}
		var payload struct {
			Model    string `json:"model"`
			Messages []struct {
				Images []string `json:"images"`
			} `json:"messages"`
			Stream bool `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if payload.Model != "gemma3" || len(payload.Messages) != 1 || len(payload.Messages[0].Images) != 1 || payload.Stream {
			t.Errorf("unexpected Ollama payload: %+v", payload)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"Invoice total: 42 EUR"},"done":true}`))
	}))
	defer ollama.Close()

	input := filepath.Join(t.TempDir(), "invoice.png")
	if err := os.WriteFile(input, []byte("not decoded by the fake server"), 0600); err != nil {
		t.Fatal(err)
	}
	worker, err := NewWorker(WorkerConfig{
		ServerURL: ollama.URL, Token: testToken, Engine: "ollama",
		OllamaURL: ollama.URL, OllamaModel: "gemma3",
	})
	if err != nil {
		t.Fatal(err)
	}
	text, err := worker.ollamaOCR(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if text != "Invoice total: 42 EUR" {
		t.Fatalf("unexpected OCR text %q", text)
	}
}

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
