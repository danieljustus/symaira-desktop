package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/BurntSushi/toml"
	"github.com/danieljustus/symaira-corekit/configkit"
)

const defaultReviewThreshold = 85

const defaultMaxTokens = 8192

// DefaultAnthropicModel is the Anthropic model used when no explicit model
// is configured via llm_model / SYMDESK_LLM_MODEL.
const DefaultAnthropicModel = "claude-sonnet-5"

// Default retention for the version-history and trash safety net.
const (
	defaultHistoryMaxPerFile  = 20
	defaultHistoryMaxAgeDays  = 90
	defaultTrashRetentionDays = 30
)

type Config struct {
	Vault           string `toml:"vault" env:"SYMDESK_VAULT"`
	Inbox           string `toml:"inbox" env:"SYMDESK_INBOX"`
	ReviewThreshold int    `toml:"review_threshold" env:"SYMDESK_REVIEW_THRESHOLD"`
	LLMProvider     string `toml:"llm_provider" env:"SYMDESK_LLM_PROVIDER"`
	LLMAPIKey       string `toml:"llm_api_key" env:"SYMDESK_LLM_API_KEY"`
	LLMModel        string `toml:"llm_model" env:"SYMDESK_LLM_MODEL"`
	Language        string `toml:"language" env:"SYMDESK_LANG"`
	MaxTokens       int    `toml:"max_tokens" env:"SYMDESK_MAX_TOKENS"`

	// HistoryMaxPerFile is the maximum number of snapshots kept per file
	// when pruning (0 = unlimited).
	HistoryMaxPerFile int `toml:"history_max_per_file" env:"SYMDESK_HISTORY_MAX_PER_FILE"`
	// HistoryMaxAgeDays drops snapshots older than this many days when
	// pruning; the newest snapshot per file is always kept (0 = unlimited).
	HistoryMaxAgeDays int `toml:"history_max_age_days" env:"SYMDESK_HISTORY_MAX_AGE_DAYS"`
	// TrashRetentionDays is the default age after which "trash purge"
	// permanently removes soft-deleted files.
	TrashRetentionDays int `toml:"trash_retention_days" env:"SYMDESK_TRASH_RETENTION_DAYS"`

	// StoragePathTemplate is an optional template that determines where
	// ingested documents are placed in the vault. See internal/templatepath
	// for the supported syntax.
	StoragePathTemplate string `toml:"storage_path_template" env:"SYMDESK_STORAGE_PATH_TEMPLATE"`
}

func DefaultConfig() *Config {
	return &Config{
		Vault:           "",
		Inbox:           "",
		ReviewThreshold: defaultReviewThreshold,
		LLMProvider:     "ollama",
		LLMAPIKey:       "",
		LLMModel:        DefaultAnthropicModel,
		Language:        "",
		MaxTokens:       defaultMaxTokens,

		HistoryMaxPerFile:  defaultHistoryMaxPerFile,
		HistoryMaxAgeDays:  defaultHistoryMaxAgeDays,
		TrashRetentionDays: defaultTrashRetentionDays,
	}
}

func Load() (*Config, error) {
	return LoadFromPath(GlobalPath())
}

// applyEnvOverrides applies the SYMDESK_* environment variable overrides to
// cfg. Env vars always take precedence over TOML values and defaults.
func applyEnvOverrides(cfg *Config) {
	if envVault := os.Getenv("SYMDESK_VAULT"); envVault != "" {
		cfg.Vault = envVault
	}
	if envInbox := os.Getenv("SYMDESK_INBOX"); envInbox != "" {
		cfg.Inbox = envInbox
	}
	if envThresh := os.Getenv("SYMDESK_REVIEW_THRESHOLD"); envThresh != "" {
		if v, err := strconv.Atoi(envThresh); err == nil && v >= 0 && v <= 100 {
			cfg.ReviewThreshold = v
		}
	}
	if envProv := os.Getenv("SYMDESK_LLM_PROVIDER"); envProv != "" {
		cfg.LLMProvider = envProv
	}
	if envKey := os.Getenv("SYMDESK_LLM_API_KEY"); envKey != "" {
		cfg.LLMAPIKey = envKey
	}
	if envModel := os.Getenv("SYMDESK_LLM_MODEL"); envModel != "" {
		cfg.LLMModel = envModel
	}
	if envLang := os.Getenv("SYMDESK_LANG"); envLang != "" {
		cfg.Language = envLang
	}
	if envMaxTokens := os.Getenv("SYMDESK_MAX_TOKENS"); envMaxTokens != "" {
		if v, err := strconv.Atoi(envMaxTokens); err == nil && v > 0 {
			cfg.MaxTokens = v
		}
	}
	for _, ev := range []struct {
		name   string
		target *int
	}{
		{"SYMDESK_HISTORY_MAX_PER_FILE", &cfg.HistoryMaxPerFile},
		{"SYMDESK_HISTORY_MAX_AGE_DAYS", &cfg.HistoryMaxAgeDays},
		{"SYMDESK_TRASH_RETENTION_DAYS", &cfg.TrashRetentionDays},
	} {
		if raw := os.Getenv(ev.name); raw != "" {
			if v, err := strconv.Atoi(raw); err == nil && v >= 0 {
				*ev.target = v
			}
		}
	}
}

func LoadFromPath(path string) (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			applyEnvOverrides(cfg)
			return cfg, nil
		}
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to decode config file: %w", err)
	}

	applyEnvOverrides(cfg)

	return cfg, nil
}

func GlobalPath() string {
	return configkit.DefaultPath("symdesk")
}

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
