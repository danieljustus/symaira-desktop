package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// RUST-015 HTTP slices: the real Go `symdesk serve` process is started in an
// isolated HOME with a disposable vault and probed over a raw socket, so the
// recorded status line, header fields, body and connection behavior are the
// bytes the Go server wrote. Tokens are generated per run and appear in the
// fixture only as placeholders; every recorded response and the server's
// stderr are scanned for them (SEC-002). The Rust replay lives in
// `crates/symdesk-cli/tests/http_process.rs` and shares the step schema.

// httpFixtureSecrets are the per-run credentials a case may reference as
// `$NAME` in requests and files.
type httpSecrets struct {
	values map[string]string
	// vars are values captured from responses (share ids, share tokens). They
	// expand and redact like secrets but are expected in responses, so they are
	// not leak-scanned.
	vars map[string]string
}

func newHTTPSecrets() *httpSecrets {
	s := &httpSecrets{values: map[string]string{}, vars: map[string]string{}}
	for _, name := range []string{"ADMIN", "WORKER", "ADMIN2", "WORKER2", "NAMED", "WNAMED", "NAMEDADM", "SHARE", "SHARE2", "PROVKEY"} {
		buf := make([]byte, 20)
		if _, err := rand.Read(buf); err != nil {
			panic(err)
		}
		s.values[name] = hex.EncodeToString(buf)
	}
	for _, name := range []string{"NAMED", "WNAMED", "NAMEDADM", "SHARE", "SHARE2"} {
		sum := sha256.Sum256([]byte(s.values[name]))
		s.values[name+"_HASH"] = hex.EncodeToString(sum[:])
	}
	return s
}

// order lists placeholder names longest first so `$ADMIN2` is not read as
// `$ADMIN` followed by `2`.
func (s *httpSecrets) order() []string {
	names := make([]string, 0, len(s.values)+len(s.vars))
	for name := range s.values {
		names = append(names, name)
	}
	for name := range s.vars {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if len(names[i]) != len(names[j]) {
			return len(names[i]) > len(names[j])
		}
		return names[i] < names[j]
	})
	return names
}

func (s *httpSecrets) lookup(name string) string {
	if value, ok := s.values[name]; ok {
		return value
	}
	return s.vars[name]
}

func (s *httpSecrets) expand(text string) string {
	for _, name := range s.order() {
		text = strings.ReplaceAll(text, "$"+name, s.lookup(name))
	}
	return text
}

// redact is the inverse of expand for recorded output. The hash of a token is
// a distinct secret and is redacted before the plain token.
func (s *httpSecrets) redact(text string) string {
	for _, name := range s.order() {
		if value := s.lookup(name); value != "" {
			text = strings.ReplaceAll(text, value, "$"+name)
		}
	}
	return text
}

func (s *httpSecrets) leaks(text string) []string {
	var found []string
	for _, name := range s.order() {
		if value, ok := s.values[name]; ok && strings.Contains(text, value) {
			found = append(found, name)
		}
	}
	return found
}

type httpRepeat struct {
	Text  string `json:"text"`
	Count int    `json:"count"`
}

// httpObserved is what the Go server did for one step.
type httpObserved struct {
	Status    int         `json:"status,omitempty"`
	Reason    string      `json:"reason,omitempty"`
	Headers   [][2]string `json:"headers,omitempty"`
	Body      string      `json:"body,omitempty"`
	BodyB64   string      `json:"body_b64,omitempty"`
	BodyLen   int         `json:"body_len,omitempty"`
	BodySHA   string      `json:"body_sha256,omitempty"`
	Continues int         `json:"continues,omitempty"`
	// Closed is true when the server closed the connection once the response
	// was complete (false: it stayed open).
	Closed bool `json:"closed"`
	// Silent is set when the server wrote nothing at all.
	Silent bool `json:"silent,omitempty"`
	// Tree is the vault tree of a tree step.
	Tree map[string]string `json:"tree,omitempty"`
	// Exit is the process outcome of a restart or shutdown step.
	Exit string `json:"exit,omitempty"`
	// Early is the response body read before the provider was released (a
	// streamed response must be flushed while the upstream is still running).
	Early string `json:"early,omitempty"`
	// TLS is the negotiated protocol of a TLS exchange.
	TLS string `json:"tls,omitempty"`
}

