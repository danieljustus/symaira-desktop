package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-ingest/internal/config"
)

func TestIsMaskedSecret(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"<redacted>", true},
		{"***", true},
		{"[redacted]", true},
		{"", false},
		{"plaintext", false},
		{"symvault://key", false},
		{"  <redacted>  ", false},
	}
	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			if got := isMaskedSecret(tc.value); got != tc.want {
				t.Errorf("isMaskedSecret(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

func TestWithSecretMarker(t *testing.T) {
	if got := withSecretMarker(true); got != "present" {
		t.Errorf("withSecretMarker(true) = %q, want present", got)
	}
	if got := withSecretMarker(false); got != "" {
		t.Errorf("withSecretMarker(false) = %q, want empty", got)
	}
}

func TestRawAccountField(t *testing.T) {
	raw := json.RawMessage(`{"host":"imap.example.com","port":993,"password_secret":"env://PASS"}`)

	val, ok := rawAccountField(raw, "host")
	if !ok || string(val) != `"imap.example.com"` {
		t.Errorf("rawAccountField host = %q, %v", val, ok)
	}

	val, ok = rawAccountField(raw, "password_secret")
	if !ok || string(val) != `"env://PASS"` {
		t.Errorf("rawAccountField password_secret = %q, %v", val, ok)
	}

	_, ok = rawAccountField(raw, "nonexistent")
	if ok {
		t.Error("expected false for nonexistent field")
	}

	_, ok = rawAccountField(json.RawMessage(`"not-an-object"`), "host")
	if ok {
		t.Error("expected false for non-object input")
	}
}

func TestNoteTitle_FrontmatterTitle(t *testing.T) {
	content := "---\ntitle: My Invoice\n---\nBody text"
	got := noteTitle(content, "/vault/note.md")
	if got != "My Invoice" {
		t.Errorf("noteTitle = %q, want My Invoice", got)
	}
}

func TestNoteTitle_PaperlessTitle(t *testing.T) {
	content := "---\npaperless:\n  title: Paperless Title\n---\nBody text"
	got := noteTitle(content, "/vault/note.md")
	if got != "Paperless Title" {
		t.Errorf("noteTitle = %q, want Paperless Title", got)
	}
}

func TestNoteTitle_HeadingFallback(t *testing.T) {
	content := "# Important Document\nSome body text"
	got := noteTitle(content, "/vault/note.md")
	if got != "Important Document" {
		t.Errorf("noteTitle = %q, want Important Document", got)
	}
}

func TestNoteTitle_FilenameFallback(t *testing.T) {
	content := "plain body without heading"
	got := noteTitle(content, "/vault/my-document.md")
	if got != "my-document" {
		t.Errorf("noteTitle = %q, want my-document", got)
	}
}

func TestNoteTitle_EmptyContent(t *testing.T) {
	got := noteTitle("", "/vault/scan.pdf.md")
	if got != "scan.pdf" {
		t.Errorf("noteTitle = %q, want scan.pdf", got)
	}
}

func TestNoteTitle_FrontmatterTitleEmpty(t *testing.T) {
	content := "---\ntitle: \"\"\n---\nBody text"
	got := noteTitle(content, "/vault/note.md")
	if got != "note" {
		t.Errorf("noteTitle = %q, want note (filename fallback)", got)
	}
}

func TestFormatDryRunHuman(t *testing.T) {
	old := stdout
	defer func() { stdout = old }()
	r, w, _ := os.Pipe()
	stdout = w

	response := &rulesDryRunJSONResponse{
		MatchedDocuments: 5,
		TotalDocuments:   20,
		SkippedDocuments: 3,
		ProposedRule:     dryRunRule{Pattern: "invoice", Kind: "category", Value: "Finance"},
		Matches: []rulesDryRunMatch{
			{NotePath: "/vault/doc1.md", Title: "Invoice 1"},
			{NotePath: "/vault/doc2.md", Title: "Invoice 2"},
		},
	}
	formatDryRunHuman(response)
	w.Close()

	var buf bytes.Buffer
	buf.ReadFrom(r)
	out := buf.String()

	if !strings.Contains(out, "5/20") {
		t.Errorf("output missing counts: %s", out)
	}
	if !strings.Contains(out, "invoice") {
		t.Errorf("output missing pattern: %s", out)
	}
	if !strings.Contains(out, "Invoice 1") {
		t.Errorf("output missing match title: %s", out)
	}
	if !strings.Contains(out, "Skipped documents: 3") {
		t.Errorf("output missing skipped count: %s", out)
	}
}

func TestFormatDryRunHuman_NoSkips(t *testing.T) {
	old := stdout
	defer func() { stdout = old }()
	r, w, _ := os.Pipe()
	stdout = w

	response := &rulesDryRunJSONResponse{
		MatchedDocuments: 2,
		TotalDocuments:   2,
		SkippedDocuments: 0,
		ProposedRule:     dryRunRule{Pattern: "test", Kind: "tag", Value: "testing"},
	}
	formatDryRunHuman(response)
	w.Close()

	var buf bytes.Buffer
	buf.ReadFrom(r)
	out := buf.String()

	if strings.Contains(out, "Skipped") {
		t.Errorf("should not mention skipped when 0: %s", out)
	}
}

func TestRunMailValidate_ValidConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	content := `[[imap_accounts]]
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
	os.WriteFile(configPath, []byte(content), 0o600)

	err := runMailValidate(configPath, false)
	if err != nil {
		t.Fatalf("runMailValidate: %v", err)
	}
}

func TestRunMailValidate_InvalidConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	content := `[[imap_accounts]]
host = ""
port = 993
username = ""
password_secret = ""
folder = "INBOX"
from = []
subject = []
has_attachment = false
action = "mark_seen"
archive_mail = false
`
	os.WriteFile(configPath, []byte(content), 0o600)

	err := runMailValidate(configPath, false)
	if err == nil {
		t.Fatal("expected error for invalid config")
	}
}

func TestRunMailValidate_NonexistentFile(t *testing.T) {
	err := runMailValidate(filepath.Join(t.TempDir(), "nonexistent.toml"), false)
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestRunMailValidate_JSON(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	content := `[[imap_accounts]]
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
	os.WriteFile(configPath, []byte(content), 0o600)

	err := runMailValidate(configPath, true)
	if err != nil {
		t.Fatalf("runMailValidate --json: %v", err)
	}
}

func TestFindMailAccount_ByIndex(t *testing.T) {
	accounts := []config.IMAPAccount{
		{Host: "a.com", Port: 993, Username: "u1"},
		{Host: "b.com", Port: 993, Username: "u2"},
	}
	idx, err := findMailAccount(accounts, "0")
	if err != nil {
		t.Fatal(err)
	}
	if idx != 0 {
		t.Errorf("index = %d, want 0", idx)
	}
}

func TestFindMailAccount_ByAccountID(t *testing.T) {
	accounts := []config.IMAPAccount{
		{Host: "a.com", Port: 993, Username: "u1", Folder: "INBOX"},
	}
	idx, err := findMailAccount(accounts, "u1@a.com:993/INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if idx != 0 {
		t.Errorf("index = %d, want 0", idx)
	}
}

func TestFindMailAccount_EmptySelector(t *testing.T) {
	_, err := findMailAccount(nil, "")
	if err == nil {
		t.Fatal("expected error for empty selector")
	}
}

func TestFindMailAccount_NotFound(t *testing.T) {
	accounts := []config.IMAPAccount{
		{Host: "a.com", Port: 993, Username: "u1"},
	}
	_, err := findMailAccount(accounts, "999")
	if err == nil {
		t.Fatal("expected error for nonexistent account")
	}
}

func TestPrintMailList_EmptyAccounts(t *testing.T) {
	old := stdout
	defer func() { stdout = old }()
	r, w, _ := os.Pipe()
	stdout = w

	doc := &config.MailConfigDocument{
		Path:     "/tmp/config.toml",
		Accounts: []config.IMAPAccount{},
	}
	err := printMailList(doc, false, nil)
	if err != nil {
		t.Fatalf("printMailList: %v", err)
	}
	w.Close()

	var buf bytes.Buffer
	buf.ReadFrom(r)
	out := buf.String()
	if !strings.Contains(out, "No IMAP accounts") {
		t.Errorf("expected 'No IMAP accounts' message, got: %s", out)
	}
}

func TestPrintMailList_WithAccounts(t *testing.T) {
	old := stdout
	defer func() { stdout = old }()
	r, w, _ := os.Pipe()
	stdout = w

	doc := &config.MailConfigDocument{
		Path: "/tmp/config.toml",
		Accounts: []config.IMAPAccount{
			{Host: "imap.example.com", Port: 993, Username: "user", Folder: "INBOX"},
		},
	}
	err := printMailList(doc, false, nil)
	if err != nil {
		t.Fatalf("printMailList: %v", err)
	}
	w.Close()

	var buf bytes.Buffer
	buf.ReadFrom(r)
	out := buf.String()
	if !strings.Contains(out, "user@imap.example.com") {
		t.Errorf("expected account ID in output, got: %s", out)
	}
	if !strings.Contains(out, "last poll: never") {
		t.Errorf("expected 'last poll: never' for an account with no recorded poll, got: %s", out)
	}
}

func TestPrintMailList_WithPollStatus(t *testing.T) {
	old := stdout
	defer func() { stdout = old }()
	r, w, _ := os.Pipe()
	stdout = w

	acc := config.IMAPAccount{Host: "imap.example.com", Port: 993, Username: "user", Folder: "INBOX"}
	doc := &config.MailConfigDocument{
		Path:     "/tmp/config.toml",
		Accounts: []config.IMAPAccount{acc},
	}
	polledAt := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	pollStatus := map[string]mailPollStatus{
		config.AccountID(acc): {LastPolledAt: polledAt, Status: "error", LastError: "network timeout"},
	}
	err := printMailList(doc, false, pollStatus)
	if err != nil {
		t.Fatalf("printMailList: %v", err)
	}
	w.Close()

	var buf bytes.Buffer
	buf.ReadFrom(r)
	out := buf.String()
	if !strings.Contains(out, "last poll: "+polledAt.Format(time.RFC3339)+" failed (network timeout)") {
		t.Errorf("expected last poll failure line, got: %s", out)
	}
}

func TestPrintMailList_JSON(t *testing.T) {
	doc := &config.MailConfigDocument{
		Path:     "/tmp/config.toml",
		Accounts: []config.IMAPAccount{},
	}
	err := printMailList(doc, true, nil)
	if err != nil {
		t.Fatalf("printMailList --json: %v", err)
	}
}

func TestPrintMailList_JSON_IncludesPollStatus(t *testing.T) {
	var sb strings.Builder
	old := stdout
	stdout = &sb
	defer func() { stdout = old }()

	acc := config.IMAPAccount{Host: "imap.example.com", Port: 993, Username: "user", Folder: "INBOX"}
	doc := &config.MailConfigDocument{
		Path:     "/tmp/config.toml",
		Accounts: []config.IMAPAccount{acc},
	}
	polledAt := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	pollStatus := map[string]mailPollStatus{
		config.AccountID(acc): {LastPolledAt: polledAt, Status: "ok"},
	}
	if err := printMailList(doc, true, pollStatus); err != nil {
		t.Fatalf("printMailList --json: %v", err)
	}
	var decoded struct {
		PollStatus map[string]mailPollStatus `json:"poll_status"`
	}
	if err := json.Unmarshal([]byte(sb.String()), &decoded); err != nil {
		t.Fatalf("decode JSON: %v\n%s", err, sb.String())
	}
	got, ok := decoded.PollStatus[config.AccountID(acc)]
	if !ok || got.Status != "ok" {
		t.Fatalf("expected poll_status entry for %s, got: %+v", config.AccountID(acc), decoded.PollStatus)
	}
}

func TestPrintReocrResponse_JSON(t *testing.T) {
	response := reocrResponse{
		SchemaVersion: 1,
		DocumentID:    42,
		JobID:         10,
		Status:        "completed",
		OutputPath:    "/vault/doc.md",
	}
	err := printReocrResponse(response, true)
	if err != nil {
		t.Fatalf("printReocrResponse json: %v", err)
	}
}

func TestPrintReocrResponse_Human(t *testing.T) {
	old := stdout
	defer func() { stdout = old }()
	r, w, _ := os.Pipe()
	stdout = w

	response := reocrResponse{
		SchemaVersion: 1,
		DocumentID:    42,
		JobID:         10,
		Status:        "completed",
		OutputPath:    "/vault/doc.md",
	}
	err := printReocrResponse(response, false)
	if err != nil {
		t.Fatalf("printReocrResponse human: %v", err)
	}
	w.Close()

	var buf bytes.Buffer
	buf.ReadFrom(r)
	out := buf.String()
	if !strings.Contains(out, "reprocessed: document 42") {
		t.Errorf("expected reprocessed message, got: %s", out)
	}
}

func TestPrintReocrResponse_AlreadyRunning(t *testing.T) {
	old := stdout
	defer func() { stdout = old }()
	r, w, _ := os.Pipe()
	stdout = w

	response := reocrResponse{
		SchemaVersion: 1,
		DocumentID:    42,
		JobID:         10,
		Status:        "already_running",
	}
	err := printReocrResponse(response, false)
	if err != nil {
		t.Fatalf("printReocrResponse already_running: %v", err)
	}
	w.Close()

	var buf bytes.Buffer
	buf.ReadFrom(r)
	out := buf.String()
	if !strings.Contains(out, "already running") {
		t.Errorf("expected already running message, got: %s", out)
	}
}

func TestPrintReocrResponse_Error(t *testing.T) {
	response := reocrResponse{
		SchemaVersion: 1,
		DocumentID:    42,
		JobID:         10,
		Status:        "failed",
		Error:         &reocrErrorResponse{Code: "hash_mismatch", Message: "hash changed"},
	}
	err := printReocrResponse(response, false)
	if err != nil {
		t.Fatalf("printReocrResponse error: %v", err)
	}
}

func TestReocrFailure_JSON(t *testing.T) {
	err := reocrFailure(true, 42, 10, "hash_mismatch", "hash changed")
	if err == nil {
		t.Fatal("expected error from reocrFailure")
	}
}

func TestReocrFailure_Human(t *testing.T) {
	err := reocrFailure(false, 42, 10, "hash_mismatch", "hash changed")
	if err == nil {
		t.Fatal("expected error from reocrFailure")
	}
}

func TestRunReocr_NoArgs(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "db.db")
	vaultPath := filepath.Join(dir, "vault")
	os.MkdirAll(vaultPath, 0o700)

	err := runReocr([]string{"--db", dbPath, "--vault", vaultPath})
	if err == nil {
		t.Fatal("expected error for no document ID")
	}
}

