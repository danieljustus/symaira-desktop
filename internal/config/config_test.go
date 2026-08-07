package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg == nil {
		t.Fatal("DefaultConfig returned nil")
	}
	if cfg.Vault != "" {
		t.Errorf("expected empty vault, got %q", cfg.Vault)
	}
	if cfg.ReviewThreshold != 85 {
		t.Errorf("expected ReviewThreshold 85, got %d", cfg.ReviewThreshold)
	}
	if cfg.Language != "" {
		t.Errorf("expected empty default language, got %q", cfg.Language)
	}
	if cfg.MaxTokens != 8192 {
		t.Errorf("expected default MaxTokens 8192, got %d", cfg.MaxTokens)
	}
}

func TestGlobalPathReturnsNonEmpty(t *testing.T) {
	p := GlobalPath()
	if p == "" {
		t.Error("GlobalPath() returned empty string")
	}
}

func TestLoadFromPathNonExistent(t *testing.T) {
	cfg, err := LoadFromPath(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatalf("expected no error for missing file, got %v", err)
	}
	if cfg.ReviewThreshold != 85 {
		t.Errorf("expected default threshold 85, got %d", cfg.ReviewThreshold)
	}
}

func TestLoadFromPathValidTOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := `vault = "/my/vault"
review_threshold = 70
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFromPath(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Vault != "/my/vault" {
		t.Errorf("expected vault '/my/vault', got %q", cfg.Vault)
	}
	if cfg.ReviewThreshold != 70 {
		t.Errorf("expected threshold 70, got %d", cfg.ReviewThreshold)
	}
}

func TestLoadFromPathInvalidTOML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.toml")
	if err := os.WriteFile(path, []byte("{{{{not valid"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadFromPath(path)
	if err == nil {
		t.Error("expected error for invalid TOML")
	}
}

func TestLoadFromPathEnvOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := `vault = "/file/vault"
review_threshold = 50
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("SYMDESK_VAULT", "/env/vault")
	t.Setenv("SYMDESK_REVIEW_THRESHOLD", "90")

	cfg, err := LoadFromPath(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Vault != "/env/vault" {
		t.Errorf("expected env vault '/env/vault', got %q", cfg.Vault)
	}
	if cfg.ReviewThreshold != 90 {
		t.Errorf("expected env threshold 90, got %d", cfg.ReviewThreshold)
	}
}

func TestLoadFromPathEnvOverrideLLMSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := `llm_provider = "ollama"
llm_api_key = "file-key"
llm_model = "file-model"
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("SYMDESK_LLM_PROVIDER", "anthropic")
	t.Setenv("SYMDESK_LLM_API_KEY", "env-key")
	t.Setenv("SYMDESK_LLM_MODEL", "env-model")

	cfg, err := LoadFromPath(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.LLMProvider != "anthropic" {
		t.Errorf("expected env provider 'anthropic' to override the file value, got %q", cfg.LLMProvider)
	}
	if cfg.LLMAPIKey != "env-key" {
		t.Errorf("expected env API key 'env-key' to override the file value, got %q", cfg.LLMAPIKey)
	}
	if cfg.LLMModel != "env-model" {
		t.Errorf("expected env model 'env-model' to override the file value, got %q", cfg.LLMModel)
	}
}

func TestLoadFromPathEnvOverrideLLMSettingsWithoutFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist.toml")

	t.Setenv("SYMDESK_LLM_PROVIDER", "anthropic")
	t.Setenv("SYMDESK_LLM_API_KEY", "env-key")
	t.Setenv("SYMDESK_LLM_MODEL", "env-model")

	cfg, err := LoadFromPath(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.LLMProvider != "anthropic" || cfg.LLMAPIKey != "env-key" || cfg.LLMModel != "env-model" {
		t.Errorf("expected env LLM settings to apply even without a config file, got provider=%q key=%q model=%q", cfg.LLMProvider, cfg.LLMAPIKey, cfg.LLMModel)
	}
}

func TestLoadFromPathLLMModelFromTOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := `llm_model = "claude-opus-4-8"
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFromPath(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.LLMModel != "claude-opus-4-8" {
		t.Errorf("expected llm_model from file 'claude-opus-4-8', got %q", cfg.LLMModel)
	}
}

func TestDefaultConfigLLMModel(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.LLMModel != DefaultAnthropicModel {
		t.Errorf("expected default LLMModel %q, got %q", DefaultAnthropicModel, cfg.LLMModel)
	}
}

