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

type Config struct {
	Vault           string `toml:"vault" env:"SYMDESK_VAULT"`
	Inbox           string `toml:"inbox" env:"SYMDESK_INBOX"`
	ReviewThreshold int    `toml:"review_threshold" env:"SYMDESK_REVIEW_THRESHOLD"`
}

func DefaultConfig() *Config {
	return &Config{
		Vault:           "",
		Inbox:           "",
		ReviewThreshold: defaultReviewThreshold,
	}
}

func Load() (*Config, error) {
	return LoadFromPath(GlobalPath())
}

func LoadFromPath(path string) (*Config, error) {
	cfg := DefaultConfig()

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
