package ingest

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

// literal wraps a byte slice to satisfy imap.LiteralReader.
type literal struct {
	*bytes.Reader
	size int64
}

func (l *literal) Size() int64 { return l.size }

func newLiteral(data []byte) *literal {
	return &literal{bytes.NewReader(data), int64(len(data))}
}

// testCA creates a self-signed CA and returns its certificate and private key.
// The CA is used to sign server certificates for in-process IMAP tests so that
// defaultDialIMAP (which hardcodes TLSConfig without InsecureSkipVerify) can
// verify the server certificate.
func testCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}

	caSerial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("generate CA serial: %v", err)
	}

	caTemplate := &x509.Certificate{
		SerialNumber:          caSerial,
		Subject:               pkix.Name{CommonName: "Test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		IsCA:                  true,
		BasicConstraintsValid: true,
	}

	caCertDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA cert: %v", err)
	}

	caCert, err := x509.ParseCertificate(caCertDER)
	if err != nil {
		t.Fatalf("parse CA cert: %v", err)
	}

	return caCert, caKey
}

// testCert creates a server certificate signed by the given CA for the given host.
func testCert(t *testing.T, caCert *x509.Certificate, caKey *ecdsa.PrivateKey, host string) tls.Certificate {
	t.Helper()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate server key: %v", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("generate server serial: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		DNSNames:     []string{host},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, caCert, &priv.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create server cert: %v", err)
	}

	return tls.Certificate{
		Certificate: [][]byte{certDER},
		PrivateKey:  priv,
	}
}

// testIMAPServer starts an in-process IMAP server with a pre-loaded mailbox
// and returns the address to connect to, a root CA pool for TLS, and a cleanup function.
func testIMAPServer(t *testing.T) (addr string, rootCAs *x509.CertPool, cleanup func()) {
	t.Helper()

	caCert, caKey := testCA(t)
	cert := testCert(t, caCert, caKey, "localhost")

	// Set up in-memory server with a user and mailbox.
	memServer := imapmemserver.New()
	user := imapmemserver.NewUser("testuser", "testpass")
	memServer.AddUser(user)

	if err := user.Create("INBOX", nil); err != nil {
		t.Fatalf("create INBOX: %v", err)
	}

	// Append two fixture messages.
	msg1 := []byte("From: alice@example.com\r\n" +
		"Message-ID: <msg-1@example.com>\r\n" +
		"Subject: Invoice\r\n" +
		"Date: Mon, 01 Jan 2024 00:00:00 +0000\r\n" +
		"Content-Type: text/plain\r\n\r\n" +
		"Invoice body 1\r\n")

	msg2 := []byte("From: bob@example.com\r\n" +
		"Message-ID: <msg-2@example.com>\r\n" +
		"Subject: Receipt\r\n" +
		"Date: Tue, 02 Jan 2024 00:00:00 +0000\r\n" +
		"Content-Type: text/plain\r\n\r\n" +
		"Invoice body 2\r\n")

	if _, err := user.Append("INBOX", newLiteral(msg1), &imap.AppendOptions{}); err != nil {
		t.Fatalf("append msg1: %v", err)
	}
	if _, err := user.Append("INBOX", newLiteral(msg2), &imap.AppendOptions{}); err != nil {
		t.Fatalf("append msg2: %v", err)
	}

	// Create IMAP server with MOVE support.
	server := imapserver.New(&imapserver.Options{
		NewSession: func(_ *imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return memServer.NewSession(), nil, nil
		},
		Caps: imap.CapSet{
			imap.CapIMAP4rev2: {},
			imap.CapMove:      {},
		},
		InsecureAuth: true,
	})

	// Listen on localhost with TLS.
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{cert},
	})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	go server.Serve(ln)

	// Build a CertPool containing the test CA so clients can verify the server cert.
	rootCAs = x509.NewCertPool()
	rootCAs.AddCert(caCert)

	return ln.Addr().String(), rootCAs, func() {
		server.Close()
		ln.Close()
	}
}