type httpStep struct {
	Label   string      `json:"label"`
	Kind    string      `json:"kind,omitempty"`
	Method  string      `json:"method,omitempty"`
	Target  string      `json:"target,omitempty"`
	Version string      `json:"version,omitempty"`
	Headers [][2]string `json:"headers,omitempty"`
	// NoHost, NoClose and NoLength drop the Host header, the
	// `Connection: close` header and the computed Content-Length.
	NoHost     bool        `json:"no_host,omitempty"`
	NoClose    bool        `json:"no_close,omitempty"`
	NoLength   bool        `json:"no_length,omitempty"`
	Body       string      `json:"body,omitempty"`
	BodyRepeat *httpRepeat `json:"body_repeat,omitempty"`
	// Raw is a complete request written verbatim (kind "raw"); RawFill
	// replaces the token `{{FILL}}` with that many `a` bytes.
	Raw     string `json:"raw,omitempty"`
	RawFill int    `json:"raw_fill,omitempty"`
	// ReadMS bounds the wait for the response (default 5000).
	ReadMS int `json:"read_ms,omitempty"`
	// Mask lists the volatile value classes of this step: "time" hides RFC 3339
	// timestamps, "hex" hides runs of 32 or more lower-case hex digits,
	// "version" the status document's version and "date" HTTP dates.
	Mask []string `json:"mask,omitempty"`
	// VolatileLength drops Content-Length from the comparison.
	VolatileLength bool `json:"volatile_length,omitempty"`
	// IgnoreClosed drops the "connection closed" observation of the step: after
	// an unread multi-megabyte body the Go server's close is a kernel race
	// between FIN (EOF) and RST (connection reset), not a protocol behavior.
	IgnoreClosed bool `json:"ignore_closed,omitempty"`
	// Filesystem operations (kind "fs").
	Op      string `json:"op,omitempty"`
	Path    string `json:"path,omitempty"`
	Content string `json:"content,omitempty"`
	Mtime   int64  `json:"mtime,omitempty"`
	Mode    int    `json:"mode,omitempty"`
	// Restart: Rotate swaps in the ADMIN2/WORKER2 credentials.
	Rotate bool `json:"rotate,omitempty"`
	// TreeInclude lists vault-relative prefixes whose contents (not only
	// hashes) are recorded by a tree step.
	TreeInclude []string `json:"tree_include,omitempty"`
	// Stall (kind "shutdown"): headers of a request are written, the body
	// is not, then SIGTERM is sent; the observed exit is recorded.
	Stall string `json:"stall,omitempty"`
	// Capture stores top-level string members of a JSON response body as
	// `$NAME` variables for later steps.
	Capture map[string]string `json:"capture,omitempty"`
	// Count is the number of provider cancellations a "provider" step waits
	// for. StreamLines/Release/Abort drive a "stream" step: read that many
	// body lines, then release the provider gate (or drop the connection).
	Count       int `json:"count,omitempty"`
	StreamLines int `json:"stream_lines,omitempty"`
	// StreamUntil ends the early read at the first line containing the text.
	StreamUntil string `json:"stream_until,omitempty"`
	Release     bool `json:"release,omitempty"`
	Abort       bool `json:"abort,omitempty"`
	// Args are the `serve` arguments of a "launch" step.
	Args []string `json:"args,omitempty"`
	// Plain sends the request in plaintext to a TLS listener; TLSVersion pins
	// the client to "1.2" or "1.3".
	Plain      bool   `json:"plain,omitempty"`
	TLSVersion string `json:"tls_version,omitempty"`
	// TimeClass records the elapsed-time class of the exchange in Exit.
	TimeClass bool `json:"time_class,omitempty"`
	// WaitMS is the pause of a "sleep" step.
	WaitMS int `json:"wait_ms,omitempty"`

	Expect *httpObserved `json:"expect,omitempty"`
}

