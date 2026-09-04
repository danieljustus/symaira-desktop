package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/retrieval"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
)

func TestIndexStatusRetrievalTimeoutReportsPhase(t *testing.T) {
	origRun := indexStatusRun
	indexStatusRun = runIndexStatusInProcess
	origRetrieval := indexStatusRetrievalFunc
	origWalk := indexStatusVaultWalkFunc
	origTimeout := indexStatusTimeout
	t.Cleanup(func() {
		indexStatusRun = origRun
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
	origRun := indexStatusRun
	indexStatusRun = runIndexStatusInProcess
	origRetrieval := indexStatusRetrievalFunc
	origWalk := indexStatusVaultWalkFunc
	origTimeout := indexStatusTimeout
	t.Cleanup(func() {
		cfg = origConfig
		indexStatusRun = origRun
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
	origRun := indexStatusRun
	indexStatusRun = runIndexStatusInProcess
	origRetrieval := indexStatusRetrievalFunc
	origWalk := indexStatusVaultWalkFunc
	origTimeout := indexStatusTimeout
	t.Cleanup(func() {
		cfg = origConfig
		jsonFlag = origJSON
		indexStatusRun = origRun
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
	origRun := indexStatusRun
	indexStatusRun = runIndexStatusInProcess
	origRetrieval := indexStatusRetrievalFunc
	origWalk := indexStatusVaultWalkFunc
	origTimeout := indexStatusTimeout
	t.Cleanup(func() {
		indexStatusRun = origRun
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
	vaultDir := t.TempDir()
	preopened, err := sidecar.Open(filepath.Join(t.TempDir(), "sidecar.db"))
	if err != nil {
		t.Fatal(err)
	}
	origConfig := cfg
	cfg = &config.Config{Vault: vaultDir}
	origRun := indexStatusRun
	indexStatusRun = runIndexStatusInProcess
	origDocs := indexStatusDocumentsFunc
	origOpen := indexStatusSidecarOpenFunc
	indexStatusSidecarOpenFunc = func(string) (*sidecar.DB, error) { return preopened, nil }
	origTimeout := indexStatusTimeout
	t.Cleanup(func() {
		_ = preopened.Close()
		cfg = origConfig
		indexStatusRun = origRun
		indexStatusDocumentsFunc = origDocs
		indexStatusSidecarOpenFunc = origOpen
		indexStatusTimeout = origTimeout
	})

	indexStatusDocumentsFunc = func(ctx context.Context, db *sidecar.DB, state sidecar.IndexState) ([]sidecar.IndexStatus, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}

	cmd := newIndexStatusCmd()
	if err := cmd.Flags().Set("documents", "true"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("timeout", "200ms"); err != nil {
		t.Fatal(err)
	}

	err = cmd.RunE(cmd, nil)
	if err == nil {
		t.Fatal("expected timeout error for documents listing, got nil")
	}

	var timeoutErr *IndexStatusTimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("expected *IndexStatusTimeoutError, got %T: %v", err, err)
	}
	if timeoutErr.Phase != "document status listing" {
		t.Errorf("expected phase 'document status listing', got %q", timeoutErr.Phase)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected errors.Is(err, context.DeadlineExceeded), got %v", err)
	}
}

func TestIndexStatusExplicitZeroTimeoutDisablesDeadline(t *testing.T) {
	origRun := indexStatusRun
	indexStatusRun = runIndexStatusInProcess
	origRetrieval := indexStatusRetrievalFunc
	origTimeout := indexStatusTimeout
	t.Cleanup(func() {
		indexStatusRun = origRun
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

func TestIndexStatusRejectsNegativeTimeout(t *testing.T) {
	runnerCalled := false
	origRun := indexStatusRun
	indexStatusRun = func(ctx context.Context, req indexStatusRequest, report indexStatusPhaseReporter) ([]byte, error) {
		runnerCalled = true
		return nil, nil
	}
	t.Cleanup(func() { indexStatusRun = origRun })

	cmd := newIndexStatusCmd()
	if err := cmd.Flags().Set("timeout", "-1ms"); err != nil {
		t.Fatal(err)
	}

	err := cmd.RunE(cmd, nil)
	if err == nil {
		t.Fatal("expected error for negative timeout, got nil")
	}
	if !strings.Contains(err.Error(), "non-negative") {
		t.Errorf("expected error message to mention 'non-negative', got %q", err.Error())
	}
	if runnerCalled {
		t.Error("expected indexStatusRun not to be called when timeout is negative")
	}
}

func TestIndexStatusHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_INDEX_STATUS_HELPER") != "1" {
		return
	}
	fmt.Fprintf(os.Stderr, "%sretrieval status\n", phasePrefix)
	// Block until killed
	time.Sleep(10 * time.Minute)
}

func TestIndexStatusHardDeadlineKillsWorker(t *testing.T) {
	origWorkerCmd := indexStatusWorkerCmd
	origOnStart := indexStatusOnWorkerStart
	t.Cleanup(func() {
		indexStatusWorkerCmd = origWorkerCmd
		indexStatusOnWorkerStart = origOnStart
	})

	var childPid int
	indexStatusOnWorkerStart = func(pid int) {
		childPid = pid
	}

	indexStatusWorkerCmd = func(ctx context.Context, req indexStatusRequest) (*exec.Cmd, error) {
		exe, err := os.Executable()
		if err != nil {
			return nil, err
		}
		cmd := exec.Command(exe, "-test.run=TestIndexStatusHelperProcess", "--") // #nosec G204 -- os.Executable with static arguments.
		cmd.Env = append(os.Environ(), "GO_WANT_INDEX_STATUS_HELPER=1")
		return cmd, nil
	}

	var reportedPhase string
	report := func(phase string) {
		reportedPhase = phase
	}

	timeout := 200 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	start := time.Now()
	_, err := runIndexStatusChild(ctx, indexStatusRequest{Timeout: timeout}, report)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	var timeoutErr *IndexStatusTimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("expected *IndexStatusTimeoutError, got %T: %v", err, err)
	}
	if timeoutErr.Phase != "retrieval status" {
		t.Errorf("expected phase 'retrieval status', got %q", timeoutErr.Phase)
	}
	if reportedPhase != "retrieval status" {
		t.Errorf("expected reported phase 'retrieval status', got %q", reportedPhase)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected errors.Is(err, context.DeadlineExceeded), got %v", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("took %v, want under 2s (close to deadline %v)", elapsed, timeout)
	}

	if childPid <= 0 {
		t.Fatal("expected positive child PID")
	}

	if !waitForProcessExit(childPid, time.Second) {
		t.Errorf("child process %d still exists after kill", childPid)
	}
}

func waitForProcessExit(pid int, maxWait time.Duration) bool {
	deadline := time.Now().Add(maxWait)
	for time.Now().Before(deadline) {
		if processIsGone(pid) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return processIsGone(pid)
}

func TestIndexStatusRepeatedTimeoutsDoNotLeakGoroutines(t *testing.T) {
	origWorkerCmd := indexStatusWorkerCmd
	t.Cleanup(func() {
		indexStatusWorkerCmd = origWorkerCmd
	})

	indexStatusWorkerCmd = func(ctx context.Context, req indexStatusRequest) (*exec.Cmd, error) {
		exe, err := os.Executable()
		if err != nil {
			return nil, err
		}
		cmd := exec.Command(exe, "-test.run=TestIndexStatusHelperProcess", "--") // #nosec G204 -- os.Executable with static arguments.
		cmd.Env = append(os.Environ(), "GO_WANT_INDEX_STATUS_HELPER=1")
		return cmd, nil
	}

	// Warm-up to let runtime goroutines settle
	ctx0, cancel0 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	_, _ = runIndexStatusChild(ctx0, indexStatusRequest{Timeout: 100 * time.Millisecond}, nil)
	cancel0()

	before := runtime.NumGoroutine()

	for i := 0; i < 5; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		_, err := runIndexStatusChild(ctx, indexStatusRequest{Timeout: 100 * time.Millisecond}, nil)
		cancel()
		if err == nil {
			t.Fatal("expected timeout error, got nil")
		}
		var timeoutErr *IndexStatusTimeoutError
		if !errors.As(err, &timeoutErr) {
			t.Fatalf("expected *IndexStatusTimeoutError, got %T: %v", err, err)
		}
	}

	after := runtime.NumGoroutine()
	if diff := after - before; diff > 2 {
		t.Errorf("potential goroutine leak detected: before=%d, after=%d (diff=%d)", before, after, diff)
	}
}