func TestRunReocr_InvalidDocumentID(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "db.db")
	vaultPath := filepath.Join(dir, "vault")
	os.MkdirAll(vaultPath, 0o700)

	err := runReocr([]string{"--db", dbPath, "--vault", vaultPath, "--document-id", "not-a-number"})
	if err == nil {
		t.Fatal("expected error for invalid document ID")
	}
}

func TestRunReocr_NegativeDocumentID(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "db.db")
	vaultPath := filepath.Join(dir, "vault")
	os.MkdirAll(vaultPath, 0o700)

	err := runReocr([]string{"--db", dbPath, "--vault", vaultPath, "--document-id", "-1"})
	if err == nil {
		t.Fatal("expected error for negative document ID")
	}
}

func TestRunReocr_DocumentNotFound(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "db.db")
	vaultPath := filepath.Join(dir, "vault")
	os.MkdirAll(vaultPath, 0o700)

	err := runReocr([]string{"--db", dbPath, "--vault", vaultPath, "--document-id", "9999"})
	if err == nil {
		t.Fatal("expected error for nonexistent document")
	}
}

func TestRunReocr_SourceNotRegistered(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "db.db")
	vaultPath := filepath.Join(dir, "vault")
	os.MkdirAll(vaultPath, 0o700)

	err := runReocr([]string{"--db", dbPath, "--vault", vaultPath, "--source", "/nonexistent/path.pdf"})
	if err == nil {
		t.Fatal("expected error for unregistered source")
	}
}

