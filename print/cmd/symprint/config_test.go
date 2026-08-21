package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewConfigInitCmd_WritesDefaultConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cmd := newConfigInitCmd()
	out := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
	})
	if !strings.Contains(out, "wrote") {
		t.Errorf("expected a confirmation line, got: %q", out)
	}

	path := filepath.Join(home, ".config", "symprint", "config.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("config file was not written: %v", err)
	}
	if len(data) == 0 {
		t.Error("config file is empty")
	}
}

func TestNewConfigInitCmd_RefusesToOverwriteWithoutForce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".config", "symprint", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte("# existing\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cmd := newConfigInitCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected an error when the config file already exists")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error %q should mention the file already exists", err.Error())
	}

	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("ReadFile() error = %v", readErr)
	}
	if string(data) != "# existing\n" {
		t.Error("the existing config file should not have been modified")
	}
}

func TestNewConfigInitCmd_ForceOverwrites(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".config", "symprint", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte("# existing\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cmd := newConfigInitCmd()
	cmd.SetArgs([]string{"--force"})
	captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
	})

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) == "# existing\n" {
		t.Error("expected --force to overwrite the existing config file")
	}
}

func TestNewConfigInitCmd_MkdirAllError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// ".config" exists as a regular file, so MkdirAll(".config/symprint")
	// fails because a path component is not a directory.
	if err := os.WriteFile(filepath.Join(home, ".config"), []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cmd := newConfigInitCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected an error when the config directory cannot be created")
	}
}

func TestNewConfigInitCmd_WriteFileError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configDir := filepath.Join(home, ".config", "symprint")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	// A read-only directory lets MkdirAll succeed (already exists) but makes
	// the subsequent WriteFile fail with a permission error.
	if err := os.Chmod(configDir, 0o555); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(configDir, 0o755) })

	cmd := newConfigInitCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected an error when the config file cannot be written")
	}
}

func TestNewConfigPathCmd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cmd := newConfigPathCmd()
	out := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
	})
	want := filepath.Join(home, ".config", "symprint", "config.toml")
	if strings.TrimSpace(out) != want {
		t.Errorf("output = %q, want %q", strings.TrimSpace(out), want)
	}
}

func TestNewConfigCmd_HasSubcommands(t *testing.T) {
	cmd := newConfigCmd()
	expected := []string{"init", "path"}
	for _, name := range expected {
		found := false
		for _, sub := range cmd.Commands() {
			if sub.Name() == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing subcommand %q", name)
		}
	}
}
