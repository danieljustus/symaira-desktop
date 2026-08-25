package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDefaultConfig_StableValues(t *testing.T) {
	got := DefaultConfig()
	if got.OllamaURL == "" {
		t.Error("default OllamaURL must be non-empty")
	}
	if got.Model == "" {
		t.Error("default Model must be non-empty")
	}
	if got.TimeoutSeconds <= 0 {
		t.Error("default TimeoutSeconds must be positive")
	}
	if got.RetryCount < 0 {
		t.Error("default RetryCount must be non-negative")
	}
	if got.RetryBackoffMS <= 0 {
		t.Error("default RetryBackoffMS must be positive")
	}
	got2 := DefaultConfig()
	if *got != *got2 {
		t.Error("DefaultConfig must be deterministic")
	}
}

func TestOllamaConfig(t *testing.T) {
	c := Config{
		OllamaURL:      "http://x.test/api",
		Model:          "m",
		TimeoutSeconds: 30,
		RetryCount:     4,
		RetryBackoffMS: 250,
	}
	oc := c.OllamaConfig()
	if oc.URL != c.OllamaURL {
		t.Errorf("URL: got %q, want %q", oc.URL, c.OllamaURL)
	}
	if oc.Model != c.Model {
		t.Errorf("Model: got %q, want %q", oc.Model, c.Model)
	}
	if oc.Timeout.Seconds() != 30 {
		t.Errorf("Timeout: got %v, want 30s", oc.Timeout)
	}
	if oc.RetryCount != 4 {
		t.Errorf("RetryCount: got %d, want 4", oc.RetryCount)
	}
	if oc.RetryBackoff.Milliseconds() != 250 {
		t.Errorf("RetryBackoff: got %v, want 250ms", oc.RetryBackoff)
	}
}

func TestSetValue_AcceptsTimeoutSeconds(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	cfg := DefaultConfig()

	if err := SetValue(cfgFile, "timeout_seconds", "45", cfg); err != nil {
		t.Fatalf("SetValue(timeout_seconds): %v", err)
	}
	if cfg.TimeoutSeconds != 45 {
		t.Errorf("expected TimeoutSeconds=45, got %d", cfg.TimeoutSeconds)
	}
}

func TestSetValue_AcceptsRetryCount(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	cfg := DefaultConfig()

	if err := SetValue(cfgFile, "retry_count", "5", cfg); err != nil {
		t.Fatalf("SetValue(retry_count): %v", err)
	}
	if cfg.RetryCount != 5 {
		t.Errorf("expected RetryCount=5, got %d", cfg.RetryCount)
	}
}

func TestSetValue_RejectsInvalidTimeout(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	cfg := DefaultConfig()

	if err := SetValue(cfgFile, "timeout_seconds", "zero", cfg); err == nil {
		t.Error("expected error for non-numeric timeout")
	}
}

func TestSetValue_RejectsUnknownKey(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	cfg := DefaultConfig()

	if err := SetValue(cfgFile, "not-a-key", "x", cfg); err == nil {
		t.Error("expected error for unknown key")
	}
}

func TestSetValue_RejectsEmptyModel(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	cfg := DefaultConfig()

	err := SetValue(cfgFile, "model", "", cfg)
	if err == nil {
		t.Error("expected error for empty model value")
	} else if err.Error() != `--set-value is required for key "model"` {
		t.Errorf("expected actionable error message, got %q", err.Error())
	}
}

func TestSetValue_RejectsEmptyOllamaURL(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	cfg := DefaultConfig()

	err := SetValue(cfgFile, "ollama_url", "", cfg)
	if err == nil {
		t.Error("expected error for empty ollama_url value")
	} else if err.Error() != `--set-value is required for key "ollama_url"` {
		t.Errorf("expected actionable error message, got %q", err.Error())
	}
}

func TestSetValue_AcceptsValidModel(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	cfg := DefaultConfig()

	if err := SetValue(cfgFile, "model", "mxbai-embed-large", cfg); err != nil {
		t.Fatalf("SetValue(model): %v", err)
	}
	if cfg.Model != "mxbai-embed-large" {
		t.Errorf("expected Model=mxbai-embed-large, got %q", cfg.Model)
	}
}

