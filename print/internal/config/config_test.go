package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefault(t *testing.T) {
	cfg := Default()

	if cfg.Engine.Typst != "" {
		t.Errorf("Engine.Typst = %q, want empty (resolve from PATH)", cfg.Engine.Typst)
	}
	if cfg.Engine.FontPaths != nil {
		t.Errorf("Engine.FontPaths = %v, want nil", cfg.Engine.FontPaths)
	}
	if cfg.Engine.IgnoreSystemFonts {
		t.Error("Engine.IgnoreSystemFonts = true, want false")
	}
	if cfg.Engine.TimeoutSeconds != 60 {
		t.Errorf("Engine.TimeoutSeconds = %d, want 60", cfg.Engine.TimeoutSeconds)
	}
	if cfg.Defaults.Profile != "report" {
		t.Errorf("Defaults.Profile = %q, want %q", cfg.Defaults.Profile, "report")
	}
	if cfg.Defaults.Reproducible {
		t.Error("Defaults.Reproducible = true, want false")
	}
	if cfg.MCP.OutputRoot != "" {
		t.Errorf("MCP.OutputRoot = %q, want empty (containment is opt-in; the mcp server warns on startup)", cfg.MCP.OutputRoot)
	}
}

func TestEngineTimeout(t *testing.T) {
	tests := []struct {
		name           string
		timeoutSeconds int
		want           time.Duration
	}{
		{"zero falls back to 60s default", 0, 60 * time.Second},
		{"negative falls back to 60s default", -5, 60 * time.Second},
		{"positive value is honored", 30, 30 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := Engine{TimeoutSeconds: tt.timeoutSeconds}
			if got := e.Timeout(); got != tt.want {
				t.Errorf("Timeout() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got := Path()
	want := filepath.Join(home, ".config", "symprint", "config.toml")
	if got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}

func TestDefaultConfigTOML(t *testing.T) {
	toml := DefaultConfigTOML()
	if toml == "" {
		t.Fatal("DefaultConfigTOML() returned empty string")
	}
	for _, want := range []string{"[engine]", "typst", "[defaults]", "profile = \"report\""} {
		if !strings.Contains(toml, want) {
			t.Errorf("DefaultConfigTOML() missing %q", want)
		}
	}
}

func TestLoad_DefaultsWhenNoFilesOrEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	loader.ResetCache()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Defaults.Profile != "report" {
		t.Errorf("Defaults.Profile = %q, want %q", cfg.Defaults.Profile, "report")
	}
	if cfg.Engine.TimeoutSeconds != 60 {
		t.Errorf("Engine.TimeoutSeconds = %d, want 60", cfg.Engine.TimeoutSeconds)
	}
}

func TestLoad_GlobalFileOverridesDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())

	configDir := filepath.Join(home, ".config", "symprint")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	tomlContent := `[engine]
typst = "/usr/local/bin/typst"
timeout_seconds = 120

[defaults]
profile = "brief"
`
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(tomlContent), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	loader.ResetCache()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Engine.Typst != "/usr/local/bin/typst" {
		t.Errorf("Engine.Typst = %q, want %q", cfg.Engine.Typst, "/usr/local/bin/typst")
	}
	if cfg.Engine.TimeoutSeconds != 120 {
		t.Errorf("Engine.TimeoutSeconds = %d, want 120", cfg.Engine.TimeoutSeconds)
	}
	if cfg.Defaults.Profile != "brief" {
		t.Errorf("Defaults.Profile = %q, want %q", cfg.Defaults.Profile, "brief")
	}
}

func TestLoad_EnvOverridesFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())

	configDir := filepath.Join(home, ".config", "symprint")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	tomlContent := `[defaults]
profile = "brief"
`
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(tomlContent), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	t.Setenv("SYMPRINT_DEFAULTS_PROFILE", "rechnung")
	t.Setenv("SYMPRINT_ENGINE_TYPST", "/opt/typst")
	loader.ResetCache()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Defaults.Profile != "rechnung" {
		t.Errorf("Defaults.Profile = %q, want %q (env should win over file)", cfg.Defaults.Profile, "rechnung")
	}
	if cfg.Engine.Typst != "/opt/typst" {
		t.Errorf("Engine.Typst = %q, want %q (env should win)", cfg.Engine.Typst, "/opt/typst")
	}
}

func TestLoad_MCPOutputRoot_FromFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())

	configDir := filepath.Join(home, ".config", "symprint")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	tomlContent := `[mcp]
output_root = "/tmp/symprint-out"
`
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(tomlContent), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	loader.ResetCache()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.MCP.OutputRoot != "/tmp/symprint-out" {
		t.Errorf("MCP.OutputRoot = %q, want %q (explicit output_root must survive loading)", cfg.MCP.OutputRoot, "/tmp/symprint-out")
	}
}

func TestLoad_MCPOutputRoot_EnvOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	t.Setenv("SYMPRINT_MCP_OUTPUT_ROOT", "/env/symprint-out")
	loader.ResetCache()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.MCP.OutputRoot != "/env/symprint-out" {
		t.Errorf("MCP.OutputRoot = %q, want %q (env should win)", cfg.MCP.OutputRoot, "/env/symprint-out")
	}
}