func TestRunReocr_MultipleIdentifiers(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "db.db")
	vaultPath := filepath.Join(dir, "vault")
	os.MkdirAll(vaultPath, 0o700)

	err := runReocr([]string{"--db", dbPath, "--vault", vaultPath, "--document-id", "1", "--source", "/path.pdf"})
	if err == nil {
		t.Fatal("expected error for multiple identifiers")
	}
}

func TestRunReocr_PositionalAndSource(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "db.db")
	vaultPath := filepath.Join(dir, "vault")
	os.MkdirAll(vaultPath, 0o700)

	err := runReocr([]string{"--db", dbPath, "--vault", vaultPath, "--source", "/path.pdf", "extra"})
	if err == nil {
		t.Fatal("expected error for source + positional")
	}
}

func TestRunReocr_PositionalNotNumber(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "db.db")
	vaultPath := filepath.Join(dir, "vault")
	os.MkdirAll(vaultPath, 0o700)

	err := runReocr([]string{"--db", dbPath, "--vault", vaultPath, "abc"})
	if err == nil {
		t.Fatal("expected error for non-numeric identifier")
	}
}

func TestRunReocr_JSON_DocNotFound(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "db.db")
	vaultPath := filepath.Join(dir, "vault")
	os.MkdirAll(vaultPath, 0o700)

	err := runReocr([]string{"--db", dbPath, "--vault", vaultPath, "--document-id", "9999", "--json"})
	if err == nil {
		t.Fatal("expected error for nonexistent document")
	}
}