type httpCase struct {
	Name     string            `json:"name"`
	Files    map[string]string `json:"files,omitempty"`
	Symlinks map[string]string `json:"symlinks,omitempty"`
	Mtime    int64             `json:"mtime,omitempty"`
	NoWorker bool              `json:"no_worker,omitempty"`
	// Serve replaces the default `serve` command line; Env adds server
	// environment variables; TLS mints a certificate pair under $ROOT/tls and
	// makes the client speak TLS; Provider starts the fake loopback provider
	// and points the server at it; EmptyPath gives the server an empty PATH;
	// NoServer starts nothing (launch-only cases).
	Serve     *httpServe        `json:"serve,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	TLS       bool              `json:"tls,omitempty"`
	Provider  bool              `json:"provider,omitempty"`
	EmptyPath bool              `json:"empty_path,omitempty"`
	NoServer  bool              `json:"no_server,omitempty"`
	Steps     []httpStep        `json:"steps"`
	// StderrLeaks lists the secret names found in the server's stderr and in
	// every recorded response (must be empty).
	StderrLeaks []string `json:"stderr_leaks"`
}

type httpFixture struct {
	SchemaVersion int        `json:"schema_version"`
	Cases         []httpCase `json:"cases"`
}

var (
	httpRFC3339 = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})`)
	httpHexRun  = regexp.MustCompile(`[0-9a-f]{24,}`)
	httpBoundry = regexp.MustCompile(`boundary=[0-9a-f]+`)
	// httpVersion hides the build version of the JSON status document: the Go
	// test binary reports "(devel)", the Rust binary its package version.
	httpVersion = regexp.MustCompile(`"version":"[^"]*"`)
	httpDate    = regexp.MustCompile(`[A-Z][a-z]{2}, \d{2} [A-Z][a-z]{2} \d{4} \d{2}:\d{2}:\d{2} GMT`)
)

type httpServer struct {
	t       *testing.T
	binary  string
	root    string
	vault   string
	home    string
	secrets *httpSecrets
	port    int
	cmd     *exec.Cmd
	stderr  *os.File
	exited  chan error
	rotated bool
	noWork  bool
	// leaked collects the secrets seen in raw responses.
	leaked map[string]bool
	// Extension state (http_process_ext_port_test.go).
	c        httpCase
	vault2   string
	provider *httpProvider
	tlsRoots *x509.CertPool
	capture  map[string]string
}

func httpFreePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	return listener.Addr().(*net.TCPAddr).Port
}

func (s *httpServer) start() {
	s.t.Helper()
	s.port = httpFreePort(s.t)
	args, env := s.serveCommand()
	//nolint:gosec // the Go CLI built from this package, arguments are fixed.
	cmd := exec.Command(s.serveBinary(), args...)
	cmd.Dir = s.home
	cmd.Env = env
	stderr, err := os.OpenFile(filepath.Join(s.root, "stderr.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		s.t.Fatal(err)
	}
	s.stderr = stderr
	cmd.Stdout = io.Discard
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		s.t.Fatal(err)
	}
	s.cmd = cmd
	s.exited = make(chan error, 1)
	go func() { s.exited <- cmd.Wait() }()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", s.port), 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		select {
		case exit := <-s.exited:
			s.t.Fatalf("server exited during start: %v", exit)
		case <-time.After(25 * time.Millisecond):
		}
	}
	s.t.Fatal("server did not start")
}

// stop sends SIGTERM and returns the exit description.
func (s *httpServer) stop() string {
	s.t.Helper()
	if s.cmd == nil {
		return "not running"
	}
	_ = s.cmd.Process.Signal(syscall.SIGTERM)
	var result string
	select {
	case err := <-s.exited:
		result = describeExit(err)
	case <-time.After(30 * time.Second):
		_ = s.cmd.Process.Kill()
		<-s.exited
		result = "killed after 30s"
	}
	s.cmd = nil
	_ = s.stderr.Close()
	return result
}

func describeExit(err error) string {
	if err == nil {
		return "exit 0"
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return fmt.Sprintf("exit %d", exitErr.ExitCode())
	}
	return err.Error()
}