func TestSetValue_PersistsToFile(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	cfg := DefaultConfig()

	if err := SetValue(cfgFile, "model", "test-persist", cfg); err != nil {
		t.Fatalf("SetValue: %v", err)
	}

	data, err := os.ReadFile(cfgFile)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !filepath.IsAbs(cfgFile) {
		t.Fatal("expected absolute config file path")
	}
	if len(data) == 0 {
		t.Error("expected non-empty config file after SetValue")
	}
}

func TestSave_CreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "subdir", "config.toml")
	cfg := DefaultConfig()

	if err := Save(cfgFile, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, err := os.Stat(cfgFile); os.IsNotExist(err) {
		t.Error("expected config file to exist after Save")
	}
}

func TestMigrateJSONToTOML_MigratesWhenTOMLMissing(t *testing.T) {
	dir := t.TempDir()

	configDir := filepath.Join(dir, ".config", "symseek")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	jsonPath := filepath.Join(configDir, "config.json")
	jsonData := `{"ollama_url":"http://migrated.test/api","model":"migrated-model","timeout_seconds":60,"retry_count":3,"retry_backoff_ms":100}`
	if err := os.WriteFile(jsonPath, []byte(jsonData), 0600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HOME", dir)

	MigrateJSONToTOML()

	tomlPath := filepath.Join(configDir, "config.toml")
	data, err := os.ReadFile(tomlPath)
	if err != nil {
		t.Fatalf("expected TOML file to exist: %v", err)
	}
	if len(data) == 0 {
		t.Error("expected non-empty TOML file")
	}
}

func TestMigrateJSONToTOML_SkipsWhenTOMLExists(t *testing.T) {
	dir := t.TempDir()

	configDir := filepath.Join(dir, ".config", "symseek")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}

	tomlPath := filepath.Join(configDir, "config.toml")
	existingTOML := `ollama_url = "http://existing.test/api"
model = "existing-model"
`
	if err := os.WriteFile(tomlPath, []byte(existingTOML), 0600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HOME", dir)

	MigrateJSONToTOML()

	data, err := os.ReadFile(tomlPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != existingTOML {
		t.Errorf("existing TOML was modified: %q", string(data))
	}
}

func TestMigrateJSONToTOML_SkipsWhenNeitherExists(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	MigrateJSONToTOML()

	tomlPath := filepath.Join(dir, ".config", "symseek", "config.toml")
	if _, err := os.Stat(tomlPath); !os.IsNotExist(err) {
		t.Error("expected no TOML file when neither config exists")
	}
}

func TestSetValue_AcceptsEmbeddingDim(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	cfg := DefaultConfig()

	if err := SetValue(cfgFile, "embedding_dim", "512", cfg); err != nil {
		t.Fatalf("SetValue(embedding_dim): %v", err)
	}
	if cfg.EmbeddingDim != 512 {
		t.Errorf("expected EmbeddingDim=512, got %d", cfg.EmbeddingDim)
	}
}

func TestSetValue_AcceptsEmbeddingDimZero(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	cfg := DefaultConfig()

	if err := SetValue(cfgFile, "embedding_dim", "0", cfg); err != nil {
		t.Fatalf("SetValue(embedding_dim=0): %v", err)
	}
	if cfg.EmbeddingDim != 0 {
		t.Errorf("expected EmbeddingDim=0 (auto-detect), got %d", cfg.EmbeddingDim)
	}
}

func TestSetValue_RejectsInvalidEmbeddingDim(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	cfg := DefaultConfig()

	if err := SetValue(cfgFile, "embedding_dim", "abc", cfg); err == nil {
		t.Error("expected error for non-numeric embedding_dim")
	}
}

func TestOllamaConfig_PassesEmbeddingDim(t *testing.T) {
	c := Config{
		OllamaURL:    "http://x.test/api",
		Model:        "m",
		EmbeddingDim: 512,
	}
	oc := c.OllamaConfig()
	if oc.Dim != 512 {
		t.Errorf("expected Dim=512 in OllamaConfig, got %d", oc.Dim)
	}
}

func TestDefaultConfig_QuantizationDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.VectorQuantization != "off" {
		t.Errorf("expected default VectorQuantization=off, got %q", cfg.VectorQuantization)
	}
	if cfg.VectorQuantBits != 4 {
		t.Errorf("expected default VectorQuantBits=4, got %d", cfg.VectorQuantBits)
	}
	if cfg.VectorQuantizedShortlist != 200 {
		t.Errorf("expected default VectorQuantizedShortlist=200, got %d", cfg.VectorQuantizedShortlist)
	}
	if !cfg.VectorExactRerank {
		t.Error("expected default VectorExactRerank=true")
	}
}

func TestQuantDBConfig_OffReturnsNil(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.QuantDBConfig() != nil {
		t.Error("expected nil QuantDBConfig when vector_quantization=off")
	}
}

func TestQuantDBConfig_TurboProdReturnsConfig(t *testing.T) {
	cfg := DefaultConfig()
	cfg.VectorQuantization = "turbo-prod"
	cfg.VectorQuantBits = 3
	cfg.VectorQuantizedShortlist = 100
	cfg.VectorExactRerank = false

	qc := cfg.QuantDBConfig()
	if qc == nil {
		t.Fatal("expected non-nil QuantDBConfig for turbo-prod")
	}
	if !qc.Enabled {
		t.Error("expected Enabled=true")
	}
	if qc.BitWidth != 3 {
		t.Errorf("expected BitWidth=3, got %d", qc.BitWidth)
	}
	if qc.Shortlist != 100 {
		t.Errorf("expected Shortlist=100, got %d", qc.Shortlist)
	}
	if qc.ExactRerank {
		t.Error("expected ExactRerank=false")
	}
}

func TestSetValue_AcceptsVectorQuantization(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	cfg := DefaultConfig()

	if err := SetValue(cfgFile, "vector_quantization", "turbo-prod", cfg); err != nil {
		t.Fatalf("SetValue(vector_quantization): %v", err)
	}
	if cfg.VectorQuantization != "turbo-prod" {
		t.Errorf("expected VectorQuantization=turbo-prod, got %q", cfg.VectorQuantization)
	}
}

func TestSetValue_RejectsInvalidVectorQuantization(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	cfg := DefaultConfig()

	if err := SetValue(cfgFile, "vector_quantization", "invalid", cfg); err == nil {
		t.Error("expected error for invalid vector_quantization")
	}
}

func TestSetValue_AcceptsVectorQuantBits(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	cfg := DefaultConfig()

	if err := SetValue(cfgFile, "vector_quant_bits", "3", cfg); err != nil {
		t.Fatalf("SetValue(vector_quant_bits): %v", err)
	}
	if cfg.VectorQuantBits != 3 {
		t.Errorf("expected VectorQuantBits=3, got %d", cfg.VectorQuantBits)
	}
}

func TestSetValue_RejectsInvalidVectorQuantBits(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	cfg := DefaultConfig()

	if err := SetValue(cfgFile, "vector_quant_bits", "5", cfg); err == nil {
		t.Error("expected error for vector_quant_bits=5")
	}
}

func TestSetValue_AcceptsVectorQuantizedShortlist(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	cfg := DefaultConfig()

	if err := SetValue(cfgFile, "vector_quantized_shortlist", "500", cfg); err != nil {
		t.Fatalf("SetValue(vector_quantized_shortlist): %v", err)
	}
	if cfg.VectorQuantizedShortlist != 500 {
		t.Errorf("expected VectorQuantizedShortlist=500, got %d", cfg.VectorQuantizedShortlist)
	}
}

func TestSetValue_AcceptsVectorExactRerank(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	cfg := DefaultConfig()

	if err := SetValue(cfgFile, "vector_exact_rerank", "false", cfg); err != nil {
		t.Fatalf("SetValue(vector_exact_rerank): %v", err)
	}
	if cfg.VectorExactRerank {
		t.Error("expected VectorExactRerank=false")
	}
}

func TestSetValue_RejectsInvalidVectorExactRerank(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	cfg := DefaultConfig()

	if err := SetValue(cfgFile, "vector_exact_rerank", "maybe", cfg); err == nil {
		t.Error("expected error for invalid vector_exact_rerank")
	}
}

func TestDefaultConfig_RerankDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.RerankQuery {
		t.Error("expected default RerankQuery=false")
	}
	if cfg.RerankModel != "" {
		t.Errorf("expected default RerankModel empty, got %q", cfg.RerankModel)
	}
	if cfg.RerankTimeoutSeconds != 120 {
		t.Errorf("expected default RerankTimeoutSeconds=120, got %d", cfg.RerankTimeoutSeconds)
	}
}

