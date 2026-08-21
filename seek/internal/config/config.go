// Package config provides configuration management for symseek using corekit's configkit.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/BurntSushi/toml"
	"github.com/danieljustus/symaira-corekit/configkit"
	"github.com/danieljustus/symaira-seek/internal/db"
	"github.com/danieljustus/symaira-seek/internal/engine"
)

// Config holds user configuration for symseek.
type Config struct {
	OllamaURL            string `json:"ollama_url" toml:"ollama_url"`
	Model                string `json:"model" toml:"model"`
	EmbeddingDim         int    `json:"embedding_dim" toml:"embedding_dim"`
	TimeoutSeconds       int    `json:"timeout_seconds" toml:"timeout_seconds"`
	RetryCount           int    `json:"retry_count" toml:"retry_count"`
	RetryBackoffMS       int    `json:"retry_backoff_ms" toml:"retry_backoff_ms"`
	IndexCooldownSeconds int    `json:"index_cooldown_seconds" toml:"index_cooldown_seconds"`
	VectorBackend        string `json:"vector_backend" toml:"vector_backend"`

	// Quantized vector search (opt-in, off by default).
	VectorQuantization       string `json:"vector_quantization" toml:"vector_quantization"`               // "off" | "turbo-prod"
	VectorQuantBits          int    `json:"vector_quant_bits" toml:"vector_quant_bits"`                   // 2, 3, or 4
	VectorQuantizedShortlist int    `json:"vector_quantized_shortlist" toml:"vector_quantized_shortlist"` // approximate shortlist size
	VectorExactRerank        bool   `json:"vector_exact_rerank" toml:"vector_exact_rerank"`               // exact cosine rerank on shortlist

	// LLM re-ranking (opt-in, off by default).
	RerankQuery          bool   `json:"rerank_query" toml:"rerank_query"`                     // enable Ollama re-ranking of search results
	RerankModel          string `json:"rerank_model" toml:"rerank_model"`                     // chat model for re-ranking; empty = reuse embedding model
	RerankTimeoutSeconds int    `json:"rerank_timeout_seconds" toml:"rerank_timeout_seconds"` // per-request timeout for reranking

	// HyDE query expansion (opt-in, off by default).
	ExpandQuery          bool   `json:"expand_query" toml:"expand_query"`                     // enable HyDE query expansion via Ollama chat
	ExpandModel          string `json:"expand_model" toml:"expand_model"`                     // chat model for expansion; empty = reuse embedding model
	ExpandTimeoutSeconds int    `json:"expand_timeout_seconds" toml:"expand_timeout_seconds"` // per-request timeout for expansion
}

// DefaultConfig returns the default configuration values.
func DefaultConfig() *Config {
	return &Config{
		OllamaURL:                "http://localhost:11434/api/embeddings",
		Model:                    "qwen3-embedding:0.6b",
		EmbeddingDim:             768, // Matryoshka pin: qwen3-embedding:0.6b is truncated to 768 dims (issue #302)
		TimeoutSeconds:           120,
		RetryCount:               2,
		RetryBackoffMS:           500,
		IndexCooldownSeconds:     5,
		VectorBackend:            "sqlite",
		VectorQuantization:       "off",
		VectorQuantBits:          4,
		VectorQuantizedShortlist: 200,
		VectorExactRerank:        true,
		RerankQuery:              false,
		RerankModel:              "",
		RerankTimeoutSeconds:     120,
		ExpandQuery:              false,
		ExpandModel:              "",
		ExpandTimeoutSeconds:     120,
	}
}

func Load() (*Config, error) {
	return LoadFromPath(GlobalPath())
}

func LoadFromPath(path string) (*Config, error) {
	MigrateJSONToTOMLAt(path)

	cfg := DefaultConfig()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to decode config file: %w", err)
	}
	return cfg, nil
}

func Reload() (*Config, error) {
	return Load()
}

func GlobalPath() string {
	return configkit.DefaultPath("symseek")
}

// ConfigDir returns the XDG config directory for symseek
// (e.g. ~/.config/symseek).
func ConfigDir() string {
	return filepath.Dir(GlobalPath())
}

// APITokenPath returns the path of the HTTP daemon's API token file
// (e.g. ~/.config/symseek/api-token).
func APITokenPath() string {
	return filepath.Join(ConfigDir(), "api-token")
}

// LoadOrCreateAPIToken returns the HTTP daemon API token stored at path. On
// first start (file missing) it generates a cryptographically random token
// and persists it to path with 0600 permissions. An existing but unreadable
// or malformed file is returned as an error so callers can fail the serve
// command instead of silently falling back to an unauthenticated daemon.
func LoadOrCreateAPIToken(path string) (token string, created bool, err error) {
	data, err := os.ReadFile(path)
	if err == nil {
		token = strings.TrimSpace(string(data))
		if token == "" || strings.ContainsFunc(token, unicode.IsSpace) {
			return "", false, fmt.Errorf("API token file %s is malformed (expected a single token on one line)", path)
		}
		return token, false, nil
	}
	if !os.IsNotExist(err) {
		return "", false, fmt.Errorf("failed to read API token file %s: %w", path, err)
	}

	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", false, fmt.Errorf("failed to generate API token: %w", err)
	}
	token = hex.EncodeToString(buf)

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", false, fmt.Errorf("failed to create config directory for API token: %w", err)
	}
	if err := os.WriteFile(path, []byte(token+"\n"), 0600); err != nil {
		return "", false, fmt.Errorf("failed to write API token file %s: %w", path, err)
	}
	return token, true, nil
}

