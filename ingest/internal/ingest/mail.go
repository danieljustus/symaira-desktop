package ingest

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/danieljustus/symaira-ingest/internal/config"
	"github.com/danieljustus/symaira-ingest/internal/extract"
	"github.com/danieljustus/symaira-ingest/internal/secret"
	"github.com/danieljustus/symaira-ingest/internal/store"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message/mail"
)

type imapClient interface {
	Login(username, password string) error
	Select(folder string) (*mailboxStatus, error)
	SearchUID(criteria *imap.SearchCriteria) ([]imap.UID, error)
	FetchEnvelopesUID(uids []imap.UID) ([]*imapMessage, error)
	FetchUID(uids []imap.UID) ([]*imapMessage, error)
	StoreSeen(seq uint32) error
	Move(seq uint32, dest string) error
	Close() error
}

// mailboxStatus is the subset of a SELECT response needed to decide whether
// the next poll can resume from a stored UID cursor (UIDValidity unchanged)
// or must rescan from UID 1 (first poll, or the server assigned a new UID
// sequence).
type mailboxStatus struct {
	UIDValidity uint32
	UIDNext     uint32
}

type imapEnvelope struct {
	MessageID string
}

type imapMessage struct {
	SeqNum   uint32
	UID      imap.UID
	Envelope *imapEnvelope
	Body     []byte
}

type realIMAPClient struct {
	*imapclient.Client
}

func (c *realIMAPClient) Login(username, password string) error {
	return c.Client.Login(username, password).Wait()
}

func (c *realIMAPClient) Select(folder string) (*mailboxStatus, error) {
	data, err := c.Client.Select(folder, nil).Wait()
	if err != nil {
		return nil, err
	}
	return &mailboxStatus{UIDValidity: data.UIDValidity, UIDNext: uint32(data.UIDNext)}, nil
}

func (c *realIMAPClient) SearchUID(criteria *imap.SearchCriteria) ([]imap.UID, error) {
	searchData, err := c.Client.UIDSearch(criteria, nil).Wait()
	if err != nil {
		return nil, err
	}
	return searchData.AllUIDs(), nil
}

func (c *realIMAPClient) FetchEnvelopesUID(uids []imap.UID) ([]*imapMessage, error) {
	if len(uids) == 0 {
		return nil, nil
	}
	uidSet := imap.UIDSetNum(uids...)
	fetchOptions := &imap.FetchOptions{
		Envelope: true,
	}
	fetchCmd := c.Client.Fetch(uidSet, fetchOptions)
	defer fetchCmd.Close()

	var results []*imapMessage
	for {
		msg := fetchCmd.Next()
		if msg == nil {
			break
		}
		msgBuf, err := msg.Collect()
		if err != nil {
			log.Printf("[MailPoller] Failed to collect envelope: %v", err)
			continue
		}
		var env *imapEnvelope
		if msgBuf.Envelope != nil {
			env = &imapEnvelope{
				MessageID: msgBuf.Envelope.MessageID,
			}
		}
		results = append(results, &imapMessage{
			SeqNum:   msgBuf.SeqNum,
			UID:      msgBuf.UID,
			Envelope: env,
		})
	}
	return results, fetchCmd.Close()
}

func (c *realIMAPClient) FetchUID(uids []imap.UID) ([]*imapMessage, error) {
	if len(uids) == 0 {
		return nil, nil
	}
	uidSet := imap.UIDSetNum(uids...)
	fetchOptions := &imap.FetchOptions{
		Envelope: true,
		BodySection: []*imap.FetchItemBodySection{
			{Peek: true},
		},
	}
	fetchCmd := c.Client.Fetch(uidSet, fetchOptions)
	defer fetchCmd.Close()

	var results []*imapMessage
	for {
		msg := fetchCmd.Next()
		if msg == nil {
			break
		}
		msgBuf, err := msg.Collect()
		if err != nil {
			log.Printf("[MailPoller] Failed to collect message: %v", err)
			continue
		}
		var body []byte
		for _, sec := range msgBuf.BodySection {
			if sec.Section.Specifier == imap.PartSpecifierNone {
				body = sec.Bytes
				break
			}
		}
		var env *imapEnvelope
		if msgBuf.Envelope != nil {
			env = &imapEnvelope{
				MessageID: msgBuf.Envelope.MessageID,
			}
		}
		results = append(results, &imapMessage{
			SeqNum:   msgBuf.SeqNum,
			UID:      msgBuf.UID,
			Envelope: env,
			Body:     body,
		})
	}
	return results, fetchCmd.Close()
}