func TestSetValue_AcceptsRerankQuery(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	cfg := DefaultConfig()

	if err := SetValue(cfgFile, "rerank_query", "true", cfg); err != nil {
		t.Fatalf("SetValue(rerank_query): %v", err)
	}
	if !cfg.RerankQuery {
		t.Error("expected RerankQuery=true")
	}
}

func TestSetValue_RejectsInvalidRerankQuery(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	cfg := DefaultConfig()

	if err := SetValue(cfgFile, "rerank_query", "maybe", cfg); err == nil {
		t.Error("expected error for invalid rerank_query")
	}
}

func TestSetValue_AcceptsRerankModel(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	cfg := DefaultConfig()

	if err := SetValue(cfgFile, "rerank_model", "qwen2.5", cfg); err != nil {
		t.Fatalf("SetValue(rerank_model): %v", err)
	}
	if cfg.RerankModel != "qwen2.5" {
		t.Errorf("expected RerankModel=qwen2.5, got %q", cfg.RerankModel)
	}
}

func TestSetValue_AcceptsEmptyRerankModel(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	cfg := DefaultConfig()

	if err := SetValue(cfgFile, "rerank_model", "", cfg); err != nil {
		t.Fatalf("SetValue(rerank_model empty): %v", err)
	}
	if cfg.RerankModel != "" {
		t.Errorf("expected RerankModel empty, got %q", cfg.RerankModel)
	}
}

