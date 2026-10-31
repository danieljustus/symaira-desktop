package ingest

// Go oracle for the IMAP mail poller (RUST-010, INGEST-004): MailPoller
// (login, select, UID cursor, UID SEARCH, envelope dedupe, body fetch, go-message
// attachment reader, staging, enqueue, mark-seen/move, idempotency record, poll
// status) against a scripted loopback IMAP server over TLS. The server is
// a small deterministic fake so the Rust replay can run the identical script.
// Per scenario the fixture records every client command the server saw, the
// final server mailbox state, the store tables, the staged files, the log
// output and the PollOnce / Start results. No real account or network is used.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/ingest/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/ingest/internal/store"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
	_ "modernc.org/sqlite"
)

type imapMsg struct {
	UID        uint32   `json:"uid"`
	Flags      []string `json:"flags,omitempty"`
	MessageID  string   `json:"message_id,omitempty"`
	NoEnvelope bool     `json:"no_envelope,omitempty"`
	Raw        []byte   `json:"raw"`
}

type imapFolder struct {
	UIDValidity uint32    `json:"uid_validity"`
	UIDNext     uint32    `json:"uid_next,omitempty"`
	NoUIDNext   bool      `json:"no_uid_next,omitempty"`
	Messages    []imapMsg `json:"messages"`
}

type imapSpec struct {
	Password string                `json:"password"`
	Caps     string                `json:"caps"`
	Folders  map[string]imapFolder `json:"folders"`
	// Fail maps a command verb ("LOGIN", "SELECT", "UID SEARCH", "UID FETCH",
	// "STORE", "MOVE", "COPY") to the tagged completion line, e.g. "NO boom".
	Fail map[string]string `json:"fail,omitempty"`
	// Drop closes the connection without answering the first command with this verb.
	Drop string `json:"drop,omitempty"`
	// NoCapCodes omits [CAPABILITY ...] response codes (greeting and LOGIN).
	NoCapCodes bool `json:"no_cap_codes,omitempty"`
	// OmitBody leaves the BODY[] item out of UID FETCH responses.
	OmitBody bool `json:"omit_body,omitempty"`
}

type mailAccount struct {
	Username       string `json:"username"`
	PasswordSecret string `json:"password_secret"`
	Folder         string `json:"folder,omitempty"`
	Action         string `json:"action,omitempty"`
	MoveTo         string `json:"move_to,omitempty"`
	HasAttachment  bool   `json:"has_attachment,omitempty"`
	From           string `json:"from,omitempty"`
	Subject        string `json:"subject,omitempty"`
}

type mailMutation struct {
	Op     string            `json:"op"` // append, uid-validity, set-flags, drop-files, server-fail, server-drop
	Folder string            `json:"folder,omitempty"`
	Msg    imapMsg           `json:"msg,omitempty"`
	Value  uint32            `json:"value,omitempty"`
	UID    uint32            `json:"uid,omitempty"`
	Flags  []string          `json:"flags,omitempty"`
	Fail   map[string]string `json:"fail,omitempty"`
	Drop   string            `json:"drop,omitempty"`
}

type mailScenario struct {
	ID            string            `json:"id"`
	Account       mailAccount       `json:"account"`
	Accounts2     []mailAccount     `json:"accounts2,omitempty"` // extra accounts (Start only)
	Env           map[string]string `json:"env,omitempty"`
	Server        imapSpec          `json:"server"`
	Mode          string            `json:"mode,omitempty"` // "" = PollOnce, "start" = Start/Close
	Polls         int               `json:"polls,omitempty"`
	Between       [][]mailMutation  `json:"between,omitempty"` // mutations before poll i+1
	ProcessingDir string            `json:"processing_dir"`    // "proc" | "" | "file"
	Hook          string            `json:"hook,omitempty"`    // "", "ok", "fail"
	NoListen      bool              `json:"no_listen,omitempty"`
	Tools         map[string]string `json:"tools,omitempty"` // executable scripts placed first on PATH
	NoSystemPath  bool              `json:"no_system_path,omitempty"`
}

type mailPoll struct {
	Error string `json:"error"`
}

type mailServerMsg struct {
	UID   uint32   `json:"uid"`
	Flags []string `json:"flags"`
}

type mailServerFolder struct {
	UIDValidity uint32          `json:"uid_validity"`
	UIDNext     uint32          `json:"uid_next"`
	Messages    []mailServerMsg `json:"messages"`
}

type mailCase struct {
	Scenario   mailScenario                `json:"scenario"`
	Polls      []mailPoll                  `json:"polls"`
	Transcript [][]string                  `json:"transcript"`
	Server     map[string]mailServerFolder `json:"server"`
	Tables     map[string][]map[string]any `json:"tables"`
	Files      []fsEntry                   `json:"files"`
	Logs       []string                    `json:"logs"`
	Hooks      []string                    `json:"hooks"`
}

func crlf(s string) []byte {
	return []byte(strings.ReplaceAll(s, "\n", "\r\n"))
}

// ---- scripted IMAP server -------------------------------------------------

type fakeIMAP struct {
	mu    sync.Mutex
	spec  imapSpec
	conns [][]string
	ln    net.Listener
	wg    sync.WaitGroup
}

func flagSet(flags []string) string {
	return "(" + strings.Join(flags, " ") + ")"
}

func hasFlag(flags []string, f string) bool {
	for _, x := range flags {
		if strings.EqualFold(x, f) {
			return true
		}
	}
	return false
}

func (s *fakeIMAP) capLine() string {
	if s.spec.Caps == "" {
		return "IMAP4rev1"
	}
	return "IMAP4rev1 " + s.spec.Caps
}

func (s *fakeIMAP) nextUID(f *imapFolder) uint32 {
	if f.UIDNext != 0 {
		return f.UIDNext
	}
	var max uint32
	for _, m := range f.Messages {
		if m.UID > max {
			max = m.UID
		}
	}
	return max + 1
}

func startFakeIMAP(t *testing.T, spec imapSpec, cert tls.Certificate) *fakeIMAP {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeIMAP{spec: spec, ln: ln}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.conns = append(s.conns, nil)
			idx := len(s.conns) - 1
			s.mu.Unlock()
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				s.serve(c, idx)
			}()
		}
	}()
	return s
}

func (s *fakeIMAP) close() {
	_ = s.ln.Close()
	s.wg.Wait()
}

func (s *fakeIMAP) record(idx int, line string) {
	s.mu.Lock()
	s.conns[idx] = append(s.conns[idx], line)
	s.mu.Unlock()
}

