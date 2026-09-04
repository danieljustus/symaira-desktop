package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/retrieval"
)

func TestIndexStatusRetrievalTimeoutReportsPhase(t *testing.T) {
	origRetrieval := indexStatusRetrievalFunc
	origWalk := indexStatusVaultWalkFunc
	origTimeout := indexStatusTimeout
	t.Cleanup(func() {
		indexStatusRetrievalFunc = origRetrieval
		indexStatusVaultWalkFunc = origWalk
		indexStatusTimeout = origTimeout
	})

	indexStatusRetrievalFunc = func(ctx context.Context) (*retrieval.Status, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}

	cmd := newIndexStatusCmd()
	if err := cmd.Flags().Set("timeout", "20ms"); err != nil {
		t.Fatal(err)
	}

	err := cmd.RunE(cmd, nil)
	if err == nil {
		t.Fatal("expected timeout error during retrieval status, got nil")
	}

	var timeoutErr *IndexStatusTimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("expected *IndexStatusTimeoutError, got %T: %v", err, err)
	}
	if timeoutErr.Phase != "retrieval status" {
		t.Errorf("expected phase 'retrieval status', got '%s'", timeoutErr.Phase)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected errors.Is(err, context.DeadlineExceeded), got %v", err)
	}
	if !strings.Contains(err.Error(), "retrieval status") {
		t.Errorf("expected error message to mention 'retrieval status', got %q", err.Error())
	}
}

func TestIndexStatusVaultCountingTimeoutReportsPhase(t *testing.T) {
	vaultDir := t.TempDir()
	origConfig := cfg
	cfg = &config.Config{Vault: vaultDir}
	origRetrieval := indexStatusRetrievalFunc
	origWalk := indexStatusVaultWalkFunc
	origTimeout := indexStatusTimeout
	t.Cleanup(func() {
		cfg = origConfig
		indexStatusRetrievalFunc = origRetrieval
		indexStatusVaultWalkFunc = origWalk
		indexStatusTimeout = origTimeout
	})

	indexStatusRetrievalFunc = func(ctx context.Context) (*retrieval.Status, error) {
		return &retrieval.Status{DocumentCount: 10, BackendAvailable: true}, nil
	}
	indexStatusVaultWalkFunc = func(ctx context.Context, root string, fn func(path string) error) error {
		<-ctx.Done()
		return ctx.Err()
	}

	cmd := newIndexStatusCmd()
	if err := cmd.Flags().Set("timeout", "20ms"); err != nil {
		t.Fatal(err)
	}

	err := cmd.RunE(cmd, nil)
	if err == nil {
		t.Fatal("expected timeout error during vault counting, got nil")
	}

	var timeoutErr *IndexStatusTimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("expected *IndexStatusTimeoutError, got %T: %v", err, err)
	}
	if timeoutErr.Phase != "vault counting" {
		t.Errorf("expected phase 'vault counting', got '%s'", timeoutErr.Phase)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected errors.Is(err, context.DeadlineExceeded), got %v", err)
	}
	if !strings.Contains(err.Error(), "vault counting") {
		t.Errorf("expected error message to mention 'vault counting', got %q", err.Error())
	}
}

func TestIndexStatusPreservesSuccessfulJSONOutput(t *testing.T) {
	vaultDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(vaultDir, "note.md"), []byte("# Note"), 0o600); err != nil {
		t.Fatal(err)
	}

	origConfig := cfg
	cfg = &config.Config{Vault: vaultDir}
	origJSON := jsonFlag
	jsonFlag = true
	origRetrieval := indexStatusRetrievalFunc
	origWalk := indexStatusVaultWalkFunc
	origTimeout := indexStatusTimeout
	t.Cleanup(func() {
		cfg = origConfig
		jsonFlag = origJSON
		indexStatusRetrievalFunc = origRetrieval
		indexStatusVaultWalkFunc = origWalk
		indexStatusTimeout = origTimeout
	})

	indexStatusRetrievalFunc = func(ctx context.Context) (*retrieval.Status, error) {
		return &retrieval.Status{
			DocumentCount:        15,
			ChunkCount:           45,
			DatabaseBytes:        4096,
			EmbeddingModel:       "all-minilm",
			BackendAvailable:     true,
			PendingChunkCount:    0,
			MixedEmbeddingSpaces: false,
			IndexScope:           "vault",
			IndexLocation:        "/tmp/retrieval.db",
		}, nil
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStdout := os.Stdout
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	cmd := newIndexStatusCmd()
	runErr := cmd.RunE(cmd, nil)

	_ = w.Close()
	os.Stdout = origStdout
	out, _ := io.ReadAll(r)

	if runErr != nil {
		t.Fatalf("expected successful status run, got error: %v", runErr)
	}

	var status map[string]interface{}
	if err := json.Unmarshal(out, &status); err != nil {
		t.Fatalf("failed to unmarshal JSON output: %v\noutput: %s", err, out)
	}

	if int(status["document_count"].(float64)) != 15 {
		t.Errorf("document_count = %v, want 15", status["document_count"])
	}
	if int(status["chunk_count"].(float64)) != 45 {
		t.Errorf("chunk_count = %v, want 45", status["chunk_count"])
	}
	if status["embedding_model"] != "all-minilm" {
		t.Errorf("embedding_model = %v, want 'all-minilm'", status["embedding_model"])
	}
	if status["backend_available"] != true {
		t.Errorf("backend_available = %v, want true", status["backend_available"])
	}
	if status["index_scope"] != "vault" {
		t.Errorf("index_scope = %v, want 'vault'", status["index_scope"])
	}
	if int(status["vault_document_count"].(float64)) != 1 {
		t.Errorf("vault_document_count = %v, want 1", status["vault_document_count"])
	}
}

