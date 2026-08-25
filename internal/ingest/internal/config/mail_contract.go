package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/danieljustus/symaira-corekit/configkit"
	"github.com/danieljustus/symaira-corekit/fsutil"
)

const MailJSONSchemaVersion = 1

var ErrConfigNotFound = errors.New("symingest configuration file not found")

const nextWatchRestartSemantics = "Changes are written atomically and take effect on the next symingest watch restart; an already-running watcher is not hot-reloaded."

// MailConfigDocument is the file-backed portion of the IMAP configuration.
type MailConfigDocument struct {
	Path             string
	Accounts         []IMAPAccount
	IMAPPollInterval string
}

// MailAccountView is safe to expose to a companion application. A plaintext
// password is never returned; secret references remain visible because they
// are locators, not resolved credential values.
type MailAccountView struct {
	ID                       string   `json:"id"`
	Host                     string   `json:"host"`
	Port                     int      `json:"port"`
	Username                 string   `json:"username"`
	PasswordSecret           string   `json:"password_secret"`
	PasswordSecretKind       string   `json:"password_secret_kind"`
	PasswordSecretConfigured bool     `json:"password_secret_configured"`
	Folder                   string   `json:"folder"`
	From                     []string `json:"from"`
	Subject                  []string `json:"subject"`
	HasAttachment            bool     `json:"has_attachment"`
	Action                   string   `json:"action"`
	MoveTo                   string   `json:"move_to"`
	ArchiveMail              bool     `json:"archive_mail"`
}

// MailValidation is deterministic and contains no credential material.
type MailValidation struct {
	Valid    bool     `json:"valid"`
	Errors   []string `json:"errors"`
	Warnings []string `json:"warnings"`
}

// NextWatchRestartSemantics describes when a successful write is observed by
// the long-running watcher.
func NextWatchRestartSemantics() string { return nextWatchRestartSemantics }

// ConfigPath returns the target file used by the mail contract. An explicit
// path wins; otherwise the existing project file wins over the global file.
func ConfigPath(explicit string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		path, err := filepath.Abs(explicit)
		if err != nil {
			return "", fmt.Errorf("resolve config path: %w", err)
		}
		return path, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("determine current directory: %w", err)
	}
	project := filepath.Join(cwd, ".symingest.toml")
	if _, err := os.Stat(project); err == nil {
		return project, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("stat project config: %w", err)
	}
	return configkit.DefaultPath("symingest"), nil
}

// ReadMailConfig reads only the mail section from a TOML file. It does not
// resolve password_secret values and returns an error for missing or malformed
// files.
func ReadMailConfig(path string) (*MailConfigDocument, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrConfigNotFound, path)
		}
		return nil, fmt.Errorf("stat config: %w", err)
	}

	var file struct {
		IMAPAccounts     []IMAPAccount `toml:"imap_accounts"`
		IMAPPollInterval string        `toml:"imap_poll_interval"`
	}
	if _, err := toml.DecodeFile(path, &file); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	return &MailConfigDocument{Path: path, Accounts: file.IMAPAccounts, IMAPPollInterval: file.IMAPPollInterval}, nil
}

// ValidateMailAccounts validates account fields and the polling interval.
func ValidateMailAccounts(accounts []IMAPAccount, pollInterval string) MailValidation {
	report := MailValidation{Valid: true, Errors: []string{}, Warnings: []string{}}
	if pollInterval != "" {
		if _, err := time.ParseDuration(pollInterval); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("imap_poll_interval: invalid duration %q", pollInterval))
		}
	}
	for i, account := range accounts {
		prefix := fmt.Sprintf("accounts[%d]", i)
		if strings.TrimSpace(account.Host) == "" {
			report.Errors = append(report.Errors, prefix+".host is required")
		}
		if account.Port < 1 || account.Port > 65535 {
			report.Errors = append(report.Errors, fmt.Sprintf("%s.port must be between 1 and 65535", prefix))
		}
		if strings.TrimSpace(account.Username) == "" {
			report.Errors = append(report.Errors, prefix+".username is required")
		}
		if strings.TrimSpace(account.PasswordSecret) == "" {
			report.Errors = append(report.Errors, prefix+".password_secret is required")
		} else if PasswordSecretKind(account.PasswordSecret) == "plaintext" {
			report.Warnings = append(report.Warnings, fmt.Sprintf("%s.password_secret is plaintext; use a secret reference", prefix))
		}
		if account.Action != "" && account.Action != "mark_seen" && account.Action != "move" {
			report.Errors = append(report.Errors, fmt.Sprintf("%s.action must be mark_seen or move", prefix))
		}
		if account.Action == "move" && strings.TrimSpace(account.MoveTo) == "" {
			report.Errors = append(report.Errors, prefix+".move_to is required when action is move")
		}
	}
	report.Valid = len(report.Errors) == 0
	return report
}

// PasswordSecretKind classifies a stored value without resolving it.
func PasswordSecretKind(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "missing"
	}
	for _, prefix := range []string{"symvault://", "env://", "keychain://"} {
		if strings.HasPrefix(strings.ToLower(value), prefix) {
			return "reference"
		}
	}
	return "plaintext"
}

// MaskPasswordSecret returns a safe representation for JSON output.
func MaskPasswordSecret(value string) string {
	switch PasswordSecretKind(value) {
	case "missing":
		return ""
	case "reference":
		return value
	default:
		return "<redacted>"
	}
}

