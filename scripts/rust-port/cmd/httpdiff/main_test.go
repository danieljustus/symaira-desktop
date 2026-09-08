package main

import (
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

func testExecutable(t *testing.T) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return executable
}

func TestCleanupExitExpectedIntentionalKill(t *testing.T) {
	cmd := exec.Command(testExecutable(t), "-test.run=TestCleanupHelper") //nolint:gosec // testExecutable resolves the checked test binary; arguments are fixed test-only flags
	cmd.Env = append(os.Environ(), "HTTPDIFF_CLEANUP_HELPER=block")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err := cmd.Wait()
	if err == nil || !cleanupExitExpected(err, true, false, runtime.GOOS == "windows") {
		t.Fatalf("intentional kill should be accepted, err=%v", err)
	}
}

func TestCleanupExitExpectedAlreadyExited(t *testing.T) {
	cmd := exec.Command(testExecutable(t), "-test.run=TestCleanupHelper") //nolint:gosec // testExecutable resolves the checked test binary; arguments are fixed test-only flags
	cmd.Env = append(os.Environ(), "HTTPDIFF_CLEANUP_HELPER=exit")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if cleanupExitExpected(nil, true, true, runtime.GOOS == "windows") {
		t.Fatal("already-exited process must not turn a non-zero wait into success")
	}
}

func TestCleanupExitExpectedUnexpectedFailure(t *testing.T) {
	cmd := exec.Command(testExecutable(t), "-test.run=TestCleanupHelper") //nolint:gosec // testExecutable resolves the checked test binary; arguments are fixed test-only flags
	cmd.Env = append(os.Environ(), "HTTPDIFF_CLEANUP_HELPER=fail")
	err := cmd.Run()
	if err == nil {
		t.Fatal("expected helper failure")
	}
	if cleanupExitExpected(err, false, false, runtime.GOOS == "windows") {
		t.Fatal("unexpected process failure must remain an error")
	}
}

func TestRunningServerStopLifecycle(t *testing.T) {
	cmd := exec.Command(testExecutable(t), "-test.run=TestCleanupHelper") //nolint:gosec // testExecutable resolves the checked test binary; arguments are fixed test-only flags
	cmd.Env = append(os.Environ(), "HTTPDIFF_CLEANUP_HELPER=block")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	server := &runningServer{cmd: cmd}
	started := time.Now()
	if err := server.stop(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed >= serverStopTimeout {
		t.Fatalf("stop took %s, exceeded bound", elapsed)
	}
}

func TestRunningServerStopAlreadyExitedNonZero(t *testing.T) {
	cmd := exec.Command(testExecutable(t), "-test.run=TestCleanupHelper") //nolint:gosec // testExecutable resolves the checked test binary; arguments are fixed test-only flags
	cmd.Env = append(os.Environ(), "HTTPDIFF_CLEANUP_HELPER=fail")
	if err := cmd.Run(); err == nil {
		t.Fatal("expected helper failure")
	}
	server := &runningServer{cmd: cmd}
	if err := server.stop(); err == nil {
		t.Fatal("already-exited non-zero process must remain a cleanup error")
	}
}

func TestCleanupHelper(t *testing.T) {
	switch os.Getenv("HTTPDIFF_CLEANUP_HELPER") {
	case "block":
		time.Sleep(time.Minute)
	case "fail":
		os.Exit(1)
	case "exit":
		return
	}
}
