package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Extensions of the RUST-015 HTTP process harness: scripted serve command
// lines (Home Assistant options, TLS, local worker), a fake loopback Ollama
// provider, streamed responses, launch-only cases and captured variables.

// httpServe replaces the default `serve` command line of a case. Placeholders:
// $PORT $VAULT $VAULT2 $ROOT $TLSCERT $TLSKEY and every secret name.
type httpServe struct {
	Args []string          `json:"args,omitempty"`
	Env  map[string]string `json:"env,omitempty"`
	// Options is the content of the Home Assistant options file.
	Options string `json:"options,omitempty"`
}

var haBinaryBuild struct {
	sync.Once
	path string
	err  error
	out  []byte
}

// haOptionsFile is the file the HA-aware binary reads. The production source
// stays untouched: the binary is built with a `go build -overlay` that replaces
// the fixed "/data/options.json" literal of selfhost.go in memory.
func haOptionsFile() string { return filepath.Join(processFixtureBuild.root, "ha-options.json") }

func haBinary(t *testing.T) string {
	t.Helper()
	haBinaryBuild.Do(func() {
		haBinaryBuild.path = filepath.Join(processFixtureBuild.root, "symdesk-ha")
		source, err := os.ReadFile("selfhost.go")
		if err != nil {
			haBinaryBuild.err = err
			return
		}
		const literal = `"/data/options.json"`
		if !bytes.Contains(source, []byte(literal)) {
			haBinaryBuild.err = errors.New("selfhost.go no longer names /data/options.json")
			return
		}
		patched := bytes.ReplaceAll(source, []byte(literal), []byte(strconv.Quote(haOptionsFile())))
		absolute, err := filepath.Abs("selfhost.go")
		if err != nil {
			haBinaryBuild.err = err
			return
		}
		patchedPath := filepath.Join(processFixtureBuild.root, "selfhost-ha.go")
		overlayPath := filepath.Join(processFixtureBuild.root, "ha-overlay.json")
		overlay, _ := json.Marshal(map[string]map[string]string{"Replace": {absolute: patchedPath}})
		if err := os.WriteFile(patchedPath, patched, 0o600); err != nil {
			haBinaryBuild.err = err
			return
		}
		if err := os.WriteFile(overlayPath, overlay, 0o600); err != nil {
			haBinaryBuild.err = err
			return
		}
		//nolint:gosec // fixed Go build of this package into the process-owned temporary root.
		command := exec.Command("go", "build", "-overlay", overlayPath, "-o", haBinaryBuild.path, ".")
		command.Env = processFixtureBuild.environment
		haBinaryBuild.out, haBinaryBuild.err = command.CombinedOutput()
	})
	if haBinaryBuild.err != nil {
		t.Fatalf("build HA-aware CLI: %v\n%s", haBinaryBuild.err, haBinaryBuild.out)
	}
	return haBinaryBuild.path
}

func (s *httpServer) serveBinary() string {
	if s.c.Serve != nil {
		return haBinary(s.t)
	}
	return s.binary
}

func (s *httpServer) expandPaths(text string) string {
	replacer := strings.NewReplacer(
		"$PORT", strconv.Itoa(s.port), "$VAULT2", s.vault2, "$VAULT", s.vault, "$ROOT", s.root,
		"$TLSCERT", filepath.Join(s.root, "tls", "cert.pem"), "$TLSKEY", filepath.Join(s.root, "tls", "key.pem"),
	)
	return s.secrets.expand(replacer.Replace(text))
}