func (c *realIMAPClient) StoreSeen(seq uint32) error {
	seqSet := imap.SeqSetNum(seq)
	flags := imap.StoreFlags{
		Op:    imap.StoreFlagsAdd,
		Flags: []imap.Flag{imap.FlagSeen},
	}
	return c.Client.Store(seqSet, &flags, nil).Close()
}

func (c *realIMAPClient) Move(seq uint32, dest string) error {
	seqSet := imap.SeqSetNum(seq)
	_, err := c.Client.Move(seqSet, dest).Wait()
	return err
}

func (c *realIMAPClient) Close() error {
	return c.Client.Logout().Wait()
}

var defaultIMAPTLSConfig = func(host string) *tls.Config {
	return &tls.Config{ServerName: host}
}

func defaultDialIMAP(ctx context.Context, addr string, host string) (imapClient, error) {
	c, err := imapclient.DialTLS(addr, &imapclient.Options{TLSConfig: defaultIMAPTLSConfig(host)})
	if err != nil {
		return nil, err
	}
	return &realIMAPClient{c}, nil
}

// MailPoller periodically connects to IMAP accounts to fetch and ingest attachments.
type MailPoller struct {
	store         *store.Store
	accounts      []config.IMAPAccount
	interval      time.Duration
	processingDir string
	failedDir     string
	wg            sync.WaitGroup
	cancel        context.CancelFunc
	dialIMAP      func(ctx context.Context, addr string, host string) (imapClient, error)
	newMailReader func(r io.Reader) (*mail.Reader, error)
}

type MailPollerOptions struct {
	Interval      time.Duration
	ProcessingDir string
	FailedDir     string
}

// NewMailPoller creates a new MailPoller.
func NewMailPoller(s *store.Store, accounts []config.IMAPAccount, opts MailPollerOptions) (*MailPoller, error) {
	if opts.Interval <= 0 {
		opts.Interval = 5 * time.Minute
	}
	processingDir, err := cleanOptionalDir(opts.ProcessingDir)
	if err != nil {
		return nil, fmt.Errorf("resolve processing dir: %w", err)
	}
	failedDir, err := cleanOptionalDir(opts.FailedDir)
	if err != nil {
		return nil, fmt.Errorf("resolve failed dir: %w", err)
	}
	return &MailPoller{
		store:         s,
		accounts:      accounts,
		interval:      opts.Interval,
		processingDir: processingDir,
		failedDir:     failedDir,
		dialIMAP:      defaultDialIMAP,
		newMailReader: mail.CreateReader,
	}, nil
}

// Start begins the polling loop.
func (m *MailPoller) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	m.cancel = cancel

	for i, acc := range m.accounts {
		if secret.IsPlaintext(acc.PasswordSecret) {
			log.Printf("[MailPoller] Account %d (%s) stores its password in plaintext config; use keychain:// or symvault:// instead (run 'symingest doctor' for details)", i, acc.Username)
		}
		m.wg.Add(1)
		go func(account config.IMAPAccount, index int) {
			defer m.wg.Done()
			m.pollLoop(ctx, account, index)
		}(acc, i)
	}
	return nil
}

// Close stops the polling loops.
func (m *MailPoller) Close() error {
	if m.cancel != nil {
		m.cancel()
	}
	m.wg.Wait()
	return nil
}

// mailPollLogReason reduces a poll error to a credential-free reason string
// for logging. Poll errors can wrap secret-backend details (vault paths,
// keychain items, env names), IMAP auth responses and TLS errors; the raw
// error text must never reach the logs. The returned value is always a
// static classification, never derived from the error message.
func mailPollLogReason(err error) string {
	var netErr net.Error
	switch {
	case errors.Is(err, context.Canceled):
		return "shutting down"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline exceeded"
	case errors.As(err, &netErr) && netErr.Timeout():
		return "network timeout"
	case errors.As(err, &netErr):
		return "network error"
	default:
		return "authentication or configuration error"
	}
}

