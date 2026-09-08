//go:build windows

package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestTerminateProcessTreeTaskkillFailurePreserved(t *testing.T) {
	original := taskkillCommand
	t.Cleanup(func() { taskkillCommand = original })
	taskkillCommand = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		command := exec.CommandContext(ctx, testExecutable(t), "-test.run=TestCleanupHelper") //nolint:gosec // testExecutable resolves the checked test binary; arguments are fixed test-only flags
		command.Env = append(os.Environ(), "HTTPDIFF_CLEANUP_HELPER=fail")
		return command
	}
	cmd := exec.Command(testExecutable(t), "-test.run=TestCleanupHelper") //nolint:gosec // testExecutable resolves the checked test binary; arguments are fixed test-only flags
	cmd.Env = append(os.Environ(), "HTTPDIFF_CLEANUP_HELPER=block")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	err := terminateProcessTree(cmd)
	if err == nil || !strings.Contains(err.Error(), "taskkill process tree") {
		t.Fatalf("error = %v, want preserved taskkill failure", err)
	}
}

func TestTerminateProcessTreeTaskkillTimeoutIsBounded(t *testing.T) {
	original := taskkillCommand
	t.Cleanup(func() { taskkillCommand = original })
	taskkillCommand = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		command := exec.CommandContext(ctx, testExecutable(t), "-test.run=TestCleanupHelper") //nolint:gosec // testExecutable resolves the checked test binary; arguments are fixed test-only flags
		command.Env = append(os.Environ(), "HTTPDIFF_CLEANUP_HELPER=block")
		return command
	}
	cmd := exec.Command(testExecutable(t), "-test.run=TestCleanupHelper") //nolint:gosec // testExecutable resolves the checked test binary; arguments are fixed test-only flags
	cmd.Env = append(os.Environ(), "HTTPDIFF_CLEANUP_HELPER=block")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	started := time.Now()
	err := terminateProcessTree(cmd)
	if err == nil || !strings.Contains(err.Error(), "taskkill process tree") {
		t.Fatalf("error = %v, want timeout failure", err)
	}
	if elapsed := time.Since(started); elapsed > processTerminationTimeout+time.Second {
		t.Fatalf("termination took %s, exceeded timeout bound", elapsed)
	}
}
