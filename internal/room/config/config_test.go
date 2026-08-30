package config

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-corekit/configkit"
	"github.com/danieljustus/symaira-corekit/exitcodes"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Approval.DefaultTTL != "30m" {
		t.Errorf("expected default_ttl 30m, got %s", cfg.Approval.DefaultTTL)
	}
	if !cfg.Updatecheck.Enabled {
		t.Errorf("expected updatecheck.enabled true, got false")
	}
}

func TestEnvOverrides(t *testing.T) {
	tests := []struct {
		name   string
		envKey string
		envVal string
		check  func(*testing.T, *Config)
	}{
		{
			name:   "override default_identity",
			envKey: "SYMROOM_DEFAULT_IDENTITY",
			envVal: "agent-alice",
			check: func(t *testing.T, cfg *Config) {
				if cfg.DefaultIdentity != "agent-alice" {
					t.Errorf("expected agent-alice, got %s", cfg.DefaultIdentity)
				}
			},
		},
		{
			name:   "override approval default_ttl",
			envKey: "SYMROOM_APPROVAL_DEFAULT_TTL",
			envVal: "1h",
			check: func(t *testing.T, cfg *Config) {
				if cfg.Approval.DefaultTTL != "1h" {
					t.Errorf("expected 1h, got %s", cfg.Approval.DefaultTTL)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(tt.envKey, tt.envVal)
			loader := NewLoader()
			loader.ResetCache()
			cfg, err := loader.Load()
			if err != nil {
				t.Fatalf("unexpected error loading config: %v", err)
			}
			tt.check(t, cfg)
		})
	}
}

func TestInvalidTOML(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)
	t.Setenv("XDG_CONFIG_HOME", tempDir)

	symroomDir := filepath.Join(tempDir, ".config", "symroom")
	if err := os.MkdirAll(symroomDir, 0755); err != nil {
		t.Fatalf("failed to create dir: %v", err)
	}
	invalidFile := filepath.Join(symroomDir, "config.toml")
	if err := os.WriteFile(invalidFile, []byte("[approval]\ndefault_ttl = [invalid toml syntax"), 0644); err != nil {
		t.Fatalf("failed to write invalid toml: %v", err)
	}

	t.Logf("DefaultPath: %s, file exists: %v", configkit.DefaultPath("symroom"), func() bool {
		_, err := os.Stat(configkit.DefaultPath("symroom"))
		return err == nil
	}())

	loader := NewLoader()
	loader.ResetCache()
	_, err := loader.Load()
	if err == nil {
		t.Fatalf("expected error for invalid TOML at %s, got nil", invalidFile)
	}
}

func TestValidateConfig(t *testing.T) {
	if err := ValidateConfig(nil); err == nil {
		t.Error("expected error for nil config")
	}

	// No fields are currently required: both the defaults and an empty config
	// pass validation.
	if err := ValidateConfig(DefaultConfig()); err != nil {
		t.Errorf("expected default config to be valid, got %v", err)
	}
	if err := ValidateConfig(&Config{}); err != nil {
		t.Errorf("expected empty config to be valid, got %v", err)
	}
}

func TestTOMLFileApplied(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)

	cfgDir := filepath.Join(tempDir, ".config", "symroom")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	tomlContent := `default_identity = "agent-file"

[approval]
default_ttl = "45m"
`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(tomlContent), 0o644); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}

	loader := NewLoader()
	loader.ResetCache()
	cfg, err := loader.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.DefaultIdentity != "agent-file" {
		t.Errorf("expected default_identity agent-file, got %s", cfg.DefaultIdentity)
	}
	if cfg.Approval.DefaultTTL != "45m" {
		t.Errorf("expected approval.default_ttl 45m, got %s", cfg.Approval.DefaultTTL)
	}
	if !cfg.Updatecheck.Enabled {
		t.Errorf("expected updatecheck.enabled to stay at default true, got false")
	}
}

