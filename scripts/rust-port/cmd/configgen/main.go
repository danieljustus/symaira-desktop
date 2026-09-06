// Command configgen freezes the unified SymDesk configuration contract without exposing secret values.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

var configEnvKeys = []string{
	"SYMDESK_VAULT", "SYMDESK_INBOX", "SYMDESK_REVIEW_THRESHOLD", "SYMDESK_LLM_PROVIDER",
	"SYMDESK_LLM_API_KEY", "SYMDESK_LLM_MODEL", "SYMDESK_OLLAMA_URL", "SYMDESK_RECIPE_RUNNER",
	"SYMDESK_HERMES_SESSION", "SYMDESK_LANG", "SYMDESK_MAX_TOKENS", "SYMDESK_AGENT_MAX_ITERATIONS",
	"SYMDESK_HISTORY_MAX_PER_FILE", "SYMDESK_HISTORY_MAX_AGE_DAYS", "SYMDESK_HISTORY_CHECKPOINT_MAX_AGE_DAYS",
	"SYMDESK_TRASH_RETENTION_DAYS", "SYMDESK_RESULTS_MAX_AGE_DAYS", "SYMDESK_RESULTS_MAX_PER_TASK",
	"SYMDESK_DATASET_EXPORT_MAX_SENSITIVITY", "SYMDESK_STORAGE_PATH_TEMPLATE",
	"XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "HOME", "USERPROFILE",
}

type document struct {
	SchemaVersion int              `json:"schema_version"`
	Oracle        inventory.Oracle `json:"oracle"`
	Cases         cases            `json:"cases"`
}

type cases struct {
	Defaults   safeConfig       `json:"defaults"`
	Loads      []loadCase       `json:"loads"`
	Validation []validationCase `json:"validation"`
	Save       saveCase         `json:"save"`
	Paths      []pathCase       `json:"paths"`
}

type safeConfig struct {
	Vault                       string `json:"vault"`
	Inbox                       string `json:"inbox"`
	ReviewThreshold             int    `json:"review_threshold"`
	LLMProvider                 string `json:"llm_provider"`
	HasAPIKey                   bool   `json:"has_api_key"`
	LLMModel                    string `json:"llm_model"`
	OllamaURL                   string `json:"ollama_url"`
	RecipeRunner                string `json:"recipe_runner"`
	HermesSession               string `json:"hermes_session"`
	Language                    string `json:"language"`
	MaxTokens                   int    `json:"max_tokens"`
	AgentMaxIterations          int    `json:"agent_max_iterations"`
	HistoryMaxPerFile           int    `json:"history_max_per_file"`
	HistoryMaxAgeDays           int    `json:"history_max_age_days"`
	HistoryCheckpointMaxAgeDays int    `json:"history_checkpoint_max_age_days"`
	TrashRetentionDays          int    `json:"trash_retention_days"`
	ResultsMaxAgeDays           int    `json:"results_max_age_days"`
	ResultsMaxPerTask           int    `json:"results_max_per_task"`
	DatasetExportMaxSensitivity string `json:"dataset_export_max_sensitivity"`
	StoragePathTemplate         string `json:"storage_path_template"`
}