// dialTestClient connects to the test server with the given root CA pool.
func dialTestClient(t *testing.T, addr string, rootCAs *x509.CertPool) *imapclient.Client {
	t.Helper()
	c, err := imapclient.DialTLS(addr, &imapclient.Options{
		TLSConfig: &tls.Config{
			RootCAs: rootCAs,
		},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return c
}

func TestRealIMAPClient_Login(t *testing.T) {
	addr, rootCAs, cleanup := testIMAPServer(t)
	defer cleanup()

	c := dialTestClient(t, addr, rootCAs)
	client := &realIMAPClient{c}

	if err := client.Login("testuser", "testpass"); err != nil {
		t.Fatalf("Login failed: %v", err)
	}

	c.Logout().Wait()
}

func TestRealIMAPClient_Login_Failure(t *testing.T) {
	addr, rootCAs, cleanup := testIMAPServer(t)
	defer cleanup()

	c := dialTestClient(t, addr, rootCAs)
	client := &realIMAPClient{c}

	// Wrong password should fail.
	if err := client.Login("testuser", "wrongpass"); err == nil {
		t.Fatal("expected login failure with wrong password")
	}

	c.Logout().Wait()
}

func TestRealIMAPClient_Select(t *testing.T) {
	addr, rootCAs, cleanup := testIMAPServer(t)
	defer cleanup()

	c := dialTestClient(t, addr, rootCAs)
	client := &realIMAPClient{c}

	if err := client.Login("testuser", "testpass"); err != nil {
		t.Fatalf("Login: %v", err)
	}

	if _, err := client.Select("INBOX"); err != nil {
		t.Fatalf("Select failed: %v", err)
	}

	c.Logout().Wait()
}

func TestRealIMAPClient_Search(t *testing.T) {
	addr, rootCAs, cleanup := testIMAPServer(t)
	defer cleanup()

	c := dialTestClient(t, addr, rootCAs)
	client := &realIMAPClient{c}

	if err := client.Login("testuser", "testpass"); err != nil {
		t.Fatalf("Login: %v", err)
	}
	status, err := client.Select("INBOX")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if status.UIDNext == 0 {
		t.Errorf("expected a non-zero UIDNext for a mailbox with messages, got %d", status.UIDNext)
	}

	// Search all messages.
	uids, err := client.SearchUID(&imap.SearchCriteria{})
	if err != nil {
		t.Fatalf("SearchUID failed: %v", err)
	}
	if len(uids) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(uids))
	}

	// Search with keyword filter.
	uids, err = client.SearchUID(&imap.SearchCriteria{
		Header: []imap.SearchCriteriaHeaderField{{
			Key:   "Subject",
			Value: "Invoice",
		}},
	})
	if err != nil {
		t.Fatalf("SearchUID with filter failed: %v", err)
	}
	if len(uids) != 1 {
		t.Fatalf("expected 1 message for Subject=Invoice, got %d", len(uids))
	}

	c.Logout().Wait()
}