// readCommand reads one command, following synchronizing literals.
func readCommand(br *bufio.Reader, bw *bufio.Writer) (string, error) {
	var sb strings.Builder
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return "", err
		}
		line = strings.TrimRight(line, "\r\n")
		sb.WriteString(line)
		if i := strings.LastIndex(line, "{"); i >= 0 && strings.HasSuffix(line, "}") {
			n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSuffix(line[i+1:len(line)-1], "+"), ""))
			if err == nil {
				if !strings.HasSuffix(line, "+}") {
					_, _ = bw.WriteString("+ ready\r\n")
					_ = bw.Flush()
				}
				buf := make([]byte, n)
				if _, err := io.ReadFull(br, buf); err != nil {
					return "", err
				}
				sb.WriteString(fmt.Sprintf("<%d bytes>", n))
				continue
			}
		}
		return sb.String(), nil
	}
}

func (s *fakeIMAP) serve(c net.Conn, idx int) {
	defer func() { _ = c.Close() }()
	br := bufio.NewReader(c)
	bw := bufio.NewWriter(c)
	w := func(format string, a ...any) {
		_, _ = fmt.Fprintf(bw, format+"\r\n", a...)
	}
	flush := func() { _ = bw.Flush() }
	if s.spec.NoCapCodes {
		w("* OK fake ready")
	} else {
		w("* OK [CAPABILITY %s] fake ready", s.capLine())
	}
	flush()
	var selected string
	dropped := map[string]bool{}
	for {
		cmd, err := readCommand(br, bw)
		if err != nil {
			return
		}
		tag, rest, _ := strings.Cut(cmd, " ")
		verb, args, _ := strings.Cut(rest, " ")
		upper := strings.ToUpper(verb)
		full := upper
		if upper == "UID" {
			sub, a2, _ := strings.Cut(args, " ")
			full = "UID " + strings.ToUpper(sub)
			args = a2
		}
		logged := full
		if args != "" {
			logged += " " + args
		}
		if upper == "LOGIN" {
			user, _, _ := strings.Cut(args, " ")
			logged = "LOGIN " + user + " <PASSWORD>"
		}
		s.record(idx, logged)
		s.mu.Lock()
		failLine, failing := s.spec.Fail[full]
		drop := s.spec.Drop == full && !dropped[full]
		s.mu.Unlock()
		if drop {
			dropped[full] = true
			return
		}
		if failing && upper != "LOGOUT" {
			w("%s %s", tag, failLine)
			flush()
			continue
		}
		switch upper {
		case "CAPABILITY":
			w("* CAPABILITY %s", s.capLine())
			w("%s OK CAPABILITY completed", tag)
		case "LOGIN":
			user, pw, _ := strings.Cut(args, " ")
			_ = user
			if strings.Trim(pw, "\"") != s.spec.Password {
				w("%s NO [AUTHENTICATIONFAILED] invalid credentials", tag)
			} else {
				if s.spec.NoCapCodes {
					w("%s OK LOGIN completed", tag)
				} else {
					w("%s OK [CAPABILITY %s] LOGIN completed", tag, s.capLine())
				}
			}
		case "SELECT":
			name := strings.Trim(args, "\"")
			s.mu.Lock()
			f, ok := s.spec.Folders[name]
			s.mu.Unlock()
			if !ok {
				w("%s NO [NONEXISTENT] no such mailbox", tag)
				break
			}
			selected = name
			w("* %d EXISTS", len(f.Messages))
			w("* 0 RECENT")
			w("* FLAGS (\\Seen \\Answered \\Flagged \\Deleted \\Draft)")
			w("* OK [PERMANENTFLAGS (\\Seen \\Answered \\Flagged \\Deleted \\Draft \\*)] flags")
			w("* OK [UIDVALIDITY %d] uids valid", f.UIDValidity)
			if !f.NoUIDNext {
				w("* OK [UIDNEXT %d] predicted", s.nextUID(&f))
			}
			w("%s OK [READ-WRITE] SELECT completed", tag)
		case "UID":
			s.mu.Lock()
			f := s.spec.Folders[selected]
			s.mu.Unlock()
			switch full {
			case "UID SEARCH":
				toks := strings.Fields(args)
				start := uint32(1)
				unseen := false
				for i := 0; i < len(toks); i++ {
					switch strings.ToUpper(toks[i]) {
					case "UID":
						if i+1 < len(toks) {
							a, _, _ := strings.Cut(toks[i+1], ":")
							n, _ := strconv.ParseUint(a, 10, 32)
							start = uint32(n)
							i++
						}
					case "UNSEEN":
						unseen = true
					}
				}
				var max uint32
				for _, m := range f.Messages {
					if m.UID > max {
						max = m.UID
					}
				}
				var out []string
				for _, m := range f.Messages {
					in := m.UID >= start || (start > max && m.UID == max)
					if unseen && hasFlag(m.Flags, "\\Seen") {
						in = false
					}
					if in {
						out = append(out, strconv.Itoa(int(m.UID)))
					}
				}
				if len(out) > 0 {
					w("* SEARCH %s", strings.Join(out, " "))
				} else {
					w("* SEARCH")
				}
				w("%s OK SEARCH completed", tag)
			case "UID FETCH":
				set, items, _ := strings.Cut(args, " ")
				want := map[uint32]bool{}
				for _, part := range strings.Split(set, ",") {
					a, b, isRange := strings.Cut(part, ":")
					lo, _ := strconv.ParseUint(a, 10, 32)
					hi := lo
					if isRange {
						hi = 1<<32 - 1
						if b != "*" {
							h, _ := strconv.ParseUint(b, 10, 32)
							hi = h
						}
					}
					for _, m := range f.Messages {
						if uint64(m.UID) >= lo && uint64(m.UID) <= hi {
							want[m.UID] = true
						}
					}
				}
				itemsU := strings.ToUpper(items)
				wantEnv := strings.Contains(itemsU, "ENVELOPE")
				wantBody := strings.Contains(itemsU, "BODY.PEEK[]") || strings.Contains(itemsU, "BODY[]")
				for i, m := range f.Messages {
					if !want[m.UID] {
						continue
					}
					var parts []string
					parts = append(parts, fmt.Sprintf("UID %d", m.UID))
					if wantEnv && !m.NoEnvelope {
						mid := "NIL"
						if m.MessageID != "" {
							mid = strconv.Quote(m.MessageID)
						}
						parts = append(parts, "ENVELOPE (NIL NIL NIL NIL NIL NIL NIL NIL NIL "+mid+")")
					}
					if wantBody && !s.spec.OmitBody {
						parts = append(parts, fmt.Sprintf("BODY[] {%d}\r\n%s", len(m.Raw), m.Raw))
					}
					_, _ = fmt.Fprintf(bw, "* %d FETCH (%s)\r\n", i+1, strings.Join(parts, " "))
				}
				w("%s OK FETCH completed", tag)
			case "UID EXPUNGE":
				w("%s OK EXPUNGE completed", tag)
			default:
				w("%s BAD unsupported", tag)
			}
		case "STORE":
			seqS, spec, _ := strings.Cut(args, " ")
			seq, _ := strconv.Atoi(seqS)
			s.mu.Lock()
			f := s.spec.Folders[selected]
			if seq < 1 || seq > len(f.Messages) {
				s.mu.Unlock()
				w("%s BAD invalid message sequence number", tag)
				break
			}
			{
				m := &f.Messages[seq-1]
				for _, fl := range strings.Fields(strings.Trim(spec[strings.Index(spec, "(")+1:strings.LastIndex(spec, ")")], " ")) {
					if !hasFlag(m.Flags, fl) {
						m.Flags = append(m.Flags, fl)
					}
				}
				silent := strings.Contains(strings.ToUpper(spec), ".SILENT")
				if !silent {
					w("* %d FETCH (FLAGS %s)", seq, flagSet(m.Flags))
				}
			}
			s.mu.Unlock()
			w("%s OK STORE completed", tag)
		case "MOVE", "COPY":
			seqS, dest, _ := strings.Cut(args, " ")
			dest = strings.Trim(dest, "\"")
			seq, _ := strconv.Atoi(seqS)
			s.mu.Lock()
			f := s.spec.Folders[selected]
			df, ok := s.spec.Folders[dest]
			if !ok {
				s.mu.Unlock()
				w("%s NO [TRYCREATE] no such mailbox", tag)
				break
			}
			if seq < 1 || seq > len(f.Messages) {
				s.mu.Unlock()
				w("%s BAD invalid message sequence number", tag)
				break
			}
			{
				m := f.Messages[seq-1]
				nu := s.nextUID(&df)
				df.Messages = append(df.Messages, imapMsg{UID: nu, Flags: append([]string(nil), m.Flags...), MessageID: m.MessageID, NoEnvelope: m.NoEnvelope, Raw: m.Raw})
				df.UIDNext = nu + 1
				s.spec.Folders[dest] = df
				if upper == "MOVE" {
					f.Messages = append(f.Messages[:seq-1:seq-1], f.Messages[seq:]...)
					s.spec.Folders[selected] = f
				}
			}
			s.mu.Unlock()
			if upper == "MOVE" {
				w("* %d EXPUNGE", seq)
			}
			w("%s OK %s completed", tag, upper)
		case "EXPUNGE":
			s.mu.Lock()
			f := s.spec.Folders[selected]
			var keep []imapMsg
			removed := 0
			for i, m := range f.Messages {
				if hasFlag(m.Flags, "\\Deleted") {
					w("* %d EXPUNGE", i+1-removed)
					removed++
					continue
				}
				keep = append(keep, m)
			}
			f.Messages = keep
			s.spec.Folders[selected] = f
			s.mu.Unlock()
			w("%s OK EXPUNGE completed", tag)
		case "LOGOUT":
			w("* BYE logging out")
			w("%s OK LOGOUT completed", tag)
			flush()
			return
		case "NOOP", "CLOSE", "UNSELECT":
			w("%s OK %s completed", tag, upper)
		default:
			w("%s BAD unsupported %s", tag, upper)
		}
		flush()
	}
}