// OllamaConfig converts a Config to the engine.OllamaConfig format.
func (c *Config) OllamaConfig() engine.OllamaConfig {
	return engine.OllamaConfig{
		URL:          c.OllamaURL,
		Model:        c.Model,
		Dim:          c.EmbeddingDim,
		Timeout:      time.Duration(c.TimeoutSeconds) * time.Second,
		RetryCount:   c.RetryCount,
		RetryBackoff: time.Duration(c.RetryBackoffMS) * time.Millisecond,
	}
}

// RerankConfig converts the config's rerank fields to an engine.RerankConfig.
func (c *Config) RerankConfig() engine.RerankConfig {
	model := c.RerankModel
	if model == "" {
		model = c.Model
	}
	timeout := time.Duration(c.RerankTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	return engine.RerankConfig{
		Enabled: c.RerankQuery,
		URL:     c.OllamaURL,
		Model:   model,
		Timeout: timeout,
	}
}

// ExpandConfig converts the config's expansion fields to an engine.ExpandConfig.
func (c *Config) ExpandConfig() engine.ExpandConfig {
	model := c.ExpandModel
	if model == "" {
		model = c.Model
	}
	timeout := time.Duration(c.ExpandTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	return engine.ExpandConfig{
		Enabled: c.ExpandQuery,
		URL:     c.OllamaURL,
		Model:   model,
		Timeout: timeout,
	}
}

// QuantDBConfig returns the QuantConfig for db.DB, or nil when quantization
// is disabled.
func (c *Config) QuantDBConfig() *db.QuantConfig {
	if c.VectorQuantization == "off" || c.VectorQuantization == "" {
		return nil
	}
	return &db.QuantConfig{
		Enabled:     true,
		BitWidth:    c.VectorQuantBits,
		Shortlist:   c.VectorQuantizedShortlist,
		ExactRerank: c.VectorExactRerank,
		Seed:        42,
	}
}

func MigrateJSONToTOMLAt(tomlPath string) {
	jsonPath := filepath.Join(filepath.Dir(tomlPath), "config.json")

	if _, err := os.Stat(tomlPath); err == nil {
		return
	}

	data, err := os.ReadFile(jsonPath)
	if err != nil {
		return
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return
	}

	dir := filepath.Dir(tomlPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return
	}

	f, err := os.OpenFile(tomlPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return
	}
	defer f.Close()

	_ = toml.NewEncoder(f).Encode(cfg)
}

func MigrateJSONToTOML() {
	MigrateJSONToTOMLAt(GlobalPath())
}

// Save writes the config to the specified TOML file.
func Save(path string, cfg *Config) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("failed to create config file: %w", err)
	}
	defer f.Close()
	if err := toml.NewEncoder(f).Encode(cfg); err != nil {
		return fmt.Errorf("failed to encode config: %w", err)
	}
	return nil
}

// configKey describes one settable config key: set validates the raw
// --set-value string and assigns the parsed value; desc documents the key.
// configKeys is the single source for SetValue dispatch and the supported-keys
// error text, so a new key needs exactly one entry; its order is the
// documented key order.
type configKey struct {
	name string
	desc string
	set  func(cfg *Config, key, value string) error
}