func (s *httpServer) exchange(raw []byte, headOnly bool, step httpStep, readMS int, mask []string, volatileLength bool) httpObserved {
	s.t.Helper()
	if readMS == 0 {
		readMS = 5000
	}
	conn, tlsState, failure := s.dialStep(step.Plain, step.TLSVersion)
	if failure != "" {
		return httpObserved{Exit: failure}
	}
	defer func() { _ = conn.Close() }()
	// Large requests are written concurrently so a server answering early
	// (and closing) cannot deadlock the client.
	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		_, _ = conn.Write(raw)
	}()
	_ = conn.SetReadDeadline(time.Now().Add(time.Duration(readMS) * time.Millisecond))
	var wire bytes.Buffer
	reader := bufio.NewReaderSize(io.TeeReader(conn, &wire), 1<<20)
	observed := s.readResponse(conn, reader, headOnly, mask, volatileLength)
	if step.IgnoreClosed {
		observed.Closed = false
	}
	observed.TLS = tlsState()
	<-writeDone
	for _, name := range s.secrets.leaks(wire.String()) {
		s.leaked[name] = true
	}
	return observed
}

func (s *httpServer) readResponse(conn net.Conn, reader *bufio.Reader, headOnly bool, mask []string, volatileLength bool) httpObserved {
	var observed httpObserved
	for {
		line, err := reader.ReadString('\n')
		if err != nil && line == "" {
			observed.Silent = true
			observed.Closed = errors.Is(err, io.EOF)
			return observed
		}
		line = strings.TrimRight(line, "\r\n")
		parts := strings.SplitN(line, " ", 3)
		if len(parts) < 2 || !strings.HasPrefix(parts[0], "HTTP/") {
			if strings.HasPrefix(line, "\x15\x03") {
				// A TLS alert record: the alert bytes differ between TLS stacks.
				observed.Exit = "tls alert"
				observed.Closed = true
				return observed
			}
			s.t.Fatalf("unparsable status line %q", line)
		}
		status, convErr := strconv.Atoi(parts[1])
		if convErr != nil {
			s.t.Fatalf("status line %q: %v", line, convErr)
		}
		observed.Status = status
		observed.Reason = ""
		if len(parts) == 3 {
			observed.Reason = parts[2]
		}
		var headers [][2]string
		for {
			h, err := reader.ReadString('\n')
			if err != nil {
				s.t.Fatalf("read headers: %v", err)
			}
			h = strings.TrimRight(h, "\r\n")
			if h == "" {
				break
			}
			name, value, _ := strings.Cut(h, ":")
			headers = append(headers, [2]string{strings.ToLower(strings.TrimSpace(name)), strings.TrimSpace(value)})
		}
		if status == 100 {
			observed.Continues++
			continue
		}
		observed.Headers = s.normalizeHeaders(headers, mask, volatileLength)
		var body []byte
		chunked, length := false, int64(-1)
		for _, h := range headers {
			switch h[0] {
			case "transfer-encoding":
				if strings.EqualFold(h[1], "chunked") {
					chunked = true
				}
			case "content-length":
				if n, err := strconv.ParseInt(h[1], 10, 64); err == nil {
					length = n
				}
			}
		}
		noBody := headOnly || status == 204 || status == 304 || (status >= 100 && status < 200)
		switch {
		case noBody:
		case chunked:
			body = readChunked(reader)
		case length >= 0:
			body = make([]byte, length)
			n, _ := io.ReadFull(reader, body)
			body = body[:n]
		default:
			body, _ = io.ReadAll(reader)
		}
		s.applyCapture(body, headers)
		s.recordBody(&observed, body, mask)
		// Does the server close the connection once the response is complete?
		_ = conn.SetReadDeadline(time.Now().Add(400 * time.Millisecond))
		_, err = reader.ReadByte()
		observed.Closed = errors.Is(err, io.EOF)
		return observed
	}
}

func readChunked(reader *bufio.Reader) []byte {
	var out []byte
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return out
		}
		sizeText, _, _ := strings.Cut(strings.TrimSpace(line), ";")
		size, err := strconv.ParseInt(sizeText, 16, 64)
		if err != nil {
			return out
		}
		if size == 0 {
			for {
				trailer, err := reader.ReadString('\n')
				if err != nil || strings.TrimRight(trailer, "\r\n") == "" {
					return out
				}
			}
		}
		chunk := make([]byte, size)
		n, _ := io.ReadFull(reader, chunk)
		out = append(out, chunk[:n]...)
		_, _ = reader.ReadString('\n')
	}
}