func TestSetValue_AcceptsRerankTimeoutSeconds(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	cfg := DefaultConfig()

	if err := SetValue(cfgFile, "rerank_timeout_seconds", "60", cfg); err != nil {
		t.Fatalf("SetValue(rerank_timeout_seconds): %v", err)
	}
	if cfg.RerankTimeoutSeconds != 60 {
		t.Errorf("expected RerankTimeoutSeconds=60, got %d", cfg.RerankTimeoutSeconds)
	}
}

func TestSetValue_RejectsInvalidRerankTimeoutSeconds(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	cfg := DefaultConfig()

	if err := SetValue(cfgFile, "rerank_timeout_seconds", "zero", cfg); err == nil {
		t.Error("expected error for non-numeric rerank_timeout_seconds")
	}
}

func TestRerankConfig_UsesEmbeddingModelWhenEmpty(t *testing.T) {
	cfg := &Config{
		OllamaURL:            "http://x.test/api",
		Model:                "nomic-embed-text",
		RerankQuery:          true,
		RerankModel:          "",
		RerankTimeoutSeconds: 60,
	}
	rc := cfg.RerankConfig()
	if !rc.Enabled {
		t.Error("expected Enabled=true")
	}
	if rc.Model != "nomic-embed-text" {
		t.Errorf("expected Model to fall back to embedding model, got %q", rc.Model)
	}
	if rc.Timeout.Seconds() != 60 {
		t.Errorf("expected Timeout=60s, got %v", rc.Timeout)
	}
}

func TestRerankConfig_UsesExplicitModel(t *testing.T) {
	cfg := &Config{
		OllamaURL:            "http://x.test/api",
		Model:                "nomic-embed-text",
		RerankQuery:          true,
		RerankModel:          "qwen2.5",
		RerankTimeoutSeconds: 90,
	}
	rc := cfg.RerankConfig()
	if rc.Model != "qwen2.5" {
		t.Errorf("expected explicit model qwen2.5, got %q", rc.Model)
	}
	if rc.Timeout.Seconds() != 90 {
		t.Errorf("expected Timeout=90s, got %v", rc.Timeout)
	}
}

