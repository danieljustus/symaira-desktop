package config

import (
	"errors"
	"fmt"
	"os"

	"github.com/danieljustus/symaira-corekit/configkit"
	"github.com/danieljustus/symaira-corekit/exitcodes"
)

type Config struct {
	DefaultIdentity string                   `json:"default_identity"`
	Approval        ApprovalConfig           `json:"approval"`
	Adapters        map[string]AdapterConfig `json:"adapters"`
	Updatecheck     UpdatecheckConfig        `json:"updatecheck"`
}

type ApprovalConfig struct {
	DefaultTTL string `json:"default_ttl"`
}

type AdapterConfig struct {
	Command []string `json:"command"`
	Workdir string   `json:"workdir"`
}

type UpdatecheckConfig struct {
	Enabled bool `json:"enabled"`
}

func DefaultConfig() *Config {
	return &Config{
		DefaultIdentity: "",
		Approval: ApprovalConfig{
			DefaultTTL: "30m",
		},
		Adapters: make(map[string]AdapterConfig),
		Updatecheck: UpdatecheckConfig{
			Enabled: true,
		},
	}
}

func NewLoader() *configkit.Loader[Config] {
	opts := configkit.Options{
		AppName:   "symroom",
		EnvPrefix: "SYMROOM",
	}
	return configkit.NewLoader[Config](opts, DefaultConfig)
}

func LoadOrExit() *Config {
	loader := NewLoader()
	cfg, err := loader.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading configuration: %v\n", err)
		os.Exit(int(exitcodes.ExitNoInput))
	}
	return cfg
}

func ValidateConfig(cfg *Config) error {
	if cfg == nil {
		return errors.New("config is nil")
	}
	return nil
}
