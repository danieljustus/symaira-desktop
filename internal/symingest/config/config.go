// Package config loads symingest configuration.
package config

import (
	"github.com/danieljustus/symaira-corekit/configkit"
)

// Config holds symingest configuration.
// Values are loaded from ~/.config/symingest/config.toml, ./.symingest.toml,
// and environment variables prefixed with SYMINGEST_.
type Config struct {
	Vault            string        `json:"vault"`
	OCRLang          string        `json:"ocr_lang"`
	DBPath           string        `json:"db_path"`
	ArchivePath      string        `json:"archive_path"`
	Inbox            string        `json:"inbox"`
	PaperlessBaseURL string        `json:"paperless_base_url"`
	SymseekEnabled   bool          `json:"symseek_enabled"`
	SymseekBinary    string        `json:"symseek_binary"`
	IMAPAccounts     []IMAPAccount `json:"imap_accounts"`
	IMAPPollInterval string        `json:"imap_poll_interval"` // e.g. "5m"

	// Ollama config for VLM-based OCR. When OllamaModel is set, the VLM
	// engine is used for image formats (PNG, JPEG, TIFF, WebP) with
	// Tesseract as fallback. PDF and HEIC continue through the Tesseract
	// pipeline. When empty, only Tesseract is used — no behaviour change.
	OllamaBaseURL string `json:"ollama_base_url"`
	OllamaModel   string `json:"ollama_model"`
}

// IMAPAccount configures an IMAP mailbox source for ingestion.
type IMAPAccount struct {
	Host           string   `json:"host" toml:"host"`
	Port           int      `json:"port" toml:"port"`
	Username       string   `json:"username" toml:"username"`
	PasswordSecret string   `json:"password_secret" toml:"password_secret"` // symvault:// or env:// or plaintext
	Folder         string   `json:"folder" toml:"folder"`                   // e.g. "INBOX"
	From           []string `json:"from" toml:"from"`                       // filter rules
	Subject        []string `json:"subject" toml:"subject"`                 // filter rules
	HasAttachment  bool     `json:"has_attachment" toml:"has_attachment"`   // require attachment
	Action         string   `json:"action" toml:"action"`                   // "mark_seen" or "move"
	MoveTo         string   `json:"move_to" toml:"move_to"`                 // folder to move to if action="move"
	ArchiveMail    bool     `json:"archive_mail" toml:"archive_mail"`       // whether to ingest the .eml message itself
}

// Defaults returns the default configuration.
func Defaults() *Config {
	return &Config{
		Vault:            "",
		OCRLang:          "eng",
		DBPath:           "",
		ArchivePath:      "",
		Inbox:            "",
		PaperlessBaseURL: "",
		SymseekEnabled:   false,
		SymseekBinary:    "",
		IMAPAccounts:     nil,
		IMAPPollInterval: "5m",
	}
}

// Loader is the application-wide config loader.
var Loader = configkit.NewLoader(configkit.Options{
	AppName:    "symingest",
	EnvPrefix:  "SYMINGEST",
	ConfigName: "symingest",
}, Defaults)

// Load returns the loaded configuration.
func Load() (*Config, error) {
	return Loader.Load()
}