func (s *httpServer) maskText(text string, mask []string) string {
	text = s.secrets.redact(text)
	text = strings.ReplaceAll(text, fmt.Sprintf("127.0.0.1:%d", s.port), "127.0.0.1:$PORT")
	text = strings.ReplaceAll(text, s.vault, "$VAULT")
	text = strings.ReplaceAll(text, s.root, "$ROOT")
	for _, class := range mask {
		switch class {
		case "time":
			text = httpRFC3339.ReplaceAllString(text, "$$TIME")
		case "hex":
			text = httpHexRun.ReplaceAllString(text, "$$HEX")
		case "version":
			text = httpVersion.ReplaceAllString(text, `"version":"$$VERSION"`)
		case "date":
			text = httpDate.ReplaceAllString(text, "$$DATE")
		}
	}
	return text
}

func (s *httpServer) normalizeHeaders(headers [][2]string, mask []string, volatileLength bool) [][2]string {
	var out [][2]string
	for _, h := range headers {
		name, value := h[0], h[1]
		switch name {
		case "date":
			continue
		case "content-length":
			if volatileLength {
				continue
			}
		case "retry-after":
			value = "$RETRY"
		case "content-type":
			value = httpBoundry.ReplaceAllString(value, "boundary=$$BOUNDARY")
		}
		out = append(out, [2]string{name, s.maskText(value, mask)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i][0] != out[j][0] {
			return out[i][0] < out[j][0]
		}
		return out[i][1] < out[j][1]
	})
	return out
}

func (s *httpServer) recordBody(observed *httpObserved, body []byte, mask []string) {
	for _, class := range mask {
		if class == "nobody" {
			return
		}
	}
	observed.BodyLen = len(body)
	if len(body) > 16<<10 {
		sum := sha256.Sum256(body)
		observed.BodySHA = hex.EncodeToString(sum[:])
		return
	}
	if len(body) == 0 {
		return
	}
	text := string(body)
	if !isValidUTF8(body) {
		observed.BodyB64 = base64.StdEncoding.EncodeToString(body)
		return
	}
	observed.Body = s.maskText(text, mask)
	observed.BodyLen = len(observed.Body)
	if observed.BodyLen != len(body) {
		// The body was altered by masking; keep the observed length class
		// out of the comparison through VolatileLength on the step.
		observed.BodyLen = 0
	}
}

func isValidUTF8(b []byte) bool {
	return strings.ToValidUTF8(string(b), "\x00") == string(b)
}

// tree walks the vault and returns path -> description. `include` prefixes
// have their (masked) text recorded, everything else its SHA-256 and mode.
func (s *httpServer) tree(include []string, mask []string) map[string]string {
	s.t.Helper()
	// users.json carries per-run random token hashes: always compare it masked.
	include = append(append([]string{}, include...), ".symdesk/users.json")
	out := map[string]string{}
	err := filepath.WalkDir(s.vault, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(s.vault, path)
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if strings.HasPrefix(rel, ".http-harness") || strings.HasPrefix(rel, ".symdesk/server/sidecar.db") ||
			strings.HasPrefix(rel, ".symdesk/server/state.db") || strings.HasSuffix(rel, ".tmp") ||
			strings.HasPrefix(rel, ".symdesk/server/.") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			target, _ := os.Readlink(path)
			out[rel] = "-> " + s.maskText(target, nil)
			return nil
		}
		if entry.IsDir() {
			out[rel+"/"] = fmt.Sprintf("dir %04o", info.Mode().Perm())
			return nil
		}
		data, err := os.ReadFile(path) //nolint:gosec // walk below the case's disposable vault.
		if err != nil {
			return err
		}
		mode := fmt.Sprintf("%04o", info.Mode().Perm())
		for _, prefix := range include {
			if strings.HasPrefix(rel, prefix) && isValidUTF8(data) {
				out[rel] = mode + " " + s.maskText(string(data), mask)
				return nil
			}
		}
		sum := sha256.Sum256(data)
		out[rel] = mode + " sha256:" + hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		s.t.Fatal(err)
	}
	return out
}