type loadCase struct {
	ID          string            `json:"id"`
	TOML        string            `json:"toml,omitempty"`
	Missing     bool              `json:"missing,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
	Config      *safeConfig       `json:"config,omitempty"`
	ErrorPrefix string            `json:"error_prefix,omitempty"`
}

type findingDTO struct {
	Severity string `json:"severity"`
	Field    string `json:"field"`
	Message  string `json:"message"`
}

type validationCase struct {
	ID         string       `json:"id"`
	Config     safeConfig   `json:"config"`
	PathsExist bool         `json:"paths_exist"`
	Findings   []findingDTO `json:"findings"`
}

type saveCase struct {
	Config safeConfig `json:"config"`
	TOML   string     `json:"toml"`
}

type pathCase struct {
	ID          string            `json:"id"`
	Environment map[string]string `json:"environment"`
	DataHome    string            `json:"data_home"`
	ConfigHome  string            `json:"config_home"`
	CacheHome   string            `json:"cache_home"`
	DataDir     string            `json:"data_dir"`
	ConfigDir   string            `json:"config_dir"`
	CacheDir    string            `json:"cache_dir"`
	GlobalPath  string            `json:"global_path"`
}

func main() {
	output := flag.String("output", "testdata/port/core/config.json", "fixture path")
	check := flag.Bool("check", false, "fail if fixture differs")
	commit := flag.String("oracle-commit", "ae86331930fdfa2b128b68ae5af7437091b9949a", "Go oracle commit")
	release := flag.String("oracle-release", "v0.12.2", "Go oracle release")
	flag.Parse()

	value, err := buildDocument(inventory.Oracle{Commit: *commit, Release: *release})
	if err != nil {
		fatal("build fixture: %v", err)
	}
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		fatal("marshal fixture: %v", err)
	}
	content = append(content, '\n')
	if *check {
		existing, err := os.ReadFile(*output)
		if err != nil {
			fatal("read fixture: %v", err)
		}
		if !bytes.Equal(existing, content) {
			fatal("config fixture drift; regenerate deliberately")
		}
		fmt.Println("PASS config fixture")
		return
	}
	if err := os.MkdirAll(filepath.Dir(*output), 0o750); err != nil {
		fatal("create fixture directory: %v", err)
	}
	if err := os.WriteFile(*output, content, 0o600); err != nil {
		fatal("write fixture: %v", err)
	}
	fmt.Println("PASS config fixture generated")
}

func buildDocument(oracle inventory.Oracle) (document, error) {
	loads, err := buildLoadCases()
	if err != nil {
		return document{}, err
	}
	validation := buildValidationCases()
	save, err := buildSaveCase()
	if err != nil {
		return document{}, err
	}
	paths, err := buildPathCases()
	if err != nil {
		return document{}, err
	}
	return document{SchemaVersion: 1, Oracle: oracle, Cases: cases{Defaults: safe(config.DefaultConfig()), Loads: loads, Validation: validation, Save: save, Paths: paths}}, nil
}

func buildLoadCases() ([]loadCase, error) {
	specs := []loadCase{
		{ID: "missing-defaults", Missing: true},
		{ID: "toml-values-and-unknown", TOML: "vault = \"/toml/vault\"\nreview_threshold = 70\nllm_provider = \"anthropic\"\nllm_api_key = \"synthetic-fixture-value\"\nmax_tokens = 4096\nunknown_future_key = \"preserved-by-file-but-ignored-by-model\"\n"},
		{ID: "supported-env-overrides", Missing: true, Environment: map[string]string{"SYMDESK_VAULT": "/env/vault", "SYMDESK_INBOX": "/env/inbox", "SYMDESK_REVIEW_THRESHOLD": "90", "SYMDESK_LLM_PROVIDER": "openai", "SYMDESK_LLM_API_KEY": "synthetic-fixture-value", "SYMDESK_LLM_MODEL": "fixture-model", "SYMDESK_HERMES_SESSION": "fixture-session", "SYMDESK_LANG": "de", "SYMDESK_MAX_TOKENS": "12000", "SYMDESK_HISTORY_MAX_PER_FILE": "7", "SYMDESK_HISTORY_MAX_AGE_DAYS": "8", "SYMDESK_HISTORY_CHECKPOINT_MAX_AGE_DAYS": "9", "SYMDESK_TRASH_RETENTION_DAYS": "10", "SYMDESK_RESULTS_MAX_AGE_DAYS": "11", "SYMDESK_RESULTS_MAX_PER_TASK": "12", "SYMDESK_DATASET_EXPORT_MAX_SENSITIVITY": "  CONFIDENTIAL  "}},
		{ID: "invalid-env-is-ignored", Missing: true, Environment: map[string]string{"SYMDESK_REVIEW_THRESHOLD": "101", "SYMDESK_MAX_TOKENS": "0", "SYMDESK_HISTORY_MAX_PER_FILE": "-1", "SYMDESK_RESULTS_MAX_PER_TASK": "nope"}},
		{ID: "tagged-but-ignored-env-issue-854", Missing: true, Environment: map[string]string{"SYMDESK_OLLAMA_URL": "http://ignored.test", "SYMDESK_RECIPE_RUNNER": "ignored-runner", "SYMDESK_AGENT_MAX_ITERATIONS": "99", "SYMDESK_STORAGE_PATH_TEMPLATE": "ignored/{title}"}},
		{ID: "malformed-toml", TOML: "{{{{not valid", ErrorPrefix: "failed to decode config file:"},
	}
	result := make([]loadCase, 0, len(specs))
	for _, spec := range specs {
		out := spec
		err := withEnvironment(spec.Environment, func() error {
			dir, err := os.MkdirTemp("", "symdesk-config-port-")
			if err != nil {
				return err
			}
			defer func() { _ = os.RemoveAll(dir) }()
			path := filepath.Join(dir, "config.toml")
			if !spec.Missing {
				if err := os.WriteFile(path, []byte(spec.TOML), 0o600); err != nil {
					return err
				}
			}
			loaded, loadErr := config.LoadFromPath(path)
			if loadErr != nil {
				if spec.ErrorPrefix == "" || !strings.HasPrefix(loadErr.Error(), spec.ErrorPrefix) {
					return fmt.Errorf("%s unexpected error: %w", spec.ID, loadErr)
				}
				return nil
			}
			if spec.ErrorPrefix != "" {
				return fmt.Errorf("%s expected error prefix %q", spec.ID, spec.ErrorPrefix)
			}
			value := safe(loaded)
			out.Config = &value
			return nil
		})
		if err != nil {
			return nil, err
		}
		result = append(result, out)
	}
	return result, nil
}

func buildValidationCases() []validationCase {
	defaults := config.DefaultConfig()
	invalid := config.DefaultConfig()
	invalid.ReviewThreshold = 101
	invalid.MaxTokens = 0
	invalid.HistoryMaxAgeDays = -1
	invalid.HistoryCheckpointMaxAgeDays = -2
	invalid.TrashRetentionDays = -3
	invalid.HistoryMaxPerFile = -4
	invalid.ResultsMaxAgeDays = -5
	invalid.ResultsMaxPerTask = -6
	invalid.DatasetExportMaxSensitivity = "secret"
	invalid.LLMProvider = "cohere"
	invalid.Language = "fr"
	missing := config.DefaultConfig()
	missing.Vault = "/fixture/missing-vault"
	missing.Inbox = "/fixture/missing-inbox"
	return []validationCase{
		{ID: "defaults", Config: safe(defaults), PathsExist: true, Findings: findings(defaults.Validate())},
		{ID: "ordered-invalid-values", Config: safe(invalid), PathsExist: true, Findings: findings(invalid.Validate())},
		{ID: "missing-paths", Config: safe(missing), PathsExist: false, Findings: findings(missing.Validate())},
	}
}

func buildSaveCase() (saveCase, error) {
	value := config.DefaultConfig()
	value.Vault = "/saved/vault"
	value.Inbox = "/saved/inbox"
	value.ReviewThreshold = 60
	value.LLMProvider = "hermes"
	value.LLMModel = "fixture-model"
	value.Language = "de"
	value.MaxTokens = 2048
	value.StoragePathTemplate = "documents/{year}/{title}"
	dir, err := os.MkdirTemp("", "symdesk-config-save-")
	if err != nil {
		return saveCase{}, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path := filepath.Join(dir, "nested", "config.toml")
	if err := config.Save(path, value); err != nil {
		return saveCase{}, err
	}
	//nolint:gosec // path is the just-written temp config file
	content, err := os.ReadFile(path)
	return saveCase{Config: safe(value), TOML: string(content)}, err
}

func buildPathCases() ([]pathCase, error) {
	specs := []struct {
		id  string
		env map[string]string
	}{
		{"xdg-absolute", map[string]string{"HOME": "/fixture/home", "XDG_DATA_HOME": "  /fixture/data  ", "XDG_CONFIG_HOME": "/fixture/config", "XDG_CACHE_HOME": "/fixture/cache"}},
		{"home-fallback", map[string]string{"HOME": "/fixture/home"}},
		{"relative-xdg-config", map[string]string{"HOME": "/fixture/home", "XDG_CONFIG_HOME": "relative/config"}},
	}
	result := make([]pathCase, 0, len(specs))
	for _, spec := range specs {
		var out pathCase
		err := withEnvironment(spec.env, func() error {
			dataHome, err := config.ResolveDataHome()
			if err != nil {
				return err
			}
			configHome, err := config.ResolveConfigHome()
			if err != nil {
				return err
			}
			cacheHome, err := config.ResolveCacheHome()
			if err != nil {
				return err
			}
			out = pathCase{ID: spec.id, Environment: spec.env, DataHome: slash(dataHome), ConfigHome: slash(configHome), CacheHome: slash(cacheHome), DataDir: slash(config.DataDir()), ConfigDir: slash(config.ConfigDir()), CacheDir: slash(config.CacheDir()), GlobalPath: slash(config.GlobalPath())}
			return nil
		})
		if err != nil {
			return nil, err
		}
		result = append(result, out)
	}
	return result, nil
}

func safe(value *config.Config) safeConfig {
	return safeConfig{Vault: value.Vault, Inbox: value.Inbox, ReviewThreshold: value.ReviewThreshold, LLMProvider: value.LLMProvider, HasAPIKey: value.LLMAPIKey != "", LLMModel: value.LLMModel, OllamaURL: value.OllamaURL, RecipeRunner: value.RecipeRunner, HermesSession: value.HermesSession, Language: value.Language, MaxTokens: value.MaxTokens, AgentMaxIterations: value.AgentMaxIterations, HistoryMaxPerFile: value.HistoryMaxPerFile, HistoryMaxAgeDays: value.HistoryMaxAgeDays, HistoryCheckpointMaxAgeDays: value.HistoryCheckpointMaxAgeDays, TrashRetentionDays: value.TrashRetentionDays, ResultsMaxAgeDays: value.ResultsMaxAgeDays, ResultsMaxPerTask: value.ResultsMaxPerTask, DatasetExportMaxSensitivity: value.DatasetExportMaxSensitivity, StoragePathTemplate: value.StoragePathTemplate}
}

func findings(values []config.Finding) []findingDTO {
	result := make([]findingDTO, 0, len(values))
	for _, value := range values {
		severity := "warning"
		if value.Severity == config.SeverityFatal {
			severity = "fatal"
		}
		result = append(result, findingDTO{Severity: severity, Field: value.Field, Message: value.Message})
	}
	return result
}

func withEnvironment(values map[string]string, run func() error) error {
	previous := make(map[string]*string, len(configEnvKeys))
	for _, key := range configEnvKeys {
		if value, ok := os.LookupEnv(key); ok {
			copy := value
			previous[key] = &copy
		} else {
			previous[key] = nil
		}
		_ = os.Unsetenv(key)
	}
	defer func() {
		for key, value := range previous {
			if value == nil {
				_ = os.Unsetenv(key)
			} else {
				_ = os.Setenv(key, *value)
			}
		}
	}()
	for key, value := range values {
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return run()
}

func slash(value string) string { return filepath.ToSlash(value) }

func fatal(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, "FAIL "+format+"\n", args...)
	os.Exit(1)
}
