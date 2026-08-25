// Package mail implements automated IMAP mail ingestion through the absorbed
// ingest pipeline, following the same scheduled-intake pattern as the inbox
// watcher. It periodically polls the configured accounts and routes the
// fetched attachments through the existing ingest pipeline.
//
// Password safety: this package never logs, stores, or even sees credential
// values. The pipeline resolves them from its own config (TOML with
// symvault:// references or redacted plaintext passwords) and only ever hands
// back staged files.
package mail

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/ingest"
	"github.com/danieljustus/symaira-desktop/internal/service"
	"github.com/danieljustus/symaira-desktop/internal/simhash"
)

// Status holds the runtime state for a single mail account.
type Status struct {
	AccountID     string    `json:"account_id"`
	Host          string    `json:"host"`
	Username      string    `json:"username"`
	LastRun       time.Time `json:"last_run"`
	LastError     string    `json:"last_error,omitempty"`
	MessagesTotal int       `json:"messages_total"`
	LastRunCount  int       `json:"last_run_count"`
}

// MailWatcher periodically polls the configured IMAP accounts and routes the
// fetched attachments into the vault through the existing ingest pipeline.
// It tracks per-account status and deduplicates processed messages using
// SimHash fingerprints.
type MailWatcher struct {
	configPath        string
	svc               *service.Service
	interval          time.Duration
	dedupFingerprints map[string]struct{} // hex fingerprints of processed messages
	mu                sync.RWMutex
	statuses          map[string]*Status // keyed by account stable ID
	statusMu          sync.RWMutex

	vaultRoot string // cached for path operations
}

// New creates a MailWatcher with the given mail config path and service
// reference. The config path should point to the symingest config.toml that
// holds the mail account definitions.
func New(configPath string, svc *service.Service) (*MailWatcher, error) {
	if configPath == "" {
		configPath = filepath.Join(os.Getenv("HOME"), ".config", "symingest", "config.toml")
	}
	return NewWithInterval(configPath, svc, 5*time.Minute)
}

// NewWithInterval creates a MailWatcher with an explicit fetch interval.
// Used in tests to make the watcher fast and deterministic.
func NewWithInterval(configPath string, svc *service.Service, interval time.Duration) (*MailWatcher, error) {
	return &MailWatcher{
		configPath:        configPath,
		svc:               svc,
		interval:          interval,
		dedupFingerprints: make(map[string]struct{}),
		statuses:          make(map[string]*Status),
		vaultRoot:         svc.VaultRoot,
	}, nil
}

// Statuses returns a copy of all per-account status entries.
func (w *MailWatcher) Statuses() []Status {
	w.statusMu.RLock()
	defer w.statusMu.RUnlock()
	result := make([]Status, 0, len(w.statuses))
	for _, s := range w.statuses {
		result = append(result, *s)
	}
	return result
}

// Start begins the periodic mail fetch loop. It blocks until ctx is
// cancelled. The first fetch runs immediately on start, then every
// interval.
func (w *MailWatcher) Start(ctx context.Context) error {
	log.Printf("MailWatcher: starting with config %s, interval %s", w.configPath, w.interval)

	// Run an initial fetch immediately.
	w.fetchAll(ctx)

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Printf("MailWatcher: stopped")
			return nil
		case <-ticker.C:
			w.fetchAll(ctx)
		}
	}
}

// fetchAll lists configured accounts and fetches mail for each.
func (w *MailWatcher) fetchAll(ctx context.Context) {
	accounts, err := w.listAccounts()
	if err != nil {
		log.Printf("MailWatcher: failed to list accounts: %v", err)
		return
	}

	if len(accounts) == 0 {
		return
	}

	for _, acct := range accounts {
		w.fetchAccount(ctx, acct)
	}
}

// accountInfo is a minimal representation of a configured mail account.
type accountInfo struct {
	ID       string `json:"id"`
	Host     string `json:"host"`
	Username string `json:"username"`
}