func (s *httpServer) baseEnv() []string {
	tmp := filepath.Join(s.home, "tmp")
	path := os.Getenv("PATH")
	if s.c.EmptyPath {
		path = filepath.Join(s.root, "emptybin")
	}
	return []string{
		"HOME=" + s.home, "USERPROFILE=" + s.home,
		"XDG_CONFIG_HOME=" + filepath.Join(s.home, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(s.home, ".local", "share"),
		"XDG_CACHE_HOME=" + filepath.Join(s.home, ".cache"),
		"TMPDIR=" + tmp, "TMP=" + tmp, "TEMP=" + tmp,
		"LANG=C", "LC_ALL=C", "TZ=UTC", "TERM=dumb", "NO_COLOR=1",
		"SYMDESK_VAULT=" + s.vault, "SYMDESK_VERSION=0.12.2",
		"PATH=" + path,
	}
}

// serveCommand returns the arguments after `serve` and the environment.
func (s *httpServer) serveCommand() ([]string, []string) {
	env := s.baseEnv()
	var args []string
	if s.c.Serve != nil {
		for _, arg := range s.c.Serve.Args {
			args = append(args, s.expandPaths(arg))
		}
		// A case that names its own environment may drop the default vault.
		for name, value := range s.c.Serve.Env {
			env = append(env, name+"="+s.expandPaths(value))
		}
		s.writeOptions()
	} else {
		token, worker := s.secrets.values["ADMIN"], s.secrets.values["WORKER"]
		if s.rotated {
			token, worker = s.secrets.values["ADMIN2"], s.secrets.values["WORKER2"]
		}
		args = []string{"--listen", fmt.Sprintf("127.0.0.1:%d", s.port), "--vault", s.vault, "--token", token}
		if !s.noWork {
			args = append(args, "--worker-token", worker)
		}
		if s.c.TLS {
			args = append(args, "--tls-cert", filepath.Join(s.root, "tls", "cert.pem"), "--tls-key", filepath.Join(s.root, "tls", "key.pem"))
		}
	}
	args = append([]string{"serve"}, args...)
	for name, value := range s.c.Env {
		env = append(env, name+"="+s.expandPaths(value))
	}
	if s.provider != nil {
		env = append(env, "SYMDESK_LLM_PROVIDER=ollama", "SYMDESK_OLLAMA_URL="+s.provider.server.URL, "SYMDESK_OLLAMA_MODEL=fixture-model")
	}
	return args, env
}

// writeOptions installs (or removes) the Home Assistant options file.
func (s *httpServer) writeOptions() {
	if s.c.Serve == nil {
		return
	}
	if s.c.Serve.Options == "" {
		_ = os.Remove(haOptionsFile())
		return
	}
	if err := os.WriteFile(haOptionsFile(), []byte(s.expandPaths(s.c.Serve.Options)), 0o600); err != nil {
		s.t.Fatal(err)
	}
}

// prepareExt creates the extension fixtures of a case before the server starts.
func (s *httpServer) prepareExt() {
	t := s.t
	t.Helper()
	if err := os.MkdirAll(s.vault2, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.vault2, "Second.md"), []byte("---\ntitle: Second\n---\nsecond vault"), 0o600); err != nil {
		t.Fatal(err)
	}
	if s.c.EmptyPath {
		if err := os.MkdirAll(filepath.Join(s.root, "emptybin"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if s.c.TLS || s.c.NoServer {
		s.mintCertificate()
	}
	if s.c.Provider {
		s.provider = newHTTPProvider(s.secrets)
	}
}

func (s *httpServer) closeExt() {
	if s.provider != nil {
		s.provider.server.Close()
	}
}

// mintCertificate writes a fresh self-signed pair for 127.0.0.1/localhost.
func (s *httpServer) mintCertificate() {
	t := s.t
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "symdesk http fixture"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true, DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(s.root, "tls")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cert.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "key.pem"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	s.tlsRoots = x509.NewCertPool()
	s.tlsRoots.AddCert(certificate)
}

// dialStep connects to the server, wrapping the connection in TLS when the case
// is a TLS case. The returned function describes the negotiated protocol; a
// non-empty third value names a failed handshake.
func (s *httpServer) dialStep(plain bool, version string) (net.Conn, func() string, string) {
	s.t.Helper()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", s.port), 5*time.Second)
	if err != nil {
		s.t.Fatal(err)
	}
	if !s.c.TLS || plain {
		return conn, func() string { return "" }, ""
	}
	config := &tls.Config{RootCAs: s.tlsRoots, ServerName: "127.0.0.1", NextProtos: []string{"http/1.1"}, MinVersion: tls.VersionTLS12}
	switch version {
	case "1.2":
		config.MaxVersion = tls.VersionTLS12
	case "1.3":
		config.MinVersion = tls.VersionTLS13
	}
	client := tls.Client(conn, config)
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	if err := client.Handshake(); err != nil {
		_ = conn.Close()
		return nil, nil, "tls handshake failed"
	}
	_ = client.SetDeadline(time.Time{})
	state := client.ConnectionState()
	label := map[uint16]string{tls.VersionTLS12: "TLS1.2", tls.VersionTLS13: "TLS1.3"}[state.Version]
	label += " " + state.NegotiatedProtocol
	return client, func() string { return label }, ""
}

// applyCapture stores `header:<name>` response headers and top-level string
// members of a JSON body as variables.
func (s *httpServer) applyCapture(body []byte, headers [][2]string) {
	if len(s.capture) == 0 {
		return
	}
	var object map[string]any
	_ = json.Unmarshal(body, &object)
	for name, key := range s.capture {
		if header, ok := strings.CutPrefix(key, "header:"); ok {
			for _, h := range headers {
				if h[0] == header {
					s.secrets.vars[name] = h[1]
				}
			}
			continue
		}
		if value, ok := object[key].(string); ok {
			s.secrets.vars[name] = value
		}
	}
}

// timed records the elapsed-time class of a step (Exit) when asked to.
func (s *httpServer) timed(step httpStep, run func() httpObserved) httpObserved {
	if !step.TimeClass {
		return run()
	}
	started := time.Now()
	observed := run()
	switch elapsed := time.Since(started); {
	case elapsed < 3*time.Second:
		observed.Exit = "fast"
	case elapsed >= 9*time.Second && elapsed <= 14*time.Second:
		observed.Exit = "after the 10 s header timeout"
	default:
		observed.Exit = "slow"
	}
	return observed
}

func (s *httpServer) observeExtStep(step httpStep) (httpObserved, bool) {
	switch step.Kind {
	case "sleep":
		time.Sleep(time.Duration(step.WaitMS) * time.Millisecond)
		return httpObserved{}, true
	case "wait_jobs":
		return s.waitJobs(step), true
	case "provider":
		return s.observeProvider(step), true
	case "launch":
		return s.launch(step), true
	case "stream":
		s.capture = step.Capture
		defer func() { s.capture = nil }()
		return s.stream(step), true
	}
	return httpObserved{}, false
}

// waitJobs polls the job list until no job is pending or processing.
func (s *httpServer) waitJobs(step httpStep) httpObserved {
	client := &http.Client{Timeout: 2 * time.Second}
	token := s.secrets.expand(step.Body)
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		request, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/api/v1/jobs", s.port), nil)
		if err != nil {
			s.t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+token)
		if response, err := client.Do(request); err == nil {
			data, _ := io.ReadAll(response.Body)
			_ = response.Body.Close()
			var jobs []struct {
				Status string `json:"status"`
			}
			if json.Unmarshal(data, &jobs) == nil && len(jobs) > 0 {
				open := false
				for _, job := range jobs {
					if job.Status == "pending" || job.Status == "processing" {
						open = true
					}
				}
				if !open {
					return httpObserved{Exit: "settled"}
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return httpObserved{Exit: "timeout"}
}

// launch runs `serve <args>` to completion and records its exit and stderr.
func (s *httpServer) launch(step httpStep) httpObserved {
	s.port = httpFreePort(s.t)
	s.writeOptions()
	args := []string{"serve"}
	for _, arg := range step.Args {
		args = append(args, s.expandPaths(arg))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	//nolint:gosec // the Go CLI built from this package, arguments are fixed by the fixture.
	cmd := exec.CommandContext(ctx, s.serveBinary(), args...)
	cmd.Dir = s.home
	cmd.Env = s.baseEnv()
	for name, value := range s.c.Env {
		cmd.Env = append(cmd.Env, name+"="+s.expandPaths(value))
	}
	var stderr bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	observed := httpObserved{Exit: describeExit(err)}
	if ctx.Err() != nil {
		observed.Exit = "still running after 8s"
	}
	var kept []string
	for _, line := range strings.Split(stderr.String(), "\n") {
		if line == "" || strings.HasPrefix(line, "time=") || strings.HasPrefix(line, "LISTENING") {
			continue
		}
		kept = append(kept, line)
	}
	observed.Body = s.maskText(strings.Join(kept, "\n"), step.Mask)
	for _, name := range s.secrets.leaks(stderr.String()) {
		s.leaked[name] = true
	}
	return observed
}

// stream sends a request and reads a streamed (chunked) response in two
// phases: StreamLines lines first, then, after the provider gate is released
// (or the connection is dropped), the rest.
func (s *httpServer) stream(step httpStep) httpObserved {
	raw, _ := buildHTTPRequest(s.secrets, step, s.port)
	conn, tlsState, failure := s.dialStep(step.Plain, step.TLSVersion)
	if failure != "" {
		return httpObserved{Exit: failure}
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write(raw); err != nil {
		s.t.Fatal(err)
	}
	readMS := step.ReadMS
	if readMS == 0 {
		readMS = 8000
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Duration(readMS) * time.Millisecond))
	var wire bytes.Buffer
	reader := bufio.NewReaderSize(io.TeeReader(conn, &wire), 1<<20)
	defer func() {
		for _, name := range s.secrets.leaks(wire.String()) {
			s.leaked[name] = true
		}
	}()
	observed := httpObserved{TLS: tlsState()}
	line, err := reader.ReadString('\n')
	if err != nil {
		s.t.Fatalf("stream %s: status line: %v", step.Label, err)
	}
	parts := strings.SplitN(strings.TrimRight(line, "\r\n"), " ", 3)
	observed.Status, _ = strconv.Atoi(parts[1])
	if len(parts) == 3 {
		observed.Reason = parts[2]
	}
	var headers [][2]string
	chunked := false
	for {
		header, err := reader.ReadString('\n')
		if err != nil {
			s.t.Fatalf("stream %s: headers: %v", step.Label, err)
		}
		header = strings.TrimRight(header, "\r\n")
		if header == "" {
			break
		}
		name, value, _ := strings.Cut(header, ":")
		name, value = strings.ToLower(strings.TrimSpace(name)), strings.TrimSpace(value)
		headers = append(headers, [2]string{name, value})
		if name == "transfer-encoding" && strings.EqualFold(value, "chunked") {
			chunked = true
		}
	}
	observed.Headers = s.normalizeHeaders(headers, step.Mask, true)
	var body io.Reader = reader
	if chunked {
		body = httputil.NewChunkedReader(reader)
	}
	lines := bufio.NewReaderSize(body, 8<<20)
	var early strings.Builder
	for i := 0; i < step.StreamLines || step.StreamUntil != ""; i++ {
		text, err := lines.ReadString('\n')
		early.WriteString(text)
		if err != nil || (step.StreamUntil != "" && strings.Contains(text, step.StreamUntil)) || i >= 200 {
			break
		}
	}
	observed.Early = s.maskText(early.String(), step.Mask)
	if step.Abort {
		_ = conn.Close()
		observed.Closed = true
		return observed
	}
	if step.Release && s.provider != nil {
		s.provider.release()
	}
	rest, _ := io.ReadAll(lines)
	full := append([]byte(early.String()), rest...)
	s.applyCapture(full, headers)
	s.recordBody(&observed, full, step.Mask)
	observed.Closed = true
	return observed
}

// observeProvider records the requests the fake provider has received.
func (s *httpServer) observeProvider(step httpStep) httpObserved {
	if s.provider == nil {
		s.t.Fatal("provider step without a provider")
	}
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) && s.provider.cancelled() < step.Count {
		time.Sleep(20 * time.Millisecond)
	}
	return httpObserved{Body: s.maskText(s.provider.report(), step.Mask)}
}

// httpProvider is the fake loopback Ollama endpoint. The Rust replay
// implements the same script.
type httpProvider struct {
	server        *httptest.Server
	mu            sync.Mutex
	requests      []string
	cancellations int
	gate          chan struct{}
}

type httpProviderRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Stream bool   `json:"stream"`
}

func newHTTPProvider(_ *httpSecrets) *httpProvider {
	provider := &httpProvider{gate: make(chan struct{}, 8)}
	provider.server = httptest.NewServer(http.HandlerFunc(provider.serve))
	return provider
}

func (p *httpProvider) release() { p.gate <- struct{}{} }

func (p *httpProvider) cancelled() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cancellations
}

func jsonNoEscape(value any) string {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
	return strings.TrimRight(buffer.String(), "\n")
}

func (p *httpProvider) report() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out strings.Builder
	for _, line := range p.requests {
		out.WriteString(line + "\n")
	}
	fmt.Fprintf(&out, "cancellations=%d", p.cancellations)
	return out.String()
}

func (p *httpProvider) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/api/generate" {
		http.Error(w, "unexpected provider route", http.StatusNotFound)
		return
	}
	var request httpProviderRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&request); err != nil {
		http.Error(w, "invalid provider request", http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	p.requests = append(p.requests, "POST /api/generate "+jsonNoEscape(request))
	p.mu.Unlock()
	prompt := request.Prompt
	flush := func() {
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	cancelled := func() {
		p.mu.Lock()
		p.cancellations++
		p.mu.Unlock()
	}
	switch {
	case strings.Contains(prompt, "provider error input"):
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "fixture provider failure")
		return
	case strings.Contains(prompt, "disconnect provider input"):
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"response":"partial","done":false}`+"\n")
		flush()
		<-r.Context().Done()
		cancelled()
		return
	case strings.Contains(prompt, "oversized provider input"):
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"response":"`)
		_, _ = io.WriteString(w, strings.Repeat("x", 4<<20))
		_, _ = io.WriteString(w, `"}`+"\n")
		flush()
		return
	case strings.Contains(prompt, "split provider input"):
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"response":"spl`)
		flush()
		time.Sleep(150 * time.Millisecond)
		_, _ = io.WriteString(w, `it","done":false}`+"\n"+`{"response":"","done":true}`+"\n")
		flush()
		return
	case strings.Contains(prompt, "hang provider input"):
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		flush()
		<-r.Context().Done()
		cancelled()
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, `{"response":"first","done":false}`+"\n")
	flush()
	if strings.Contains(prompt, "gated provider input") {
		select {
		case <-p.gate:
		case <-r.Context().Done():
			cancelled()
			return
		case <-time.After(20 * time.Second):
		}
	}
	for _, line := range []string{
		`{"response":" second","done":false}` + "\n",
		`{"response":"","done":true}` + "\n",
	} {
		_, _ = io.WriteString(w, line)
		flush()
	}
}