func buildHTTPRequest(secrets *httpSecrets, step httpStep, port int) ([]byte, bool) {
	method := step.Method
	if method == "" {
		method = "GET"
	}
	version := step.Version
	if version == "" {
		version = "HTTP/1.1"
	}
	body := secrets.expand(step.Body)
	if step.BodyRepeat != nil {
		body += strings.Repeat(step.BodyRepeat.Text, step.BodyRepeat.Count)
	}
	var head strings.Builder
	fmt.Fprintf(&head, "%s %s %s\r\n", method, secrets.expand(step.Target), version)
	hasHost, hasLength := false, false
	for _, h := range step.Headers {
		switch strings.ToLower(h[0]) {
		case "host":
			hasHost = true
		case "content-length", "transfer-encoding":
			hasLength = true
		}
		fmt.Fprintf(&head, "%s: %s\r\n", h[0], secrets.expand(h[1]))
	}
	if !hasHost && !step.NoHost {
		fmt.Fprintf(&head, "Host: 127.0.0.1:%d\r\n", port)
	}
	if !step.NoClose {
		head.WriteString("Connection: close\r\n")
	}
	if body != "" && !hasLength && !step.NoLength {
		fmt.Fprintf(&head, "Content-Length: %d\r\n", len(body))
	}
	head.WriteString("\r\n")
	return []byte(head.String() + body), method == "HEAD"
}

func httpApplyFS(t *testing.T, s *httpServer, step httpStep, defaultMtime int64) {
	t.Helper()
	target := filepath.Join(s.vault, filepath.FromSlash(step.Path))
	switch step.Op {
	case "write":
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(s.secrets.expand(step.Content)), 0o600); err != nil {
			t.Fatal(err)
		}
		mtime := step.Mtime
		if mtime == 0 {
			mtime = defaultMtime
		}
		when := time.Unix(mtime, 0)
		if err := os.Chtimes(target, when, when); err != nil {
			t.Fatal(err)
		}
	case "symlink":
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(step.Content, target); err != nil {
			t.Fatal(err)
		}
	case "remove":
		if err := os.RemoveAll(target); err != nil {
			t.Fatal(err)
		}
	case "mkdir":
		if err := os.MkdirAll(target, 0o750); err != nil {
			t.Fatal(err)
		}
	case "chmod":
		if err := os.Chmod(target, fs.FileMode(step.Mode)); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown fs op %q", step.Op)
	}
}

