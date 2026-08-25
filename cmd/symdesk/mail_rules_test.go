package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/ingest"
)

func TestMailRulesCreateListDeleteJSON(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "mail.toml")

	jsonFlag = true
	t.Cleanup(func() { jsonFlag = false })

	createCmd := newMailRulesCreateCmd(&configPath)
	payload := `{"host":"imap.example.com","port":993,"username":"user@example.com","password_secret":"symvault://mail/example","folder":"INBOX","action":"mark_seen"}`
	createCmd.SetIn(strings.NewReader(payload))

	out, err := runCommand(t, createCmd, nil)
	if err != nil {
		t.Fatalf("mail rules create failed: %v", err)
	}
	var createResp mailRulesResponse
	if err := json.Unmarshal([]byte(out), &createResp); err != nil {
		t.Fatalf("mail rules create output is not valid JSON: %v\noutput: %s", err, out)
	}
	if createResp.SchemaVersion != ingest.SchemaVersion || createResp.Operation != "create" {
		t.Fatalf("unexpected create response header: %+v", createResp)
	}
	if len(createResp.Accounts) != 1 || !createResp.ReloadRequired {
		t.Fatalf("unexpected create response: %+v", createResp)
	}
	account := createResp.Accounts[0]
	if account.ID == "" {
		t.Fatal("expected non-empty account id")
	}
	if account.PasswordSecret != "symvault://mail/example" {
		t.Errorf("expected the secret reference unchanged, got %q", account.PasswordSecret)
	}

	listCmd := newMailRulesListCmd(&configPath)
	listOut, err := runCommand(t, listCmd, nil)
	if err != nil {
		t.Fatalf("mail rules list failed: %v", err)
	}
	var listResp mailRulesResponse
	if err := json.Unmarshal([]byte(listOut), &listResp); err != nil {
		t.Fatalf("mail rules list output is not valid JSON: %v\noutput: %s", err, listOut)
	}
	if listResp.Operation != "list" || len(listResp.Accounts) != 1 || listResp.Accounts[0].ID != account.ID {
		t.Fatalf("round-trip mismatch: %+v", listResp)
	}

	deleteCmd := newMailRulesDeleteCmd(&configPath)
	deleteOut, err := runCommand(t, deleteCmd, []string{account.ID})
	if err != nil {
		t.Fatalf("mail rules delete failed: %v", err)
	}
	var deleteResp mailRulesResponse
	if err := json.Unmarshal([]byte(deleteOut), &deleteResp); err != nil {
		t.Fatalf("mail rules delete output is not valid JSON: %v\noutput: %s", err, deleteOut)
	}
	if deleteResp.Operation != "delete" || len(deleteResp.Accounts) != 0 {
		t.Fatalf("unexpected delete response: %+v", deleteResp)
	}
}

func TestMailRulesUpdatePreservesMaskedSecret(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "mail.toml")

	jsonFlag = true
	t.Cleanup(func() { jsonFlag = false })

	createCmd := newMailRulesCreateCmd(&configPath)
	createCmd.SetIn(strings.NewReader(`{"host":"imap.example.com","port":993,"username":"user@example.com","password_secret":"hunter2","folder":"INBOX","action":"mark_seen"}`))
	out, err := runCommand(t, createCmd, nil)
	if err != nil {
		t.Fatalf("mail rules create failed: %v", err)
	}
	var createResp mailRulesResponse
	if err := json.Unmarshal([]byte(out), &createResp); err != nil {
		t.Fatalf("mail rules create output is not valid JSON: %v\noutput: %s", err, out)
	}
	account := createResp.Accounts[0]
	if account.PasswordSecret != "<redacted>" {
		t.Fatalf("expected the plaintext secret masked on read, got %q", account.PasswordSecret)
	}

	// A client reading `mail rules list`/`create` only ever sees the masked
	// placeholder; echoing it back on update must not clobber the real
	// stored secret with the literal placeholder string.
	updateInput := account
	updateInput.Folder = "Archive"
	updatePayload, err := json.Marshal(updateInput)
	if err != nil {
		t.Fatal(err)
	}
	updateCmd := newMailRulesUpdateCmd(&configPath)
	updateCmd.SetIn(strings.NewReader(string(updatePayload)))
	updateOut, err := runCommand(t, updateCmd, []string{account.ID})
	if err != nil {
		t.Fatalf("mail rules update failed: %v", err)
	}
	var updateResp mailRulesResponse
	if err := json.Unmarshal([]byte(updateOut), &updateResp); err != nil {
		t.Fatalf("mail rules update output is not valid JSON: %v\noutput: %s", err, updateOut)
	}
	if len(updateResp.Accounts) != 1 || updateResp.Accounts[0].Folder != "Archive" {
		t.Fatalf("unexpected update response: %+v", updateResp)
	}
	if updateResp.Accounts[0].PasswordSecret != "<redacted>" {
		t.Fatalf("expected the secret to still read as masked, got %q", updateResp.Accounts[0].PasswordSecret)
	}
}

func TestMailRulesNoConfigFileError(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "missing.toml")

	jsonFlag = false
	t.Cleanup(func() { jsonFlag = false })

	updateCmd := newMailRulesUpdateCmd(&configPath)
	updateCmd.SetIn(strings.NewReader(`{"host":"h","port":993,"username":"u","password_secret":"s","action":"mark_seen"}`))
	if _, err := runCommand(t, updateCmd, []string{"someone@example.com:993/INBOX"}); err == nil {
		t.Error("expected error updating an account with no config file present")
	}

	deleteCmd := newMailRulesDeleteCmd(&configPath)
	if _, err := runCommand(t, deleteCmd, []string{"someone@example.com:993/INBOX"}); err == nil {
		t.Error("expected error deleting an account with no config file present")
	}
}

func TestMailRulesUnknownAccountID(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "mail.toml")

	jsonFlag = false
	t.Cleanup(func() { jsonFlag = false })

	createCmd := newMailRulesCreateCmd(&configPath)
	createCmd.SetIn(strings.NewReader(`{"host":"imap.example.com","port":993,"username":"user@example.com","password_secret":"symvault://x","action":"mark_seen"}`))
	if _, err := runCommand(t, createCmd, nil); err != nil {
		t.Fatalf("mail rules create failed: %v", err)
	}

	deleteCmd := newMailRulesDeleteCmd(&configPath)
	if _, err := runCommand(t, deleteCmd, []string{"nonexistent-id"}); err == nil {
		t.Error("expected error deleting an unknown account id")
	}
}