// listAccounts returns the configured mail accounts. On error (unreadable
// config, and so on) it returns an empty slice and logs the error; the watcher
// continues to retry on the next tick.
func (w *MailWatcher) listAccounts() ([]accountInfo, error) {
	accounts, err := ingest.MailAccountsFunc(w.configPath)
	if err != nil {
		return nil, fmt.Errorf("read mail accounts: %w", err)
	}

	result := make([]accountInfo, 0, len(accounts))
	for _, account := range accounts {
		result = append(result, accountInfo{
			ID:       account.ID,
			Host:     account.Host,
			Username: account.Username,
		})
	}
	return result, nil
}

// fetchAccount fetches mail for a single account and processes results.
func (w *MailWatcher) fetchAccount(ctx context.Context, acct accountInfo) {
	stableID := acct.ID
	if stableID == "" {
		stableID = fmt.Sprintf("%s@%s", acct.Username, acct.Host)
	}

	w.updateStatus(stableID, acct, "", 0)

	now := time.Now()
	messages, err := w.fetchMessages(acct.ID)
	if err != nil {
		errStr := err.Error()
		// Redact any credential-like patterns from error messages
		errStr = redactCredentials(errStr)
		log.Printf("MailWatcher: fetch failed for %s: %s", stableID, errStr)
		w.updateStatus(stableID, acct, errStr, 0)
		return
	}

	newCount := 0
	for _, msg := range messages {
		fp := msg.simhash()
		if w.isDuplicate(fp) {
			continue
		}

		if err := w.ingestMessage(ctx, msg); err != nil {
			log.Printf("MailWatcher: ingest failed for message from %s: %v", stableID, err)
			continue
		}

		w.markProcessed(fp)
		newCount++
	}

	if newCount > 0 {
		log.Printf("MailWatcher: fetched %d messages (%d new) for %s", len(messages), newCount, stableID)
	}

	w.statusMu.Lock()
	if s, ok := w.statuses[stableID]; ok {
		s.LastRun = now
		s.MessagesTotal += newCount
		s.LastRunCount = newCount
	}
	w.statusMu.Unlock()
}

// fetchMessage is a parsed email message ready for ingestion.
type fetchMessage struct {
	UID     string `json:"uid"`
	From    string `json:"from"`
	Subject string `json:"subject"`
	Date    string `json:"date"`
	Body    string `json:"body"`
	// FilePath is the staged attachment the poll wrote to disk. The watcher
	// hands that file to the service ingest pipeline as-is.
	FilePath string `json:"file_path"`
}

// simhash computes a stable fingerprint for deduplication. The fingerprint
// combines the UID, From, Subject, and the first 4 KB of the body.
func (m *fetchMessage) simhash() string {
	text := fmt.Sprintf("%s|%s|%s|", m.UID, m.From, m.Subject)
	if len(m.Body) > 4096 {
		text += m.Body[:4096]
	} else {
		text += m.Body
	}
	return simhash.ComputeHex(text)
}

// fetchMessages runs a single IMAP poll for the given account and returns the
// attachments it staged. Idempotency (per-message tracking and the per-account
// UID cursor) lives in the pipeline's own store, so a message is fetched once
// even across restarts of this watcher.
func (w *MailWatcher) fetchMessages(accountID string) ([]fetchMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	stagingDir := filepath.Join(w.vaultRoot, "inbox", ".mail_tmp")

	result, err := ingest.FetchMailFunc(ctx, ingest.MailFetchOptions{
		ConfigPath: w.configPath,
		AccountID:  accountID,
		StagingDir: stagingDir,
	})
	if err != nil {
		return nil, fmt.Errorf("mail fetch failed: %w", err)
	}
	if reason, ok := result.Errors[accountID]; ok {
		return nil, fmt.Errorf("mail fetch failed: %s", reason)
	}

	messages := make([]fetchMessage, 0, len(result.Attachments))
	for _, attachment := range result.Attachments {
		messages = append(messages, fetchMessage{
			UID:      attachment.MessageID,
			From:     attachment.Correspondent,
			FilePath: attachment.Path,
		})
	}
	return messages, nil
}