func (m *MailPoller) pollLoop(ctx context.Context, acc config.IMAPAccount, index int) {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	// Initial poll
	if err := m.pollAccountAndRecord(ctx, acc); err != nil {
		log.Printf("[MailPoller] Account %d (%s) initial poll failed: %s (run 'symingest doctor' for details)", index, acc.Username, mailPollLogReason(err))
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.pollAccountAndRecord(ctx, acc); err != nil {
				log.Printf("[MailPoller] Account %d (%s) poll failed: %s (run 'symingest doctor' for details)", index, acc.Username, mailPollLogReason(err))
			}
		}
	}
}

// pollAccountAndRecord runs pollAccount and persists the outcome so
// `symingest mail list` and `symingest doctor` can surface the last poll
// status without relying on log files.
func (m *MailPoller) pollAccountAndRecord(ctx context.Context, acc config.IMAPAccount) error {
	err := m.pollAccount(ctx, acc)
	status := "ok"
	lastError := ""
	if err != nil {
		status = "error"
		lastError = mailPollLogReason(err)
	}
	if recErr := m.store.RecordMailPollStatus(ctx, config.AccountID(acc), time.Now(), status, lastError); recErr != nil {
		log.Printf("[MailPoller] failed to record poll status for %s: %v", acc.Username, recErr)
	}
	return err
}

func (m *MailPoller) pollAccount(ctx context.Context, acc config.IMAPAccount) error {
	pwd, err := secret.Resolve(ctx, acc.PasswordSecret)
	if err != nil {
		return fmt.Errorf("resolve password_secret for %s: %w", acc.Username, err)
	}

	addr := fmt.Sprintf("%s:%d", acc.Host, acc.Port)
	client, err := m.dialIMAP(ctx, addr, acc.Host)
	if err != nil {
		return fmt.Errorf("dial tls: %w", err)
	}
	defer client.Close()

	if err := client.Login(acc.Username, pwd); err != nil {
		return fmt.Errorf("login failed: %w", err)
	}

	folder := acc.Folder
	if folder == "" {
		folder = "INBOX"
	}
	status, err := client.Select(folder)
	if err != nil {
		return fmt.Errorf("select folder %q: %w", folder, err)
	}

	// Resume from the last processed UID when the mailbox's UID sequence is
	// still the one we last saw; otherwise (first poll, or the server
	// assigned a new UID sequence) scan from the beginning.
	accountID := config.AccountID(acc)
	cursor, err := m.store.GetMailPollCursor(ctx, accountID)
	if err != nil {
		return fmt.Errorf("load poll cursor: %w", err)
	}
	startUID := uint32(1)
	if cursor != nil && cursor.UIDValidity == status.UIDValidity {
		startUID = cursor.LastUID + 1
	}

	searchCriteria := &imap.SearchCriteria{
		UID: []imap.UIDSet{{imap.UIDRange{Start: imap.UID(startUID), Stop: 0}}},
	}
	if acc.Action == "mark_seen" {
		searchCriteria.NotFlag = []imap.Flag{imap.FlagSeen}
	}

	uids, err := client.SearchUID(searchCriteria)
	if err != nil {
		return fmt.Errorf("uid search: %w", err)
	}

	if len(uids) > 0 {
		envelopes, err := client.FetchEnvelopesUID(uids)
		if err != nil {
			return fmt.Errorf("fetch envelopes: %w", err)
		}

		var newUIDs []imap.UID
		for _, env := range envelopes {
			if env.Envelope == nil || env.Envelope.MessageID == "" {
				newUIDs = append(newUIDs, env.UID)
				continue
			}
			has, err := m.store.HasMailMessage(ctx, env.Envelope.MessageID)
			if err != nil {
				return fmt.Errorf("check idempotency: %w", err)
			}
			if !has {
				newUIDs = append(newUIDs, env.UID)
			}
		}

		if len(newUIDs) > 0 {
			messages, err := client.FetchUID(newUIDs)
			if err != nil {
				return fmt.Errorf("fetch: %w", err)
			}
			for _, msg := range messages {
				if err := m.processMessage(ctx, acc, client, msg); err != nil {
					log.Printf("[MailPoller] Failed to process message %v: %v", msg.SeqNum, err)
				}
			}
		}
	}

	// UIDNext is always one past the highest UID the server currently knows
	// about, so it is a safe resume point even for a poll that found nothing.
	newLastUID := uint32(0)
	if status.UIDNext > 0 {
		newLastUID = status.UIDNext - 1
	}
	if err := m.store.SetMailPollCursor(ctx, accountID, folder, status.UIDValidity, newLastUID); err != nil {
		return fmt.Errorf("save poll cursor: %w", err)
	}

	return nil
}