func TestIndexStatusCommandHelpAndFlag(t *testing.T) {
	cmd := newIndexStatusCmd()

	flag := cmd.Flags().Lookup("timeout")
	if flag == nil {
		t.Fatal("expected --timeout flag to be registered on status command")
	}
	if flag.DefValue != (10 * time.Second).String() {
		t.Errorf("expected default timeout 10s, got %s", flag.DefValue)
	}
	if !strings.Contains(flag.Usage, "duration") && !strings.Contains(flag.Usage, "wait") {
		t.Errorf("expected flag usage to document duration/wait behavior, got: %s", flag.Usage)
	}

	if !strings.Contains(cmd.Long, "deadline") || !strings.Contains(cmd.Long, "--timeout") {
		t.Errorf("expected command Long description to document timeout deadline, got: %s", cmd.Long)
	}
}

func TestIndexStatusNoGoroutineLeakOnTimeout(t *testing.T) {
	origRetrieval := indexStatusRetrievalFunc
	origWalk := indexStatusVaultWalkFunc
	origTimeout := indexStatusTimeout
	t.Cleanup(func() {
		indexStatusRetrievalFunc = origRetrieval
		indexStatusVaultWalkFunc = origWalk
		indexStatusTimeout = origTimeout
	})

	indexStatusRetrievalFunc = func(ctx context.Context) (*retrieval.Status, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}

	// Warm-up to let runtime goroutines settle
	cmd := newIndexStatusCmd()
	_ = cmd.Flags().Set("timeout", "5ms")
	_ = cmd.RunE(cmd, nil)

	before := runtime.NumGoroutine()

	for i := 0; i < 5; i++ {
		c := newIndexStatusCmd()
		_ = c.Flags().Set("timeout", "5ms")
		if err := c.RunE(c, nil); err == nil {
			t.Fatal("expected timeout error, got nil")
		}
	}

	after := runtime.NumGoroutine()
	// Allow tiny jitter for runtime GC/scavenger, but no leak of 5 goroutines
	if diff := after - before; diff > 2 {
		t.Errorf("potential goroutine leak detected: before=%d, after=%d (diff=%d)", before, after, diff)
	}
}

func TestIndexStatusDocumentsTimeoutReportsPhase(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cmd := newIndexStatusCmd()
	cmd.SetContext(ctx)
	if err := cmd.Flags().Set("documents", "true"); err != nil {
		t.Fatal(err)
	}

	err := cmd.RunE(cmd, nil)
	if err == nil {
		t.Fatal("expected timeout error for documents listing with cancelled context, got nil")
	}

	var timeoutErr *IndexStatusTimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("expected *IndexStatusTimeoutError, got %T: %v", err, err)
	}
	if timeoutErr.Phase != "document status listing" {
		t.Errorf("expected phase 'document status listing', got '%s'", timeoutErr.Phase)
	}
}

func TestIndexStatusExplicitZeroTimeoutDisablesDeadline(t *testing.T) {
	origRetrieval := indexStatusRetrievalFunc
	origTimeout := indexStatusTimeout
	t.Cleanup(func() {
		indexStatusRetrievalFunc = origRetrieval
		indexStatusTimeout = origTimeout
	})

	called := false
	indexStatusRetrievalFunc = func(ctx context.Context) (*retrieval.Status, error) {
		called = true
		if _, hasDeadline := ctx.Deadline(); hasDeadline {
			t.Error("expected no deadline when timeout is 0")
		}
		return &retrieval.Status{DocumentCount: 1}, nil
	}

	cmd := newIndexStatusCmd()
	if err := cmd.Flags().Set("timeout", "0s"); err != nil {
		t.Fatal(err)
	}

	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("retrieval func was not called")
	}
}
