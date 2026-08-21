package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-print/internal/config"
	"github.com/danieljustus/symaira-print/internal/press"
)

// TestNewDoctorCmd_ConfigLoadFallsBackOnError exercises the config.Load()
// error branch in newDoctorCmd.
func TestNewDoctorCmd_ConfigLoadFallsBackOnError(t *testing.T) {
	config.ResetForTest()
	jsonOut = false
	home := t.TempDir()
	t.Setenv("HOME", home)
	configDir := filepath.Join(home, ".config", "symprint")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("not = [valid toml"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cmd := newDoctorCmd()
	out := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("Execute() error = %v, want nil (errors fall back to defaults)", err)
		}
	})
	if !strings.Contains(out, "symprint doctor") {
		t.Errorf("expected normal doctor output despite the bad config, got: %q", out)
	}
}

func TestNewDoctorCmd_TextOutput(t *testing.T) {
	jsonOut = false
	cmd := newDoctorCmd()
	out := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
	})
	if !strings.Contains(out, "symprint doctor") {
		t.Errorf("output missing header, got: %q", out)
	}
	if !strings.Contains(out, "typst") || !strings.Contains(out, "pandoc") || !strings.Contains(out, "verapdf") {
		t.Errorf("output missing engine rows, got: %q", out)
	}
}

func TestNewDoctorCmd_JSON(t *testing.T) {
	jsonOut = true
	defer func() { jsonOut = false }()
	cmd := newDoctorCmd()
	out := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
	})
	var result struct {
		Typst   press.EngineInfo `json:"typst"`
		Pandoc  press.EngineInfo `json:"pandoc"`
		Verapdf press.EngineInfo `json:"verapdf"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, out)
	}
	if result.Typst.Name != "typst" {
		t.Errorf("typst.name = %q, want %q", result.Typst.Name, "typst")
	}
}

func TestNewDoctorCmd_TypstUnavailable(t *testing.T) {
	press.ResetProbeCache()
	defer press.ResetProbeCache()
	jsonOut = false
	// config.Load() caches process-wide (sync.Once), so overriding
	// SYMPRINT_ENGINE_TYPST here would not be picked up once another test has
	// already loaded it. The default Engine.Typst is "", which makes
	// DetectTypst resolve "typst" via exec.LookPath against the live PATH —
	// emptying PATH reliably forces the unavailable branch regardless of
	// config caching.
	t.Setenv("PATH", "")
	cmd := newDoctorCmd()
	out := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
	})
	if !strings.Contains(out, press.TypstInstallHint) {
		t.Errorf("expected the install hint when typst is unavailable, got: %q", out)
	}
	if !strings.Contains(out, "✗") {
		t.Errorf("expected a failure glyph for typst, got: %q", out)
	}
}

func TestLookOptional(t *testing.T) {
	t.Run("found", func(t *testing.T) {
		info := lookOptional("ls")
		if !info.Available {
			t.Error("expected ls to be found on PATH")
		}
		if info.Path == "" {
			t.Error("expected a resolved path")
		}
		if info.Name != "ls" {
			t.Errorf("Name = %q, want %q", info.Name, "ls")
		}
	})

	t.Run("not found", func(t *testing.T) {
		info := lookOptional("symprint-doctor-test-nonexistent-binary")
		if info.Available {
			t.Error("expected binary to be reported as unavailable")
		}
		if info.Path != "" {
			t.Errorf("Path = %q, want empty", info.Path)
		}
	})
}

func TestEngineLine(t *testing.T) {
	tests := []struct {
		name string
		info press.EngineInfo
		want string
	}{
		{"not available", press.EngineInfo{Available: false}, "not found"},
		{"available with version", press.EngineInfo{Available: true, Path: "/usr/bin/typst", Version: "0.15.0"}, "0.15.0  (/usr/bin/typst)"},
		{"available without version", press.EngineInfo{Available: true, Path: "/usr/bin/typst"}, "?  (/usr/bin/typst)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := engineLine(tt.info); got != tt.want {
				t.Errorf("engineLine() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPathOrDash(t *testing.T) {
	if got := pathOrDash(press.EngineInfo{Available: true, Path: "/usr/bin/pandoc"}); got != "/usr/bin/pandoc" {
		t.Errorf("pathOrDash() = %q, want %q", got, "/usr/bin/pandoc")
	}
	if got := pathOrDash(press.EngineInfo{Available: false}); got != "—" {
		t.Errorf("pathOrDash() = %q, want %q", got, "—")
	}
}

func TestOkGlyph(t *testing.T) {
	if got := okGlyph(true); got != "✓" {
		t.Errorf("okGlyph(true) = %q, want ✓", got)
	}
	if got := okGlyph(false); got != "✗" {
		t.Errorf("okGlyph(false) = %q, want ✗", got)
	}
}
