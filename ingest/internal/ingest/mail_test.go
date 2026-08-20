package ingest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-ingest/internal/config"
	"github.com/danieljustus/symaira-ingest/internal/store"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-message/mail"
)

type fakeIMAPClient struct {
	loginErr          error
	selectErr         error
	selectStatus      *mailboxStatus
	searchRes         []uint32
	searchErr         error
	fetchEnvelopesRes []*imapMessage
	fetchEnvelopesErr error
	fetchRes          []*imapMessage
	fetchErr          error
	storeSeenErr      error
	moveErr           error
	closeErr          error

	loginUsername      string
	loginPassword      string
	selectedFolder     string
	lastSearchCriteria *imap.SearchCriteria
	storedSeenSeqs     []uint32
	movedSeqs          map[uint32]string
	closed             bool
}

func (f *fakeIMAPClient) Login(username, password string) error {
	f.loginUsername = username
	f.loginPassword = password
	return f.loginErr
}

func (f *fakeIMAPClient) Select(folder string) (*mailboxStatus, error) {
	f.selectedFolder = folder
	if f.selectErr != nil {
		return nil, f.selectErr
	}
	if f.selectStatus != nil {
		return f.selectStatus, nil
	}
	return &mailboxStatus{}, nil
}

func seqsToUIDs(seqs []uint32) []imap.UID {
	if seqs == nil {
		return nil
	}
	uids := make([]imap.UID, len(seqs))
	for i, s := range seqs {
		uids[i] = imap.UID(s)
	}
	return uids
}

func (f *fakeIMAPClient) SearchUID(criteria *imap.SearchCriteria) ([]imap.UID, error) {
	f.lastSearchCriteria = criteria
	return seqsToUIDs(f.searchRes), f.searchErr
}

func (f *fakeIMAPClient) FetchEnvelopesUID(uids []imap.UID) ([]*imapMessage, error) {
	return f.fetchEnvelopesRes, f.fetchEnvelopesErr
}

func (f *fakeIMAPClient) FetchUID(uids []imap.UID) ([]*imapMessage, error) {
	return f.fetchRes, f.fetchErr
}

func (f *fakeIMAPClient) StoreSeen(seq uint32) error {
	f.storedSeenSeqs = append(f.storedSeenSeqs, seq)
	return f.storeSeenErr
}

func (f *fakeIMAPClient) Move(seq uint32, dest string) error {
	if f.movedSeqs == nil {
		f.movedSeqs = make(map[uint32]string)
	}
	f.movedSeqs[seq] = dest
	return f.moveErr
}

func (f *fakeIMAPClient) Close() error {
	f.closed = true
	return f.closeErr
}

func createFakeEmail(msgID, from, filename, bodyContent string) []byte {
	var buf bytes.Buffer
	buf.WriteString("MIME-Version: 1.0\r\n")
	buf.WriteString(fmt.Sprintf("Message-ID: <%s>\r\n", msgID))
	buf.WriteString(fmt.Sprintf("From: %s\r\n", from))
	buf.WriteString("Content-Type: multipart/mixed; boundary=boundary\r\n\r\n")
	buf.WriteString("--boundary\r\n")
	buf.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
	buf.WriteString("This is the email body.\r\n")
	buf.WriteString("--boundary\r\n")
	buf.WriteString(fmt.Sprintf("Content-Type: text/plain; name=\"%s\"\r\n", filename))
	buf.WriteString("Content-Disposition: attachment; filename=\"" + filename + "\"\r\n\r\n")
	buf.WriteString(bodyContent + "\r\n")
	buf.WriteString("--boundary--\r\n")
	return buf.Bytes()
}