func TestDecodeMailInput_EmptyInput(t *testing.T) {
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "empty.json")
	os.WriteFile(inputPath, []byte(""), 0o600)

	_, _, _, _, err := decodeMailInput(inputPath, false)
	if err == nil {
		t.Fatal("expected error for empty input")
	}
}

func TestDecodeMailInput_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "bad.json")
	os.WriteFile(inputPath, []byte("not json"), 0o600)

	_, _, _, _, err := decodeMailInput(inputPath, false)
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestDecodeMailInput_SingleAccount(t *testing.T) {
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "account.json")
	inputJSON := `{"host":"imap.example.com","port":993,"username":"user","password_secret":"env://PASS","folder":"INBOX","from":[],"subject":[],"has_attachment":false,"action":"mark_seen","archive_mail":false}`
	os.WriteFile(inputPath, []byte(inputJSON), 0o600)

	account, _, hasSecret, _, err := decodeMailInput(inputPath, true)
	if err != nil {
		t.Fatalf("decodeMailInput: %v", err)
	}
	if account == nil {
		t.Fatal("expected non-nil account")
	}
	if account.Username != "user" {
		t.Errorf("username = %q, want user", account.Username)
	}
	if !hasSecret {
		t.Error("expected hasSecret=true")
	}
}

func TestDecodeMailInput_AccountObject(t *testing.T) {
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "account.json")
	inputJSON := `{"account":{"host":"imap.example.com","port":993,"username":"user","password_secret":"env://PASS","folder":"INBOX","from":[],"subject":[],"has_attachment":false,"action":"mark_seen","archive_mail":false}}`
	os.WriteFile(inputPath, []byte(inputJSON), 0o600)

	account, _, hasSecret, _, err := decodeMailInput(inputPath, true)
	if err != nil {
		t.Fatalf("decodeMailInput: %v", err)
	}
	if account == nil {
		t.Fatal("expected non-nil account")
	}
	if account.Username != "user" {
		t.Errorf("username = %q, want user", account.Username)
	}
	if !hasSecret {
		t.Error("expected hasSecret=true")
	}
}