func TestRealIMAPClient_Fetch(t *testing.T) {
	addr, rootCAs, cleanup := testIMAPServer(t)
	defer cleanup()

	c := dialTestClient(t, addr, rootCAs)
	client := &realIMAPClient{c}

	if err := client.Login("testuser", "testpass"); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if _, err := client.Select("INBOX"); err != nil {
		t.Fatalf("Select: %v", err)
	}

	// Fetch both messages.
	msgs, err := client.FetchUID([]imap.UID{1, 2})
	if err != nil {
		t.Fatalf("FetchUID failed: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}

	// Verify message content.
	found1, found2 := false, false
	for _, msg := range msgs {
		if msg.Envelope == nil {
			t.Error("expected non-nil envelope")
			continue
		}
		switch msg.Envelope.MessageID {
		case "msg-1@example.com":
			found1 = true
			if len(msg.Body) == 0 {
				t.Error("expected non-empty body for msg-1")
			}
		case "msg-2@example.com":
			found2 = true
			if len(msg.Body) == 0 {
				t.Error("expected non-empty body for msg-2")
			}
		}
	}
	if !found1 || !found2 {
		t.Errorf("expected both messages, found1=%v found2=%v", found1, found2)
	}

	// Fetch empty UID set should return nil.
	msgs, err = client.FetchUID([]imap.UID{})
	if err != nil {
		t.Fatalf("FetchUID empty failed: %v", err)
	}
	if msgs != nil {
		t.Fatalf("expected nil for empty fetch, got %v", msgs)
	}

	c.Logout().Wait()
}

func TestRealIMAPClient_FetchEnvelopesUID(t *testing.T) {
	addr, rootCAs, cleanup := testIMAPServer(t)
	defer cleanup()

	c := dialTestClient(t, addr, rootCAs)
	client := &realIMAPClient{c}

	if err := client.Login("testuser", "testpass"); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if _, err := client.Select("INBOX"); err != nil {
		t.Fatalf("Select: %v", err)
	}

	// Fetch both envelopes.
	msgs, err := client.FetchEnvelopesUID([]imap.UID{1, 2})
	if err != nil {
		t.Fatalf("FetchEnvelopesUID failed: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}

	// Verify envelope content.
	found1, found2 := false, false
	for _, msg := range msgs {
		if msg.Envelope == nil {
			t.Error("expected non-nil envelope")
			continue
		}
		switch msg.Envelope.MessageID {
		case "msg-1@example.com":
			found1 = true
			if len(msg.Body) != 0 {
				t.Error("expected empty body for msg-1 envelope fetch")
			}
		case "msg-2@example.com":
			found2 = true
			if len(msg.Body) != 0 {
				t.Error("expected empty body for msg-2 envelope fetch")
			}
		}
	}
	if !found1 || !found2 {
		t.Errorf("expected both envelopes, found1=%v found2=%v", found1, found2)
	}

	// Fetch empty UID set should return nil.
	msgs, err = client.FetchEnvelopesUID([]imap.UID{})
	if err != nil {
		t.Fatalf("FetchEnvelopesUID empty failed: %v", err)
	}
	if msgs != nil {
		t.Fatalf("expected nil for empty fetch, got %v", msgs)
	}

	c.Logout().Wait()
}

func TestRealIMAPClient_StoreSeen(t *testing.T) {
	addr, rootCAs, cleanup := testIMAPServer(t)
	defer cleanup()

	c := dialTestClient(t, addr, rootCAs)
	client := &realIMAPClient{c}

	if err := client.Login("testuser", "testpass"); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if _, err := client.Select("INBOX"); err != nil {
		t.Fatalf("Select: %v", err)
	}

	// Mark message 1 as seen.
	if err := client.StoreSeen(1); err != nil {
		t.Fatalf("StoreSeen failed: %v", err)
	}

	// Verify the \Seen flag was set by searching for unseen messages.
	uids, err := client.SearchUID(&imap.SearchCriteria{
		NotFlag: []imap.Flag{imap.FlagSeen},
	})
	if err != nil {
		t.Fatalf("SearchUID for unseen failed: %v", err)
	}
	if len(uids) != 1 {
		t.Fatalf("expected 1 unseen message, got %d", len(uids))
	}
	if uids[0] != 2 {
		t.Errorf("expected unseen message UID 2, got %d", uids[0])
	}

	c.Logout().Wait()
}

func TestRealIMAPClient_Move(t *testing.T) {
	addr, rootCAs, cleanup := testIMAPServer(t)
	defer cleanup()

	c := dialTestClient(t, addr, rootCAs)
	client := &realIMAPClient{c}

	if err := client.Login("testuser", "testpass"); err != nil {
		t.Fatalf("Login: %v", err)
	}

	// Create destination mailbox via raw command.
	if err := client.Create("Archive", nil).Wait(); err != nil {
		t.Fatalf("Create Archive: %v", err)
	}

	if _, err := client.Select("INBOX"); err != nil {
		t.Fatalf("Select: %v", err)
	}

	// Move message 1 to Archive.
	if err := client.Move(1, "Archive"); err != nil {
		t.Fatalf("Move failed: %v", err)
	}

	// Verify INBOX now has 1 message.
	uids, err := client.SearchUID(&imap.SearchCriteria{})
	if err != nil {
		t.Fatalf("SearchUID INBOX failed: %v", err)
	}
	if len(uids) != 1 {
		t.Fatalf("expected 1 message in INBOX after move, got %d", len(uids))
	}

	// Select Archive and verify message is there.
	if _, err := client.Select("Archive"); err != nil {
		t.Fatalf("Select Archive: %v", err)
	}
	uids, err = client.SearchUID(&imap.SearchCriteria{})
	if err != nil {
		t.Fatalf("SearchUID Archive failed: %v", err)
	}
	if len(uids) != 1 {
		t.Fatalf("expected 1 message in Archive after move, got %d", len(uids))
	}

	c.Logout().Wait()
}

func TestRealIMAPClient_Close(t *testing.T) {
	addr, rootCAs, cleanup := testIMAPServer(t)
	defer cleanup()

	c := dialTestClient(t, addr, rootCAs)
	client := &realIMAPClient{c}

	if err := client.Login("testuser", "testpass"); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if _, err := client.Select("INBOX"); err != nil {
		t.Fatalf("Select: %v", err)
	}

	// Close (logout) should succeed.
	if err := client.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}

func TestRealIMAPClient_EndToEnd(t *testing.T) {
	addr, rootCAs, cleanup := testIMAPServer(t)
	defer cleanup()

	c := dialTestClient(t, addr, rootCAs)
	client := &realIMAPClient{c}

	// Full flow: Login → Select → SearchUID → FetchUID → StoreSeen → Close.
	if err := client.Login("testuser", "testpass"); err != nil {
		t.Fatalf("Login: %v", err)
	}

	if _, err := client.Select("INBOX"); err != nil {
		t.Fatalf("Select: %v", err)
	}

	uids, err := client.SearchUID(&imap.SearchCriteria{})
	if err != nil {
		t.Fatalf("SearchUID: %v", err)
	}
	if len(uids) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(uids))
	}

	msgs, err := client.FetchUID(uids)
	if err != nil {
		t.Fatalf("FetchUID: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 fetched messages, got %d", len(msgs))
	}

	// Mark both as seen.
	for _, msg := range msgs {
		if err := client.StoreSeen(msg.SeqNum); err != nil {
			t.Fatalf("StoreSeen seq %d: %v", msg.SeqNum, err)
		}
	}

	// Verify no unseen messages remain.
	unseen, err := client.SearchUID(&imap.SearchCriteria{
		NotFlag: []imap.Flag{imap.FlagSeen},
	})
	if err != nil {
		t.Fatalf("SearchUID unseen: %v", err)
	}
	if len(unseen) != 0 {
		t.Errorf("expected 0 unseen messages, got %d", len(unseen))
	}

	// Close connection.
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestDefaultDialIMAP(t *testing.T) {
	addr, _, cleanup := testIMAPServer(t)
	defer cleanup()

	origTLSConfig := defaultIMAPTLSConfig
	defer func() { defaultIMAPTLSConfig = origTLSConfig }()
	defaultIMAPTLSConfig = func(_ string) *tls.Config {
		return &tls.Config{InsecureSkipVerify: true}
	}

	ic, err := defaultDialIMAP(t.Context(), addr, "localhost")
	if err != nil {
		t.Fatalf("defaultDialIMAP: %v", err)
	}

	if err := ic.Login("testuser", "testpass"); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if _, err := ic.Select("INBOX"); err != nil {
		t.Fatalf("Select: %v", err)
	}

	ic.Close()
}