func observeHTTPCase(t *testing.T, binary string, c httpCase) httpCase {
	t.Helper()
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	secrets := newHTTPSecrets()
	s := &httpServer{t: t, binary: binary, root: root, vault: filepath.Join(root, "vault"), home: filepath.Join(root, "home"), secrets: secrets, noWork: c.NoWorker, leaked: map[string]bool{}, c: c, vault2: filepath.Join(root, "vault2")}
	defer s.closeExt()
	for _, dir := range []string{s.vault, filepath.Join(s.home, "tmp")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	mtime := c.Mtime
	if mtime == 0 {
		mtime = 1767323045
	}
	names := make([]string, 0, len(c.Files))
	for name := range c.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		httpApplyFS(t, s, httpStep{Op: "write", Path: name, Content: c.Files[name], Mtime: mtime}, mtime)
	}
	linkNames := make([]string, 0, len(c.Symlinks))
	for name := range c.Symlinks {
		linkNames = append(linkNames, name)
	}
	sort.Strings(linkNames)
	for _, name := range linkNames {
		target := c.Symlinks[name]
		if strings.HasPrefix(target, "$ROOT/") {
			target = filepath.Join(root, strings.TrimPrefix(target, "$ROOT/"))
		}
		httpApplyFS(t, s, httpStep{Op: "symlink", Path: name, Content: target}, mtime)
	}
	// Files that live beside the vault (symlink escape targets).
	if err := os.WriteFile(filepath.Join(root, "outside.md"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.prepareExt()
	if !c.NoServer {
		s.start()
	}
	out := c
	out.Steps = nil
	for _, step := range c.Steps {
		observed := observeHTTPStep(t, s, step, mtime)
		step.Expect = &observed
		out.Steps = append(out.Steps, step)
	}
	if !c.NoServer {
		exit := s.stop()
		if exit != "exit 0" && exit != "exit 1" && exit != "not running" {
			t.Fatalf("case %s: server stop: %s", c.Name, exit)
		}
	}
	logData, _ := os.ReadFile(filepath.Join(root, "stderr.log")) //nolint:gosec // disposable case root.
	// A hit is a live credential in the server's log or in any raw response.
	out.StderrLeaks = []string{}
	for _, name := range secrets.leaks(string(logData)) {
		s.leaked[name] = true
	}
	for name := range s.leaked {
		out.StderrLeaks = append(out.StderrLeaks, name)
	}
	sort.Strings(out.StderrLeaks)
	return out
}

func observeHTTPStep(t *testing.T, s *httpServer, step httpStep, mtime int64) httpObserved {
	t.Helper()
	if observed, handled := s.observeExtStep(step); handled {
		return observed
	}
	s.capture = step.Capture
	defer func() { s.capture = nil }()
	switch step.Kind {
	case "fs":
		httpApplyFS(t, s, step, mtime)
		// Let the vault watcher deliver the change before the next request.
		time.Sleep(300 * time.Millisecond)
		return httpObserved{}
	case "tree":
		return httpObserved{Tree: s.tree(step.TreeInclude, step.Mask)}
	case "restart":
		exit := s.stop()
		if step.Rotate {
			s.rotated = true
		}
		s.start()
		return httpObserved{Exit: exit}
	case "shutdown":
		return s.observeShutdown(step)
	case "raw":
		raw := s.secrets.expand(step.Raw)
		raw = strings.ReplaceAll(raw, "{{FILL}}", strings.Repeat("a", step.RawFill))
		raw = strings.ReplaceAll(raw, "{{PORT}}", strconv.Itoa(s.port))
		method, _, _ := strings.Cut(raw, " ")
		return s.timed(step, func() httpObserved {
			return s.exchange([]byte(raw), method == "HEAD", step, step.ReadMS, step.Mask, step.VolatileLength)
		})
	default:
		raw, head := buildHTTPRequest(s.secrets, step, s.port)
		return s.timed(step, func() httpObserved {
			return s.exchange(raw, head, step, step.ReadMS, step.Mask, step.VolatileLength)
		})
	}
}

func (s *httpServer) observeShutdown(step httpStep) httpObserved {
	s.t.Helper()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", s.port), 5*time.Second)
	if err != nil {
		s.t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte(s.secrets.expand(step.Stall))); err != nil {
		s.t.Fatal(err)
	}
	// Let the server register the in-flight request.
	time.Sleep(300 * time.Millisecond)
	started := time.Now()
	exit := s.stop()
	waited := time.Since(started)
	class := "immediately"
	if waited > 5*time.Second {
		class = "at the deadline"
	}
	return httpObserved{Exit: exit + " " + class}
}

func writeHTTPFixture(t *testing.T, relative string, fixture httpFixture) {
	t.Helper()
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source path unavailable")
	}
	path := filepath.Join(filepath.Dir(source), relative)
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded, 0o644); err != nil { //nolint:gosec // generated contract fixture is intentionally world-readable
			t.Fatal(err)
		}
		return
	}
	current, err := os.ReadFile(path) //nolint:gosec // fixed fixture path
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, encoded) {
		offset := 0
		for offset < len(current) && offset < len(encoded) && current[offset] == encoded[offset] {
			offset++
		}
		from := max(offset-200, 0)
		t.Fatalf("%s is stale; regenerate through portgen\nfirst difference at byte %d\ncommitted: %q\nobserved:  %q", relative, offset, string(current[from:min(offset+200, len(current))]), string(encoded[from:min(offset+200, len(encoded))]))
	}
}

func observeHTTPFixture(t *testing.T, cases []httpCase) httpFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("HTTP process fixtures are recorded and replayed on Unix hosts")
	}
	binary := processFixtureBinary(t)
	fixture := httpFixture{SchemaVersion: 1}
	for _, c := range cases {
		fixture.Cases = append(fixture.Cases, observeHTTPCase(t, binary, c))
	}
	return fixture
}