func (m *MailPoller) processMessage(ctx context.Context, acc config.IMAPAccount, client imapClient, msgBuf *imapMessage) error {
	envelope := msgBuf.Envelope
	if envelope == nil {
		return nil
	}

	msgID := envelope.MessageID
	if msgID == "" {
		msgID = fmt.Sprintf("fallback-seq-%d", msgBuf.SeqNum)
	}

	// Idempotency check
	has, err := m.store.HasMailMessage(ctx, msgID)
	if err != nil {
		return fmt.Errorf("check idempotency: %w", err)
	}
	if has {
		return nil // already processed
	}

	body := msgBuf.Body
	if len(body) == 0 {
		return fmt.Errorf("no body section found")
	}

	mr, err := m.newMailReader(bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create mail reader: %w", err)
	}

	correspondent := ""
	if from := mr.Header.Get("From"); from != "" {
		correspondent = from
	}

	var attachments []string

	for {
		if ctx.Err() != nil {
			break
		}
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Printf("[MailPoller] Error reading part: %v", err)
			continue
		}

		switch h := p.Header.(type) {
		case *mail.AttachmentHeader:
			if m.processingDir == "" {
				return fmt.Errorf("processing_dir is required for mail attachment ingestion")
			}
			filename, _ := h.Filename()
			filename = filepath.Base(filename)
			if filename == "" || filename == "." || filename == ".." {
				filename = "attachment.bin"
			}

			// Save attachment
			outPath := filepath.Join(m.processingDir, fmt.Sprintf("%s-%s", strings.ReplaceAll(msgID, "/", "_"), filename))
			if !isPathWithin(outPath, m.processingDir) {
				log.Printf("[MailPoller] Attachment path %q escapes processing directory, skipping", outPath)
				continue
			}
			if err := m.saveStream(outPath, p.Body); err != nil {
				return fmt.Errorf("save attachment %s: %w", filename, err)
			}
			attachments = append(attachments, outPath)
		}
	}

	if len(attachments) == 0 && acc.HasAttachment {
		// skip processing if no attachments and config requires it
	} else {
		for _, attPath := range attachments {
			if err := m.enqueueFile(ctx, attPath, msgID, correspondent); err != nil {
				return fmt.Errorf("enqueue attachment %s: %w", attPath, err)
			}
		}
	}

	// Apply IMAP Action
	if acc.Action == "mark_seen" {
		if err := client.StoreSeen(msgBuf.SeqNum); err != nil {
			return fmt.Errorf("mark seen: %w", err)
		}
	} else if acc.Action == "move" && acc.MoveTo != "" {
		if err := client.Move(msgBuf.SeqNum, acc.MoveTo); err != nil {
			return fmt.Errorf("move to %s: %w", acc.MoveTo, err)
		}
	}

	// Record idempotency
	if err := m.store.TrackMailMessage(ctx, msgID, acc.Username); err != nil {
		return fmt.Errorf("track mail message: %w", err)
	}

	return nil
}

func (m *MailPoller) saveStream(path string, r io.Reader) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, r)
	return err
}

func (m *MailPoller) enqueueFile(ctx context.Context, workPath, msgID, correspondent string) error {
	kind, err := extract.Detect(workPath)
	if err != nil {
		return fmt.Errorf("detect file kind: %w", err)
	}

	hash, err := hashFile(workPath)
	if err != nil {
		return fmt.Errorf("hash file: %w", err)
	}

	doc, created, err := m.store.CreateOrGet(ctx, workPath, hash, string(kind))
	if err != nil {
		return fmt.Errorf("create or get document: %w", err)
	}

	if !created {
		log.Printf("[MailPoller] File %s (hash %s) already ingested.", workPath, hash)
		return nil
	}

	if err := m.store.SetProvenance(ctx, doc.ID, msgID, correspondent); err != nil {
		return fmt.Errorf("set provenance: %w", err)
	}

	_, err = m.store.EnqueueJob(ctx, doc.ID, string(kind))
	if err != nil {
		return fmt.Errorf("enqueue job: %w", err)
	}

	return nil
}
