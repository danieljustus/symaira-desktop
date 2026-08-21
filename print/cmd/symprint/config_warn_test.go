package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-print/internal/config"
	"github.com/spf13/cobra"
)

// captureStderr is a helper to capture os.Stderr.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured stderr: %v", err)
	}
	return string(out)
}

func TestLoadConfigOrWarn_StderrWarning(t *testing.T) {
	config.ResetForTest()
	jsonOut = false
	configWarnings = nil

	home := t.TempDir()
	t.Setenv("HOME", home)
	configDir := filepath.Join(home, ".config", "symprint")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("not = [valid toml"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cmd := &cobra.Command{Use: "doctor"}
	stderr := captureStderr(t, func() {
		cfg := loadConfigOrWarn(cmd)
		if cfg == nil {
			t.Error("expected config fallback, got nil")
		}
	})

	if !strings.Contains(stderr, "warning: config ignored") {
		t.Errorf("expected stderr warning, got: %q", stderr)
	}
	if len(configWarnings) > 0 {
		t.Errorf("expected no configWarnings stored in non-json mode, got: %v", configWarnings)
	}
}

func TestLoadConfigOrWarn_JSONWarning(t *testing.T) {
	config.ResetForTest()
	jsonOut = true
	defer func() { jsonOut = false }()
	configWarnings = nil

	home := t.TempDir()
	t.Setenv("HOME", home)
	configDir := filepath.Join(home, ".config", "symprint")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("not = [valid toml"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cmd := &cobra.Command{Use: "doctor"}
	stderr := captureStderr(t, func() {
		cfg := loadConfigOrWarn(cmd)
		if cfg == nil {
			t.Error("expected config fallback, got nil")
		}
	})

	if stderr != "" {
		t.Errorf("expected no stderr output in json mode, got: %q", stderr)
	}
	if len(configWarnings) != 1 || !strings.Contains(configWarnings[0], "warning: config ignored") {
		t.Errorf("expected warning stored in configWarnings, got: %v", configWarnings)
	}

	// Verify printJSON includes warning
	out := captureStdout(t, func() {
		if err := printJSON(map[string]any{"ok": true}); err != nil {
			t.Fatalf("printJSON error = %v", err)
		}
	})

	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal error = %v, output: %s", err, out)
	}
	warnings, ok := result["warnings"].([]any)
	if !ok || len(warnings) != 1 {
		t.Errorf("expected warnings slice in JSON output, got: %v", result["warnings"])
	}
}

func TestLoadConfigOrWarn_MCPKeepStdoutClean(t *testing.T) {
	config.ResetForTest()
	jsonOut = true
	defer func() { jsonOut = false }()
	configWarnings = nil

	home := t.TempDir()
	t.Setenv("HOME", home)
	configDir := filepath.Join(home, ".config", "symprint")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("not = [valid toml"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cmd := &cobra.Command{Use: "mcp"}
	stderr := captureStderr(t, func() {
		cfg := loadConfigOrWarn(cmd)
		if cfg == nil {
			t.Error("expected config fallback, got nil")
		}
	})

	// MCP warning goes to stderr even in json mode
	if !strings.Contains(stderr, "warning: config ignored") {
		t.Errorf("expected MCP warnings in stderr, got: %q", stderr)
	}
	if len(configWarnings) > 0 {
		t.Errorf("expected no configWarnings stored in MCP mode, got: %v", configWarnings)
	}
}
