package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

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
	defaultHistoryMaxPerFile           = 20
	defaultHistoryMaxAgeDays           = 90
	defaultHistoryCheckpointMaxAgeDays = 30
	defaultTrashRetentionDays          = 30
)

// Default retention for the externalized agent-results area (issue #418):
// task ids are unique per run, so the agent loop prunes by age and count.
const (
	defaultResultsMaxAgeDays = 30
	defaultResultsMaxPerTask = 20
)

type Config struct {
	Vault           string `toml:"vault" env:"SYMDESK_VAULT"`
	Inbox           string `toml:"inbox" env:"SYMDESK_INBOX"`
	ReviewThreshold int    `toml:"review_threshold" env:"SYMDESK_REVIEW_THRESHOLD"`
	LLMProvider     string `toml:"llm_provider" env:"SYMDESK_LLM_PROVIDER"`
	LLMAPIKey       string `toml:"llm_api_key" env:"SYMDESK_LLM_API_KEY"`
	LLMModel        string `toml:"llm_model" env:"SYMDESK_LLM_MODEL"`
	OllamaURL       string `toml:"ollama_url" env:"SYMDESK_OLLAMA_URL"`
	// RecipeRunner names the executable that `symdesk recipe run` delegates
	// change proposals to, resolved via internal/compose. Empty disables
	// recipe execution; validate/accept keep working. Previously hardwired
	// to symvibe, which was discontinued on 2026-08-26.
	RecipeRunner string `toml:"recipe_runner" env:"SYMDESK_RECIPE_RUNNER"`
	// HermesSession is the Hermes session id or title resumed by the optional
	// Hermes backend (llm_provider = hermes). Empty uses the most recent
	// session (issue #559).
	HermesSession string `toml:"hermes_session" env:"SYMDESK_HERMES_SESSION"`
	Language      string `toml:"language" env:"SYMDESK_LANG"`
	MaxTokens     int    `toml:"max_tokens" env:"SYMDESK_MAX_TOKENS"`
	// AgentMaxIterations caps the agentic tool loop (issue #317). Zero uses
	// the package default (5).
	AgentMaxIterations int `toml:"agent_max_iterations" env:"SYMDESK_AGENT_MAX_ITERATIONS"`

	// HistoryMaxPerFile is the maximum number of snapshots kept per file
	// when pruning (0 = unlimited).
	HistoryMaxPerFile int `toml:"history_max_per_file" env:"SYMDESK_HISTORY_MAX_PER_FILE"`
	// HistoryMaxAgeDays drops snapshots older than this many days when
	// pruning; the newest snapshot per file is always kept (0 = unlimited).
	HistoryMaxAgeDays int `toml:"history_max_age_days" env:"SYMDESK_HISTORY_MAX_AGE_DAYS"`
	// HistoryCheckpointMaxAgeDays drops task checkpoints older than this
	// many days when pruning; their blobs are then no longer protected from
	// garbage collection (0 = unlimited).
	HistoryCheckpointMaxAgeDays int `toml:"history_checkpoint_max_age_days" env:"SYMDESK_HISTORY_CHECKPOINT_MAX_AGE_DAYS"`
	// TrashRetentionDays is the default age after which "trash purge"
	// permanently removes soft-deleted files.
	TrashRetentionDays int `toml:"trash_retention_days" env:"SYMDESK_TRASH_RETENTION_DAYS"`

	// ResultsMaxAgeDays drops externalized agent results older than this
	// many days when the agent loop prunes the results area (0 = keep
	// results of any age).
	ResultsMaxAgeDays int `toml:"results_max_age_days" env:"SYMDESK_RESULTS_MAX_AGE_DAYS"`
	// ResultsMaxPerTask caps the externalized results kept per agent run
	// (newest wins; 0 = unlimited).
	ResultsMaxPerTask int `toml:"results_max_per_task" env:"SYMDESK_RESULTS_MAX_PER_TASK"`

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

		HistoryMaxPerFile:           defaultHistoryMaxPerFile,
		HistoryMaxAgeDays:           defaultHistoryMaxAgeDays,
		HistoryCheckpointMaxAgeDays: defaultHistoryCheckpointMaxAgeDays,
		TrashRetentionDays:          defaultTrashRetentionDays,
		ResultsMaxAgeDays:           defaultResultsMaxAgeDays,
		ResultsMaxPerTask:           defaultResultsMaxPerTask,
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
	if envSession := os.Getenv("SYMDESK_HERMES_SESSION"); envSession != "" {
		cfg.HermesSession = envSession
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
		{"SYMDESK_HISTORY_CHECKPOINT_MAX_AGE_DAYS", &cfg.HistoryCheckpointMaxAgeDays},
		{"SYMDESK_TRASH_RETENTION_DAYS", &cfg.TrashRetentionDays},
		{"SYMDESK_RESULTS_MAX_AGE_DAYS", &cfg.ResultsMaxAgeDays},
		{"SYMDESK_RESULTS_MAX_PER_TASK", &cfg.ResultsMaxPerTask},
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

// MailConfigPath resolves the IMAP mail configuration file location: an
// explicit path wins, otherwise it falls back to configkit's XDG-aware
// default (XDG_CONFIG_HOME, then $HOME/.config), matching every other
// absorbed store's resolver instead of a hardcoded $HOME/.config/symingest
// path (issue #755).
func MailConfigPath(explicit string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return filepath.Abs(explicit)
	}
	return configkit.DefaultPath("symingest"), nil
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

// Severity indicates whether a validation finding is fatal (blocks startup)
// or a warning (startup proceeds with a hint).
type Severity int

const (
	SeverityFatal   Severity = iota // must be fixed before startup
	SeverityWarning                 // should be fixed, but startup can proceed
)

// Finding is a single validation result with a severity, field name and
// human-readable message.
type Finding struct {
	Severity Severity
	Field    string
	Message  string
}

func (f Finding) Error() string {
	return f.Message
}

// Validate checks every configurable field for sensible values and returns
// all findings — both fatal and warning — so the caller can decide whether
// to abort startup.
func (c *Config) Validate() []Finding {
	var findings []Finding

	// --- Integer range checks ---

	if c.ReviewThreshold < 0 || c.ReviewThreshold > 100 {
		findings = append(findings, Finding{
			Severity: SeverityFatal,
			Field:    "review_threshold",
			Message:  fmt.Sprintf("review_threshold must be 0–100, got %d", c.ReviewThreshold),
		})
	}
	if c.MaxTokens <= 0 {
		findings = append(findings, Finding{
			Severity: SeverityFatal,
			Field:    "max_tokens",
			Message:  fmt.Sprintf("max_tokens must be > 0, got %d", c.MaxTokens),
		})
	}
	if c.HistoryMaxAgeDays < 0 {
		findings = append(findings, Finding{
			Severity: SeverityWarning,
			Field:    "history_max_age_days",
			Message:  fmt.Sprintf("history_max_age_days must be >= 0, got %d", c.HistoryMaxAgeDays),
		})
	}
	if c.HistoryCheckpointMaxAgeDays < 0 {
		findings = append(findings, Finding{
			Severity: SeverityWarning,
			Field:    "history_checkpoint_max_age_days",
			Message:  fmt.Sprintf("history_checkpoint_max_age_days must be >= 0, got %d", c.HistoryCheckpointMaxAgeDays),
		})
	}
	if c.TrashRetentionDays < 0 {
		findings = append(findings, Finding{
			Severity: SeverityWarning,
			Field:    "trash_retention_days",
			Message:  fmt.Sprintf("trash_retention_days must be >= 0, got %d", c.TrashRetentionDays),
		})
	}
	if c.HistoryMaxPerFile < 0 {
		findings = append(findings, Finding{
			Severity: SeverityWarning,
			Field:    "history_max_per_file",
			Message:  fmt.Sprintf("history_max_per_file must be >= 0, got %d", c.HistoryMaxPerFile),
		})
	}
	if c.ResultsMaxAgeDays < 0 {
		findings = append(findings, Finding{
			Severity: SeverityWarning,
			Field:    "results_max_age_days",
			Message:  fmt.Sprintf("results_max_age_days must be >= 0, got %d", c.ResultsMaxAgeDays),
		})
	}
	if c.ResultsMaxPerTask < 0 {
		findings = append(findings, Finding{
			Severity: SeverityWarning,
			Field:    "results_max_per_task",
			Message:  fmt.Sprintf("results_max_per_task must be >= 0, got %d", c.ResultsMaxPerTask),
		})
	}

	// --- Path existence checks ---

	if c.Vault != "" {
		if _, err := os.Stat(c.Vault); os.IsNotExist(err) {
			findings = append(findings, Finding{
				Severity: SeverityFatal,
				Field:    "vault",
				Message:  fmt.Sprintf("vault path does not exist: %s", c.Vault),
			})
		}
	}
	if c.Inbox != "" {
		if _, err := os.Stat(c.Inbox); os.IsNotExist(err) {
			findings = append(findings, Finding{
				Severity: SeverityWarning,
				Field:    "inbox",
				Message:  fmt.Sprintf("inbox path does not exist: %s", c.Inbox),
			})
		}
	}

	// --- Enum-like field checks ---

	validProviders := map[string]bool{
		"ollama":    true,
		"anthropic": true,
		"openai":    true,
		"hermes":    true,
		"":          true, // empty = default, not an error
	}
	if c.LLMProvider != "" && !validProviders[c.LLMProvider] {
		findings = append(findings, Finding{
			Severity: SeverityWarning,
			Field:    "llm_provider",
			Message:  fmt.Sprintf("unsupported llm_provider %q — expected one of: ollama, anthropic, openai, hermes", c.LLMProvider),
		})
	}

	validLanguages := map[string]bool{
		"en": true,
		"de": true,
		"":   true,
	}
	if c.Language != "" && !validLanguages[c.Language] {
		findings = append(findings, Finding{
			Severity: SeverityWarning,
			Field:    "language",
			Message:  fmt.Sprintf("unsupported language %q — expected en or de", c.Language),
		})
	}

	return findings
}

// ValidateFatal returns only the fatal findings from Validate().
func (c *Config) ValidateFatal() []Finding {
	var fatal []Finding
	for _, f := range c.Validate() {
		if f.Severity == SeverityFatal {
			fatal = append(fatal, f)
		}
	}
	return fatal
}