func TestSetValue_PersistsRerankConfig(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	cfg := DefaultConfig()

	if err := SetValue(cfgFile, "rerank_query", "true", cfg); err != nil {
		t.Fatalf("SetValue: %v", err)
	}

	data, err := os.ReadFile(cfgFile)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "rerank_query") {
		t.Error("expected rerank_query in config file")
	}
}

func TestDefaultConfig_ExpandDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.ExpandQuery {
		t.Error("expected default ExpandQuery=false")
	}
	if cfg.ExpandModel != "" {
		t.Errorf("expected default ExpandModel empty, got %q", cfg.ExpandModel)
	}
	if cfg.ExpandTimeoutSeconds != 120 {
		t.Errorf("expected default ExpandTimeoutSeconds=120, got %d", cfg.ExpandTimeoutSeconds)
	}
}

func TestSetValue_AcceptsExpandQuery(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	cfg := DefaultConfig()

	if err := SetValue(cfgFile, "expand_query", "true", cfg); err != nil {
		t.Fatalf("SetValue(expand_query): %v", err)
	}
	if !cfg.ExpandQuery {
		t.Error("expected ExpandQuery=true")
	}
}

func TestSetValue_RejectsInvalidExpandQuery(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	cfg := DefaultConfig()

	if err := SetValue(cfgFile, "expand_query", "maybe", cfg); err == nil {
		t.Error("expected error for invalid expand_query")
	}
}

func TestSetValue_AcceptsExpandModel(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	cfg := DefaultConfig()

	if err := SetValue(cfgFile, "expand_model", "qwen2.5", cfg); err != nil {
		t.Fatalf("SetValue(expand_model): %v", err)
	}
	if cfg.ExpandModel != "qwen2.5" {
		t.Errorf("expected ExpandModel=qwen2.5, got %q", cfg.ExpandModel)
	}
}

func TestSetValue_AcceptsEmptyExpandModel(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	cfg := DefaultConfig()

	if err := SetValue(cfgFile, "expand_model", "", cfg); err != nil {
		t.Fatalf("SetValue(expand_model empty): %v", err)
	}
	if cfg.ExpandModel != "" {
		t.Errorf("expected ExpandModel empty, got %q", cfg.ExpandModel)
	}
}

func TestSetValue_AcceptsExpandTimeoutSeconds(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	cfg := DefaultConfig()

	if err := SetValue(cfgFile, "expand_timeout_seconds", "60", cfg); err != nil {
		t.Fatalf("SetValue(expand_timeout_seconds): %v", err)
	}
	if cfg.ExpandTimeoutSeconds != 60 {
		t.Errorf("expected ExpandTimeoutSeconds=60, got %d", cfg.ExpandTimeoutSeconds)
	}
}

func TestSetValue_RejectsInvalidExpandTimeoutSeconds(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	cfg := DefaultConfig()

	if err := SetValue(cfgFile, "expand_timeout_seconds", "zero", cfg); err == nil {
		t.Error("expected error for non-numeric expand_timeout_seconds")
	}
}

func TestExpandConfig_UsesEmbeddingModelWhenEmpty(t *testing.T) {
	cfg := &Config{
		OllamaURL:            "http://x.test/api",
		Model:                "nomic-embed-text",
		ExpandQuery:          true,
		ExpandModel:          "",
		ExpandTimeoutSeconds: 60,
	}
	ec := cfg.ExpandConfig()
	if !ec.Enabled {
		t.Error("expected Enabled=true")
	}
	if ec.Model != "nomic-embed-text" {
		t.Errorf("expected Model to fall back to embedding model, got %q", ec.Model)
	}
	if ec.Timeout.Seconds() != 60 {
		t.Errorf("expected Timeout=60s, got %v", ec.Timeout)
	}
}

