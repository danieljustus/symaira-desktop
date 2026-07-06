package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/danieljustus/symaira-corekit/configkit"
)

type Config struct {
	Vault string `toml:"vault" env:"SYMDESK_VAULT"`
}

func DefaultConfig() *Config {
	return &Config{
		Vault: "",
	}
}

func Load() (*Config, error) {
	return LoadFromPath(GlobalPath())
}

func LoadFromPath(path string) (*Config, error) {
	cfg := DefaultConfig()

	// Read from environment variable first
	if envVault := os.Getenv("SYMDESK_VAULT"); envVault != "" {
		cfg.Vault = envVault
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

	// Environment variable overrides file config if set
	if envVault := os.Getenv("SYMDESK_VAULT"); envVault != "" {
		cfg.Vault = envVault
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
