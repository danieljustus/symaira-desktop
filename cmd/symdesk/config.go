package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"

	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/ingest"
	"github.com/danieljustus/symaira-desktop/internal/retrieval"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/spf13/cobra"
)

// newConfigCmd implements the shared CLI vocabulary `<tool> config` with
// get | set | path | paths | init (issues #593, #757). It makes the resolved
// configuration and store paths discoverable and editable from the CLI.
func newConfigCmd() *cobra.Command {
	configCmd := &cobra.Command{
		Use:     "config",
		Short:   "Inspect or change the resolved symdesk configuration",
		GroupID: groupMaintenance,
	}

	configCmd.AddCommand(&cobra.Command{
		Use:   "path",
		Short: "Print the configuration file path that is actually in effect",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println(config.GlobalPath())
			return nil
		},
	})

	configCmd.AddCommand(&cobra.Command{
		Use:   "paths",
		Short: "Print resolved filesystem paths for absorbed stores and directories",
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot := ""
			if cfg != nil {
				vRoot = cfg.Vault
			}
			sp, err := config.ResolveStorePaths(vRoot)
			if err != nil {
				return err
			}
			sp.Sidecar, err = sidecar.PathForVault(vRoot)
			if err != nil {
				return err
			}
			sp.Retrieval, err = retrieval.IndexLocationForVault(vRoot)
			if err != nil {
				return err
			}
			ingestPaths, err := ingest.ResolveDataPaths(vRoot)
			if err != nil {
				return err
			}
			sp.Ingest = ingestPaths.Database
			sp.IngestArchive = ingestPaths.Archive
			if jsonFlag {
				b, err := json.MarshalIndent(sp, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(b))
				return nil
			}
			fmt.Printf("data_dir: %s\n", sp.DataDir)
			fmt.Printf("config_dir: %s\n", sp.ConfigDir)
			fmt.Printf("cache_dir: %s\n", sp.CacheDir)
			fmt.Printf("sidecar: %s\n", sp.Sidecar)
			fmt.Printf("retrieval: %s\n", sp.Retrieval)
			fmt.Printf("ingest: %s\n", sp.Ingest)
			fmt.Printf("ingest_archive: %s\n", sp.IngestArchive)
			fmt.Printf("contacts: %s\n", sp.Contacts)
			return nil
		},
	})

	configCmd.AddCommand(&cobra.Command{
		Use:   "get [key]",
		Short: "Print the resolved configuration (all keys, or one key)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if len(args) == 1 {
				val, ok := configField(cfg, args[0])
				if !ok {
					return fmt.Errorf("unknown config key %q", args[0])
				}
				fmt.Println(val)
				return nil
			}
			if len(args) > 1 {
				return fmt.Errorf("usage: config get [key]")
			}
			return printConfig(cfg)
		},
	})

	configCmd.AddCommand(&cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a configuration value in the config file",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := config.GlobalPath()
			cfg, err := config.LoadFromPath(path)
			if err != nil {
				return err
			}
			if !setConfigField(cfg, args[0], args[1]) {
				return fmt.Errorf("unknown config key %q", args[0])
			}
			if err := config.Save(path, cfg); err != nil {
				return fmt.Errorf("failed to save config: %w", err)
			}
			fmt.Printf("set %s = %s in %s\n", args[0], args[1], path)
			return nil
		},
	})

	configCmd.AddCommand(&cobra.Command{
		Use:   "init",
		Short: "Create the config file with current defaults if it does not exist",
		RunE: func(cmd *cobra.Command, args []string) error {
			path := config.GlobalPath()
			if _, err := os.Stat(path); err == nil {
				fmt.Printf("config already exists at %s\n", path)
				return nil
			}
			if err := config.Save(path, config.DefaultConfig()); err != nil {
				return fmt.Errorf("failed to write config: %w", err)
			}
			fmt.Printf("wrote default config to %s\n", path)
			return nil
		},
	})

	return configCmd
}

// configFields maps the public config keys to a setter/getter pair, keeping
// the CLI vocabulary small and stable: keys are the lowercase snake_case
// TOML names users see in the config file.
func configFields(cfg *config.Config) map[string]func() (string, bool) {
	return map[string]func() (string, bool){
		"vault":                 func() (string, bool) { return cfg.Vault, true },
		"inbox":                 func() (string, bool) { return cfg.Inbox, true },
		"review_threshold":      func() (string, bool) { return strconv.Itoa(cfg.ReviewThreshold), true },
		"llm_provider":          func() (string, bool) { return cfg.LLMProvider, true },
		"llm_model":             func() (string, bool) { return cfg.LLMModel, true },
		"ollama_url":            func() (string, bool) { return cfg.OllamaURL, true },
		"language":              func() (string, bool) { return cfg.Language, true },
		"max_tokens":            func() (string, bool) { return strconv.Itoa(cfg.MaxTokens), true },
		"agent_max_iterations":  func() (string, bool) { return strconv.Itoa(cfg.AgentMaxIterations), true },
		"history_max_per_file":  func() (string, bool) { return strconv.Itoa(cfg.HistoryMaxPerFile), true },
		"history_max_age_days":  func() (string, bool) { return strconv.Itoa(cfg.HistoryMaxAgeDays), true },
		"trash_retention_days":  func() (string, bool) { return strconv.Itoa(cfg.TrashRetentionDays), true },
		"results_max_age_days":  func() (string, bool) { return strconv.Itoa(cfg.ResultsMaxAgeDays), true },
		"results_max_per_task":  func() (string, bool) { return strconv.Itoa(cfg.ResultsMaxPerTask), true },
		"storage_path_template": func() (string, bool) { return cfg.StoragePathTemplate, true },
	}
}

// configField returns the string value of one public config key.
func configField(cfg *config.Config, key string) (string, bool) {
	getter, ok := configFields(cfg)[key]
	if !ok {
		return "", false
	}
	return getter()
}

// setConfigField sets one public config key from its string form.
func setConfigField(cfg *config.Config, key, value string) bool {
	switch key {
	case "vault":
		cfg.Vault = value
	case "inbox":
		cfg.Inbox = value
	case "review_threshold":
		return setInt(&cfg.ReviewThreshold, value)
	case "llm_provider":
		cfg.LLMProvider = value
	case "llm_model":
		cfg.LLMModel = value
	case "ollama_url":
		cfg.OllamaURL = value
	case "language":
		cfg.Language = value
	case "max_tokens":
		return setInt(&cfg.MaxTokens, value)
	case "agent_max_iterations":
		return setInt(&cfg.AgentMaxIterations, value)
	case "history_max_per_file":
		return setInt(&cfg.HistoryMaxPerFile, value)
	case "history_max_age_days":
		return setInt(&cfg.HistoryMaxAgeDays, value)
	case "trash_retention_days":
		return setInt(&cfg.TrashRetentionDays, value)
	case "results_max_age_days":
		return setInt(&cfg.ResultsMaxAgeDays, value)
	case "results_max_per_task":
		return setInt(&cfg.ResultsMaxPerTask, value)
	case "storage_path_template":
		cfg.StoragePathTemplate = value
	default:
		return false
	}
	return true
}

func setInt(dst *int, value string) bool {
	v, err := strconv.Atoi(value)
	if err != nil {
		return false
	}
	*dst = v
	return true
}

// printConfig prints the resolved configuration as sorted key = value lines.
func printConfig(cfg *config.Config) error {
	fields := configFields(cfg)
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		val, _ := fields[key]()
		fmt.Printf("%s = %s\n", key, val)
	}
	return nil
}
