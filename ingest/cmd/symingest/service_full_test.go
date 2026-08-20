package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRunService_NoArgs(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("service management supports macOS LaunchAgents only")
	}
	sb := withCapturedStdout(t)
	if err := run([]string{"service"}); err != nil {
		t.Fatalf("run(service): %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "Usage:") {
		t.Errorf("expected usage output, got %q", out)
	}
}

func TestRunService_UnknownCommand(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("service management supports macOS LaunchAgents only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	withCapturedStdout(t)
	err := run([]string{
		"service",
		"-vault", filepath.Join(home, "vault"),
		"-archive", filepath.Join(home, "archive"),
		"-db", filepath.Join(home, "test.db"),
		"-inbox", filepath.Join(home, "inbox"),
		"bogus",
	})
	if err == nil {
		t.Fatal("expected error for unknown service command")
	}
	if !strings.Contains(err.Error(), "unknown service command") {
		t.Errorf("error = %v, want mention of 'unknown service command'", err)
	}
}

func TestRunService_InstallDryRun(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("service management supports macOS LaunchAgents only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	sb := withCapturedStdout(t)
	err := run([]string{
		"service", "-dry-run",
		"-vault", filepath.Join(home, "vault"),
		"-archive", filepath.Join(home, "archive"),
		"-db", filepath.Join(home, "test.db"),
		"-inbox", filepath.Join(home, "inbox"),
		"install",
	})
	if err != nil {
		t.Fatalf("run(service install -dry-run): %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "Would write LaunchAgent") {
		t.Errorf("output missing 'Would write LaunchAgent', got %q", out)
	}
}

func TestRunService_InstallDryRunJSON(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("service management supports macOS LaunchAgents only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	sb := withCapturedStdout(t)
	err := run([]string{
		"service", "-dry-run", "-json",
		"-vault", filepath.Join(home, "vault"),
		"-archive", filepath.Join(home, "archive"),
		"-db", filepath.Join(home, "test.db"),
		"-inbox", filepath.Join(home, "inbox"),
		"install",
	})
	if err != nil {
		t.Fatalf("run(service install -dry-run -json): %v", err)
	}
	out := sb.String()
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if result["action"] != "install" {
		t.Errorf("action = %v, want install", result["action"])
	}
	if result["dry_run"] != true {
		t.Errorf("dry_run = %v, want true", result["dry_run"])
	}
}

func TestRunService_UninstallDryRun(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("service management supports macOS LaunchAgents only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	sb := withCapturedStdout(t)
	err := run([]string{
		"service", "-dry-run",
		"-vault", filepath.Join(home, "vault"),
		"-archive", filepath.Join(home, "archive"),
		"-db", filepath.Join(home, "test.db"),
		"-inbox", filepath.Join(home, "inbox"),
		"uninstall",
	})
	if err != nil {
		t.Fatalf("run(service uninstall -dry-run): %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "Would stop and remove") {
		t.Errorf("output missing 'Would stop and remove', got %q", out)
	}
}

func TestRunService_UninstallDryRunJSON(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("service management supports macOS LaunchAgents only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	sb := withCapturedStdout(t)
	err := run([]string{
		"service", "-dry-run", "-json",
		"-vault", filepath.Join(home, "vault"),
		"-archive", filepath.Join(home, "archive"),
		"-db", filepath.Join(home, "test.db"),
		"-inbox", filepath.Join(home, "inbox"),
		"uninstall",
	})
	if err != nil {
		t.Fatalf("run(service uninstall -dry-run -json): %v", err)
	}
	out := sb.String()
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if result["action"] != "uninstall" {
		t.Errorf("action = %v, want uninstall", result["action"])
	}
}

func TestRunService_StartDryRun(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("service management supports macOS LaunchAgents only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	sb := withCapturedStdout(t)
	err := run([]string{
		"service", "-dry-run",
		"-vault", filepath.Join(home, "vault"),
		"-archive", filepath.Join(home, "archive"),
		"-db", filepath.Join(home, "test.db"),
		"-inbox", filepath.Join(home, "inbox"),
		"start",
	})
	if err != nil {
		t.Fatalf("run(service start -dry-run): %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "Would bootstrap/start") {
		t.Errorf("output missing 'Would bootstrap/start', got %q", out)
	}
}

func TestRunService_StartDryRunJSON(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("service management supports macOS LaunchAgents only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	sb := withCapturedStdout(t)
	err := run([]string{
		"service", "-dry-run", "-json",
		"-vault", filepath.Join(home, "vault"),
		"-archive", filepath.Join(home, "archive"),
		"-db", filepath.Join(home, "test.db"),
		"-inbox", filepath.Join(home, "inbox"),
		"start",
	})
	if err != nil {
		t.Fatalf("run(service start -dry-run -json): %v", err)
	}
	out := sb.String()
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if result["action"] != "start" {
		t.Errorf("action = %v, want start", result["action"])
	}
}

func TestRunService_StopDryRun(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("service management supports macOS LaunchAgents only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	sb := withCapturedStdout(t)
	err := run([]string{
		"service", "-dry-run",
		"-vault", filepath.Join(home, "vault"),
		"-archive", filepath.Join(home, "archive"),
		"-db", filepath.Join(home, "test.db"),
		"-inbox", filepath.Join(home, "inbox"),
		"stop",
	})
	if err != nil {
		t.Fatalf("run(service stop -dry-run): %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "Would bootout") {
		t.Errorf("output missing 'Would bootout', got %q", out)
	}
}

func TestRunService_StopDryRunJSON(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("service management supports macOS LaunchAgents only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	sb := withCapturedStdout(t)
	err := run([]string{
		"service", "-dry-run", "-json",
		"-vault", filepath.Join(home, "vault"),
		"-archive", filepath.Join(home, "archive"),
		"-db", filepath.Join(home, "test.db"),
		"-inbox", filepath.Join(home, "inbox"),
		"stop",
	})
	if err != nil {
		t.Fatalf("run(service stop -dry-run -json): %v", err)
	}
	out := sb.String()
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if result["action"] != "stop" {
		t.Errorf("action = %v, want stop", result["action"])
	}
}

func TestRunService_Status(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("service management supports macOS LaunchAgents only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	sb := withCapturedStdout(t)
	err := run([]string{
		"service",
		"-vault", filepath.Join(home, "vault"),
		"-archive", filepath.Join(home, "archive"),
		"-db", filepath.Join(home, "test.db"),
		"-inbox", filepath.Join(home, "inbox"),
		"status",
	})
	// Status may fail because launchctl will fail, but we should still get output.
	_ = err
	out := sb.String()
	if !strings.Contains(out, "Label:") {
		t.Errorf("output missing 'Label:', got %q", out)
	}
}

func TestRunService_StatusJSON(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("service management supports macOS LaunchAgents only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	sb := withCapturedStdout(t)
	err := run([]string{
		"service", "-json",
		"-vault", filepath.Join(home, "vault"),
		"-archive", filepath.Join(home, "archive"),
		"-db", filepath.Join(home, "test.db"),
		"-inbox", filepath.Join(home, "inbox"),
		"status",
	})
	// Status may fail because launchctl will fail, but we should still get JSON output.
	_ = err
	out := sb.String()
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if result["label"] != serviceLabel {
		t.Errorf("label = %v, want %s", result["label"], serviceLabel)
	}
}

func TestRunService_Logs(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("service management supports macOS LaunchAgents only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Create log directory and files.
	logDir := filepath.Join(home, "Library", "Logs", "symingest")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "watch.log"), []byte("log line 1\nlog line 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "watch.err.log"), []byte("error line 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	sb := withCapturedStdout(t)
	err := run([]string{
		"service",
		"-vault", filepath.Join(home, "vault"),
		"-archive", filepath.Join(home, "archive"),
		"-db", filepath.Join(home, "test.db"),
		"-inbox", filepath.Join(home, "inbox"),
		"logs",
	})
	if err != nil {
		t.Fatalf("run(service logs): %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "watch.log") {
		t.Errorf("output missing 'watch.log', got %q", out)
	}
	if !strings.Contains(out, "log line 1") {
		t.Errorf("output missing log content, got %q", out)
	}
}

func TestRunService_LogsJSON(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("service management supports macOS LaunchAgents only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	sb := withCapturedStdout(t)
	err := run([]string{
		"service", "-json",
		"-vault", filepath.Join(home, "vault"),
		"-archive", filepath.Join(home, "archive"),
		"-db", filepath.Join(home, "test.db"),
		"-inbox", filepath.Join(home, "inbox"),
		"logs",
	})
	if err != nil {
		t.Fatalf("run(service logs -json): %v", err)
	}
	out := sb.String()
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if result["label"] != serviceLabel {
		t.Errorf("label = %v, want %s", result["label"], serviceLabel)
	}
}

func TestBuildServiceOptions_MissingInbox(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("service management supports macOS LaunchAgents only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := &resolvedConfig{
		vault:   filepath.Join(home, "vault"),
		archive: filepath.Join(home, "archive"),
		db:      filepath.Join(home, "test.db"),
		inbox:   "",
		ocrLang: "eng",
	}

	_, err := buildServiceOptions(cfg, "", "", "", "", "1s")
	if err == nil {
		t.Fatal("expected error for missing inbox")
	}
	if !strings.Contains(err.Error(), "no inbox configured") {
		t.Errorf("error = %v, want mention of 'no inbox configured'", err)
	}
}

func TestBuildServiceOptions_MissingVault(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("service management supports macOS LaunchAgents only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := &resolvedConfig{
		vault:   "",
		archive: filepath.Join(home, "archive"),
		db:      filepath.Join(home, "test.db"),
		inbox:   filepath.Join(home, "inbox"),
		ocrLang: "eng",
	}

	_, err := buildServiceOptions(cfg, "", "", "", "", "1s")
	if err == nil {
		t.Fatal("expected error for missing vault")
	}
	if !strings.Contains(err.Error(), "no vault configured") {
		t.Errorf("error = %v, want mention of 'no vault configured'", err)
	}
}

func TestBuildServiceOptions_Success(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("service management supports macOS LaunchAgents only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := &resolvedConfig{
		vault:   filepath.Join(home, "vault"),
		archive: filepath.Join(home, "archive"),
		db:      filepath.Join(home, "test.db"),
		inbox:   filepath.Join(home, "inbox"),
		ocrLang: "eng",
	}

	opts, err := buildServiceOptions(cfg, "", "", "", "", "1s")
	if err != nil {
		t.Fatalf("buildServiceOptions: %v", err)
	}
	if opts.Inbox == "" {
		t.Error("Inbox is empty")
	}
	if opts.Vault != cfg.vault {
		t.Errorf("Vault = %q, want %q", opts.Vault, cfg.vault)
	}
	if opts.PlistPath == "" {
		t.Error("PlistPath is empty")
	}
	if opts.LogDir == "" {
		t.Error("LogDir is empty")
	}
}

func TestLaunchAgentPath(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("service management supports macOS LaunchAgents only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	path, err := launchAgentPath()
	if err != nil {
		t.Fatalf("launchAgentPath: %v", err)
	}
	if !strings.Contains(path, serviceLabel) {
		t.Errorf("path = %q, missing service label %q", path, serviceLabel)
	}
	if !strings.Contains(path, "LaunchAgents") {
		t.Errorf("path = %q, missing 'LaunchAgents'", path)
	}
}

func TestServiceLogDir(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("service management supports macOS LaunchAgents only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir, err := serviceLogDir()
	if err != nil {
		t.Fatalf("serviceLogDir: %v", err)
	}
	if !strings.Contains(dir, "symingest") {
		t.Errorf("dir = %q, missing 'symingest'", dir)
	}
	if !strings.Contains(dir, "Logs") {
		t.Errorf("dir = %q, missing 'Logs'", dir)
	}
}

func TestLaunchctlDomain(t *testing.T) {
	domain := launchctlDomain()
	if !strings.HasPrefix(domain, "gui/") {
		t.Errorf("domain = %q, want prefix 'gui/'", domain)
	}
}

func TestRenderLaunchAgent(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("service management supports macOS LaunchAgents only")
	}
	opts := &serviceOptions{
		Inbox:         "/tmp/inbox",
		Vault:         "/tmp/vault",
		Archive:       "/tmp/archive",
		DB:            "/tmp/test.db",
		OCRLang:       "eng",
		ProcessingDir: "/tmp/inbox/.processing",
		ProcessedDir:  "/tmp/inbox/.processed",
		FailedDir:     "/tmp/inbox/.failed",
		StableFor:     "1s",
		Binary:        "/usr/local/bin/symingest",
		PlistPath:     "/tmp/plist",
		LogDir:        "/tmp/logs",
	}

	plist := renderLaunchAgent(opts)
	if !strings.Contains(plist, serviceLabel) {
		t.Errorf("plist missing service label")
	}
	if !strings.Contains(plist, opts.Binary) {
		t.Errorf("plist missing binary path")
	}
	if !strings.Contains(plist, opts.Inbox) {
		t.Errorf("plist missing inbox path")
	}
	if !strings.Contains(plist, "watch.log") {
		t.Errorf("plist missing log path")
	}
}

func TestWatchLockPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path, err := watchLockPath("/tmp/inbox", "/tmp/test.db")
	if err != nil {
		t.Fatalf("watchLockPath: %v", err)
	}
	if !strings.Contains(path, "watch-") {
		t.Errorf("path = %q, missing 'watch-'", path)
	}
	if !strings.HasSuffix(path, ".lock") {
		t.Errorf("path = %q, missing '.lock' suffix", path)
	}
}

func TestAcquireWatchLock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	inbox := filepath.Join(home, "inbox")
	db := filepath.Join(home, "test.db")

	lock, err := acquireWatchLock(inbox, db)
	if err != nil {
		t.Fatalf("acquireWatchLock: %v", err)
	}
	if lock == nil {
		t.Fatal("lock is nil")
	}
	if lock.path == "" {
		t.Error("lock.path is empty")
	}

	// Release the lock.
	lock.Release()

	// Verify lock file is removed.
	if _, err := os.Stat(lock.path); !os.IsNotExist(err) {
		t.Errorf("lock file still exists after Release")
	}
}

func TestAcquireWatchLock_Duplicate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	inbox := filepath.Join(home, "inbox")
	db := filepath.Join(home, "test.db")

	lock1, err := acquireWatchLock(inbox, db)
	if err != nil {
		t.Fatalf("first acquireWatchLock: %v", err)
	}
	defer lock1.Release()

	// Second acquire should fail because lock is held.
	_, err = acquireWatchLock(inbox, db)
	if err == nil {
		t.Fatal("expected error for duplicate lock")
	}
	if !strings.Contains(err.Error(), "watcher already running") {
		t.Errorf("error = %v, want mention of 'watcher already running'", err)
	}
}

func TestReadLockPID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.lock")

	// Write a lock file with PID.
	content := "12345\n/tmp/inbox\n2026-01-01T00:00:00Z\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	pid, stale := readLockPID(path)
	if stale {
		t.Error("stale = true, want false")
	}
	if pid != 12345 {
		t.Errorf("pid = %d, want 12345", pid)
	}
}

func TestReadLockPID_MissingFile(t *testing.T) {
	pid, stale := readLockPID(filepath.Join(t.TempDir(), "nonexistent.lock"))
	if !stale {
		t.Error("stale = false, want true for missing file")
	}
	if pid != 0 {
		t.Errorf("pid = %d, want 0", pid)
	}
}

func TestReadLockPID_InvalidPID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.lock")

	content := "not-a-number\n/tmp/inbox\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	pid, stale := readLockPID(path)
	if !stale {
		t.Error("stale = false, want true for invalid PID")
	}
	if pid != 0 {
		t.Errorf("pid = %d, want 0", pid)
	}
}

func TestProcessAlive(t *testing.T) {
	// Test with current process PID (should be alive).
	pid := os.Getpid()
	if !processAlive(pid) {
		t.Errorf("processAlive(%d) = false, want true for current process", pid)
	}

	// Test with invalid PID (should not be alive).
	if processAlive(0) {
		t.Error("processAlive(0) = true, want false")
	}
	if processAlive(-1) {
		t.Error("processAlive(-1) = true, want false")
	}
}

func TestServiceInstall_Real(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("service management supports macOS LaunchAgents only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	opts := &serviceOptions{
		Inbox:         filepath.Join(home, "inbox"),
		Vault:         filepath.Join(home, "vault"),
		Archive:       filepath.Join(home, "archive"),
		DB:            filepath.Join(home, "test.db"),
		OCRLang:       "eng",
		ProcessingDir: filepath.Join(home, "inbox", ".processing"),
		ProcessedDir:  filepath.Join(home, "inbox", ".processed"),
		FailedDir:     filepath.Join(home, "inbox", ".failed"),
		StableFor:     "1s",
		Binary:        "/usr/local/bin/symingest",
		PlistPath:     filepath.Join(home, "Library", "LaunchAgents", serviceLabel+".plist"),
		LogDir:        filepath.Join(home, "Library", "Logs", "symingest"),
	}

	if err := os.MkdirAll(filepath.Dir(opts.PlistPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(opts.LogDir, 0o700); err != nil {
		t.Fatal(err)
	}

	sb := withCapturedStdout(t)
	if err := serviceInstall(opts, false, false); err != nil {
		t.Fatalf("serviceInstall: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "Installed LaunchAgent") {
		t.Errorf("output missing 'Installed LaunchAgent', got %q", out)
	}

	if _, err := os.Stat(opts.PlistPath); err != nil {
		t.Errorf("plist not written: %v", err)
	}
}

func TestServiceInstall_RealJSON(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("service management supports macOS LaunchAgents only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	opts := &serviceOptions{
		Inbox:         filepath.Join(home, "inbox"),
		Vault:         filepath.Join(home, "vault"),
		Archive:       filepath.Join(home, "archive"),
		DB:            filepath.Join(home, "test.db"),
		OCRLang:       "eng",
		ProcessingDir: filepath.Join(home, "inbox", ".processing"),
		ProcessedDir:  filepath.Join(home, "inbox", ".processed"),
		FailedDir:     filepath.Join(home, "inbox", ".failed"),
		StableFor:     "1s",
		Binary:        "/usr/local/bin/symingest",
		PlistPath:     filepath.Join(home, "Library", "LaunchAgents", serviceLabel+".plist"),
		LogDir:        filepath.Join(home, "Library", "Logs", "symingest"),
	}

	if err := os.MkdirAll(filepath.Dir(opts.PlistPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(opts.LogDir, 0o700); err != nil {
		t.Fatal(err)
	}

	sb := withCapturedStdout(t)
	if err := serviceInstall(opts, false, true); err != nil {
		t.Fatalf("serviceInstall: %v", err)
	}
	out := sb.String()
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if result["action"] != "install" {
		t.Errorf("action = %v, want install", result["action"])
	}
}

func TestServiceUninstall_Real(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("service management supports macOS LaunchAgents only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	opts := &serviceOptions{
		PlistPath: filepath.Join(home, "Library", "LaunchAgents", serviceLabel+".plist"),
	}

	if err := os.MkdirAll(filepath.Dir(opts.PlistPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(opts.PlistPath, []byte("<plist></plist>"), 0o600); err != nil {
		t.Fatal(err)
	}

	sb := withCapturedStdout(t)
	_ = serviceUninstall(opts, false, false)
	out := sb.String()
	if _, err := os.Stat(opts.PlistPath); !os.IsNotExist(err) {
		t.Errorf("plist still exists after uninstall")
	}
	_ = out
}

func TestServiceStart_NotInstalled(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("service management supports macOS LaunchAgents only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	opts := &serviceOptions{
		PlistPath: filepath.Join(home, "Library", "LaunchAgents", serviceLabel+".plist"),
	}

	withCapturedStdout(t)
	err := serviceStart(opts, false, false)
	if err == nil {
		t.Fatal("expected error for not installed")
	}
	if !strings.Contains(err.Error(), "not installed") {
		t.Errorf("error = %v, want mention of 'not installed'", err)
	}
}
