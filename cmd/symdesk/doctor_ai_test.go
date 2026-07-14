package main

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/spf13/cobra"
)

func findDoctorCmd(t *testing.T) *cobra.Command {
	t.Helper()
	rootCmd := &cobra.Command{Use: "test"}
	registerCommands(rootCmd)
	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() == "doctor" {
			return cmd
		}
	}
	t.Fatal("doctor command not found")
	return nil
}

// runDoctorCaptured runs the doctor command with jsonFlag forced on and
// returns its parsed JSON output, restoring package-level state afterward.
func runDoctorCaptured(t *testing.T) map[string]interface{} {
	t.Helper()

	origJSON := jsonFlag
	jsonFlag = true
	t.Cleanup(func() { jsonFlag = origJSON })

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStdout := os.Stdout
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	doctorCmd := findDoctorCmd(t)
	runErr := doctorCmd.RunE(doctorCmd, nil)

	w.Close()
	os.Stdout = origStdout
	out, _ := io.ReadAll(r)
	if runErr != nil {
		t.Fatalf("doctor command returned an error: %v", runErr)
	}

	var results map[string]interface{}
	if err := json.Unmarshal(out, &results); err != nil {
		t.Fatalf("failed to parse doctor JSON output: %v\noutput: %s", err, out)
	}
	return results
}

func TestDoctorReportsAnthropicProviderAndSecretSource(t *testing.T) {
	origCfg := cfg
	cfg = &config.Config{Vault: t.TempDir(), LLMProvider: "anthropic", LLMAPIKey: "test-key"}
	t.Cleanup(func() { cfg = origCfg })
	t.Setenv("PATH", "/usr/bin:/bin")

	results := runDoctorCaptured(t)

	aiInfo, ok := results["ai"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected an 'ai' object in doctor output, got: %#v", results["ai"])
	}
	if aiInfo["provider"] != "anthropic" {
		t.Errorf("expected provider 'anthropic', got %v", aiInfo["provider"])
	}
	if aiInfo["secret_source"] != "config/env" {
		t.Errorf("expected secret_source 'config/env' for a plain configured key, got %v", aiInfo["secret_source"])
	}
	if aiInfo["model"] != config.DefaultAnthropicModel {
		t.Errorf("expected the default effective model %q when none is configured, got %v", config.DefaultAnthropicModel, aiInfo["model"])
	}
}

func TestDoctorReportsConfiguredAnthropicModel(t *testing.T) {
	origCfg := cfg
	cfg = &config.Config{Vault: t.TempDir(), LLMProvider: "anthropic", LLMAPIKey: "test-key", LLMModel: "claude-opus-4-8"}
	t.Cleanup(func() { cfg = origCfg })
	t.Setenv("PATH", "/usr/bin:/bin")

	results := runDoctorCaptured(t)

	aiInfo, ok := results["ai"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected an 'ai' object in doctor output, got: %#v", results["ai"])
	}
	if aiInfo["model"] != "claude-opus-4-8" {
		t.Errorf("expected the configured model 'claude-opus-4-8', got %v", aiInfo["model"])
	}
}

func TestDoctorReportsOllamaProviderWithoutSecretSource(t *testing.T) {
	origCfg := cfg
	cfg = &config.Config{Vault: t.TempDir(), LLMProvider: "", LLMAPIKey: ""}
	t.Cleanup(func() { cfg = origCfg })
	t.Setenv("PATH", "/usr/bin:/bin")

	results := runDoctorCaptured(t)

	aiInfo, ok := results["ai"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected an 'ai' object in doctor output, got: %#v", results["ai"])
	}
	if aiInfo["provider"] != "ollama" {
		t.Errorf("expected provider to default to 'ollama', got %v", aiInfo["provider"])
	}
	if _, hasSecretSource := aiInfo["secret_source"]; hasSecretSource {
		t.Errorf("did not expect secret_source to be reported for the ollama provider, got %v", aiInfo["secret_source"])
	}
	if _, hasModel := aiInfo["model"]; hasModel {
		t.Errorf("did not expect model to be reported for the ollama provider, got %v", aiInfo["model"])
	}
}

// runDoctorTextCaptured runs the doctor command in its human-readable
// (non-JSON) output mode and returns the raw printed text.
func runDoctorTextCaptured(t *testing.T) string {
	t.Helper()

	origJSON := jsonFlag
	jsonFlag = false
	t.Cleanup(func() { jsonFlag = origJSON })

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStdout := os.Stdout
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	doctorCmd := findDoctorCmd(t)
	runErr := doctorCmd.RunE(doctorCmd, nil)

	w.Close()
	os.Stdout = origStdout
	out, _ := io.ReadAll(r)
	if runErr != nil {
		t.Fatalf("doctor command returned an error: %v", runErr)
	}
	return string(out)
}

func TestDoctorTextOutputReportsAnthropicProviderAndSecretSource(t *testing.T) {
	origCfg := cfg
	cfg = &config.Config{Vault: t.TempDir(), LLMProvider: "anthropic", LLMAPIKey: "test-key"}
	t.Cleanup(func() { cfg = origCfg })
	t.Setenv("PATH", "/usr/bin:/bin")

	out := runDoctorTextCaptured(t)

	wantLine := "ai: provider=anthropic, secret_source=config/env, model=" + config.DefaultAnthropicModel
	if !strings.Contains(out, wantLine) {
		t.Errorf("expected AI provider/secret_source/model line %q in text output, got:\n%s", wantLine, out)
	}
	if !strings.Contains(out, "conflicts: none") {
		t.Errorf("expected a conflicts summary line, got:\n%s", out)
	}
	if !strings.Contains(out, "tools:") {
		t.Errorf("expected a tools summary section, got:\n%s", out)
	}
}

func TestDoctorTextOutputReportsOllamaProviderWithoutSecretSource(t *testing.T) {
	origCfg := cfg
	cfg = &config.Config{Vault: t.TempDir(), LLMProvider: "", LLMAPIKey: ""}
	t.Cleanup(func() { cfg = origCfg })
	t.Setenv("PATH", "/usr/bin:/bin")

	out := runDoctorTextCaptured(t)

	if !strings.Contains(out, "ai: provider=ollama\n") {
		t.Errorf("expected AI provider line without secret_source, got:\n%s", out)
	}
	if strings.Contains(out, "secret_source") {
		t.Errorf("did not expect secret_source in text output for the ollama provider, got:\n%s", out)
	}
}