func TestExpandConfig_UsesExplicitModel(t *testing.T) {
	cfg := &Config{
		OllamaURL:            "http://x.test/api",
		Model:                "nomic-embed-text",
		ExpandQuery:          true,
		ExpandModel:          "qwen2.5",
		ExpandTimeoutSeconds: 90,
	}
	ec := cfg.ExpandConfig()
	if ec.Model != "qwen2.5" {
		t.Errorf("expected explicit model qwen2.5, got %q", ec.Model)
	}
	if ec.Timeout.Seconds() != 90 {
		t.Errorf("expected Timeout=90s, got %v", ec.Timeout)
	}
}

func TestSetValue_PersistsExpandConfig(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	cfg := DefaultConfig()

	if err := SetValue(cfgFile, "expand_query", "true", cfg); err != nil {
		t.Fatalf("SetValue: %v", err)
	}

	data, err := os.ReadFile(cfgFile)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "expand_query") {
		t.Error("expected expand_query in config file")
	}
}

func TestLoadFromPath(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T) string
		wantErr bool
		check   func(t *testing.T, cfg *Config)
	}{
		{
			name: "valid TOML parses values",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				path := filepath.Join(dir, "config.toml")
				content := `ollama_url = "http://custom.test/api"
model = "custom-model"
timeout_seconds = 99
retry_count = 7
vector_backend = "sqlite"
`
				if err := os.WriteFile(path, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
				return path
			},
			check: func(t *testing.T, cfg *Config) {
				if cfg.OllamaURL != "http://custom.test/api" {
					t.Errorf("OllamaURL = %q, want %q", cfg.OllamaURL, "http://custom.test/api")
				}
				if cfg.Model != "custom-model" {
					t.Errorf("Model = %q, want %q", cfg.Model, "custom-model")
				}
				if cfg.TimeoutSeconds != 99 {
					t.Errorf("TimeoutSeconds = %d, want 99", cfg.TimeoutSeconds)
				}
				if cfg.RetryCount != 7 {
					t.Errorf("RetryCount = %d, want 7", cfg.RetryCount)
				}
			},
		},
		{
			name: "valid JSON migrated to TOML",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				configDir := filepath.Join(dir, "subdir")
				if err := os.MkdirAll(configDir, 0700); err != nil {
					t.Fatal(err)
				}
				jsonPath := filepath.Join(configDir, "config.json")
				jsonData := `{"ollama_url":"http://json.test/api","model":"json-model","timeout_seconds":42,"retry_count":3}`
				if err := os.WriteFile(jsonPath, []byte(jsonData), 0600); err != nil {
					t.Fatal(err)
				}
				return filepath.Join(configDir, "config.toml")
			},
			check: func(t *testing.T, cfg *Config) {
				if cfg.OllamaURL != "http://json.test/api" {
					t.Errorf("OllamaURL = %q, want %q", cfg.OllamaURL, "http://json.test/api")
				}
				if cfg.Model != "json-model" {
					t.Errorf("Model = %q, want %q", cfg.Model, "json-model")
				}
				if cfg.TimeoutSeconds != 42 {
					t.Errorf("TimeoutSeconds = %d, want 42", cfg.TimeoutSeconds)
				}
			},
		},
		{
			name: "missing file returns default config",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				return filepath.Join(dir, "nonexistent", "config.toml")
			},
			check: func(t *testing.T, cfg *Config) {
				def := DefaultConfig()
				if *cfg != *def {
					t.Error("expected default config when file does not exist")
				}
			},
		},
		{
			name: "malformed TOML returns error",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				path := filepath.Join(dir, "config.toml")
				if err := os.WriteFile(path, []byte("this is not valid {{{ toml content"), 0600); err != nil {
					t.Fatal(err)
				}
				return path
			},
			wantErr: true,
		},
		{
			name: "path is a directory returns read error",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				configDir := filepath.Join(dir, "config.toml")
				if err := os.MkdirAll(configDir, 0700); err != nil {
					t.Fatal(err)
				}
				return configDir
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tt.setup(t)
			cfg, err := LoadFromPath(path)
			if (err != nil) != tt.wantErr {
				t.Fatalf("LoadFromPath() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr {
				if cfg == nil {
					t.Fatal("expected non-nil config")
				}
				if tt.check != nil {
					tt.check(t, cfg)
				}
			}
		})
	}
}