// TestTOMLFalseValueIgnored pins configkit's non-zero-application rule: a TOML
// `enabled = false` is treated as a zero value and silently skipped, so the
// default stays true. Disabling must go through the env override instead.
func TestTOMLFalseValueIgnored(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)

	cfgDir := filepath.Join(tempDir, ".config", "symroom")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	tomlContent := `[updatecheck]
enabled = false
`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(tomlContent), 0o644); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}

	loader := NewLoader()
	loader.ResetCache()
	cfg, err := loader.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if !cfg.Updatecheck.Enabled {
		t.Errorf("expected updatecheck.enabled to remain true (TOML false is ignored), got false")
	}
}

func TestEnvOverrideUpdatecheckEnabled(t *testing.T) {
	// Env key derives from the json tag: updatecheck -> SYMROOM_UPDATECHECK_ENABLED.
	t.Setenv("SYMROOM_UPDATECHECK_ENABLED", "false")

	loader := NewLoader()
	loader.ResetCache()
	cfg, err := loader.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.Updatecheck.Enabled {
		t.Errorf("expected updatecheck.enabled false via env override, got true")
	}
}

func TestTOMLWithAdaptersReturnsError(t *testing.T) {
	// configkit intentionally rejects map fields; document that behavior so a
	// future config file with [adapters] fails loudly instead of silently.
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)

	cfgDir := filepath.Join(tempDir, ".config", "symroom")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	tomlContent := `default_identity = "agent-file"

[adapters.deploy]
command = ["echo", "hi"]
workdir = "/tmp"
`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(tomlContent), 0o644); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}

	loader := NewLoader()
	loader.ResetCache()
	_, err := loader.Load()
	if err == nil {
		t.Fatal("expected error for adapters map field, got nil")
	}
	if !strings.Contains(err.Error(), "map fields are not supported") {
		t.Errorf("expected map-field error, got %v", err)
	}
}

// TestLoadOrExit runs LoadOrExit in a subprocess: os.Exit cannot be exercised
// in-process. The helper child process either hits the error path (invalid
// config -> exit code ExitNoInput + stderr message) or the success path
// (valid defaults -> exit code 0).
func TestLoadOrExit(t *testing.T) {
	if os.Getenv("GO_WANT_LOADOREXIT_HELPER") == "1" {
		LoadOrExit()
		return
	}

	runHelper := func(t *testing.T, home string) (string, int) {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^TestLoadOrExit$")
		var env []string
		for _, kv := range os.Environ() {
			if strings.HasPrefix(kv, "SYMROOM_") {
				continue
			}
			env = append(env, kv)
		}
		env = append(env, "GO_WANT_LOADOREXIT_HELPER=1", "HOME="+home)
		cmd.Env = env

		out, err := cmd.CombinedOutput()
		code := 0
		if err != nil {
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("run helper: %v", err)
			}
			code = exitErr.ExitCode()
		}
		return string(out), code
	}

	t.Run("invalid config exits with ExitNoInput", func(t *testing.T) {
		home := t.TempDir()
		cfgDir := filepath.Join(home, ".config", "symroom")
		if err := os.MkdirAll(cfgDir, 0o755); err != nil {
			t.Fatalf("mkdir config dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte("[approval]\ndefault_ttl = [broken"), 0o644); err != nil {
			t.Fatalf("write config.toml: %v", err)
		}

		out, code := runHelper(t, home)
		if code != int(exitcodes.ExitNoInput) {
			t.Errorf("expected exit code %d, got %d (output: %s)", exitcodes.ExitNoInput, code, out)
		}
		if !strings.Contains(out, "Error loading configuration") {
			t.Errorf("expected error message on stderr, got: %s", out)
		}
	})

	t.Run("valid config exits zero", func(t *testing.T) {
		out, code := runHelper(t, t.TempDir())
		if code != 0 {
			t.Errorf("expected exit code 0, got %d (output: %s)", code, out)
		}
		if strings.Contains(out, "Error loading configuration") {
			t.Errorf("expected no config error, got: %s", out)
		}
	})
}
