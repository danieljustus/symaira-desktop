package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunMailJSONMasksPlaintextAndReportsRestartRequirement(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	configText := `vault = "/tmp/vault"

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
`
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}

	out := withCapturedStdout(t)
	if err := run([]string{"mail", "--json", "--config", configPath, "list"}); err != nil {
		t.Fatalf("mail list: %v", err)
	}
	var listed struct {
		SchemaVersion int `json:"schema_version"`
		Accounts      []struct {
			PasswordSecret string `json:"password_secret"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal([]byte(out.String()), &listed); err != nil {
		t.Fatalf("decode list response: %v\n%s", err, out.String())
	}
	if listed.SchemaVersion != 1 || len(listed.Accounts) != 1 {
		t.Fatalf("unexpected list response: %+v", listed)
	}
	if listed.Accounts[0].PasswordSecret != "<redacted>" || strings.Contains(out.String(), "plain-secret") {
		t.Fatalf("plaintext secret leaked: %s", out.String())
	}

	inputPath := filepath.Join(dir, "update.json")
	if err := os.WriteFile(inputPath, []byte(`{"host":"imap.changed.example.com","port":993,"username":"daniel","folder":"INBOX","action":"mark_seen"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := run([]string{"mail", "--json", "--config", configPath, "--input", inputPath, "update", "0"}); err != nil {
		t.Fatalf("mail update: %v", err)
	}
	var updated struct {
		ReloadRequired bool   `json:"reload_required"`
		Semantics      string `json:"reload_semantics"`
	}
	if err := json.Unmarshal([]byte(out.String()), &updated); err != nil {
		t.Fatalf("decode update response: %v\n%s", err, out.String())
	}
	if !updated.ReloadRequired || !strings.Contains(updated.Semantics, "next symingest watch restart") {
		t.Fatalf("missing restart semantics: %+v", updated)
	}
	written, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), "imap.changed.example.com") || !strings.Contains(string(written), "plain-secret") {
		t.Fatalf("update did not preserve the existing secret safely: %s", written)
	}
}
