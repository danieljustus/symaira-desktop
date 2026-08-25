package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPasswordSecretIsMaskedWithoutResolvingIt(t *testing.T) {
	if got := MaskPasswordSecret("symvault://imap/account"); got != "symvault://imap/account" {
		t.Fatalf("reference was not preserved: %q", got)
	}
	if got := MaskPasswordSecret("plain-secret"); got != "<redacted>" {
		t.Fatalf("plaintext was not redacted: %q", got)
	}
	if got := MaskPasswordSecret(""); got != "" {
		t.Fatalf("missing secret was not empty: %q", got)
	}
}

func TestWriteMailConfigPreservesUnrelatedTOMLAndUsesAtomicContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	original := `vault = "/tmp/vault"
archive = "/tmp/archive"
custom_flag = true

[[imap_accounts]]
host = "imap.example.com"
port = 993
username = "daniel"
password_secret = "plain-secret"
folder = "INBOX"
from = ["billing@example.com"]
subject = ["invoice"]
has_attachment = true
action = "mark_seen"
archive_mail = false

imap_poll_interval = "5m"
`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	accounts := []IMAPAccount{{
		Host:           "imap.example.com",
		Port:           993,
		Username:       "daniel",
		PasswordSecret: "symvault://imap/daniel",
		Folder:         "INBOX",
		From:           []string{"billing@example.com"},
		Subject:        []string{"invoice"},
		HasAttachment:  true,
		Action:         "mark_seen",
	}}
	if err := WriteMailConfig(path, accounts, "10m"); err != nil {
		t.Fatalf("WriteMailConfig: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, want := range []string{`vault = "/tmp/vault"`, `archive = "/tmp/archive"`, "custom_flag = true", `password_secret = "symvault://imap/daniel"`, `imap_poll_interval = "10m"`} {
		if !strings.Contains(content, want) {
			t.Errorf("rewritten config missing %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, "plain-secret") {
		t.Fatal("old plaintext secret leaked into rewritten config")
	}
	document, err := ReadMailConfig(path)
	if err != nil {
		t.Fatalf("ReadMailConfig: %v", err)
	}
	if len(document.Accounts) != 1 || document.Accounts[0].PasswordSecret != "symvault://imap/daniel" {
		t.Fatalf("unexpected decoded account: %+v", document.Accounts)
	}
	if got := NextWatchRestartSemantics(); !strings.Contains(got, "next symingest watch restart") {
		t.Fatalf("restart semantics missing: %q", got)
	}
}

func TestReadMailConfigRejectsMalformedTOML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.toml")
	if err := os.WriteFile(path, []byte("[[imap_accounts]\nhost = \"broken\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadMailConfig(path); err == nil {
		t.Fatal("expected malformed TOML error")
	}
}

func TestValidateMailAccountsRejectsInvalidActionAndMissingMoveTarget(t *testing.T) {
	report := ValidateMailAccounts([]IMAPAccount{{
		Host:           "imap.example.com",
		Port:           993,
		Username:       "daniel",
		PasswordSecret: "env://IMAP_PASSWORD",
		Action:         "move",
	}}, "not-a-duration")
	if report.Valid || len(report.Errors) < 2 {
		t.Fatalf("expected invalid report, got %+v", report)
	}
}