func TestDecodeMailInput_ArrayOfAccounts(t *testing.T) {
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "accounts.json")
	inputJSON := `[{"host":"imap.example.com","port":993,"username":"user1","password_secret":"env://PASS1","folder":"INBOX","from":[],"subject":[],"has_attachment":false,"action":"mark_seen","archive_mail":false}]`
	os.WriteFile(inputPath, []byte(inputJSON), 0o600)

	_, accounts, _, _, err := decodeMailInput(inputPath, false)
	if err != nil {
		t.Fatalf("decodeMailInput: %v", err)
	}
	if len(accounts) != 1 {
		t.Fatalf("expected 1 account, got %d", len(accounts))
	}
}

func TestDecodeMailInput_AccountsObject(t *testing.T) {
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "accounts.json")
	inputJSON := `{"accounts":[{"host":"imap.example.com","port":993,"username":"user1","password_secret":"env://PASS1","folder":"INBOX","from":[],"subject":[],"has_attachment":false,"action":"mark_seen","archive_mail":false}]}`
	os.WriteFile(inputPath, []byte(inputJSON), 0o600)

	_, accounts, _, _, err := decodeMailInput(inputPath, false)
	if err != nil {
		t.Fatalf("decodeMailInput: %v", err)
	}
	if len(accounts) != 1 {
		t.Fatalf("expected 1 account, got %d", len(accounts))
	}
}