var configKeys = []configKey{
	{name: "ollama_url", desc: "Ollama embeddings endpoint URL", set: func(cfg *Config, key, value string) error {
		if err := requireValue(key, value); err != nil {
			return err
		}
		cfg.OllamaURL = value
		return nil
	}},
	{name: "model", desc: "Embedding model name", set: func(cfg *Config, key, value string) error {
		if err := requireValue(key, value); err != nil {
			return err
		}
		cfg.Model = value
		return nil
	}},
	{name: "embedding_dim", desc: "Embedding dimension (0 = auto-detect)", set: func(cfg *Config, key, value string) error {
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			return fmt.Errorf("invalid %s value %q (must be a non-negative integer; 0 means auto-detect)", key, value)
		}
		cfg.EmbeddingDim = n
		return nil
	}},
	{name: "timeout_seconds", desc: "Embedding request timeout in seconds", set: func(cfg *Config, key, value string) error {
		n, err := parsePositiveInt(key, value)
		if err != nil {
			return err
		}
		cfg.TimeoutSeconds = n
		return nil
	}},
	{name: "retry_count", desc: "Embedding request retry count", set: func(cfg *Config, key, value string) error {
		n, err := parseNonNegativeInt(key, value)
		if err != nil {
			return err
		}
		cfg.RetryCount = n
		return nil
	}},
	{name: "retry_backoff_ms", desc: "Backoff between embedding retries in milliseconds", set: func(cfg *Config, key, value string) error {
		n, err := parsePositiveInt(key, value)
		if err != nil {
			return err
		}
		cfg.RetryBackoffMS = n
		return nil
	}},
	{name: "index_cooldown_seconds", desc: "Cooldown between index runs in seconds", set: func(cfg *Config, key, value string) error {
		n, err := parsePositiveInt(key, value)
		if err != nil {
			return err
		}
		cfg.IndexCooldownSeconds = n
		return nil
	}},
	{name: "vector_backend", desc: `Vector storage backend (only "sqlite" supported)`, set: func(cfg *Config, key, value string) error {
		if err := requireValue(key, value); err != nil {
			return err
		}
		if value != "sqlite" {
			return fmt.Errorf("invalid %s %q (only %q is currently supported)", key, value, "sqlite")
		}
		cfg.VectorBackend = value
		return nil
	}},
	{name: "vector_quantization", desc: `Vector quantization mode ("off" or "turbo-prod")`, set: func(cfg *Config, key, value string) error {
		if err := requireValue(key, value); err != nil {
			return err
		}
		if value != "off" && value != "turbo-prod" {
			return fmt.Errorf("invalid %s %q (supported: %q, %q)", key, value, "off", "turbo-prod")
		}
		cfg.VectorQuantization = value
		return nil
	}},
	{name: "vector_quant_bits", desc: "Quantization bit width (2, 3, or 4)", set: func(cfg *Config, key, value string) error {
		n, err := strconv.Atoi(value)
		if err != nil || (n != 2 && n != 3 && n != 4) {
			return fmt.Errorf("invalid %s value %q (must be 2, 3, or 4)", key, value)
		}
		cfg.VectorQuantBits = n
		return nil
	}},
	{name: "vector_quantized_shortlist", desc: "Approximate shortlist size for quantized search", set: func(cfg *Config, key, value string) error {
		n, err := parsePositiveInt(key, value)
		if err != nil {
			return err
		}
		cfg.VectorQuantizedShortlist = n
		return nil
	}},
	{name: "vector_exact_rerank", desc: "Exact cosine rerank on the quantized shortlist", set: func(cfg *Config, key, value string) error {
		b, err := parseBoolValue(key, value)
		if err != nil {
			return err
		}
		cfg.VectorExactRerank = b
		return nil
	}},
	{name: "rerank_query", desc: "Enable Ollama re-ranking of search results", set: func(cfg *Config, key, value string) error {
		b, err := parseBoolValue(key, value)
		if err != nil {
			return err
		}
		cfg.RerankQuery = b
		return nil
	}},
	{name: "rerank_model", desc: "Chat model for re-ranking (empty = reuse embedding model)", set: func(cfg *Config, _ string, value string) error {
		cfg.RerankModel = value
		return nil
	}},
	{name: "rerank_timeout_seconds", desc: "Per-request timeout for re-ranking", set: func(cfg *Config, key, value string) error {
		n, err := parsePositiveInt(key, value)
		if err != nil {
			return err
		}
		cfg.RerankTimeoutSeconds = n
		return nil
	}},
	{name: "expand_query", desc: "Enable HyDE query expansion via Ollama chat", set: func(cfg *Config, key, value string) error {
		b, err := parseBoolValue(key, value)
		if err != nil {
			return err
		}
		cfg.ExpandQuery = b
		return nil
	}},
	{name: "expand_model", desc: "Chat model for expansion (empty = reuse embedding model)", set: func(cfg *Config, _ string, value string) error {
		cfg.ExpandModel = value
		return nil
	}},
	{name: "expand_timeout_seconds", desc: "Per-request timeout for expansion", set: func(cfg *Config, key, value string) error {
		n, err := parsePositiveInt(key, value)
		if err != nil {
			return err
		}
		cfg.ExpandTimeoutSeconds = n
		return nil
	}},
}

func findConfigKey(name string) *configKey {
	for i := range configKeys {
		if configKeys[i].name == name {
			return &configKeys[i]
		}
	}
	return nil
}

func supportedConfigKeys() string {
	names := make([]string, len(configKeys))
	for i, k := range configKeys {
		names[i] = k.name
	}
	return strings.Join(names, ", ")
}

func requireValue(key, value string) error {
	if value == "" {
		return fmt.Errorf("--set-value is required for key %q", key)
	}
	return nil
}

func parsePositiveInt(key, value string) (int, error) {
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid %s value %q (must be a positive integer)", key, value)
	}
	return n, nil
}

func parseNonNegativeInt(key, value string) (int, error) {
	n, err := strconv.Atoi(value)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid %s value %q (must be a non-negative integer)", key, value)
	}
	return n, nil
}

func parseBoolValue(key, value string) (bool, error) {
	b, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("invalid %s value %q (must be true or false)", key, value)
	}
	return b, nil
}

// SetValue validates and sets a single config key, then saves to disk.
func SetValue(cfgFile string, key, value string, cfg *Config) error {
	entry := findConfigKey(key)
	if entry == nil {
		return fmt.Errorf("unknown config key %q (supported: %s)", key, supportedConfigKeys())
	}
	if err := entry.set(cfg, key, value); err != nil {
		return err
	}
	return Save(cfgFile, cfg)
}
