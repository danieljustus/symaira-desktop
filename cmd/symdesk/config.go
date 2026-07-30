package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/config"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Read and write configuration values",
	}

	getCmd := &cobra.Command{
		Use:   "get [key]",
		Short: "Get a configuration value or all values as JSON",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				key := args[0]
				val := getConfigValue(cfg, key)
				if jsonFlag {
					b, _ := json.Marshal(map[string]string{key: val})
					fmt.Println(string(b))
				} else {
					fmt.Println(val)
				}
				return nil
			}
			// Print all as JSON
			b, _ := json.Marshal(configToMap(cfg))
			fmt.Println(string(b))
			return nil
		},
	}

	setCmd := &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a configuration value and save to config file",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			value := args[1]

			if err := setConfigValue(cfg, key, value); err != nil {
				return err
			}

			path := config.GlobalPath()
			if err := config.Save(path, cfg); err != nil {
				return fmt.Errorf("failed to save config: %w", err)
			}

			if jsonFlag {
				b, _ := json.Marshal(map[string]string{"status": "ok", "key": key, "value": value})
				fmt.Println(string(b))
			} else {
				fmt.Printf("config %s set to %q\n", key, value)
			}
			return nil
		},
	}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List all configuration values as JSON",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			b, _ := json.Marshal(configToMap(cfg))
			fmt.Println(string(b))
			return nil
		},
	}

	cmd.AddCommand(getCmd)
	cmd.AddCommand(setCmd)
	cmd.AddCommand(listCmd)
	return cmd
}

func getConfigValue(cfg *config.Config, key string) string {
	switch strings.ToLower(key) {
	case "vault":
		return cfg.Vault
	case "inbox":
		return cfg.Inbox
	case "review_threshold":
		return fmt.Sprintf("%d", cfg.ReviewThreshold)
	case "llm_provider":
		return cfg.LLMProvider
	case "llm_api_key":
		return cfg.LLMAPIKey
	case "llm_model":
		return cfg.LLMModel
	case "language":
		return cfg.Language
	case "max_tokens":
		return fmt.Sprintf("%d", cfg.MaxTokens)
	case "history_max_per_file":
		return fmt.Sprintf("%d", cfg.HistoryMaxPerFile)
	case "history_max_age_days":
		return fmt.Sprintf("%d", cfg.HistoryMaxAgeDays)
	case "trash_retention_days":
		return fmt.Sprintf("%d", cfg.TrashRetentionDays)
	case "storage_path_template":
		return cfg.StoragePathTemplate
	case "ollama_url":
		return cfg.OllamaURL
	case "ollama_model":
		return cfg.OllamaModel
	default:
		return ""
	}
}

func setConfigValue(cfg *config.Config, key, value string) error {
	switch strings.ToLower(key) {
	case "vault":
		cfg.Vault = value
	case "inbox":
		cfg.Inbox = value
	case "review_threshold":
		cfg.ReviewThreshold = 0
		if _, err := fmt.Sscanf(value, "%d", &cfg.ReviewThreshold); err != nil {
			return fmt.Errorf("invalid review_threshold value %q: %w", value, err)
		}
	case "llm_provider":
		cfg.LLMProvider = value
	case "llm_api_key":
		cfg.LLMAPIKey = value
	case "llm_model":
		cfg.LLMModel = value
	case "language":
		cfg.Language = value
	case "max_tokens":
		cfg.MaxTokens = 0
		if _, err := fmt.Sscanf(value, "%d", &cfg.MaxTokens); err != nil {
			return fmt.Errorf("invalid max_tokens value %q: %w", value, err)
		}
	case "history_max_per_file":
		cfg.HistoryMaxPerFile = 0
		if _, err := fmt.Sscanf(value, "%d", &cfg.HistoryMaxPerFile); err != nil {
			return fmt.Errorf("invalid history_max_per_file value %q: %w", value, err)
		}
	case "history_max_age_days":
		cfg.HistoryMaxAgeDays = 0
		if _, err := fmt.Sscanf(value, "%d", &cfg.HistoryMaxAgeDays); err != nil {
			return fmt.Errorf("invalid history_max_age_days value %q: %w", value, err)
		}
	case "trash_retention_days":
		cfg.TrashRetentionDays = 0
		if _, err := fmt.Sscanf(value, "%d", &cfg.TrashRetentionDays); err != nil {
			return fmt.Errorf("invalid trash_retention_days value %q: %w", value, err)
		}
	case "storage_path_template":
		cfg.StoragePathTemplate = value
	case "ollama_url":
		cfg.OllamaURL = value
	case "ollama_model":
		cfg.OllamaModel = value
	default:
		return fmt.Errorf("unknown config key %q", key)
	}
	return nil
}

func configToMap(cfg *config.Config) map[string]interface{} {
	return map[string]interface{}{
		"vault":                  cfg.Vault,
		"inbox":                  cfg.Inbox,
		"review_threshold":       cfg.ReviewThreshold,
		"llm_provider":           cfg.LLMProvider,
		"llm_api_key":            cfg.LLMAPIKey == "" || strings.HasPrefix(cfg.LLMAPIKey, "secret:") ? cfg.LLMAPIKey : "***",
		"llm_model":              cfg.LLMModel,
		"language":               cfg.Language,
		"max_tokens":             cfg.MaxTokens,
		"history_max_per_file":   cfg.HistoryMaxPerFile,
		"history_max_age_days":   cfg.HistoryMaxAgeDays,
		"trash_retention_days":   cfg.TrashRetentionDays,
		"storage_path_template":  cfg.StoragePathTemplate,
		"ollama_url":            cfg.OllamaURL,
		"ollama_model":          cfg.OllamaModel,
	}
}
