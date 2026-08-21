package run_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danieljustus/symaira-room/internal/approval"
	"github.com/danieljustus/symaira-room/internal/config"
	"github.com/danieljustus/symaira-room/internal/identity"
	"github.com/danieljustus/symaira-room/internal/journal"
	"github.com/danieljustus/symaira-room/internal/run"
)

func TestExecuteAdapterEndToEnd(t *testing.T) {
	tempDir := t.TempDir()
	ownerID, _ := identity.Generate("owner")

	// 1. Setup config with shell adapter
	cfg := &config.Config{
		Adapters: map[string]config.AdapterConfig{
			"shell": {
				Command: []string{"sh", "-c", "echo '{prompt}'"},
				Workdir: "{room_artifact_root}",
			},
		},
	}

	evReq, _ := run.Request(tempDir, "Hello Adapter", "", "shell", ownerID)
	var bReq struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(evReq.Body, &bReq); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	runID := bReq.RunID

	// Approve run with scope shell
	if _, err := approval.Approve(tempDir, runID, "shell", 10*time.Minute, ownerID); err != nil {
		t.Fatalf("Approve failed: %v", err)
	}

	// Execute adapter
	err := run.ExecuteAdapter(context.Background(), tempDir, runID, ownerID, cfg)
	if err != nil {
		t.Fatalf("ExecuteAdapter failed: %v", err)
	}

	// Verify logs written to .symroom/runs/<id>/stdout.log
	logPath := filepath.Join(tempDir, ".symroom", "runs", runID, "stdout.log")
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("Failed to read stdout log: %v", err)
	}
	if string(content) != "Hello Adapter\n" {
		t.Errorf("unexpected stdout content: %s", string(content))
	}

	// Verify run state is finished
	r, _ := run.Get(tempDir, runID)
	if r.State != run.StateFinished {
		t.Errorf("expected state finished, got %s", r.State)
	}

	// Verify journal contains NO stdout output
	j := journal.New(filepath.Join(tempDir, "journal"))
	merged, _ := j.MergeAll()
	for _, ev := range merged {
		if ev.Kind == "run.finished" {
			if len(ev.Body) > 500 {
				t.Errorf("journal entry body is too large, output should not be in journal!")
			}
		}
	}
}

func TestExecuteAdapterScopeRefused(t *testing.T) {
	tempDir := t.TempDir()
	ownerID, _ := identity.Generate("owner")

	cfg := &config.Config{
		Adapters: map[string]config.AdapterConfig{
			"dangerous_shell": {
				Command: []string{"sh", "-c", "echo dangerous"},
			},
		},
	}

	evReq, _ := run.Request(tempDir, "Dangerous Task", "", "dangerous_shell", ownerID)
	var bReq struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(evReq.Body, &bReq); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	runID := bReq.RunID

	// Approve run with scope read_only (not dangerous_shell)
	if _, err := approval.Approve(tempDir, runID, "read_only", 10*time.Minute, ownerID); err != nil {
		t.Fatalf("Approve failed: %v", err)
	}

	err := run.ExecuteAdapter(context.Background(), tempDir, runID, ownerID, cfg)
	if err == nil {
		t.Fatalf("expected error executing unapproved adapter, got nil")
	}
	if !errors.Is(err, run.ErrAdapterNotApproved) {
		t.Errorf("expected ErrAdapterNotApproved, got %v", err)
	}
}

func TestExecuteAdapterNonZeroExitFailsRun(t *testing.T) {
	tempDir := t.TempDir()
	ownerID, _ := identity.Generate("owner")

	cfg := &config.Config{
		Adapters: map[string]config.AdapterConfig{
			"failing": {
				Command: []string{"sh", "-c", "exit 42"},
			},
		},
	}

	evReq, _ := run.Request(tempDir, "Failing Task", "", "failing", ownerID)
	var bReq struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(evReq.Body, &bReq); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	runID := bReq.RunID

	if _, err := approval.Approve(tempDir, runID, "failing", 10*time.Minute, ownerID); err != nil {
		t.Fatalf("Approve failed: %v", err)
	}

	err := run.ExecuteAdapter(context.Background(), tempDir, runID, ownerID, cfg)
	if err == nil {
		t.Fatalf("expected error from non-zero exit, got nil")
	}

	r, _ := run.Get(tempDir, runID)
	if r.State != run.StateFailed {
		t.Errorf("expected state failed, got %s", r.State)
	}
}