func TestDecodeMailInput_Stdin(t *testing.T) {
	old := os.Stdin
	defer func() { os.Stdin = old }()

	inputJSON := `{"host":"imap.example.com","port":993,"username":"user","password_secret":"env://PASS","folder":"INBOX","from":[],"subject":[],"has_attachment":false,"action":"mark_seen","archive_mail":false}`
	r, w, _ := os.Pipe()
	os.Stdin = r
	w.Write([]byte(inputJSON))
	w.Close()

	account, _, _, _, err := decodeMailInput("", true)
	if err != nil {
		t.Fatalf("decodeMailInput stdin: %v", err)
	}
	if account == nil {
		t.Fatal("expected non-nil account from stdin")
	}
}

func TestRunMailValidate_AllBranches(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	content := `imap_poll_interval = "5m"

[[imap_accounts]]
host = "imap.example.com"
port = 993
username = "user"
password_secret = "env://PASS"
folder = "INBOX"
from = ["billing@example.com"]
subject = ["invoice"]
has_attachment = true
action = "move"
move_to = "Processed"
archive_mail = false
`
	os.WriteFile(configPath, []byte(content), 0o600)

	err := runMailValidate(configPath, false)
	if err != nil {
		t.Fatalf("runMailValidate: %v", err)
	}
}

func TestRunMailUpdate_MaskedSecret(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	initial := `[[imap_accounts]]
host = "imap.example.com"
port = 993
username = "user1"
password_secret = "env://PASS1"
folder = "INBOX"
from = []
subject = []
has_attachment = false
action = "mark_seen"
archive_mail = false
`
	os.WriteFile(configPath, []byte(initial), 0o600)

	inputPath := filepath.Join(dir, "input.json")
	inputJSON := `{"host":"imap.changed.com","port":993,"username":"user1","password_secret":"<redacted>","folder":"INBOX","from":[],"subject":[],"has_attachment":false,"action":"mark_seen","archive_mail":false}`
	os.WriteFile(inputPath, []byte(inputJSON), 0o600)

	doc, _ := config.ReadMailConfig(configPath)
	err := runMailUpdate(configPath, doc, "0", inputPath, false)
	if err != nil {
		t.Fatalf("runMailUpdate with masked secret: %v", err)
	}

	doc2, _ := config.ReadMailConfig(configPath)
	if doc2.Accounts[0].PasswordSecret != "env://PASS1" {
		t.Errorf("password_secret = %q, want original env://PASS1 (masked secret preserved)", doc2.Accounts[0].PasswordSecret)
	}
}

func TestRunMailUpdate_WithSecret(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	initial := `[[imap_accounts]]
host = "imap.example.com"
port = 993
username = "user1"
password_secret = "env://PASS1"
folder = "INBOX"
from = []
subject = []
has_attachment = false
action = "mark_seen"
archive_mail = false
`
	os.WriteFile(configPath, []byte(initial), 0o600)

	inputPath := filepath.Join(dir, "input.json")
	inputJSON := `{"host":"imap.changed.com","port":993,"username":"user1","password_secret":"env://NEW_PASS","folder":"INBOX","from":[],"subject":[],"has_attachment":false,"action":"mark_seen","archive_mail":false}`
	os.WriteFile(inputPath, []byte(inputJSON), 0o600)

	doc, _ := config.ReadMailConfig(configPath)
	err := runMailUpdate(configPath, doc, "0", inputPath, false)
	if err != nil {
		t.Fatalf("runMailUpdate with new secret: %v", err)
	}

	doc2, _ := config.ReadMailConfig(configPath)
	if doc2.Accounts[0].PasswordSecret != "env://NEW_PASS" {
		t.Errorf("password_secret = %q, want env://NEW_PASS", doc2.Accounts[0].PasswordSecret)
	}
}

func TestRunMailUpdate_JSON(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	initial := `[[imap_accounts]]
host = "imap.example.com"
port = 993
username = "user1"
password_secret = "env://PASS1"
folder = "INBOX"
from = []
subject = []
has_attachment = false
action = "mark_seen"
archive_mail = false
`
	os.WriteFile(configPath, []byte(initial), 0o600)

	inputPath := filepath.Join(dir, "input.json")
	inputJSON := `{"host":"imap.changed.com","port":993,"username":"user1","password_secret":"env://NEW_PASS","folder":"INBOX","from":[],"subject":[],"has_attachment":false,"action":"mark_seen","archive_mail":false}`
	os.WriteFile(inputPath, []byte(inputJSON), 0o600)

	doc, _ := config.ReadMailConfig(configPath)
	err := runMailUpdate(configPath, doc, "0", inputPath, true)
	if err != nil {
		t.Fatalf("runMailUpdate --json: %v", err)
	}
}