func TestLoadFromPathEnvOverrideInvalidThreshold(t *testing.T) {
	t.Setenv("SYMDESK_REVIEW_THRESHOLD", "not-a-number")

	cfg, err := LoadFromPath(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// invalid env value should be ignored, keeping default
	if cfg.ReviewThreshold != 85 {
		t.Errorf("expected default threshold 85 with bad env, got %d", cfg.ReviewThreshold)
	}
}

func TestLoadFromPathEnvThresholdOutOfRange(t *testing.T) {
	t.Setenv("SYMDESK_REVIEW_THRESHOLD", "150")

	cfg, err := LoadFromPath(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ReviewThreshold != 85 {
		t.Errorf("expected default threshold 85 with out-of-range env, got %d", cfg.ReviewThreshold)
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "config.toml")

	original := &Config{
		Vault:           "/saved/vault",
		ReviewThreshold: 60,
	}

	if err := Save(path, original); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("config file was not created")
	}

	loaded, err := LoadFromPath(path)
	if err != nil {
		t.Fatalf("LoadFromPath failed: %v", err)
	}
	if loaded.Vault != original.Vault {
		t.Errorf("expected vault %q, got %q", original.Vault, loaded.Vault)
	}
	if loaded.ReviewThreshold != original.ReviewThreshold {
		t.Errorf("expected threshold %d, got %d", original.ReviewThreshold, loaded.ReviewThreshold)
	}
}

func TestLoadWrapsLoadFromPath(t *testing.T) {
	// Load() calls LoadFromPath(GlobalPath()). Without a real config file
	// it should return defaults without error.
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg == nil {
		t.Fatal("Load() returned nil config")
	}
}

func TestLoadFromPathEnvOverrideLanguageAndMaxTokens(t *testing.T) {
	t.Setenv("SYMDESK_LANG", "French")
	t.Setenv("SYMDESK_MAX_TOKENS", "12000")

	cfg, err := LoadFromPath(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Language != "French" {
		t.Errorf("expected language override French, got %q", cfg.Language)
	}
	if cfg.MaxTokens != 12000 {
		t.Errorf("expected max tokens override 12000, got %d", cfg.MaxTokens)
	}
}

func TestSaveBadDirPermission(t *testing.T) {
	// Save to a path where the parent cannot be created (read-only root)
	badPath := "/nonexistent_parent_dir_12345/config.toml"
	err := Save(badPath, DefaultConfig())
	if err == nil {
		t.Error("expected error for unwritable path")
	}
}

// --- Validation tests ---

func TestValidateDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	findings := cfg.Validate()
	for _, f := range findings {
		t.Errorf("unexpected finding in default config: [%s] %s", f.Field, f.Message)
	}
}

func TestValidateReviewThresholdOutOfRange(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ReviewThreshold = -1
	findings := cfg.Validate()
	if !hasFinding(findings, "review_threshold") {
		t.Error("expected finding for negative review_threshold")
	}

	cfg.ReviewThreshold = 101
	findings = cfg.Validate()
	if !hasFinding(findings, "review_threshold") {
		t.Error("expected finding for review_threshold > 100")
	}
}

func TestValidateMaxTokensZero(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxTokens = 0
	findings := cfg.Validate()
	if !hasFinding(findings, "max_tokens") {
		t.Error("expected fatal finding for max_tokens = 0")
	}
	if !hasFatal(findings) {
		t.Error("expected at least one fatal finding for max_tokens = 0")
	}
}

func TestValidateHistoryMaxAgeDaysNegative(t *testing.T) {
	cfg := DefaultConfig()
	cfg.HistoryMaxAgeDays = -1
	findings := cfg.Validate()
	if !hasFinding(findings, "history_max_age_days") {
		t.Error("expected finding for negative history_max_age_days")
	}
}

func TestValidateHistoryCheckpointMaxAgeDaysNegative(t *testing.T) {
	cfg := DefaultConfig()
	cfg.HistoryCheckpointMaxAgeDays = -1
	findings := cfg.Validate()
	if !hasFinding(findings, "history_checkpoint_max_age_days") {
		t.Error("expected finding for negative history_checkpoint_max_age_days")
	}
}

func TestValidateNonExistentVaultPath(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Vault = "/nonexistent/vault/path"
	findings := cfg.Validate()
	if !hasFinding(findings, "vault") {
		t.Error("expected fatal finding for non-existent vault path")
	}
	if !hasFatal(findings) {
		t.Error("expected at least one fatal finding for missing vault")
	}
}

func TestValidateUnsupportedProvider(t *testing.T) {
	cfg := DefaultConfig()
	cfg.LLMProvider = "cohere"
	findings := cfg.Validate()
	if !hasFinding(findings, "llm_provider") {
		t.Error("expected finding for unsupported llm_provider")
	}
}

func TestValidateUnsupportedLanguage(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Language = "fr"
	findings := cfg.Validate()
	if !hasFinding(findings, "language") {
		t.Error("expected finding for unsupported language")
	}
}

func TestValidateFatalOnly(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxTokens = 0   // fatal
	cfg.Language = "fr" // warning only
	fatal := cfg.ValidateFatal()
	if len(fatal) != 1 {
		t.Errorf("expected 1 fatal finding, got %d", len(fatal))
	}
	if fatal[0].Field != "max_tokens" {
		t.Errorf("expected fatal on max_tokens, got %s", fatal[0].Field)
	}
}

func hasFinding(findings []Finding, field string) bool {
	for _, f := range findings {
		if f.Field == field {
			return true
		}
	}
	return false
}

func hasFatal(findings []Finding) bool {
	for _, f := range findings {
		if f.Severity == SeverityFatal {
			return true
		}
	}
	return false
}