func TestLoad_UsesGlobalPath(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".config", "symseek")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	tomlPath := filepath.Join(configDir, "config.toml")
	content := `ollama_url = "http://load.test/api"
model = "load-model"
`
	if err := os.WriteFile(tomlPath, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HOME", dir)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.OllamaURL != "http://load.test/api" {
		t.Errorf("OllamaURL = %q, want %q", cfg.OllamaURL, "http://load.test/api")
	}
	if cfg.Model != "load-model" {
		t.Errorf("Model = %q, want %q", cfg.Model, "load-model")
	}
}

func TestReload_UsesGlobalPath(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".config", "symseek")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	tomlPath := filepath.Join(configDir, "config.toml")
	content := `ollama_url = "http://reload.test/api"
model = "reload-model"
`
	if err := os.WriteFile(tomlPath, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HOME", dir)

	cfg, err := Reload()
	if err != nil {
		t.Fatalf("Reload() error: %v", err)
	}
	if cfg.OllamaURL != "http://reload.test/api" {
		t.Errorf("OllamaURL = %q, want %q", cfg.OllamaURL, "http://reload.test/api")
	}
	if cfg.Model != "reload-model" {
		t.Errorf("Model = %q, want %q", cfg.Model, "reload-model")
	}
}

func TestSave_MkdirAllFails_ParentIsFile(t *testing.T) {
	dir := t.TempDir()
	parentFile := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(parentFile, []byte("I am a file"), 0600); err != nil {
		t.Fatal(err)
	}

	cfgFile := filepath.Join(parentFile, "config.toml")
	cfg := DefaultConfig()

	err := Save(cfgFile, cfg)
	if err == nil {
		t.Fatal("expected error when parent is a file, not a directory")
	}
	if !strings.Contains(err.Error(), "failed to create config directory") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestSave_OpenFileFails_PathIsDirectory(t *testing.T) {
	dir := t.TempDir()
	targetDir := filepath.Join(dir, "config.toml")
	if err := os.MkdirAll(targetDir, 0700); err != nil {
		t.Fatal(err)
	}

	cfg := DefaultConfig()

	err := Save(targetDir, cfg)
	if err == nil {
		t.Fatal("expected error when path is a directory")
	}
	if !strings.Contains(err.Error(), "failed to create config file") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestSave_EncodeError_BrokenPipe(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	r.Close()

	fdLink := filepath.Join("/dev", "fd", fmt.Sprintf("%d", w.Fd()))
	if err := os.Symlink(fdLink, cfgFile); err != nil {
		w.Close()
		t.Skipf("symlink not supported: %v", err)
	}

	cfg := DefaultConfig()
	saveErr := Save(cfgFile, cfg)
	w.Close()

	if saveErr == nil {
		t.Skip("encode succeeded on broken pipe (OS buffered the write)")
	}
	if !strings.Contains(saveErr.Error(), "failed to encode config") {
		t.Errorf("unexpected error: %v", saveErr)
	}
}

func TestSetValue_UnknownKeyListsRegistryKeysInOrder(t *testing.T) {
	cfg := DefaultConfig()
	cfgFile := filepath.Join(t.TempDir(), "config.toml")

	err := SetValue(cfgFile, "not-a-key", "x", cfg)
	if err == nil {
		t.Fatal("expected error for unknown key")
	}

	wantNames := []string{
		"ollama_url", "model", "embedding_dim", "timeout_seconds", "retry_count",
		"retry_backoff_ms", "index_cooldown_seconds", "vector_backend",
		"vector_quantization", "vector_quant_bits", "vector_quantized_shortlist",
		"vector_exact_rerank", "rerank_query", "rerank_model",
		"rerank_timeout_seconds", "expand_query", "expand_model",
		"expand_timeout_seconds",
	}
	want := fmt.Sprintf("unknown config key %q (supported: %s)", "not-a-key", strings.Join(wantNames, ", "))
	if err.Error() != want {
		t.Errorf("unknown-key error drifted from registry order/wording:\n got: %s\nwant: %s", err.Error(), want)
	}
}

func TestConfigKeys_RegistryEntriesComplete(t *testing.T) {
	seen := make(map[string]bool, len(configKeys))
	for _, k := range configKeys {
		if k.name == "" {
			t.Error("registry entry with empty name")
		}
		if seen[k.name] {
			t.Errorf("duplicate registry entry %q", k.name)
		}
		seen[k.name] = true
		if k.desc == "" {
			t.Errorf("registry entry %q has no description", k.name)
		}
		if k.set == nil {
			t.Errorf("registry entry %q has no set function", k.name)
		}
	}
}

func TestConfigKeys_CoversAllConfigFields(t *testing.T) {
	registryNames := make(map[string]bool, len(configKeys))
	for _, k := range configKeys {
		registryNames[k.name] = true
	}

	tomlTags := make(map[string]bool)
	cfgType := reflect.TypeOf(Config{})
	for i := 0; i < cfgType.NumField(); i++ {
		tag := cfgType.Field(i).Tag.Get("toml")
		if tag == "" || tag == "-" {
			continue
		}
		tomlTags[tag] = true
		if !registryNames[tag] {
			t.Errorf("Config field %s (toml %q) has no configKeys entry", cfgType.Field(i).Name, tag)
		}
	}
	for name := range registryNames {
		if !tomlTags[name] {
			t.Errorf("configKeys entry %q does not match any Config toml field", name)
		}
	}
}

// ---------------------------------------------------------------------------
// API token file tests (issue #318)
// ---------------------------------------------------------------------------

func TestAPITokenPath_UnderConfigDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	got := APITokenPath()
	want := filepath.Join(dir, ".config", "symseek", "api-token")
	if got != want {
		t.Errorf("APITokenPath() = %q, want %q", got, want)
	}
	if ConfigDir() != filepath.Dir(want) {
		t.Errorf("ConfigDir() = %q, want %q", ConfigDir(), filepath.Dir(want))
	}
}

func TestLoadOrCreateAPIToken_CreatesOnFirstStart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "api-token")

	token, created, err := LoadOrCreateAPIToken(path)
	if err != nil {
		t.Fatalf("LoadOrCreateAPIToken: %v", err)
	}
	if !created {
		t.Error("expected created=true on first start")
	}
	if len(token) != 64 {
		t.Errorf("expected 64-hex-char token, got %d chars", len(token))
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read token file: %v", err)
	}
	if strings.TrimSpace(string(data)) != token {
		t.Errorf("token file content = %q, want %q", string(data), token)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat token file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("token file permissions = %v, want 0600", perm)
	}
}

