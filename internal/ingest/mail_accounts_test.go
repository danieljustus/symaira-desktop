package ingest

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestMailAccountsCRUDRoundTrip(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.toml")

	list, err := ListMailAccounts(configPath)
	if err != nil {
		t.Fatalf("ListMailAccounts failed: %v", err)
	}
	if len(list.Accounts) != 0 {
		t.Errorf("expected no accounts, got %+v", list.Accounts)
	}
	if list.ReloadRequired {
		t.Error("list must not require a reload")
	}

	created, err := CreateMailAccount(configPath, MailAccountRecord{
		Host:           "imap.example.com",
		Port:           993,
		Username:       "user@example.com",
		PasswordSecret: "symvault://mail/example",
		Folder:         "INBOX",
		From:           []string{"billing@example.com"},
		Action:         "mark_seen",
	})
	if err != nil {
		t.Fatalf("CreateMailAccount failed: %v", err)
	}
	if len(created.Accounts) != 1 {
		t.Fatalf("expected 1 account, got %d", len(created.Accounts))
	}
	account := created.Accounts[0]
	if account.PasswordSecret != "symvault://mail/example" {
		t.Errorf("expected the secret reference unmasked, got %q", account.PasswordSecret)
	}
	if !created.ReloadRequired {
		t.Error("expected ReloadRequired after create")
	}

	// Round-trip: list reads back exactly what create wrote.
	relisted, err := ListMailAccounts(configPath)
	if err != nil {
		t.Fatalf("ListMailAccounts failed: %v", err)
	}
	if len(relisted.Accounts) != 1 || !reflect.DeepEqual(relisted.Accounts[0], account) {
		t.Errorf("round-trip mismatch: got %+v, want %+v", relisted.Accounts, account)
	}

	updateInput := account
	updateInput.Folder = "Archive"
	updated, err := UpdateMailAccount(configPath, account.ID, updateInput)
	if err != nil {
		t.Fatalf("UpdateMailAccount failed: %v", err)
	}
	if len(updated.Accounts) != 1 || updated.Accounts[0].Folder != "Archive" {
		t.Fatalf("unexpected update result: %+v", updated.Accounts)
	}
	if updated.Accounts[0].PasswordSecret != "symvault://mail/example" {
		t.Errorf("expected stored secret preserved, got %q", updated.Accounts[0].PasswordSecret)
	}

	deleted, err := DeleteMailAccount(configPath, updated.Accounts[0].ID)
	if err != nil {
		t.Fatalf("DeleteMailAccount failed: %v", err)
	}
	if len(deleted.Accounts) != 0 {
		t.Errorf("expected no accounts after delete, got %+v", deleted.Accounts)
	}
}

func TestMailAccountUpdatePreservesMaskedPlaintextSecret(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.toml")

	created, err := CreateMailAccount(configPath, MailAccountRecord{
		Host: "imap.example.com", Port: 993, Username: "user@example.com",
		PasswordSecret: "hunter2", Folder: "INBOX", Action: "mark_seen",
	})
	if err != nil {
		t.Fatalf("CreateMailAccount failed: %v", err)
	}
	account := created.Accounts[0]
	if account.PasswordSecret != "<redacted>" {
		t.Fatalf("expected the plaintext secret masked on read, got %q", account.PasswordSecret)
	}

	// A client only ever sees the masked placeholder; sending it back on
	// update must not overwrite the real stored secret with the literal
	// string "<redacted>" (issue #609 hard constraint).
	updateInput := account
	updateInput.Folder = "Archive"
	updated, err := UpdateMailAccount(configPath, account.ID, updateInput)
	if err != nil {
		t.Fatalf("UpdateMailAccount failed: %v", err)
	}
	if updated.Accounts[0].PasswordSecret != "<redacted>" {
		t.Fatalf("expected the secret to still read as masked, got %q", updated.Accounts[0].PasswordSecret)
	}

	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `password_secret = "hunter2"`) {
		t.Errorf("expected the stored secret to remain %q, config:\n%s", "hunter2", raw)
	}
	if strings.Contains(string(raw), "<redacted>") {
		t.Error("the masked placeholder must never be written to the config file")
	}
}

func TestMailAccountUpdateDeleteMissingConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "missing.toml")

	if _, err := UpdateMailAccount(configPath, "someone@example.com:993/INBOX", MailAccountRecord{}); err == nil {
		t.Error("expected error updating an account with no config file present")
	}
	if _, err := DeleteMailAccount(configPath, "someone@example.com:993/INBOX"); err == nil {
		t.Error("expected error deleting an account with no config file present")
	}
}

func TestMailAccountUnknownID(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if _, err := CreateMailAccount(configPath, MailAccountRecord{Host: "h", Port: 993, Username: "u", PasswordSecret: "s", Action: "mark_seen"}); err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateMailAccount(configPath, "nonexistent-id", MailAccountRecord{}); err == nil {
		t.Error("expected error updating an unknown account id")
	}
	if _, err := DeleteMailAccount(configPath, "nonexistent-id"); err == nil {
		t.Error("expected error deleting an unknown account id")
	}
}

func TestSetMailAccounts(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")

	result, err := SetMailAccounts(configPath, []MailAccountRecord{
		{Host: "imap.example.com", Port: 993, Username: "a@example.com", PasswordSecret: "symvault://a", Action: "mark_seen"},
		{Host: "imap.example.com", Port: 993, Username: "b@example.com", PasswordSecret: "symvault://b", Action: "mark_seen"},
	}, "")
	if err != nil {
		t.Fatalf("SetMailAccounts failed: %v", err)
	}
	if len(result.Accounts) != 2 {
		t.Fatalf("expected 2 accounts, got %d", len(result.Accounts))
	}

	if _, err := SetMailAccounts(configPath, []MailAccountRecord{
		{Port: 993, Username: "x", PasswordSecret: "s", Action: "mark_seen"},
	}, ""); err == nil {
		t.Error("expected validation error for a missing host")
	}
}

// A client reads accounts (masked) and writes the list back. Without secret
// preservation that round-trip replaces every plaintext password on disk with
// the mask, silently locking the user out of their own mail accounts.
func TestSetMailAccountsPreservesStoredSecretOnMaskedRoundTrip(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")

	created, err := CreateMailAccount(configPath, MailAccountRecord{
		Host: "imap.example.com", Port: 993, Username: "a@example.com",
		PasswordSecret: "plaintext-password", Folder: "INBOX", Action: "mark_seen",
	})
	if err != nil {
		t.Fatalf("CreateMailAccount failed: %v", err)
	}
	if got := created.Accounts[0].PasswordSecret; got == "plaintext-password" {
		t.Fatalf("plaintext secret was returned unmasked: %q", got)
	}

	// Write back exactly what a reader would have received.
	if _, err := SetMailAccounts(configPath, created.Accounts, ""); err != nil {
		t.Fatalf("SetMailAccounts failed: %v", err)
	}

	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "plaintext-password") {
		t.Errorf("stored secret was destroyed by the masked round-trip:\n%s", raw)
	}
}