func (s *fakeIMAP) snapshot() map[string]mailServerFolder {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]mailServerFolder{}
	for name, f := range s.spec.Folders {
		sf := mailServerFolder{UIDValidity: f.UIDValidity, UIDNext: s.nextUID(&f), Messages: []mailServerMsg{}}
		for _, m := range f.Messages {
			fl := append([]string{}, m.Flags...)
			sort.Strings(fl)
			sf.Messages = append(sf.Messages, mailServerMsg{UID: m.UID, Flags: fl})
		}
		out[name] = sf
	}
	return out
}

// ---- helpers ---------------------------------------------------------------

func imapTLSMaterial(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(file), "../../../../testdata/port/ingest/imap-tls")
	cert, err := tls.LoadX509KeyPair(filepath.Join(dir, "server.pem"), filepath.Join(dir, "server.key"))
	if err != nil {
		t.Fatal(err)
	}
	ca, err := os.ReadFile(filepath.Join(dir, "ca.pem")) //nolint:gosec // repository test material
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		t.Fatal("bad CA")
	}
	return cert, pool
}

var (
	reTS   = regexp.MustCompile(`\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?(Z|[+-]\d\d:\d\d)`)
	reLogT = regexp.MustCompile(`^\d{4}/\d\d/\d\d \d\d:\d\d:\d\d `)
)

type sqlDB = sql.DB

func openRO(path string) (*sqlDB, error) {
	return sql.Open("sqlite", "file:"+path+"?mode=ro")
}

func dumpTable(t *testing.T, db *sqlDB, table string, norm func(string) string) []map[string]any {
	t.Helper()
	rows, err := db.Query("SELECT * FROM " + table + " ORDER BY rowid")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	cols, _ := rows.Columns()
	out := []map[string]any{}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		row := map[string]any{}
		for i, c := range cols {
			switch v := vals[i].(type) {
			case []byte:
				row[c] = norm(string(v))
			case string:
				row[c] = reTS.ReplaceAllString(norm(v), "<TS>")
			case time.Time:
				row[c] = "<TS>"
			default:
				row[c] = v
			}
			if s, ok := row[c].(string); strings.HasSuffix(c, "_at") && ok && s != "" {
				row[c] = "<TS>"
			}
		}
		out = append(out, row)
	}
	return out
}

func tableExists(db *sqlDB, name string) bool {
	var n int
	_ = db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&n)
	return n > 0
}