func TestLoadOrCreateAPIToken_ReusesExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "api-token")
	if err := os.WriteFile(path, []byte("existing-token\n"), 0600); err != nil {
		t.Fatal(err)
	}

	token, created, err := LoadOrCreateAPIToken(path)
	if err != nil {
		t.Fatalf("LoadOrCreateAPIToken: %v", err)
	}
	if created {
		t.Error("expected created=false when the file already exists")
	}
	if token != "existing-token" {
		t.Errorf("token = %q, want %q", token, "existing-token")
	}
}

func TestLoadOrCreateAPIToken_RejectsEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "api-token")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := LoadOrCreateAPIToken(path); err == nil {
		t.Fatal("expected error for empty token file")
	}
}

func TestLoadOrCreateAPIToken_RejectsMalformedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "api-token")
	if err := os.WriteFile(path, []byte("line-one\nline-two\n"), 0600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := LoadOrCreateAPIToken(path); err == nil {
		t.Fatal("expected error for multi-line token file")
	}
}

func TestLoadOrCreateAPIToken_RejectsUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; permission checks are bypassed")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "api-token")
	if err := os.WriteFile(path, []byte("secret\n"), 0000); err != nil {
		t.Fatal(err)
	}

	if _, _, err := LoadOrCreateAPIToken(path); err == nil {
		t.Fatal("expected error for unreadable token file")
	}
}