// AccountID is a stable selector for an account in a config array. It does not
// contain password material and remains stable when unrelated TOML changes.
func AccountID(account IMAPAccount) string {
	folder := account.Folder
	if folder == "" {
		folder = "INBOX"
	}
	return fmt.Sprintf("%s@%s:%d/%s", account.Username, account.Host, account.Port, folder)
}

func ViewAccount(account IMAPAccount) MailAccountView {
	return MailAccountView{
		ID:                       AccountID(account),
		Host:                     account.Host,
		Port:                     account.Port,
		Username:                 account.Username,
		PasswordSecret:           MaskPasswordSecret(account.PasswordSecret),
		PasswordSecretKind:       PasswordSecretKind(account.PasswordSecret),
		PasswordSecretConfigured: strings.TrimSpace(account.PasswordSecret) != "",
		Folder:                   account.Folder,
		From:                     append([]string{}, account.From...),
		Subject:                  append([]string{}, account.Subject...),
		HasAttachment:            account.HasAttachment,
		Action:                   account.Action,
		MoveTo:                   account.MoveTo,
		ArchiveMail:              account.ArchiveMail,
	}
}

// WriteMailConfig atomically replaces only the imap_accounts section and,
// when non-empty, the root imap_poll_interval assignment. Other TOML text is
// retained byte-for-byte, including unrelated settings and comments.
func WriteMailConfig(path string, accounts []IMAPAccount, pollInterval string) error {
	path, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}
	validation := ValidateMailAccounts(accounts, pollInterval)
	if !validation.Valid {
		return fmt.Errorf("invalid mail configuration: %s", strings.Join(validation.Errors, "; "))
	}

	var original []byte
	mode := os.FileMode(0o600)
	if info, statErr := os.Stat(path); statErr == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("config path is not a regular file: %s", path)
		}
		mode = info.Mode().Perm()
		original, err = os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read config: %w", err)
		}
		var check map[string]interface{}
		if _, err := toml.Decode(string(original), &check); err != nil {
			return fmt.Errorf("parse config %s: %w", path, err)
		}
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("stat config: %w", statErr)
	}

	updated, err := replaceMailSection(string(original), accounts, pollInterval)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	if err := fsutil.AtomicWriteFile(path, []byte(updated), mode); err != nil {
		return fmt.Errorf("write config atomically: %w", err)
	}
	return nil
}

func replaceMailSection(original string, accounts []IMAPAccount, pollInterval string) (string, error) {
	section, err := renderMailSection(accounts)
	if err != nil {
		return "", err
	}
	lines := strings.SplitAfter(original, "\n")
	start, end := -1, -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "[[imap_accounts]]" {
			if start == -1 {
				start = i
			}
			continue
		}
		if start != -1 && i > start && strings.HasPrefix(trimmed, "[") && !strings.HasPrefix(trimmed, "[[imap_accounts]]") {
			end = i
			break
		}
	}
	if start == -1 {
		prefix := original
		if prefix != "" && !strings.HasSuffix(prefix, "\n") {
			prefix += "\n"
		}
		original = prefix + section
	} else {
		if end == -1 {
			end = len(lines)
		}
		lines = append(lines[:start], append([]string{section}, lines[end:]...)...)
		original = strings.Join(lines, "")
	}
	if pollInterval != "" {
		original = replaceRootPollInterval(original, pollInterval)
	}
	return original, nil
}

func replaceRootPollInterval(content, interval string) string {
	lines := strings.SplitAfter(content, "\n")
	inArray := false
	replacement := fmt.Sprintf("imap_poll_interval = %q\n", interval)
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[[") {
			inArray = true
		}
		if inArray && strings.HasPrefix(trimmed, "[") && !strings.HasPrefix(trimmed, "[[imap_accounts]]") {
			inArray = false
		}
		if !inArray && strings.HasPrefix(trimmed, "imap_poll_interval") {
			lines[i] = replacement
			return strings.Join(lines, "")
		}
	}
	return replacement + content
}

func renderMailSection(accounts []IMAPAccount) (string, error) {
	var b strings.Builder
	for _, account := range accounts {
		b.WriteString("[[imap_accounts]]\n")
		writeTOMLString(&b, "host", account.Host)
		fmt.Fprintf(&b, "port = %d\n", account.Port)
		writeTOMLString(&b, "username", account.Username)
		writeTOMLString(&b, "password_secret", account.PasswordSecret)
		if account.Folder != "" {
			writeTOMLString(&b, "folder", account.Folder)
		}
		writeTOMLStrings(&b, "from", account.From)
		writeTOMLStrings(&b, "subject", account.Subject)
		fmt.Fprintf(&b, "has_attachment = %t\n", account.HasAttachment)
		if account.Action != "" {
			writeTOMLString(&b, "action", account.Action)
		}
		if account.MoveTo != "" {
			writeTOMLString(&b, "move_to", account.MoveTo)
		}
		fmt.Fprintf(&b, "archive_mail = %t\n\n", account.ArchiveMail)
	}
	return b.String(), nil
}

func writeTOMLString(b *strings.Builder, key, value string) {
	fmt.Fprintf(b, "%s = %q\n", key, value)
}

func writeTOMLStrings(b *strings.Builder, key string, values []string) {
	fmt.Fprintf(b, "%s = [", key)
	for i, value := range values {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(b, "%q", value)
	}
	b.WriteString("]\n")
}