func TestRunMailCreate_EmptyInput(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	inputPath := filepath.Join(dir, "empty.json")
	os.WriteFile(inputPath, []byte(""), 0o600)

	err := runMailCreate(configPath, &config.MailConfigDocument{Path: configPath}, inputPath, false)
	if err == nil {
		t.Fatal("expected error for empty input")
	}
}

func TestRunMailDelete_NotFound(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	os.WriteFile(configPath, []byte(""), 0o600)

	doc := &config.MailConfigDocument{Path: configPath, Accounts: []config.IMAPAccount{}}
	err := runMailDelete(configPath, doc, "0", false)
	if err == nil {
		t.Fatal("expected error for nonexistent account")
	}
}

func TestRunMailReplace_EmptyInput(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	inputPath := filepath.Join(dir, "empty.json")
	os.WriteFile(configPath, []byte(""), 0o600)
	os.WriteFile(inputPath, []byte(""), 0o600)

	doc := &config.MailConfigDocument{Path: configPath}
	err := runMailReplace(configPath, doc, inputPath, false)
	if err == nil {
		t.Fatal("expected error for empty input")
	}
}

func TestRunMailCreate_NilAccount(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	inputPath := filepath.Join(dir, "input.json")
	os.WriteFile(inputPath, []byte(`{"accounts":[]}`), 0o600)

	err := runMailCreate(configPath, &config.MailConfigDocument{Path: configPath}, inputPath, false)
	if err == nil {
		t.Fatal("expected error for nil account")
	}
}

func TestRunMailUpdate_NilAccount(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	inputPath := filepath.Join(dir, "input.json")
	os.WriteFile(inputPath, []byte(`{"accounts":[]}`), 0o600)

	doc := &config.MailConfigDocument{Path: configPath, Accounts: []config.IMAPAccount{{Host: "a.com", Port: 993, Username: "u"}}}
	err := runMailUpdate(configPath, doc, "0", inputPath, false)
	if err == nil {
		t.Fatal("expected error for nil account")
	}
}

func TestRunMailDelete_JSON(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	initial := `[[imap_accounts]]
host = "imap.example.com"
port = 993
username = "user1"
password_secret = "env://PASS1"
folder = "INBOX"
from = []
subject = []
has_attachment = false
action = "mark_seen"
archive_mail = false
`
	os.WriteFile(configPath, []byte(initial), 0o600)

	doc, _ := config.ReadMailConfig(configPath)
	err := runMailDelete(configPath, doc, "0", true)
	if err != nil {
		t.Fatalf("runMailDelete --json: %v", err)
	}
}

func TestRunMailCreate_JSON(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	inputPath := filepath.Join(dir, "input.json")

	inputJSON := `{"host":"imap.new.com","port":993,"username":"newuser","password_secret":"env://NEW_PASS","folder":"INBOX","from":[],"subject":[],"has_attachment":false,"action":"mark_seen","archive_mail":false}`
	os.WriteFile(inputPath, []byte(inputJSON), 0o600)

	err := runMailCreate(configPath, &config.MailConfigDocument{Path: configPath}, inputPath, true)
	if err != nil {
		t.Fatalf("runMailCreate --json: %v", err)
	}
}

func TestRunMailValidate_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	os.WriteFile(configPath, []byte("not valid toml"), 0o600)

	err := runMailValidate(configPath, false)
	if err == nil {
		t.Fatal("expected error for invalid TOML")
	}
}

func TestRunMailValidate_InvalidJSON_JSONMode(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	os.WriteFile(configPath, []byte("not valid toml"), 0o600)

	err := runMailValidate(configPath, true)
	if err != nil {
		t.Fatalf("runMailValidate --json should succeed for invalid config in JSON mode: %v", err)
	}
}

func TestValidateReportFile_ValidJSON(t *testing.T) {
	dir := t.TempDir()
	reportPath := filepath.Join(dir, "report.json")
	report := `{"schema_version":1,"tool_version":"0.1.0","dry_run":true,"total":100,"documents":[]}`
	os.WriteFile(reportPath, []byte(report), 0o600)

	result := validateReportFile(reportPath)
	if !result.Valid {
		t.Errorf("validateReportFile: %v", result.Errors)
	}
}