func snapshotProcessing(t *testing.T, root string, rel string, norm func(string) string) []fsEntry {
	out := []fsEntry{}
	base := filepath.Join(root, rel)
	if _, err := os.Lstat(base); err != nil {
		return out
	}
	err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		r, _ := filepath.Rel(root, path)
		e := fsEntry{Path: filepath.ToSlash(r), Mode: fmt.Sprintf("%04o", info.Mode().Perm())}
		if d.IsDir() {
			e.Type = "dir"
		} else {
			e.Type = "file"
			b, err := os.ReadFile(path) //nolint:gosec // test scratch
			if err != nil {
				return err
			}
			e.B64 = base64.StdEncoding.EncodeToString(b)
		}
		out = append(out, e)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func applyMutation(srv *fakeIMAP, m mailMutation, root, proc string) {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	switch m.Op {
	case "append":
		f := srv.spec.Folders[m.Folder]
		f.Messages = append(f.Messages, m.Msg)
		srv.spec.Folders[m.Folder] = f
	case "uid-validity":
		f := srv.spec.Folders[m.Folder]
		f.UIDValidity = m.Value
		srv.spec.Folders[m.Folder] = f
	case "set-flags":
		f := srv.spec.Folders[m.Folder]
		for i := range f.Messages {
			if f.Messages[i].UID == m.UID {
				f.Messages[i].Flags = m.Flags
			}
		}
		srv.spec.Folders[m.Folder] = f
	case "server-fail":
		srv.spec.Fail = m.Fail
	case "server-drop":
		srv.spec.Drop = m.Drop
	case "drop-files":
		_ = os.RemoveAll(filepath.Join(root, proc))
		_ = os.MkdirAll(filepath.Join(root, proc), 0o700)
	}
}

func runMailScenario(t *testing.T, sc mailScenario) mailCase {
	root := t.TempDir()
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	cert, pool := imapTLSMaterial(t)
	for k, v := range sc.Env {
		t.Setenv(k, v)
	}
	if len(sc.Tools) > 0 || sc.NoSystemPath {
		tools := filepath.Join(root, "tools")
		if err := os.MkdirAll(tools, 0o700); err != nil {
			t.Fatal(err)
		}
		for name, body := range sc.Tools {
			if err := os.WriteFile(filepath.Join(tools, name), []byte(body), 0o700); err != nil { //nolint:gosec // fake tool must be executable
				t.Fatal(err)
			}
			if err := os.Chmod(filepath.Join(tools, name), 0o700); err != nil { //nolint:gosec // fake tool must be executable
				t.Fatal(err)
			}
		}
		t.Setenv("PATH", tools+":/usr/bin:/bin")
		if sc.NoSystemPath {
			t.Setenv("PATH", tools)
		}
	}
	spec := sc.Server
	// Deep copy folders so mutations do not leak into the recorded scenario.
	spec.Folders = map[string]imapFolder{}
	for k, f := range sc.Server.Folders {
		f.Messages = append([]imapMsg(nil), f.Messages...)
		spec.Folders[k] = f
	}
	srv := startFakeIMAP(t, spec, cert)
	addr := srv.ln.Addr().String()
	host, portS, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portS)
	if sc.NoListen {
		srv.close()
	}

	prevTLS := defaultIMAPTLSConfig
	defaultIMAPTLSConfig = func(h string) *tls.Config {
		return &tls.Config{ServerName: h, RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	defer func() { defaultIMAPTLSConfig = prevTLS }()

	var logBuf bytes.Buffer
	var logMu sync.Mutex
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetFlags(0)
	log.SetOutput(writerFunc(func(p []byte) (int, error) {
		logMu.Lock()
		defer logMu.Unlock()
		return logBuf.Write(p)
	}))
	defer func() { log.SetOutput(prevOut); log.SetFlags(prevFlags) }()

	st, err := store.Open(filepath.Join(root, "db", "docs.db"))
	if err != nil {
		t.Fatal(err)
	}
	mk := func(a mailAccount) config.IMAPAccount {
		var from, subject []string
		if a.From != "" {
			from = []string{a.From}
		}
		if a.Subject != "" {
			subject = []string{a.Subject}
		}
		return config.IMAPAccount{Host: host, Port: port, Username: a.Username, PasswordSecret: a.PasswordSecret, Folder: a.Folder, Action: a.Action, MoveTo: a.MoveTo, HasAttachment: a.HasAttachment, From: from, Subject: subject}
	}
	acc := mk(sc.Account)
	accounts := []config.IMAPAccount{acc}
	for _, a := range sc.Accounts2 {
		accounts = append(accounts, mk(a))
	}
	proc := ""
	switch sc.ProcessingDir {
	case "proc":
		proc = filepath.Join(root, "proc")
		if err := os.MkdirAll(proc, 0o700); err != nil {
			t.Fatal(err)
		}
	case "file":
		proc = filepath.Join(root, "proc")
		if err := os.WriteFile(proc, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	p, err := NewMailPoller(st, accounts, MailPollerOptions{Interval: time.Hour, ProcessingDir: proc})
	if err != nil {
		t.Fatal(err)
	}
	var hooks []string
	var hookMu sync.Mutex
	norm := func(s string) string {
		s = strings.ReplaceAll(s, root, "<ROOT>")
		s = strings.ReplaceAll(s, host+":"+portS, "127.0.0.1:<PORT>")
		return s
	}
	switch sc.Hook {
	case "ok", "fail":
		p.Enqueue = func(_ context.Context, workPath, msgID, correspondent string) error {
			hookMu.Lock()
			hooks = append(hooks, fmt.Sprintf("%s|%s|%s", norm(workPath), msgID, correspondent))
			hookMu.Unlock()
			if sc.Hook == "fail" {
				return fmt.Errorf("hook refused")
			}
			return nil
		}
	}
	c := mailCase{Scenario: sc, Polls: []mailPoll{}}
	ctx := context.Background()
	polls := sc.Polls
	if polls == 0 {
		polls = 1
	}
	if sc.Mode == "start" {
		if err := p.Start(ctx); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			done := true
			for _, a := range accounts {
				s, _ := st.GetMailPollStatus(ctx, config.AccountID(a))
				if s == nil {
					done = false
				}
			}
			if done {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if err := p.Close(); err != nil {
			t.Fatal(err)
		}
	} else {
		for i := 0; i < polls; i++ {
			if i > 0 && i-1 < len(sc.Between) {
				for _, m := range sc.Between[i-1] {
					applyMutation(srv, m, root, "proc")
				}
			}
			err := p.PollOnce(ctx, acc)
			text := ""
			if err != nil {
				text = norm(err.Error())
			}
			c.Polls = append(c.Polls, mailPoll{Error: text})
		}
	}
	if !sc.NoListen {
		srv.close()
	}
	_ = st.Close()

	db, err := openRO(filepath.Join(root, "db", "docs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	c.Tables = map[string][]map[string]any{}
	for _, tbl := range []string{"documents", "jobs", "mail_messages", "mail_poll_cursor", "mail_poll_status"} {
		if tableExists(db, tbl) {
			c.Tables[tbl] = dumpTable(t, db, tbl, norm)
		}
	}
	c.Server = srv.snapshot()
	srv.mu.Lock()
	for _, cn := range srv.conns {
		c.Transcript = append(c.Transcript, append([]string{}, cn...))
	}
	srv.mu.Unlock()
	if c.Transcript == nil {
		c.Transcript = [][]string{}
	}
	sort.SliceStable(c.Transcript, func(i, j int) bool {
		return strings.Join(c.Transcript[i], "\n") < strings.Join(c.Transcript[j], "\n")
	})
	c.Files = snapshotProcessing(t, root, "proc", norm)
	logMu.Lock()
	lines := strings.Split(strings.TrimRight(logBuf.String(), "\n"), "\n")
	logMu.Unlock()
	c.Logs = []string{}
	for _, l := range lines {
		if l != "" {
			c.Logs = append(c.Logs, norm(l))
		}
	}
	sort.Strings(c.Logs)
	sort.Strings(hooks)
	c.Hooks = hooks
	if c.Hooks == nil {
		c.Hooks = []string{}
	}
	return c
}

type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// ---- scenarios -------------------------------------------------------------

func mailScenarios() []mailScenario {
	const pw = "s3cret-pass"
	base := func(id string, msgs ...imapMsg) mailScenario {
		return mailScenario{
			ID:            id,
			Account:       mailAccount{Username: "user@example.test", PasswordSecret: "MAILPORT_PW"},
			Env:           map[string]string{"MAILPORT_PW": pw},
			Server:        imapSpec{Password: pw, Caps: "MOVE UIDPLUS", Folders: map[string]imapFolder{"INBOX": {UIDValidity: 7, Messages: msgs}, "Archive": {UIDValidity: 9}}},
			ProcessingDir: "proc",
		}
	}
	msg := func(uid uint32, mid string, raw string, flags ...string) imapMsg {
		return imapMsg{UID: uid, MessageID: mid, Raw: crlf(raw), Flags: flags}
	}
	const pdfB64 = "JVBERi0xLjQKJSBzbWFsbCB0ZXN0IHBkZgolJUVPRgo="
	mixed := func(parts ...string) string {
		s := "From: Alice Sender <alice@example.test>\nTo: bob@example.test\nSubject: Documents\nMessage-ID: <m@x>\nMIME-Version: 1.0\nContent-Type: multipart/mixed; boundary=\"B1\"\n\n--B1\nContent-Type: text/plain; charset=utf-8\n\nSee attached.\n"
		for _, p := range parts {
			s += "--B1\n" + p
		}
		return s + "--B1--\n"
	}
	pdfPart := func(disp string) string {
		return "Content-Type: application/pdf\nContent-Transfer-Encoding: base64\nContent-Disposition: " + disp + "\n\n" + pdfB64 + "\n"
	}
	var out []mailScenario
	add := func(sc mailScenario) { out = append(out, sc) }

	add(base("attachment-basic", msg(1, "<a1@x>", mixed(pdfPart("attachment; filename=\"invoice.pdf\"")))))
	add(base("two-attachments", msg(1, "<a2@x>", mixed(
		pdfPart("attachment; filename=\"one.pdf\""),
		"Content-Type: text/plain; charset=utf-8\nContent-Disposition: attachment; filename=\"notes.txt\"\nContent-Transfer-Encoding: quoted-printable\n\nline one=\nstill one\nsoft=20break =E2=82=AC\n"))))
	add(base("inline-only-no-attachment", msg(1, "<a3@x>", mixed("Content-Type: text/html; charset=utf-8\n\n<p>hi</p>\n"))))
	hasAtt := base("has-attachment-required-skips", msg(1, "<a4@x>", mixed("Content-Type: text/html\n\n<p>x</p>\n")), msg(2, "<a4b@x>", mixed(pdfPart("attachment; filename=\"b.pdf\""))))
	hasAtt.Account.HasAttachment = true
	add(hasAtt)
	add(base("nested-multipart", msg(1, "<a5@x>", "From: n@example.test\nContent-Type: multipart/mixed; boundary=\"OUT\"\n\n--OUT\nContent-Type: multipart/alternative; boundary=\"ALT\"\n\n--ALT\nContent-Type: text/plain\n\nplain\n--ALT\nContent-Type: text/html\n\n<b>h</b>\n--ALT--\n--OUT\nContent-Type: multipart/mixed; boundary=\"IN\"\n\n--IN\nContent-Type: application/octet-stream\nContent-Disposition: attachment; filename=deep.bin\nContent-Transfer-Encoding: base64\n\nAAECAwQ=\n--IN--\n--OUT--\n")))
	add(base("non-multipart-attachment", msg(1, "<a6@x>", "From: s@example.test\nSubject: single\nContent-Type: application/pdf; name=\"single.pdf\"\nContent-Transfer-Encoding: base64\nContent-Disposition: attachment; filename=single.pdf\n\n"+pdfB64+"\n")))
	add(base("non-multipart-text", msg(1, "<a7@x>", "From: s@example.test\nSubject: plain\n\njust text\n")))
	add(base("filename-rfc2231", msg(1, "<a8@x>", mixed(pdfPart("attachment; filename*=UTF-8''%E2%82%AC-offer.pdf")))))
	add(base("filename-encoded-word", msg(1, "<a9@x>", mixed(pdfPart("attachment; filename=\"=?UTF-8?Q?r=C3=A9sum=C3=A9.pdf?=\"")))))
	add(base("filename-encoded-word-latin1", msg(1, "<a10@x>", mixed(pdfPart("attachment; filename=\"=?ISO-8859-1?Q?r=E9sum=E9.pdf?=\"")))))
	add(base("filename-traversal", msg(1, "<a11@x>", mixed(pdfPart("attachment; filename=\"../../etc/passwd\""), pdfPart("attachment; filename=\"/abs/evil.pdf\""), pdfPart("attachment; filename=\"..\\\\win.pdf\"")))))
	add(base("filename-from-content-type-name", msg(1, "<a12@x>", mixed("Content-Type: application/pdf; name=\"named.pdf\"\nContent-Transfer-Encoding: base64\n\n"+pdfB64+"\n"))))
	add(base("filename-missing", msg(1, "<a13@x>", mixed("Content-Type: application/zip\nContent-Disposition: attachment\nContent-Transfer-Encoding: base64\n\nUEsFBgAAAAAAAAAAAAAAAAAAAAAAAA==\n"))))
	add(base("filename-dot-names", msg(1, "<a14@x>", mixed(pdfPart("attachment; filename=\".\""), pdfPart("attachment; filename=\"..\"")))))
	add(base("duplicate-filename-same-message", msg(1, "<a15@x>", mixed(pdfPart("attachment; filename=\"same.pdf\""), pdfPart("attachment; filename=\"same.pdf\"")))))
	add(base("message-id-with-slash", msg(1, "<dir/slash@x>", mixed(pdfPart("attachment; filename=\"s.pdf\"")))))
	add(base("no-message-id", msg(1, "", mixed(pdfPart("attachment; filename=\"n.pdf\""))), msg(2, "", mixed(pdfPart("attachment; filename=\"n2.pdf\"")))))
	add(base("unknown-transfer-encoding-part-dropped", msg(1, "<a16@x>", mixed("Content-Type: application/pdf\nContent-Transfer-Encoding: x-weird\nContent-Disposition: attachment; filename=\"weird.pdf\"\n\nbody\n", pdfPart("attachment; filename=\"fine.pdf\"")))))
	add(base("text-attachment-latin1-part-dropped", msg(1, "<a17@x>", mixed("Content-Type: text/plain; charset=iso-8859-1\nContent-Disposition: attachment; filename=\"latin.txt\"\nContent-Transfer-Encoding: quoted-printable\n\ncaf=E9\n", "Content-Type: text/plain; charset=us-ascii\nContent-Disposition: attachment; filename=\"ascii.txt\"\n\nascii\n"))))
	add(base("inline-pdf-is-inline", msg(1, "<a18@x>", mixed(pdfPart("inline; filename=\"inline.pdf\"")))))
	add(base("octet-stream-no-disposition", msg(1, "<a19@x>", mixed("Content-Type: application/octet-stream; name=blob.bin\nContent-Transfer-Encoding: base64\n\nAAECAwQ=\n"))))
	add(base("message-rfc822-attachment", msg(1, "<a20@x>", mixed("Content-Type: message/rfc822\nContent-Disposition: attachment; filename=\"fwd.eml\"\n\nFrom: x@example.test\nSubject: inner\n\ninner body\n"))))
	add(base("base64-wrapped-with-indent", msg(1, "<a21@x>", mixed("Content-Type: application/pdf\nContent-Transfer-Encoding: base64\nContent-Disposition: attachment; filename=\"wrap.pdf\"\n\nJVBERi0x\n LjQKJSBz\n\tbWFsbCB0\nZXN0IHBkZgol\nJUVPRgo=\n"))))
	add(base("base64-corrupt", msg(1, "<a22@x>", mixed("Content-Type: application/pdf\nContent-Transfer-Encoding: base64\nContent-Disposition: attachment; filename=\"bad.pdf\"\n\nJVBE!!!!Ri0x\n"))))
	add(base("quoted-printable-attachment", msg(1, "<a23@x>", mixed("Content-Type: application/x-test\nContent-Transfer-Encoding: quoted-printable\nContent-Disposition: attachment; filename=\"qp.bin\"\n\nA=3DB=\nC =E2=82\n"))))
	add(base("empty-body-section", msg(1, "<a24@x>", ""), msg(2, "<a24b@x>", mixed(pdfPart("attachment; filename=\"after.pdf\"")))))
	add(base("garbage-message", msg(1, "<a25@x>", "this is not a header\nat all\n\nbody\n")))
	add(base("root-unknown-charset", msg(1, "<a26@x>", "From: r@example.test\nContent-Type: text/plain; charset=iso-8859-1\n\ncaf\xe9\n")))
	add(base("preamble-epilogue-lf-only", msg(1, "<a27@x>", "From: p@example.test\nContent-Type: multipart/mixed; boundary=XX\n\npreamble text\n--XX\nContent-Type: application/pdf\nContent-Disposition: attachment; filename=p.pdf\nContent-Transfer-Encoding: base64\n\n"+pdfB64+"\n--XX--\nepilogue\n")))
	add(base("missing-final-boundary", msg(1, "<a28@x>", "From: p@example.test\nContent-Type: multipart/mixed; boundary=XX\n\n--XX\nContent-Type: application/pdf\nContent-Disposition: attachment; filename=trunc.pdf\nContent-Transfer-Encoding: base64\n\n"+pdfB64+"\n")))
	// A multipart/* part without a boundary makes mail.Reader.NextPart fail on every
	// call and the poller loops forever (`continue` on a non-EOF error), so that case
	// cannot be recorded; the Rust port bounds the loop (declared limit).
	add(base("correspondent-header-variants", msg(1, "<a30@x>", "From: =?UTF-8?Q?J=C3=BCrgen?= <j@example.test>\nContent-Type: application/pdf\nContent-Disposition: attachment; filename=j.pdf\nContent-Transfer-Encoding: base64\n\n"+pdfB64+"\n")))
	dupContent := base("same-attachment-two-messages", msg(1, "<d1@x>", mixed(pdfPart("attachment; filename=\"dup.pdf\""))), msg(2, "<d2@x>", mixed(pdfPart("attachment; filename=\"dup.pdf\""))))
	add(dupContent)
	large := strings.Repeat("A", 76*200)
	var lb strings.Builder
	for i := 0; i < 200; i++ {
		lb.WriteString(large[i*76:(i+1)*76] + "\n")
	}
	add(base("large-attachment", msg(1, "<a31@x>", mixed("Content-Type: application/octet-stream\nContent-Transfer-Encoding: base64\nContent-Disposition: attachment; filename=big.bin\n\n"+lb.String()))))

	// account / protocol flow
	ms := base("mark-seen-action", msg(1, "<f1@x>", mixed(pdfPart("attachment; filename=\"a.pdf\""))), msg(2, "<f2@x>", mixed(pdfPart("attachment; filename=\"b.pdf\"")), "\\Seen"))
	ms.Account.Action = "mark_seen"
	add(ms)
	mv := base("move-action", msg(1, "<f3@x>", mixed(pdfPart("attachment; filename=\"m.pdf\""))), msg(2, "<f4@x>", mixed(pdfPart("attachment; filename=\"m2.pdf\""))))
	mv.Account.Action, mv.Account.MoveTo = "move", "Archive"
	add(mv)
	mvf := base("move-fallback-copy-store-expunge", msg(1, "<f5@x>", mixed(pdfPart("attachment; filename=\"m.pdf\""))))
	mvf.Account.Action, mvf.Account.MoveTo = "move", "Archive"
	mvf.Server.Caps = "UIDPLUS"
	add(mvf)
	mvf2 := base("move-fallback-without-uidplus", msg(1, "<f5b@x>", mixed(pdfPart("attachment; filename=\"m.pdf\""))))
	mvf2.Account.Action, mvf2.Account.MoveTo = "move", "Archive"
	mvf2.Server.Caps = ""
	add(mvf2)
	mvn := base("move-without-target", msg(1, "<f6@x>", mixed(pdfPart("attachment; filename=\"m.pdf\""))))
	mvn.Account.Action = "move"
	add(mvn)
	mvm := base("move-to-missing-folder", msg(1, "<f7@x>", mixed(pdfPart("attachment; filename=\"m.pdf\""))))
	mvm.Account.Action, mvm.Account.MoveTo = "move", "Nowhere"
	mvm.Polls = 2
	add(mvm)
	unknownAction := base("unknown-action-ignored", msg(1, "<f8@x>", mixed(pdfPart("attachment; filename=\"u.pdf\""))))
	unknownAction.Account.Action = "delete"
	add(unknownAction)
	rp := base("second-poll-resume-and-dedupe", msg(1, "<g1@x>", mixed(pdfPart("attachment; filename=\"a.pdf\""))))
	rp.Polls = 3
	rp.Between = [][]mailMutation{{}, {{Op: "append", Folder: "INBOX", Msg: msg(2, "<g2@x>", mixed(pdfPart("attachment; filename=\"b.pdf\"")))}}}
	add(rp)
	uv := base("uid-validity-change-rescans", msg(1, "<h1@x>", mixed(pdfPart("attachment; filename=\"a.pdf\""))), msg(2, "<h2@x>", mixed(pdfPart("attachment; filename=\"b.pdf\""))))
	uv.Polls = 2
	uv.Between = [][]mailMutation{{{Op: "uid-validity", Folder: "INBOX", Value: 99}}}
	add(uv)
	um := base("unseen-poll-after-mark-seen", msg(1, "<i1@x>", mixed(pdfPart("attachment; filename=\"a.pdf\""))))
	um.Account.Action = "mark_seen"
	um.Polls = 2
	um.Between = [][]mailMutation{{{Op: "append", Folder: "INBOX", Msg: msg(2, "<i2@x>", mixed(pdfPart("attachment; filename=\"b.pdf\"")))}}}
	add(um)
	nu := base("no-uidnext-in-select", msg(1, "<j1@x>", mixed(pdfPart("attachment; filename=\"a.pdf\""))))
	f := nu.Server.Folders["INBOX"]
	f.NoUIDNext = true
	nu.Server.Folders["INBOX"] = f
	add(nu)
	empty := base("empty-mailbox")
	add(empty)
	cust := base("custom-folder-and-filters-ignored", msg(1, "<k1@x>", mixed(pdfPart("attachment; filename=\"a.pdf\""))))
	cust.Account.Folder, cust.Account.From, cust.Account.Subject = "Work", "boss@example.test", "Invoice"
	cust.Server.Folders["Work"] = imapFolder{UIDValidity: 3, Messages: []imapMsg{msg(5, "<k2@x>", mixed(pdfPart("attachment; filename=\"w.pdf\"")))}}
	add(cust)

	// A server without [CAPABILITY] codes makes go-imap race two CAPABILITY
	// commands (scheduling dependent), so that case is not recorded.
	mid := base("message-id-forms",
		msg(1, "bare@x", mixed(pdfPart("attachment; filename=\"a.pdf\""))),
		msg(2, "<nohost>", mixed(pdfPart("attachment; filename=\"b.pdf\""))),
		msg(3, " (note) <c.d@host.example> (tail)", mixed(pdfPart("attachment; filename=\"c.pdf\""))),
		msg(4, "<lit@[10.0.0.1]>", mixed(pdfPart("attachment; filename=\"d.pdf\""))),
		msg(5, "<a b@x>", mixed(pdfPart("attachment; filename=\"e.pdf\""))))
	add(mid)
	fn := base("folder-names-quoted-and-utf7", msg(1, "<c3@x>", mixed(pdfPart("attachment; filename=\"a.pdf\""))))
	fn.Account.Folder = "Sent Items"
	fn.Server.Folders["Sent Items"] = imapFolder{UIDValidity: 4, Messages: []imapMsg{msg(1, "<c4@x>", mixed(pdfPart("attachment; filename=\"s.pdf\"")))}}
	fn.Account.Action, fn.Account.MoveTo = "move", "Entwürfe"
	fn.Server.Folders["Entw&APw-rfe"] = imapFolder{UIDValidity: 5}
	add(fn)
	fn2 := base("folder-name-utf7-select", msg(1, "<c5@x>", mixed(pdfPart("attachment; filename=\"a.pdf\""))))
	fn2.Account.Folder = "Entwürfe"
	fn2.Server.Folders["Entw&APw-rfe"] = imapFolder{UIDValidity: 5, Messages: []imapMsg{msg(1, "<c6@x>", mixed(pdfPart("attachment; filename=\"u.pdf\"")))}}
	add(fn2)
	fn3 := base("folder-name-with-quote-and-ampersand")
	fn3.Account.Folder = "A \"b\" & c"
	fn3.Server.Folders["A \\\"b\\\" &- c"] = imapFolder{UIDValidity: 6, Messages: []imapMsg{msg(1, "<c7@x>", mixed(pdfPart("attachment; filename=\"q.pdf\"")))}}
	add(fn3)
	enc := base("filename-encoded-word-variants", msg(1, "<e1@x>", mixed(
		pdfPart("attachment; filename=\"=?UTF-8?B?w6RiYy5wZGY=?=\""),
		pdfPart("attachment; filename=\"=?utf-8?q?a_b=2Epdf?= =?utf-8?q?c?=\""),
		pdfPart("attachment; filename=\"x=?UTF-8?Q?y?=z.pdf\""),
		pdfPart("attachment; filename=\"=?UTF-8?Q?bad=ZZ?=.pdf\""),
		pdfPart("attachment; filename=\"=?UTF-8?X?q?=.pdf\""),
		pdfPart("attachment; filename=\"=?US-ASCII?Q?caf=C3=A9?=.pdf\""),
		pdfPart("attachment; filename=\"=?UTF-16?Q?a?=.pdf\""))))
	add(enc)
	hdr := base("part-header-oddities", msg(1, "<e2@x>", "From: Odd <o@example.test>\nContent-Type: multipart/mixed;\n boundary=\"ZZ\"\n\n--ZZ\nContent-Type: application/pdf; name=\"folded\n .pdf\"\nContent-Disposition: ATTACHMENT;\n\tfilename=\"Upper.PDF\"\nContent-Transfer-Encoding: BASE64\n\n"+pdfB64+"\n--ZZ\nCONTENT-TYPE: application/pdf\ncontent-disposition: attachment; filename=lower.pdf\nX-Empty:\n: skipped\nContent-Transfer-Encoding: 7bit\n\nraw seven bit\n--ZZ\nContent-Type: application/pdf\nContent-Disposition: attachment; filename=\"semi;colon.pdf\"\n\nbody\n--ZZ--\n"))
	add(hdr)
	hdr2 := base("part-header-malformed", msg(1, "<e3@x>", "From: o@example.test\nContent-Type: multipart/mixed; boundary=ZZ\n\n--ZZ\nno colon here\n\nbody\n--ZZ\nContent-Type: application/pdf\nContent-Disposition: attachment; filename=ok.pdf\n\nbody\n--ZZ--\n"))
	add(hdr2)
	hdr3 := base("disposition-parse-failure-falls-back", msg(1, "<e4@x>", mixed("Content-Type: application/pdf; name=\"fallback.pdf\"\nContent-Disposition: attachment; filename=\"unterminated\n\nbody\n", "Content-Type: text/plain; name=\"t.txt\"\nContent-Disposition: bad;;;\n\ntext body\n")))
	add(hdr3)
	ct := base("content-type-parse-failure", msg(1, "<e5@x>", "From: o@example.test\nContent-Type: multipart/mixed; boundary=ZZ\n\n--ZZ\nContent-Type: text/plain; charset\nContent-Disposition: attachment; filename=a.txt\n\nbody\n--ZZ\nContent-Type: TEXT/PLAIN\nContent-Disposition: attachment; filename=b.txt\n\nupper\n--ZZ--\n"))
	add(ct)

	// failures
	lf := base("login-failure")
	lf.Server.Password = "different"
	add(lf)
	nl := base("dial-failure")
	nl.NoListen = true
	add(nl)
	np := base("password-env-missing")
	np.Env = map[string]string{}
	np.Account.PasswordSecret = "MAILPORT_UNSET_VAR"
	add(np)
	nr := base("no-password-reference")
	nr.Account.PasswordSecret = ""
	add(nr)
	ev := base("env-scheme-reference")
	ev.Account.PasswordSecret = "env://MAILPORT_PW"
	add(ev)
	kc := base("keychain-reference-invalid")
	kc.Account.PasswordSecret = "keychain://onlyservice"
	add(kc)
	svok := base("symvault-reference-success")
	svok.Env = map[string]string{}
	svok.Account.PasswordSecret = "symvault://mail/pw"
	svok.Tools = map[string]string{"symvault": "#!/bin/sh\nprintf '  %s  \\n' \"$SYMVAULT_PW_FOR_TEST\"\n"}
	svok.Env["SYMVAULT_PW_FOR_TEST"] = pw
	add(svok)
	svfail := base("symvault-reference-failure-detail")
	svfail.Env = map[string]string{}
	svfail.Account.PasswordSecret = "symvault://mail/pw"
	svfail.Tools = map[string]string{"symvault": "#!/bin/sh\necho 'vault locked' >&2\nexit 3\n"}
	add(svfail)
	svmiss := base("symvault-binary-missing")
	svmiss.Env = map[string]string{}
	svmiss.Account.PasswordSecret = "symvault://mail/pw"
	svmiss.NoSystemPath = true
	add(svmiss)
	svbad := base("symvault-reference-invalid")
	svbad.Account.PasswordSecret = "symvault://-bad"
	add(svbad)
	sm := base("select-missing-folder")
	sm.Account.Folder = "Missing"
	add(sm)
	for _, verb := range []string{"SELECT", "UID SEARCH", "UID FETCH", "STORE", "MOVE"} {
		fs := base("server-failure-"+strings.ToLower(strings.ReplaceAll(verb, " ", "-")), msg(1, "<z1@x>", mixed(pdfPart("attachment; filename=\"a.pdf\""))))
		fs.Server.Fail = map[string]string{verb: "NO simulated failure"}
		if verb == "STORE" {
			fs.Account.Action = "mark_seen"
		}
		if verb == "MOVE" {
			fs.Account.Action, fs.Account.MoveTo = "move", "Archive"
		}
		add(fs)
	}
	fb := base("server-failure-login", msg(1, "<z2@x>", mixed(pdfPart("attachment; filename=\"a.pdf\""))))
	fb.Server.Fail = map[string]string{"LOGIN": "NO [UNAVAILABLE] try later"}
	add(fb)
	for _, verb := range []string{"SELECT", "UID SEARCH", "UID FETCH"} {
		dr := base("server-drop-"+strings.ToLower(strings.ReplaceAll(verb, " ", "-")), msg(1, "<z3@x>", mixed(pdfPart("attachment; filename=\"a.pdf\""))))
		dr.Server.Drop = verb
		add(dr)
	}
	ob := base("fetch-without-body", msg(1, "<z4@x>", mixed(pdfPart("attachment; filename=\"a.pdf\""))))
	ob.Server.OmitBody = true
	add(ob)
	ne := base("fetch-without-envelope", imapMsg{UID: 1, NoEnvelope: true, Raw: crlf(mixed(pdfPart("attachment; filename=\"a.pdf\"")))})
	ne.Polls = 2
	add(ne)
	np2 := base("processing-dir-unset", msg(1, "<y1@x>", mixed(pdfPart("attachment; filename=\"a.pdf\""))), msg(2, "<y2@x>", "From: t@example.test\n\nplain\n"))
	np2.ProcessingDir = ""
	add(np2)
	pf := base("processing-dir-is-file", msg(1, "<y3@x>", mixed(pdfPart("attachment; filename=\"a.pdf\""))))
	pf.ProcessingDir = "file"
	add(pf)
	retry := base("failed-message-retried-next-poll", msg(1, "<y4@x>", mixed(pdfPart("attachment; filename=\"r.pdf\""))))
	retry.Polls = 2
	retry.Server.Fail = map[string]string{"UID FETCH": "NO flaky"}
	retry.Between = [][]mailMutation{{{Op: "server-fail", Fail: map[string]string{}}}}
	add(retry)
	partial := base("partial-failure-continues", msg(1, "<y5@x>", mixed("Content-Type: application/pdf\nContent-Transfer-Encoding: base64\nContent-Disposition: attachment; filename=\"bad.pdf\"\n\nJVBE!!!!Ri0x\n")), msg(2, "<y6@x>", mixed(pdfPart("attachment; filename=\"good.pdf\""))))
	add(partial)
	hk := base("enqueue-hook-ok", msg(1, "<x1@x>", mixed(pdfPart("attachment; filename=\"h.pdf\""))))
	hk.Hook = "ok"
	add(hk)
	hkf := base("enqueue-hook-fails", msg(1, "<x2@x>", mixed(pdfPart("attachment; filename=\"h.pdf\""))))
	hkf.Hook = "fail"
	add(hkf)
	st1 := base("start-single-account", msg(1, "<w1@x>", mixed(pdfPart("attachment; filename=\"s.pdf\""))))
	st1.Mode = "start"
	add(st1)
	// Two accounts polled by concurrent goroutines race for document ids and row
	// order, so that case is covered by a Rust-only behavioural test.
	return out
}

func TestPortIngestMailContract(t *testing.T) {
	var cases []mailCase
	for _, sc := range mailScenarios() {
		sc := sc
		t.Run(sc.ID, func(t *testing.T) {
			cases = append(cases, runMailScenario(t, sc))
		})
	}
	if t.Failed() {
		return
	}
	var attachments, errs, polls int
	for _, c := range cases {
		for _, f := range c.Files {
			if f.Type == "file" {
				attachments++
			}
		}
		for _, p := range c.Polls {
			polls++
			if p.Error != "" {
				errs++
			}
		}
	}
	if len(cases) < 60 || attachments < 30 || errs < 12 || polls < 70 {
		t.Fatalf("oracle too thin: %d scenarios %d attachments %d polls %d errors", len(cases), attachments, polls, errs)
	}
	hashes := map[string]string{}
	_, file, _, _ := runtime.Caller(0)
	src := filepath.Dir(file)
	for _, f := range []string{"mail.go", "mail_port_test.go"} {
		b, e := os.ReadFile(filepath.Join(src, f)) //nolint:gosec // repository source
		if e != nil {
			t.Fatal(e)
		}
		hashes[f] = fmt.Sprintf("%x", sha256.Sum256(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))))
	}
	content, e := json.MarshalIndent(map[string]any{
		"schema_version": 1,
		"oracle":         fixtureoracle.Current(),
		"source_hashes":  hashes,
		"cases":          cases,
		"normalizations": []string{"temp root <ROOT>, loopback 127.0.0.1:<PORT>, RFC3339 timestamps <TS>, server transcript lines without tags, password token <PASSWORD>"},
	}, "", " ")
	if e != nil {
		t.Fatal(e)
	}
	content = append(content, '\n')
	out := filepath.Join(src, "../../../../testdata/port/ingest/mail.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if e = os.WriteFile(out, content, 0o600); e != nil {
			t.Fatal(e)
		}
		return
	}
	old, e := os.ReadFile(out) //nolint:gosec // repository fixture
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(old, content) {
		t.Fatal("mail fixture drift; regenerate deliberately")
	}
}
