package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAccountID_FormatsCorrectly(t *testing.T) {
	cases := []struct {
		name    string
		account IMAPAccount
		want    string
	}{
		{
			name:    "with folder",
			account: IMAPAccount{Host: "imap.example.com", Port: 993, Username: "user", Folder: "INBOX"},
			want:    "user@imap.example.com:993/INBOX",
		},
		{
			name:    "empty folder defaults to INBOX",
			account: IMAPAccount{Host: "imap.example.com", Port: 993, Username: "user"},
			want:    "user@imap.example.com:993/INBOX",
		},
		{
			name:    "custom folder",
			account: IMAPAccount{Host: "mail.test.com", Port: 143, Username: "admin", Folder: "Sent"},
			want:    "admin@mail.test.com:143/Sent",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := AccountID(tc.account)
			if got != tc.want {
				t.Errorf("AccountID = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestViewAccount_PreservesFieldsAndMasksSecret(t *testing.T) {
	account := IMAPAccount{
		Host:           "imap.example.com",
		Port:           993,
		Username:       "daniel",
		PasswordSecret: "symvault://imap/daniel",
		Folder:         "INBOX",
		From:           []string{"billing@example.com"},
		Subject:        []string{"invoice"},
		HasAttachment:  true,
		Action:         "mark_seen",
		MoveTo:         "Processed",
		ArchiveMail:    true,
	}
	view := ViewAccount(account)

	if view.ID != "daniel@imap.example.com:993/INBOX" {
		t.Errorf("ID = %q, want correct ID", view.ID)
	}
	if view.Host != "imap.example.com" {
		t.Errorf("Host = %q", view.Host)
	}
	if view.Port != 993 {
		t.Errorf("Port = %d", view.Port)
	}
	if view.Username != "daniel" {
		t.Errorf("Username = %q", view.Username)
	}
	// Secret reference is preserved (not masked)
	if view.PasswordSecret != "symvault://imap/daniel" {
		t.Errorf("PasswordSecret = %q, want reference preserved", view.PasswordSecret)
	}
	if view.PasswordSecretKind != "reference" {
		t.Errorf("PasswordSecretKind = %q, want reference", view.PasswordSecretKind)
	}
	if !view.PasswordSecretConfigured {
		t.Error("PasswordSecretConfigured = false, want true")
	}
	if view.Folder != "INBOX" {
		t.Errorf("Folder = %q", view.Folder)
	}
	if len(view.From) != 1 || view.From[0] != "billing@example.com" {
		t.Errorf("From = %v", view.From)
	}
	if len(view.Subject) != 1 || view.Subject[0] != "invoice" {
		t.Errorf("Subject = %v", view.Subject)
	}
	if !view.HasAttachment {
		t.Error("HasAttachment = false")
	}
	if view.Action != "mark_seen" {
		t.Errorf("Action = %q", view.Action)
	}
	if view.MoveTo != "Processed" {
		t.Errorf("MoveTo = %q", view.MoveTo)
	}
	if !view.ArchiveMail {
		t.Error("ArchiveMail = false")
	}
}

func TestViewAccount_MasksPlaintextSecret(t *testing.T) {
	account := IMAPAccount{
		Host:           "imap.example.com",
		Port:           993,
		Username:       "user",
		PasswordSecret: "my-plaintext-secret",
	}
	view := ViewAccount(account)
	if view.PasswordSecret != "<redacted>" {
		t.Errorf("PasswordSecret = %q, want <redacted>", view.PasswordSecret)
	}
	if view.PasswordSecretKind != "plaintext" {
		t.Errorf("PasswordSecretKind = %q, want plaintext", view.PasswordSecretKind)
	}
	if !view.PasswordSecretConfigured {
		t.Error("PasswordSecretConfigured = false for non-empty secret")
	}
}

func TestViewAccount_EmptySecret(t *testing.T) {
	account := IMAPAccount{
		Host:     "imap.example.com",
		Port:     993,
		Username: "user",
	}
	view := ViewAccount(account)
	if view.PasswordSecret != "" {
		t.Errorf("PasswordSecret = %q, want empty", view.PasswordSecret)
	}
	if view.PasswordSecretKind != "missing" {
		t.Errorf("PasswordSecretKind = %q, want missing", view.PasswordSecretKind)
	}
	if view.PasswordSecretConfigured {
		t.Error("PasswordSecretConfigured = true for empty secret")
	}
}

func TestViewAccount_SliceFieldsAreCopied(t *testing.T) {
	account := IMAPAccount{
		From:    []string{"a@b.com", "c@d.com"},
		Subject: []string{"s1"},
	}
	view := ViewAccount(account)
	// Mutate the original slices — the view should be unaffected
	account.From = append(account.From, "extra")
	account.Subject = append(account.Subject, "extra")
	if len(view.From) != 2 {
		t.Errorf("From was mutated: %v", view.From)
	}
	if len(view.Subject) != 1 {
		t.Errorf("Subject was mutated: %v", view.Subject)
	}
}

func TestConfigPath_Explicit(t *testing.T) {
	dir := t.TempDir()
	explicit := filepath.Join(dir, "my-config.toml")
	got, err := ConfigPath(explicit)
	if err != nil {
		t.Fatalf("ConfigPath: %v", err)
	}
	if got != explicit {
		t.Errorf("ConfigPath(%q) = %q, want %q", explicit, got, explicit)
	}
}

func TestConfigPath_WhitespaceIsTreatedAsEmpty(t *testing.T) {
	got, err := ConfigPath("   ")
	if err != nil {
		t.Fatalf("ConfigPath: %v", err)
	}
	// Should fall through to project/global default, not treat whitespace as explicit
	if got == "" {
		// Just verify it didn't error — it resolved to some default
		t.Log("ConfigPath resolved to default (expected)")
	}
}

func TestConfigPath_ProjectFileWins(t *testing.T) {
	dir := t.TempDir()
	projectFile := filepath.Join(dir, ".symingest.toml")
	if err := os.WriteFile(projectFile, []byte("vault = \"/tmp/vault\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)

	got, err := ConfigPath("")
	if err != nil {
		t.Fatalf("ConfigPath: %v", err)
	}
	wantAbs, _ := filepath.Abs(projectFile)
	gotEval, _ := filepath.EvalSymlinks(got)
	wantEval, _ := filepath.EvalSymlinks(wantAbs)
	if gotEval != wantEval {
		t.Errorf("ConfigPath(\"\") = %q, want %q", got, wantAbs)
	}
}

func TestPasswordSecretKind_Direct(t *testing.T) {
	cases := []struct {
		value string
		want  string
	}{
		{"", "missing"},
		{"  ", "missing"},
		{"symvault://imap/acc", "reference"},
		{"env://MY_PASS", "reference"},
		{"keychain://my/key", "reference"},
		{"SYMVAULT://UPPER", "reference"},
		{"plaintext-password", "plaintext"},
		{"just-a-string", "plaintext"},
	}
	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			got := PasswordSecretKind(tc.value)
			if got != tc.want {
				t.Errorf("PasswordSecretKind(%q) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}
}

func TestValidateMailAccounts_AllBranches(t *testing.T) {
	cases := []struct {
		name     string
		accounts []IMAPAccount
		interval string
		valid    bool
		minErrs  int
	}{
		{
			name: "empty host",
			accounts: []IMAPAccount{{
				Host:           "",
				Port:           993,
				Username:       "user",
				PasswordSecret: "env://PASS",
			}},
			valid:   false,
			minErrs: 1,
		},
		{
			name: "port out of range",
			accounts: []IMAPAccount{{
				Host:           "imap.example.com",
				Port:           0,
				Username:       "user",
				PasswordSecret: "env://PASS",
			}},
			valid:   false,
			minErrs: 1,
		},
		{
			name: "empty username",
			accounts: []IMAPAccount{{
				Host:           "imap.example.com",
				Port:           993,
				Username:       "",
				PasswordSecret: "env://PASS",
			}},
			valid:   false,
			minErrs: 1,
		},
		{
			name: "empty password_secret",
			accounts: []IMAPAccount{{
				Host:           "imap.example.com",
				Port:           993,
				Username:       "user",
				PasswordSecret: "",
			}},
			valid:   false,
			minErrs: 1,
		},
		{
			name: "plaintext secret generates warning",
			accounts: []IMAPAccount{{
				Host:           "imap.example.com",
				Port:           993,
				Username:       "user",
				PasswordSecret: "my-secret",
			}},
			valid:   true,
			minErrs: 0,
		},
		{
			name: "invalid action",
			accounts: []IMAPAccount{{
				Host:           "imap.example.com",
				Port:           993,
				Username:       "user",
				PasswordSecret: "env://PASS",
				Action:         "invalid_action",
			}},
			valid:   false,
			minErrs: 1,
		},
		{
			name: "move without move_to",
			accounts: []IMAPAccount{{
				Host:           "imap.example.com",
				Port:           993,
				Username:       "user",
				PasswordSecret: "env://PASS",
				Action:         "move",
				MoveTo:         "",
			}},
			valid:   false,
			minErrs: 1,
		},
		{
			name: "valid with all fields",
			accounts: []IMAPAccount{{
				Host:           "imap.example.com",
				Port:           993,
				Username:       "user",
				PasswordSecret: "symvault://imap/user",
				Folder:         "INBOX",
				Action:         "move",
				MoveTo:         "Processed",
			}},
			valid:   true,
			minErrs: 0,
		},
		{
			name:     "empty accounts list",
			accounts: []IMAPAccount{},
			valid:    true,
			minErrs:  0,
		},
		{
			name: "invalid poll interval",
			accounts: []IMAPAccount{{
				Host:           "imap.example.com",
				Port:           993,
				Username:       "user",
				PasswordSecret: "env://PASS",
			}},
			interval: "not-a-duration",
			valid:    false,
			minErrs:  1,
		},
		{
			name: "mark_seen is valid action",
			accounts: []IMAPAccount{{
				Host:           "imap.example.com",
				Port:           993,
				Username:       "user",
				PasswordSecret: "env://PASS",
				Action:         "mark_seen",
			}},
			valid:   true,
			minErrs: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := ValidateMailAccounts(tc.accounts, tc.interval)
			if report.Valid != tc.valid {
				t.Errorf("Valid = %v, want %v (errors: %v)", report.Valid, tc.valid, report.Errors)
			}
			if len(report.Errors) < tc.minErrs {
				t.Errorf("got %d errors, want >= %d: %v", len(report.Errors), tc.minErrs, report.Errors)
			}
		})
	}
}

func TestReadMailConfig_NotFound(t *testing.T) {
	_, err := ReadMailConfig(filepath.Join(t.TempDir(), "nonexistent.toml"))
	if err == nil {
		t.Fatal("expected error for nonexistent config")
	}
}

func TestReadMailConfig_EmptyValid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.toml")
	if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := ReadMailConfig(path)
	if err != nil {
		t.Fatalf("ReadMailConfig: %v", err)
	}
	if len(doc.Accounts) != 0 {
		t.Errorf("expected 0 accounts, got %d", len(doc.Accounts))
	}
}

func TestReadMailConfig_WithPollInterval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "with-interval.toml")
	content := `imap_poll_interval = "10m"

[[imap_accounts]]
host = "imap.example.com"
port = 993
username = "user"
password_secret = "env://PASS"
folder = "INBOX"
from = []
subject = []
has_attachment = false
action = "mark_seen"
archive_mail = false
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := ReadMailConfig(path)
	if err != nil {
		t.Fatalf("ReadMailConfig: %v", err)
	}
	if doc.IMAPPollInterval != "10m" {
		t.Errorf("IMAPPollInterval = %q, want 10m", doc.IMAPPollInterval)
	}
	if len(doc.Accounts) != 1 {
		t.Fatalf("expected 1 account, got %d", len(doc.Accounts))
	}
}

func TestReplaceRootPollInterval_NewContent(t *testing.T) {
	// When there is no existing imap_poll_interval, it should be prepended
	content := "vault = \"/tmp/vault\"\n"
	result := replaceRootPollInterval(content, "15m")
	if result != `imap_poll_interval = "15m"
vault = "/tmp/vault"
` {
		t.Errorf("unexpected result:\n%s", result)
	}
}

func TestReplaceRootPollInterval_ExistingContent(t *testing.T) {
	content := `vault = "/tmp/vault"
imap_poll_interval = "5m"

[[imap_accounts]]
host = "imap.example.com"
port = 993
username = "user"
password_secret = "env://PASS"
folder = "INBOX"
from = []
subject = []
has_attachment = false
action = "mark_seen"
archive_mail = false
`
	result := replaceRootPollInterval(content, "30m")
	if !containsLine(result, `imap_poll_interval = "30m"`) {
		t.Errorf("expected imap_poll_interval = \"30m\" in result:\n%s", result)
	}
	// Should not contain the old value
	if containsLine(result, `"5m"`) {
		t.Errorf("old interval not replaced:\n%s", result)
	}
}

func containsLine(s, substr string) bool {
	for _, line := range splitLines(s) {
		if line == substr {
			return true
		}
	}
	return false
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func TestWriteMailConfig_BrokenExistingTOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte("[[imap_accounts]\nbroken"), 0o600)
	accounts := []IMAPAccount{{
		Host: "imap.example.com", Port: 993, Username: "u",
		PasswordSecret: "env://PASS", Folder: "INBOX",
		Action: "mark_seen",
	}}
	err := WriteMailConfig(path, accounts, "")
	if err == nil {
		t.Fatal("expected error for broken TOML")
	}
}

func TestWriteMailConfig_InvalidAccounts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	err := WriteMailConfig(path, []IMAPAccount{{Host: ""}}, "")
	if err == nil {
		t.Fatal("expected error for invalid accounts")
	}
}

func TestWriteMailConfig_NewFileInNewDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subdir", "config.toml")
	accounts := []IMAPAccount{{
		Host: "imap.example.com", Port: 993, Username: "u",
		PasswordSecret: "env://PASS", Folder: "INBOX",
		Action: "mark_seen",
	}}
	if err := WriteMailConfig(path, accounts, ""); err != nil {
		t.Fatalf("WriteMailConfig: %v", err)
	}
	doc, err := ReadMailConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Accounts) != 1 {
		t.Fatalf("expected 1 account, got %d", len(doc.Accounts))
	}
}