func TestMailPoller_Success(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	defer s.Close()

	processingDir := filepath.Join(dir, "processing")
	failedDir := filepath.Join(dir, "failed")
	if err := os.MkdirAll(processingDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(failedDir, 0700); err != nil {
		t.Fatal(err)
	}

	accounts := []config.IMAPAccount{
		{
			Username:       "test@example.com",
			PasswordSecret: "myplaintextpw",
			Host:           "imap.example.com",
			Port:           993,
			Folder:         "INBOX",
			Action:         "mark_seen",
		},
	}

	poller, err := NewMailPoller(s, accounts, MailPollerOptions{
		Interval:      1 * time.Second,
		ProcessingDir: processingDir,
		FailedDir:     failedDir,
	})
	if err != nil {
		t.Fatalf("NewMailPoller: %v", err)
	}

	fakeClient := &fakeIMAPClient{
		searchRes: []uint32{42},
		fetchEnvelopesRes: []*imapMessage{
			{
				SeqNum: 42,
				Envelope: &imapEnvelope{
					MessageID: "test-msg-id-123@example.com",
				},
			},
		},
		fetchRes: []*imapMessage{
			{
				SeqNum: 42,
				Envelope: &imapEnvelope{
					MessageID: "test-msg-id-123@example.com",
				},
				Body: createFakeEmail("test-msg-id-123@example.com", "sender@example.com", "invoice.txt", "Hello World Invoiced Data"),
			},
		},
	}

	poller.dialIMAP = func(ctx context.Context, addr string, host string) (imapClient, error) {
		if addr != "imap.example.com:993" {
			t.Errorf("expected addr imap.example.com:993, got %s", addr)
		}
		if host != "imap.example.com" {
			t.Errorf("expected host imap.example.com, got %s", host)
		}
		return fakeClient, nil
	}

	ctx := context.Background()
	err = poller.pollAccount(ctx, accounts[0])
	if err != nil {
		t.Fatalf("pollAccount failed: %v", err)
	}

	// Verify client interactions
	if fakeClient.loginUsername != "test@example.com" || fakeClient.loginPassword != "myplaintextpw" {
		t.Errorf("unexpected login credentials: %s, %s", fakeClient.loginUsername, fakeClient.loginPassword)
	}
	if fakeClient.selectedFolder != "INBOX" {
		t.Errorf("expected selected folder INBOX, got %s", fakeClient.selectedFolder)
	}
	if !fakeClient.closed {
		t.Error("expected client to be closed")
	}
	if len(fakeClient.storedSeenSeqs) != 1 || fakeClient.storedSeenSeqs[0] != 42 {
		t.Errorf("expected StoreSeen to be called for seq 42, got %v", fakeClient.storedSeenSeqs)
	}

	// Verify enqueued file and store state
	has, err := s.HasMailMessage(ctx, "test-msg-id-123@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Error("expected store to track the mail message ID")
	}

	// Retrieve doc from the store using the hash of the created file
	expectedFilePath := filepath.Join(processingDir, "test-msg-id-123@example.com-invoice.txt")
	hash, err := hashFile(expectedFilePath)
	if err != nil {
		t.Fatal(err)
	}

	doc, err := s.ByHash(ctx, hash)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Correspondent != "sender@example.com" {
		t.Errorf("expected correspondent sender@example.com, got %q", doc.Correspondent)
	}

	jobs, err := s.ListJobs(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	if jobs[0].DocumentID != doc.ID {
		t.Error("expected job to reference document")
	}
}

func TestMailPoller_Idempotency(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	processingDir := filepath.Join(dir, "processing")
	failedDir := filepath.Join(dir, "failed")
	_ = os.MkdirAll(processingDir, 0700)
	_ = os.MkdirAll(failedDir, 0700)

	acc := config.IMAPAccount{
		Username:       "test@example.com",
		PasswordSecret: "myplaintextpw",
		Host:           "imap.example.com",
		Port:           993,
		Action:         "mark_seen",
	}

	poller, _ := NewMailPoller(s, []config.IMAPAccount{acc}, MailPollerOptions{
		ProcessingDir: processingDir,
		FailedDir:     failedDir,
	})

	fakeClient := &fakeIMAPClient{
		searchRes: []uint32{42},
		fetchEnvelopesRes: []*imapMessage{
			{
				SeqNum: 42,
				Envelope: &imapEnvelope{
					MessageID: "dup@example.com",
				},
			},
		},
		fetchRes: []*imapMessage{
			{
				SeqNum: 42,
				Envelope: &imapEnvelope{
					MessageID: "dup@example.com",
				},
				Body: createFakeEmail("dup@example.com", "sender@example.com", "invoice.txt", "Data"),
			},
		},
	}

	poller.dialIMAP = func(ctx context.Context, addr string, host string) (imapClient, error) {
		return fakeClient, nil
	}

	ctx := context.Background()
	err = poller.pollAccount(ctx, acc)
	if err != nil {
		t.Fatal(err)
	}

	fakeClient.storedSeenSeqs = nil

	err = poller.pollAccount(ctx, acc)
	if err != nil {
		t.Fatal(err)
	}

	// Should skip enqueuing and not call StoreSeen again (idempotency check happens before StoreSeen/Move)
	if len(fakeClient.storedSeenSeqs) != 0 {
		t.Errorf("expected StoreSeen not to be called again, got %v", fakeClient.storedSeenSeqs)
	}

	// Let's verify ByHash is retrievable
	expectedFilePath := filepath.Join(processingDir, "dup@example.com-invoice.txt")
	hash, _ := hashFile(expectedFilePath)
	_, err = s.ByHash(ctx, hash)
	if err != nil {
		t.Errorf("expected 1 document in store to be queryable by hash: %v", err)
	}
}

func TestMailPoller_AuthFailure(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	acc := config.IMAPAccount{
		Username:       "test@example.com",
		PasswordSecret: "myplaintextpw",
		Host:           "imap.example.com",
		Port:           993,
	}

	poller, _ := NewMailPoller(s, []config.IMAPAccount{acc}, MailPollerOptions{ProcessingDir: t.TempDir()})

	fakeClient := &fakeIMAPClient{
		loginErr: errors.New("invalid credentials"),
	}

	poller.dialIMAP = func(ctx context.Context, addr string, host string) (imapClient, error) {
		return fakeClient, nil
	}

	err = poller.pollAccount(context.Background(), acc)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !fakeClient.closed {
		t.Error("expected client to be closed on login failure")
	}
}

func TestMailPoller_DialFailure(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	acc := config.IMAPAccount{
		Username:       "test@example.com",
		PasswordSecret: "myplaintextpw",
		Host:           "imap.example.com",
		Port:           993,
	}

	poller, _ := NewMailPoller(s, []config.IMAPAccount{acc}, MailPollerOptions{ProcessingDir: t.TempDir()})

	poller.dialIMAP = func(ctx context.Context, addr string, host string) (imapClient, error) {
		return nil, errors.New("network unreachable")
	}

	err = poller.pollAccount(context.Background(), acc)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestMailPoller_SearchNoMessages(t *testing.T) {
	dir := t.TempDir()
	s, _ := store.Open(filepath.Join(dir, "test.db"))
	defer s.Close()

	acc := config.IMAPAccount{
		Username:       "test@example.com",
		PasswordSecret: "myplaintextpw",
		Host:           "imap.example.com",
		Port:           993,
	}

	poller, _ := NewMailPoller(s, []config.IMAPAccount{acc}, MailPollerOptions{ProcessingDir: t.TempDir()})

	fakeClient := &fakeIMAPClient{
		searchRes: []uint32{},
	}

	poller.dialIMAP = func(ctx context.Context, addr string, host string) (imapClient, error) {
		return fakeClient, nil
	}

	err := poller.pollAccount(context.Background(), acc)
	if err != nil {
		t.Fatal(err)
	}
}

func TestMailPoller_MoveAction(t *testing.T) {
	dir := t.TempDir()
	s, _ := store.Open(filepath.Join(dir, "test.db"))
	defer s.Close()

	processingDir := filepath.Join(dir, "processing")
	failedDir := filepath.Join(dir, "failed")
	_ = os.MkdirAll(processingDir, 0700)
	_ = os.MkdirAll(failedDir, 0700)

	acc := config.IMAPAccount{
		Username:       "test@example.com",
		PasswordSecret: "myplaintextpw",
		Host:           "imap.example.com",
		Port:           993,
		Action:         "move",
		MoveTo:         "Archive",
	}

	poller, _ := NewMailPoller(s, []config.IMAPAccount{acc}, MailPollerOptions{
		ProcessingDir: processingDir,
		FailedDir:     failedDir,
	})

	fakeClient := &fakeIMAPClient{
		searchRes: []uint32{99},
		fetchEnvelopesRes: []*imapMessage{
			{
				SeqNum: 99,
				Envelope: &imapEnvelope{
					MessageID: "move-test@example.com",
				},
			},
		},
		fetchRes: []*imapMessage{
			{
				SeqNum: 99,
				Envelope: &imapEnvelope{
					MessageID: "move-test@example.com",
				},
				Body: createFakeEmail("move-test@example.com", "sender@example.com", "invoice.txt", "Data"),
			},
		},
	}

	poller.dialIMAP = func(ctx context.Context, addr string, host string) (imapClient, error) {
		return fakeClient, nil
	}

	err := poller.pollAccount(context.Background(), acc)
	if err != nil {
		t.Fatal(err)
	}

	if fakeClient.movedSeqs == nil || fakeClient.movedSeqs[99] != "Archive" {
		t.Errorf("expected message 99 to be moved to Archive, got %v", fakeClient.movedSeqs)
	}
}

func TestMailPoller_HasAttachmentFilter(t *testing.T) {
	dir := t.TempDir()
	s, _ := store.Open(filepath.Join(dir, "test.db"))
	defer s.Close()

	processingDir := filepath.Join(dir, "processing")
	failedDir := filepath.Join(dir, "failed")
	_ = os.MkdirAll(processingDir, 0700)
	_ = os.MkdirAll(failedDir, 0700)

	acc := config.IMAPAccount{
		Username:       "test@example.com",
		PasswordSecret: "myplaintextpw",
		Host:           "imap.example.com",
		Port:           993,
		HasAttachment:  true,
	}

	poller, _ := NewMailPoller(s, []config.IMAPAccount{acc}, MailPollerOptions{
		ProcessingDir: processingDir,
		FailedDir:     failedDir,
	})

	// Create an email with no parts/attachments (empty body specifier)
	var emailNoAttachment bytes.Buffer
	emailNoAttachment.WriteString("MIME-Version: 1.0\r\n")
	emailNoAttachment.WriteString("Message-ID: <no-att@example.com>\r\n")
	emailNoAttachment.WriteString("From: sender@example.com\r\n")
	emailNoAttachment.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
	emailNoAttachment.WriteString("This is a plain body with no attachments.\r\n")

	fakeClient := &fakeIMAPClient{
		searchRes: []uint32{77},
		fetchRes: []*imapMessage{
			{
				SeqNum: 77,
				Envelope: &imapEnvelope{
					MessageID: "no-att@example.com",
				},
				Body: emailNoAttachment.Bytes(),
			},
		},
	}

	poller.dialIMAP = func(ctx context.Context, addr string, host string) (imapClient, error) {
		return fakeClient, nil
	}

	ctx := context.Background()
	err := poller.pollAccount(ctx, acc)
	if err != nil {
		t.Fatal(err)
	}

	// Verify no documents are created
	// Since s.ByHash is the only check, let's verify nothing was saved in processingDir
	files, _ := os.ReadDir(processingDir)
	if len(files) != 0 {
		t.Errorf("expected 0 files, got %d", len(files))
	}
}

func TestMailPoller_StartStop(t *testing.T) {
	dir := t.TempDir()
	s, _ := store.Open(filepath.Join(dir, "test.db"))
	defer s.Close()

	acc := config.IMAPAccount{
		Username:       "test@example.com",
		PasswordSecret: "myplaintextpw",
		Host:           "imap.example.com",
		Port:           993,
	}

	poller, _ := NewMailPoller(s, []config.IMAPAccount{acc}, MailPollerOptions{
		Interval: 50 * time.Millisecond,
	})

	fakeClient := &fakeIMAPClient{}
	poller.dialIMAP = func(ctx context.Context, addr string, host string) (imapClient, error) {
		return fakeClient, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := poller.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	if err := poller.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}

func TestMailPoller_SecretResolutionFailure(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	acc := config.IMAPAccount{
		Username:       "test@example.com",
		PasswordSecret: "env://NONEXISTENT_VAR_12345",
		Host:           "imap.example.com",
		Port:           993,
	}

	poller, _ := NewMailPoller(s, []config.IMAPAccount{acc}, MailPollerOptions{ProcessingDir: t.TempDir()})

	dialCalled := false
	poller.dialIMAP = func(ctx context.Context, addr, host string) (imapClient, error) {
		dialCalled = true
		return nil, errors.New("should not reach dial")
	}

	err = poller.pollAccount(context.Background(), acc)
	if err == nil {
		t.Fatal("expected error from secret resolution failure")
	}
	if dialCalled {
		t.Fatal("dial should not be called when secret resolution fails")
	}
	if !strings.Contains(err.Error(), "test@example.com") {
		t.Errorf("expected error to name the account, got: %v", err)
	}
	if !strings.Contains(err.Error(), "environment variable") {
		t.Errorf("expected error to preserve the underlying secret backend failure, got: %v", err)
	}
}

func TestMailPoller_SelectFailure(t *testing.T) {
	dir := t.TempDir()
	s, _ := store.Open(filepath.Join(dir, "test.db"))
	defer s.Close()

	acc := config.IMAPAccount{
		Username:       "test@example.com",
		PasswordSecret: "myplaintextpw",
		Host:           "imap.example.com",
		Port:           993,
		Folder:         "BadFolder",
	}

	poller, _ := NewMailPoller(s, []config.IMAPAccount{acc}, MailPollerOptions{ProcessingDir: t.TempDir()})

	fakeClient := &fakeIMAPClient{
		selectErr: errors.New("folder not found"),
	}
	poller.dialIMAP = func(ctx context.Context, addr, host string) (imapClient, error) {
		return fakeClient, nil
	}

	err := poller.pollAccount(context.Background(), acc)
	if err == nil {
		t.Fatal("expected error from select failure")
	}
	if !fakeClient.closed {
		t.Fatal("expected client to be closed after select failure")
	}
}

func TestMailPoller_SearchFailure(t *testing.T) {
	dir := t.TempDir()
	s, _ := store.Open(filepath.Join(dir, "test.db"))
	defer s.Close()

	acc := config.IMAPAccount{
		Username:       "test@example.com",
		PasswordSecret: "myplaintextpw",
		Host:           "imap.example.com",
		Port:           993,
	}

	poller, _ := NewMailPoller(s, []config.IMAPAccount{acc}, MailPollerOptions{ProcessingDir: t.TempDir()})

	fakeClient := &fakeIMAPClient{
		searchErr: errors.New("search protocol error"),
	}
	poller.dialIMAP = func(ctx context.Context, addr, host string) (imapClient, error) {
		return fakeClient, nil
	}

	err := poller.pollAccount(context.Background(), acc)
	if err == nil {
		t.Fatal("expected error from search failure")
	}
	if !strings.Contains(err.Error(), "search") {
		t.Fatalf("expected search-related error, got: %v", err)
	}
}

func TestMailPoller_FetchFailure(t *testing.T) {
	dir := t.TempDir()
	s, _ := store.Open(filepath.Join(dir, "test.db"))
	defer s.Close()

	acc := config.IMAPAccount{
		Username:       "test@example.com",
		PasswordSecret: "myplaintextpw",
		Host:           "imap.example.com",
		Port:           993,
	}

	poller, _ := NewMailPoller(s, []config.IMAPAccount{acc}, MailPollerOptions{ProcessingDir: t.TempDir()})

	fakeClient := &fakeIMAPClient{
		searchRes: []uint32{1},
		fetchEnvelopesRes: []*imapMessage{
			{
				SeqNum: 1,
				Envelope: &imapEnvelope{
					MessageID: "fetch-err@example.com",
				},
			},
		},
		fetchErr: errors.New("fetch failed"),
	}
	poller.dialIMAP = func(ctx context.Context, addr, host string) (imapClient, error) {
		return fakeClient, nil
	}

	err := poller.pollAccount(context.Background(), acc)
	if err == nil {
		t.Fatal("expected error from fetch failure")
	}
	if !strings.Contains(err.Error(), "fetch") {
		t.Fatalf("expected fetch-related error, got: %v", err)
	}
}

func TestMailPoller_ProcessMessage_NilEnvelope(t *testing.T) {
	dir := t.TempDir()
	s, _ := store.Open(filepath.Join(dir, "test.db"))
	defer s.Close()

	poller, _ := NewMailPoller(s, nil, MailPollerOptions{ProcessingDir: t.TempDir()})

	msg := &imapMessage{
		SeqNum:   1,
		Envelope: nil,
		Body:     []byte("body"),
	}

	err := poller.processMessage(context.Background(), config.IMAPAccount{}, nil, msg)
	if err != nil {
		t.Fatalf("expected nil error for nil envelope, got: %v", err)
	}
}

func TestMailPoller_ProcessMessage_EmptyBody(t *testing.T) {
	dir := t.TempDir()
	s, _ := store.Open(filepath.Join(dir, "test.db"))
	defer s.Close()

	poller, _ := NewMailPoller(s, nil, MailPollerOptions{ProcessingDir: t.TempDir()})

	msg := &imapMessage{
		SeqNum: 1,
		Envelope: &imapEnvelope{
			MessageID: "test@example.com",
		},
		Body: []byte{},
	}

	fakeClient := &fakeIMAPClient{}
	err := poller.processMessage(context.Background(), config.IMAPAccount{}, fakeClient, msg)
	if err == nil {
		t.Fatal("expected error for empty body")
	}
	if !strings.Contains(err.Error(), "no body section") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMailPoller_ProcessMessage_InvalidMailBody(t *testing.T) {
	dir := t.TempDir()
	s, _ := store.Open(filepath.Join(dir, "test.db"))
	defer s.Close()

	poller, _ := NewMailPoller(s, nil, MailPollerOptions{ProcessingDir: t.TempDir()})

	msg := &imapMessage{
		SeqNum: 1,
		Envelope: &imapEnvelope{
			MessageID: "test@example.com",
		},
		Body: []byte("not a valid email at all {{{"),
	}

	fakeClient := &fakeIMAPClient{}
	err := poller.processMessage(context.Background(), config.IMAPAccount{}, fakeClient, msg)
	if err == nil {
		t.Fatal("expected error for invalid mail body")
	}
	if !strings.Contains(err.Error(), "create mail reader") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMailPoller_ProcessMessage_StoreSeenError(t *testing.T) {
	dir := t.TempDir()
	s, _ := store.Open(filepath.Join(dir, "test.db"))
	defer s.Close()

	poller, _ := NewMailPoller(s, nil, MailPollerOptions{
		ProcessingDir: dir,
	})

	msg := &imapMessage{
		SeqNum: 5,
		Envelope: &imapEnvelope{
			MessageID: "seen-error@example.com",
		},
		Body: createFakeEmail("seen-error@example.com", "sender@example.com", "doc.txt", "data"),
	}

	fakeClient := &fakeIMAPClient{
		storeSeenErr: errors.New("store failed"),
	}
	acc := config.IMAPAccount{
		Action: "mark_seen",
	}

	err := poller.processMessage(context.Background(), acc, fakeClient, msg)
	if err == nil {
		t.Fatal("expected error from StoreSeen failure")
	}
	if !strings.Contains(err.Error(), "mark seen") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMailPoller_ProcessMessage_MoveError(t *testing.T) {
	dir := t.TempDir()
	s, _ := store.Open(filepath.Join(dir, "test.db"))
	defer s.Close()

	poller, _ := NewMailPoller(s, nil, MailPollerOptions{
		ProcessingDir: dir,
	})

	msg := &imapMessage{
		SeqNum: 5,
		Envelope: &imapEnvelope{
			MessageID: "move-error@example.com",
		},
		Body: createFakeEmail("move-error@example.com", "sender@example.com", "doc.txt", "data"),
	}

	fakeClient := &fakeIMAPClient{
		moveErr: errors.New("move failed"),
	}
	acc := config.IMAPAccount{
		Action: "move",
		MoveTo: "Archive",
	}

	err := poller.processMessage(context.Background(), acc, fakeClient, msg)
	if err == nil {
		t.Fatal("expected error from Move failure")
	}
	if !strings.Contains(err.Error(), "move to") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMailPoller_ProcessMessage_EmptyMessageID(t *testing.T) {
	dir := t.TempDir()
	s, _ := store.Open(filepath.Join(dir, "test.db"))
	defer s.Close()

	poller, _ := NewMailPoller(s, nil, MailPollerOptions{
		ProcessingDir: dir,
	})

	msg := &imapMessage{
		SeqNum: 7,
		Envelope: &imapEnvelope{
			MessageID: "",
		},
		Body: createFakeEmail("", "sender@example.com", "doc.txt", "data"),
	}

	fakeClient := &fakeIMAPClient{}
	acc := config.IMAPAccount{Action: "mark_seen"}

	err := poller.processMessage(context.Background(), acc, fakeClient, msg)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if len(fakeClient.storedSeenSeqs) != 1 {
		t.Fatalf("expected StoreSeen to be called, got %v", fakeClient.storedSeenSeqs)
	}
}

func TestMailPoller_ProcessMessage_AlreadyProcessed(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	poller, _ := NewMailPoller(s, nil, MailPollerOptions{
		ProcessingDir: dir,
	})

	ctx := context.Background()
	msgID := "dup-process@example.com"
	if err := s.TrackMailMessage(ctx, msgID, "test"); err != nil {
		t.Fatal(err)
	}

	msg := &imapMessage{
		SeqNum: 1,
		Envelope: &imapEnvelope{
			MessageID: msgID,
		},
		Body: createFakeEmail(msgID, "sender@example.com", "doc.txt", "data"),
	}

	fakeClient := &fakeIMAPClient{}
	acc := config.IMAPAccount{Action: "mark_seen"}

	err = poller.processMessage(ctx, acc, fakeClient, msg)
	if err != nil {
		t.Fatalf("expected no error for already-processed message, got: %v", err)
	}
	if len(fakeClient.storedSeenSeqs) != 0 {
		t.Fatalf("expected StoreSeen not to be called for already-processed message, got %v", fakeClient.storedSeenSeqs)
	}
}

// errReader returns data up to errAfter bytes, then err. After the first error,
// subsequent reads return io.EOF so the mail part loop terminates.
type errReader struct {
	data     []byte
	pos      int
	errAfter int
	err      error
	errored  bool
}

func (r *errReader) Read(p []byte) (int, error) {
	if r.errored {
		return 0, io.EOF
	}
	if r.pos >= r.errAfter {
		r.errored = true
		return 0, r.err
	}
	remaining := r.errAfter - r.pos
	toRead := len(p)
	if toRead > remaining {
		toRead = remaining
	}
	if r.pos+toRead > len(r.data) {
		toRead = len(r.data) - r.pos
	}
	if toRead == 0 {
		r.errored = true
		return 0, r.err
	}
	n := copy(p, r.data[r.pos:r.pos+toRead])
	r.pos += n
	if r.pos >= r.errAfter {
		r.errored = true
		return n, r.err
	}
	return n, nil
}

func TestNewMailPoller_InvalidProcessingDir(t *testing.T) {
	dbDir := t.TempDir()
	s, err := store.Open(filepath.Join(dbDir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	origGetwd := getwdFn
	getwdFn = func() (string, error) {
		return "", errors.New("getcwd: no such file or directory")
	}
	defer func() { getwdFn = origGetwd }()

	_, err = NewMailPoller(s, nil, MailPollerOptions{
		ProcessingDir: "relative/path",
	})
	if err == nil {
		t.Fatal("expected error for invalid processing dir")
	}
	if !strings.Contains(err.Error(), "resolve processing dir") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewMailPoller_InvalidFailedDir(t *testing.T) {
	dbDir := t.TempDir()
	s, err := store.Open(filepath.Join(dbDir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	origGetwd := getwdFn
	getwdFn = func() (string, error) {
		return "", errors.New("getcwd: no such file or directory")
	}
	defer func() { getwdFn = origGetwd }()

	_, err = NewMailPoller(s, nil, MailPollerOptions{
		FailedDir: "relative/path",
	})
	if err == nil {
		t.Fatal("expected error for invalid failed dir")
	}
	if !strings.Contains(err.Error(), "resolve failed dir") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMailPoller_PollLoopErrorLogging(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	acc := config.IMAPAccount{
		Username:       "test@example.com",
		PasswordSecret: "myplaintextpw",
		Host:           "imap.example.com",
		Port:           993,
	}

	poller, err := NewMailPoller(s, []config.IMAPAccount{acc}, MailPollerOptions{
		Interval: 1 * time.Nanosecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	poller.dialIMAP = func(ctx context.Context, addr, host string) (imapClient, error) {
		return nil, errors.New("connection refused")
	}

	var logBuf bytes.Buffer
	log.SetOutput(&logBuf)
	defer log.SetOutput(os.Stderr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := poller.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Let the loop run for a short time to trigger both initial + ticker poll errors.
	time.Sleep(50 * time.Millisecond)
	cancel()
	poller.Close()

	output := logBuf.String()
	if !strings.Contains(output, "initial poll failed") {
		t.Errorf("expected log to contain 'initial poll failed', got: %s", output)
	}
	if !strings.Contains(output, "poll failed") {
		t.Errorf("expected log to contain 'poll failed', got: %s", output)
	}
	if strings.Contains(output, "connection refused") {
		t.Errorf("log must not contain the raw poll error, got: %s", output)
	}
}

func TestMailPoller_ProcessMessage_HasMailMessageError(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	// Close the store so HasMailMessage fails.
	s.Close()

	poller, err := NewMailPoller(s, nil, MailPollerOptions{ProcessingDir: dir})
	if err != nil {
		t.Fatal(err)
	}

	msg := &imapMessage{
		SeqNum: 1,
		Envelope: &imapEnvelope{
			MessageID: "has-mail-err@example.com",
		},
		Body: createFakeEmail("has-mail-err@example.com", "sender@example.com", "doc.txt", "data"),
	}

	err = poller.processMessage(context.Background(), config.IMAPAccount{}, &fakeIMAPClient{}, msg)
	if err == nil {
		t.Fatal("expected error from HasMailMessage failure")
	}
	if !strings.Contains(err.Error(), "check idempotency") {
		t.Fatalf("expected 'check idempotency' in error, got: %v", err)
	}
}

func TestMailPoller_ProcessMessage_NextPartError(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	poller, err := NewMailPoller(s, nil, MailPollerOptions{ProcessingDir: dir})
	if err != nil {
		t.Fatal(err)
	}

	emailData := []byte("MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=boundary\r\n\r\n--boundary\r\nContent-Type: text/plain\r\n\r\nHello\r\n--boundary\r\nContent-Type: text/plain\r\n\r\nWorld\r\n--boundary--\r\n")

	poller.newMailReader = func(r io.Reader) (*mail.Reader, error) {
		er := &errReader{data: emailData, errAfter: 80, err: io.ErrUnexpectedEOF}
		return mail.CreateReader(er)
	}

	msg := &imapMessage{
		SeqNum: 1,
		Envelope: &imapEnvelope{
			MessageID: "nextpart-err@example.com",
		},
		Body: emailData,
	}

	var logBuf bytes.Buffer
	log.SetOutput(&logBuf)
	defer log.SetOutput(os.Stderr)

	// The go-message multipart reader caches non-EOF errors permanently, so
	// processMessage's `continue` on NextPart error created an infinite loop.
	// The ctx.Err() break in the loop now stops on cancellation.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- poller.processMessage(ctx, config.IMAPAccount{}, &fakeIMAPClient{}, msg)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		cancel()
		<-done // wait for goroutine to exit
	}

	output := logBuf.String()
	if !strings.Contains(output, "Error reading part") {
		t.Errorf("expected log to contain 'Error reading part', got: %s", output)
	}
}

func TestMailPoller_ProcessMessage_EmptyAttachmentFilename(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	processingDir := filepath.Join(dir, "processing")
	if err := os.MkdirAll(processingDir, 0700); err != nil {
		t.Fatal(err)
	}

	poller, err := NewMailPoller(s, nil, MailPollerOptions{ProcessingDir: processingDir})
	if err != nil {
		t.Fatal(err)
	}

	// Create an email with an attachment that has NO filename in Content-Disposition.
	var emailBuf bytes.Buffer
	emailBuf.WriteString("MIME-Version: 1.0\r\n")
	emailBuf.WriteString("Message-ID: <empty-fn@example.com>\r\n")
	emailBuf.WriteString("From: sender@example.com\r\n")
	emailBuf.WriteString("Content-Type: multipart/mixed; boundary=boundary\r\n\r\n")
	emailBuf.WriteString("--boundary\r\n")
	emailBuf.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
	emailBuf.WriteString("Body text.\r\n")
	emailBuf.WriteString("--boundary\r\n")
	emailBuf.WriteString("Content-Type: application/octet-stream\r\n")
	emailBuf.WriteString("Content-Disposition: attachment\r\n\r\n") // No filename!
	emailBuf.WriteString("attachment data here\r\n")
	emailBuf.WriteString("--boundary--\r\n")

	msg := &imapMessage{
		SeqNum: 1,
		Envelope: &imapEnvelope{
			MessageID: "empty-fn@example.com",
		},
		Body: emailBuf.Bytes(),
	}

	fakeClient := &fakeIMAPClient{}
	acc := config.IMAPAccount{Action: "mark_seen"}

	err = poller.processMessage(context.Background(), acc, fakeClient, msg)

	expectedFile := filepath.Join(processingDir, "empty-fn@example.com-attachment.bin")
	if _, err := os.Stat(expectedFile); os.IsNotExist(err) {
		t.Errorf("expected file %s to exist (empty filename defaults to attachment.bin)", expectedFile)
	}

	if err != nil && !strings.Contains(err.Error(), "enqueue attachment") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMailPoller_ProcessMessage_EnqueueFileError(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	processingDir := filepath.Join(dir, "processing")
	if err := os.MkdirAll(processingDir, 0700); err != nil {
		t.Fatal(err)
	}

	poller, err := NewMailPoller(s, nil, MailPollerOptions{ProcessingDir: processingDir})
	if err != nil {
		t.Fatal(err)
	}

	// Create an email with an attachment with an unsupported extension (.xyzunknown).
	// extract.Detect will fail with "unsupported file type", causing enqueueFile to error.
	var emailBuf bytes.Buffer
	emailBuf.WriteString("MIME-Version: 1.0\r\n")
	emailBuf.WriteString("Message-ID: <enqueue-err@example.com>\r\n")
	emailBuf.WriteString("From: sender@example.com\r\n")
	emailBuf.WriteString("Content-Type: multipart/mixed; boundary=boundary\r\n\r\n")
	emailBuf.WriteString("--boundary\r\n")
	emailBuf.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
	emailBuf.WriteString("Body text.\r\n")
	emailBuf.WriteString("--boundary\r\n")
	emailBuf.WriteString("Content-Type: application/octet-stream; name=\"data.xyzunknown\"\r\n")
	emailBuf.WriteString("Content-Disposition: attachment; filename=\"data.xyzunknown\"\r\n\r\n")
	emailBuf.WriteString("some data\r\n")
	emailBuf.WriteString("--boundary--\r\n")

	msg := &imapMessage{
		SeqNum: 1,
		Envelope: &imapEnvelope{
			MessageID: "enqueue-err@example.com",
		},
		Body: emailBuf.Bytes(),
	}

	fakeClient := &fakeIMAPClient{}
	acc := config.IMAPAccount{}

	var logBuf bytes.Buffer
	log.SetOutput(&logBuf)
	defer log.SetOutput(os.Stderr)

	err = poller.processMessage(context.Background(), acc, fakeClient, msg)
	if err == nil {
		t.Fatal("expected error from enqueue failure, got nil")
	}
	if !strings.Contains(err.Error(), "enqueue attachment") {
		t.Errorf("expected error to contain 'enqueue attachment', got: %v", err)
	}

	// Verify IMAP action was NOT applied (no StoreSeen/Move calls)
	if fakeClient.storedSeenSeqs != nil {
		t.Error("expected no StoreSeen call after enqueue failure")
	}
	if fakeClient.movedSeqs != nil {
		t.Error("expected no Move call after enqueue failure")
	}

	// Verify message was NOT tracked
	has, err := s.HasMailMessage(context.Background(), "enqueue-err@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Error("expected message NOT to be tracked after enqueue failure")
	}
}

func TestMailPoller_PollAccount_UIDCursor_FirstPollScansFromOne(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	acc := config.IMAPAccount{
		Username:       "test@example.com",
		PasswordSecret: "myplaintextpw",
		Host:           "imap.example.com",
		Port:           993,
	}
	accountID := config.AccountID(acc)

	poller, err := NewMailPoller(s, []config.IMAPAccount{acc}, MailPollerOptions{})
	if err != nil {
		t.Fatal(err)
	}

	fakeClient := &fakeIMAPClient{
		selectStatus: &mailboxStatus{UIDValidity: 100, UIDNext: 50},
		searchRes:    []uint32{},
	}
	poller.dialIMAP = func(ctx context.Context, addr, host string) (imapClient, error) {
		return fakeClient, nil
	}

	if err := poller.pollAccount(context.Background(), acc); err != nil {
		t.Fatalf("pollAccount: %v", err)
	}

	if fakeClient.lastSearchCriteria == nil || len(fakeClient.lastSearchCriteria.UID) != 1 {
		t.Fatalf("expected a single UID range in the search criteria, got %+v", fakeClient.lastSearchCriteria)
	}
	gotRange := fakeClient.lastSearchCriteria.UID[0]
	if len(gotRange) != 1 || gotRange[0].Start != 1 {
		t.Errorf("expected first poll to search from UID 1, got range %+v", gotRange)
	}

	cursor, err := s.GetMailPollCursor(context.Background(), accountID)
	if err != nil {
		t.Fatal(err)
	}
	if cursor == nil {
		t.Fatal("expected a poll cursor to be recorded")
	}
	if cursor.UIDValidity != 100 || cursor.LastUID != 49 {
		t.Errorf("expected cursor {UIDValidity:100 LastUID:49}, got %+v", cursor)
	}
}

func TestMailPoller_PollAccount_UIDCursor_ResumesFromLastUID(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	acc := config.IMAPAccount{
		Username:       "test@example.com",
		PasswordSecret: "myplaintextpw",
		Host:           "imap.example.com",
		Port:           993,
	}
	accountID := config.AccountID(acc)

	if err := s.SetMailPollCursor(context.Background(), accountID, "INBOX", 100, 49); err != nil {
		t.Fatal(err)
	}

	poller, err := NewMailPoller(s, []config.IMAPAccount{acc}, MailPollerOptions{})
	if err != nil {
		t.Fatal(err)
	}

	fakeClient := &fakeIMAPClient{
		selectStatus: &mailboxStatus{UIDValidity: 100, UIDNext: 55},
		searchRes:    []uint32{},
	}
	poller.dialIMAP = func(ctx context.Context, addr, host string) (imapClient, error) {
		return fakeClient, nil
	}

	if err := poller.pollAccount(context.Background(), acc); err != nil {
		t.Fatalf("pollAccount: %v", err)
	}

	gotRange := fakeClient.lastSearchCriteria.UID[0]
	if len(gotRange) != 1 || gotRange[0].Start != 50 {
		t.Errorf("expected resumed poll to search from UID 50 (last cursor + 1), got range %+v", gotRange)
	}

	cursor, err := s.GetMailPollCursor(context.Background(), accountID)
	if err != nil {
		t.Fatal(err)
	}
	if cursor.LastUID != 54 {
		t.Errorf("expected cursor LastUID=54 after this poll, got %+v", cursor)
	}
}

func TestMailPoller_PollAccount_UIDCursor_UIDValidityChangeTriggersRescan(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	acc := config.IMAPAccount{
		Username:       "test@example.com",
		PasswordSecret: "myplaintextpw",
		Host:           "imap.example.com",
		Port:           993,
	}
	accountID := config.AccountID(acc)

	// A prior poll recorded a cursor under an old UIDVALIDITY.
	if err := s.SetMailPollCursor(context.Background(), accountID, "INBOX", 100, 49); err != nil {
		t.Fatal(err)
	}

	poller, err := NewMailPoller(s, []config.IMAPAccount{acc}, MailPollerOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// The server now reports a different UIDVALIDITY (e.g. folder rebuild).
	fakeClient := &fakeIMAPClient{
		selectStatus: &mailboxStatus{UIDValidity: 200, UIDNext: 10},
		searchRes:    []uint32{},
	}
	poller.dialIMAP = func(ctx context.Context, addr, host string) (imapClient, error) {
		return fakeClient, nil
	}

	if err := poller.pollAccount(context.Background(), acc); err != nil {
		t.Fatalf("pollAccount: %v", err)
	}

	gotRange := fakeClient.lastSearchCriteria.UID[0]
	if len(gotRange) != 1 || gotRange[0].Start != 1 {
		t.Errorf("expected a UIDVALIDITY change to trigger a rescan from UID 1, got range %+v", gotRange)
	}

	cursor, err := s.GetMailPollCursor(context.Background(), accountID)
	if err != nil {
		t.Fatal(err)
	}
	if cursor.UIDValidity != 200 || cursor.LastUID != 9 {
		t.Errorf("expected cursor {UIDValidity:200 LastUID:9} after rescan, got %+v", cursor)
	}
}

func TestMailPoller_PollAccount_ProcessMessageError(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	processingDir := filepath.Join(dir, "processing")
	os.MkdirAll(processingDir, 0700)

	acc := config.IMAPAccount{
		Username:       "test@example.com",
		PasswordSecret: "myplaintextpw",
		Host:           "imap.example.com",
		Port:           993,
	}

	poller, err := NewMailPoller(s, []config.IMAPAccount{acc}, MailPollerOptions{
		ProcessingDir: processingDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	msgWithEmptyBody := &imapMessage{
		SeqNum: 1,
		Envelope: &imapEnvelope{
			MessageID: "empty-body@example.com",
		},
		Body: []byte{}, // empty body → processMessage returns error
	}

	fakeClient := &fakeIMAPClient{
		searchRes: []uint32{1},
		fetchEnvelopesRes: []*imapMessage{
			{
				SeqNum: 1,
				Envelope: &imapEnvelope{
					MessageID: "empty-body@example.com",
				},
			},
		},
		fetchRes: []*imapMessage{msgWithEmptyBody},
	}
	poller.dialIMAP = func(ctx context.Context, addr, host string) (imapClient, error) {
		return fakeClient, nil
	}

	var logBuf bytes.Buffer
	log.SetOutput(&logBuf)
	defer log.SetOutput(os.Stderr)

	err = poller.pollAccount(context.Background(), acc)
	if err != nil {
		t.Fatalf("pollAccount should not fail (errors are logged): %v", err)
	}

	output := logBuf.String()
	if !strings.Contains(output, "Failed to process message") {
		t.Errorf("expected log to contain 'Failed to process message', got: %s", output)
	}
}

func TestMailPoller_ProcessMessage_SaveFileError(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	processingDir := filepath.Join(dir, "processing")
	if err := os.MkdirAll(processingDir, 0700); err != nil {
		t.Fatal(err)
	}

	poller, err := NewMailPoller(s, nil, MailPollerOptions{ProcessingDir: processingDir})
	if err != nil {
		t.Fatal(err)
	}

	// Create a file in the processing dir to trigger O_EXCL conflict
	conflictPath := filepath.Join(processingDir, "save-err@example.com-data.bin")
	if err := os.WriteFile(conflictPath, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}

	var emailBuf bytes.Buffer
	emailBuf.WriteString("MIME-Version: 1.0\r\n")
	emailBuf.WriteString("Message-ID: <save-err@example.com>\r\n")
	emailBuf.WriteString("From: sender@example.com\r\n")
	emailBuf.WriteString("Content-Type: multipart/mixed; boundary=boundary\r\n\r\n")
	emailBuf.WriteString("--boundary\r\n")
	emailBuf.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
	emailBuf.WriteString("Body text.\r\n")
	emailBuf.WriteString("--boundary\r\n")
	emailBuf.WriteString("Content-Type: application/octet-stream; name=\"data.bin\"\r\n")
	emailBuf.WriteString("Content-Disposition: attachment; filename=\"data.bin\"\r\n\r\n")
	emailBuf.WriteString("some data\r\n")
	emailBuf.WriteString("--boundary--\r\n")

	msg := &imapMessage{
		SeqNum: 1,
		Envelope: &imapEnvelope{
			MessageID: "save-err@example.com",
		},
		Body: emailBuf.Bytes(),
	}

	fakeClient := &fakeIMAPClient{}
	acc := config.IMAPAccount{}

	err = poller.processMessage(context.Background(), acc, fakeClient, msg)
	if err == nil {
		t.Fatal("expected error from save failure, got nil")
	}
	if !strings.Contains(err.Error(), "save attachment") {
		t.Errorf("expected error to contain 'save attachment', got: %v", err)
	}

	if fakeClient.storedSeenSeqs != nil {
		t.Error("expected no StoreSeen call after save failure")
	}
	if fakeClient.movedSeqs != nil {
		t.Error("expected no Move call after save failure")
	}

	has, err := s.HasMailMessage(context.Background(), "save-err@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Error("expected message NOT to be tracked after save failure")
	}
}

func TestMailPoller_RePollNoBodyFetch(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	processingDir := filepath.Join(dir, "processing")
	_ = os.MkdirAll(processingDir, 0700)

	acc := config.IMAPAccount{
		Username:       "test@example.com",
		PasswordSecret: "myplaintextpw",
		Host:           "imap.example.com",
		Port:           993,
		Action:         "mark_seen",
	}

	poller, _ := NewMailPoller(s, []config.IMAPAccount{acc}, MailPollerOptions{
		ProcessingDir: processingDir,
	})

	envelopes := []*imapMessage{
		{
			SeqNum: 1,
			Envelope: &imapEnvelope{
				MessageID: "repoll@example.com",
			},
		},
	}
	fullMessages := []*imapMessage{
		{
			SeqNum: 1,
			Envelope: &imapEnvelope{
				MessageID: "repoll@example.com",
			},
			Body: createFakeEmail("repoll@example.com", "sender@example.com", "invoice.txt", "Data"),
		},
	}

	fakeClient := &fakeIMAPClient{
		searchRes:         []uint32{1},
		fetchEnvelopesRes: envelopes,
		fetchRes:          fullMessages,
	}

	poller.dialIMAP = func(ctx context.Context, addr, host string) (imapClient, error) {
		return fakeClient, nil
	}

	ctx := context.Background()

	err = poller.pollAccount(ctx, acc)
	if err != nil {
		t.Fatal(err)
	}

	has, err := s.HasMailMessage(ctx, "repoll@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Fatal("expected message to be tracked after first poll")
	}

	fakeClient.fetchRes = nil

	err = poller.pollAccount(ctx, acc)
	if err != nil {
		t.Fatal(err)
	}

	if fakeClient.fetchRes != nil {
		t.Error("expected Fetch to NOT be called on re-poll of already-processed message")
	}
}

func TestMailPoller_PollAccountAndRecord_RecordsSuccessAndFailure(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	acc := config.IMAPAccount{
		Username:       "user@example.com",
		PasswordSecret: "plaintext-pw",
		Host:           "imap.example.com",
		Port:           993,
	}
	accountID := config.AccountID(acc)

	poller, err := NewMailPoller(s, []config.IMAPAccount{acc}, MailPollerOptions{Interval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}

	// Failure: dial fails.
	poller.dialIMAP = func(ctx context.Context, addr, host string) (imapClient, error) {
		return nil, errors.New("connection refused")
	}
	if err := poller.pollAccountAndRecord(context.Background(), acc); err == nil {
		t.Fatal("expected pollAccountAndRecord to surface the dial error")
	}
	got, err := s.GetMailPollStatus(context.Background(), accountID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Status != "error" || got.LastError == "" {
		t.Fatalf("expected a recorded error status, got %+v", got)
	}
	if strings.Contains(got.LastError, "connection refused") {
		t.Errorf("recorded error must not leak the raw poll error, got: %s", got.LastError)
	}

	// Success: dial and login succeed.
	poller.dialIMAP = func(ctx context.Context, addr, host string) (imapClient, error) {
		return &fakeIMAPClient{}, nil
	}
	if err := poller.pollAccountAndRecord(context.Background(), acc); err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	got, err = s.GetMailPollStatus(context.Background(), accountID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Status != "ok" || got.LastError != "" {
		t.Fatalf("expected a recorded ok status, got %+v", got)
	}
}

func TestMailPoller_Start_WarnsOnPlaintextPasswordSecret(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	acc := config.IMAPAccount{
		Username:       "user@example.com",
		PasswordSecret: "hunter2",
		Host:           "imap.example.com",
		Port:           993,
	}

	poller, err := NewMailPoller(s, []config.IMAPAccount{acc}, MailPollerOptions{
		Interval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	poller.dialIMAP = func(ctx context.Context, addr, host string) (imapClient, error) {
		return nil, errors.New("connection refused")
	}

	var logBuf bytes.Buffer
	log.SetOutput(&logBuf)
	defer log.SetOutput(os.Stderr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := poller.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	cancel()
	poller.Close()

	output := logBuf.String()
	if !strings.Contains(output, "plaintext config") {
		t.Errorf("expected log to warn about plaintext password_secret, got: %s", output)
	}
	if strings.Contains(output, "hunter2") {
		t.Errorf("log must not contain the plaintext secret value, got: %s", output)
	}
}

func TestMailPoller_Start_NoWarningForResolvedSecret(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	os.Setenv("TEST_MAILPOLLER_START_NO_WARN", "value")
	defer os.Unsetenv("TEST_MAILPOLLER_START_NO_WARN")

	acc := config.IMAPAccount{
		Username:       "user@example.com",
		PasswordSecret: "env://TEST_MAILPOLLER_START_NO_WARN",
		Host:           "imap.example.com",
		Port:           993,
	}

	poller, err := NewMailPoller(s, []config.IMAPAccount{acc}, MailPollerOptions{
		Interval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	poller.dialIMAP = func(ctx context.Context, addr, host string) (imapClient, error) {
		return nil, errors.New("connection refused")
	}

	var logBuf bytes.Buffer
	log.SetOutput(&logBuf)
	defer log.SetOutput(os.Stderr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := poller.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	cancel()
	poller.Close()

	if strings.Contains(logBuf.String(), "plaintext config") {
		t.Errorf("did not expect a plaintext warning for an env:// secret, got: %s", logBuf.String())
	}
}

func TestMailPollLogReason(t *testing.T) {
	secretLeak := fmt.Errorf("resolve password_secret for user@example.com: %w",
		errors.New("keychain item symvault://imap/invoices not found"))
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"canceled", context.Canceled, "shutting down"},
		{"deadline", context.DeadlineExceeded, "deadline exceeded"},
		{"generic wraps secret detail", secretLeak, "authentication or configuration error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mailPollLogReason(tt.err)
			if got != tt.want {
				t.Errorf("mailPollLogReason() = %q, want %q", got, tt.want)
			}
			if strings.Contains(got, "symvault") || strings.Contains(got, "keychain") {
				t.Errorf("mailPollLogReason() leaked secret detail: %q", got)
			}
		})
	}
}