func TestValidateReportFile_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	reportPath := filepath.Join(dir, "report.json")
	os.WriteFile(reportPath, []byte("not json"), 0o600)

	result := validateReportFile(reportPath)
	if result.Valid {
		t.Error("expected invalid for bad JSON")
	}
}

func TestValidateReportFile_Nonexistent(t *testing.T) {
	result := validateReportFile("/nonexistent/report.json")
	if result.Valid {
		t.Error("expected invalid for nonexistent file")
	}
}

func TestRunJobs_EmptyQueue(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "jobs.db")
	os.Setenv("SYMINGEST_DB_PATH", dbPath)
	defer os.Unsetenv("SYMINGEST_DB_PATH")

	err := runJobs([]string{})
	if err != nil {
		t.Fatalf("runJobs: %v", err)
	}
}

func TestRunJobs_JSON(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "jobs.db")
	os.Setenv("SYMINGEST_DB_PATH", dbPath)
	defer os.Unsetenv("SYMINGEST_DB_PATH")

	err := runJobs([]string{"--json"})
	if err != nil {
		t.Fatalf("runJobs --json: %v", err)
	}
}

func TestRunRetry_NonexistentJob(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "jobs.db")
	os.Setenv("SYMINGEST_DB_PATH", dbPath)
	defer os.Unsetenv("SYMINGEST_DB_PATH")

	err := runRetry([]string{"999"})
	if err == nil {
		t.Fatal("expected error for nonexistent job")
	}
}

func TestRunMailDelete(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")

	initial := `[[imap_accounts]]
host = "imap.example.com"
port = 993
username = "user1"
password_secret = "env://PASS1"
folder = "INBOX"
from = []
subject = []
has_attachment = false
action = "mark_seen"
archive_mail = false

[[imap_accounts]]
host = "imap.example.com"
port = 993
username = "user2"
password_secret = "env://PASS2"
folder = "INBOX"
from = []
subject = []
has_attachment = false
action = "mark_seen"
archive_mail = false
`
	os.WriteFile(configPath, []byte(initial), 0o600)

	doc, _ := config.ReadMailConfig(configPath)
	err := runMailDelete(configPath, doc, "0", false)
	if err != nil {
		t.Fatalf("runMailDelete: %v", err)
	}

	doc2, _ := config.ReadMailConfig(configPath)
	if len(doc2.Accounts) != 1 {
		t.Fatalf("expected 1 account after delete, got %d", len(doc2.Accounts))
	}
	if doc2.Accounts[0].Username != "user2" {
		t.Errorf("remaining username = %q, want user2", doc2.Accounts[0].Username)
	}
}

func TestRunMailReplace(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	inputPath := filepath.Join(dir, "input.json")

	initial := `[[imap_accounts]]
host = "old.com"
port = 993
username = "old"
password_secret = "env://OLD"
folder = "INBOX"
from = []
subject = []
has_attachment = false
action = "mark_seen"
archive_mail = false
`
	os.WriteFile(configPath, []byte(initial), 0o600)

	inputJSON := `[{"host":"new.com","port":993,"username":"new1","password_secret":"env://NEW1","folder":"INBOX","from":[],"subject":[],"has_attachment":false,"action":"mark_seen","archive_mail":false},{"host":"new.com","port":993,"username":"new2","password_secret":"env://NEW2","folder":"Sent","from":[],"subject":[],"has_attachment":false,"action":"mark_seen","archive_mail":false}]`
	os.WriteFile(inputPath, []byte(inputJSON), 0o600)

	doc, _ := config.ReadMailConfig(configPath)
	err := runMailReplace(configPath, doc, inputPath, false)
	if err != nil {
		t.Fatalf("runMailReplace: %v", err)
	}

	doc2, _ := config.ReadMailConfig(configPath)
	if len(doc2.Accounts) != 2 {
		t.Fatalf("expected 2 accounts after replace, got %d", len(doc2.Accounts))
	}
	if doc2.Accounts[0].Username != "new1" || doc2.Accounts[1].Username != "new2" {
		t.Errorf("accounts = [%s, %s], want [new1, new2]", doc2.Accounts[0].Username, doc2.Accounts[1].Username)
	}
}