// ingestMessage routes a fetched message through the existing service ingest
// pipeline. The service handles note creation, indexing, and OCR.
func (w *MailWatcher) ingestMessage(ctx context.Context, msg fetchMessage) error {
	// The poll already staged the attachment on disk; use that file directly.
	if msg.FilePath != "" {
		if _, err := os.Stat(msg.FilePath); err == nil {
			_, err := w.svc.Ingest(msg.FilePath)
			return err
		}
	}

	// Otherwise create a temp .eml or .md file from the message content.
	tmpDir := filepath.Join(w.vaultRoot, "inbox", ".mail_tmp")
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		return fmt.Errorf("failed to create mail tmp dir: %w", err)
	}

	filename := fmt.Sprintf("mail_%s.eml", msg.UID)
	tmpPath := filepath.Join(tmpDir, filename)

	content := msg.Body
	if content == "" {
		// Synthesize a minimal .eml from headers.
		content = fmt.Sprintf("From: %s\r\nSubject: %s\r\nDate: %s\r\n\r\n",
			msg.From, msg.Subject, msg.Date)
	}

	if err := os.WriteFile(tmpPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write mail temp file: %w", err)
	}

	_, err := w.svc.Ingest(tmpPath)
	return err
}

func (w *MailWatcher) isDuplicate(fp string) bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	_, ok := w.dedupFingerprints[fp]
	return ok
}

func (w *MailWatcher) markProcessed(fp string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.dedupFingerprints[fp] = struct{}{}
}

func (w *MailWatcher) updateStatus(stableID string, acct accountInfo, lastError string, runCount int) {
	w.statusMu.Lock()
	defer w.statusMu.Unlock()

	if s, ok := w.statuses[stableID]; ok {
		s.LastRun = time.Now()
		if lastError != "" {
			s.LastError = lastError
		}
		s.LastRunCount = runCount
	} else {
		w.statuses[stableID] = &Status{
			AccountID:    stableID,
			Host:         acct.Host,
			Username:     acct.Username,
			LastRun:      time.Now(),
			LastError:    lastError,
			LastRunCount: runCount,
		}
	}
}

// redactCredentials removes symvault:// references and other credential
// patterns from error strings to avoid logging secrets.
func redactCredentials(s string) string {
	// Redact symvault:// references
	for {
		idx := strings.Index(strings.ToLower(s), "symvault://")
		if idx < 0 {
			break
		}
		end := strings.IndexAny(s[idx:], " \t\n\r\"')\\")
		if end < 0 {
			s = s[:idx] + "<redacted>"
			break
		}
		s = s[:idx] + "<redacted>" + s[idx+end:]
	}

	// Redact common password patterns in error output
	patterns := []struct{ prefix, suffix string }{
		{`password="`, `"`},
		{`password: "`, `"`},
		{`password_secret="`, `"`},
		{`password_secret: "`, `"`},
	}
	for _, p := range patterns {
		searchFrom := 0
		for {
			idx := strings.Index(s[searchFrom:], p.prefix)
			if idx < 0 {
				break
			}
			idx += searchFrom
			start := idx + len(p.prefix)
			end := strings.Index(s[start:], p.suffix)
			if end < 0 {
				s = s[:start] + "<redacted>" + s[start:]
				break
			}
			s = s[:start] + "<redacted>" + s[start+end:]
			// Resume after the inserted marker; the redacted form still
			// contains the prefix/suffix delimiters, so scanning from 0
			// would match forever.
			searchFrom = start + len("<redacted>")
		}
	}

	return s
}

// ListConfiguredAccounts returns the mail accounts configured in the given
// symingest config file. It is a one-shot query suitable for CLI status
// commands. On error (missing binary, no config) it returns an empty slice.
func ListConfiguredAccounts(configPath string) ([]accountInfo, error) {
	w := &MailWatcher{configPath: configPath}
	return w.listAccounts()
}

// RunOnce triggers a single fetch cycle for all configured accounts on the
// given watcher. It is the one-shot equivalent of Start without the ticker
// loop, suitable for CLI fetch commands.
func RunOnce(w *MailWatcher, configPath string, svc *service.Service) {
	if w == nil {
		w, _ = NewWithInterval(configPath, svc, 0)
	}
	w.fetchAll(context.Background())
}
