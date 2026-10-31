//go:build !windows

package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/danieljustus/symaira-desktop/internal/service"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
)

// CLI-007: real-process signal and lifecycle contract of the long-running
// commands (`events`, `mcp`, `serve`, `worker`, `sources watch`) and of the
// children they spawn. Each case starts the built Go binary in its own process
// group, waits for a readiness signal, then delivers a signal, closes stdin or
// stdout, and records the eventual state: exit status or signal, stdout,
// stderr, whether the process outlived the event, the requests a fake server
// saw and whether a spawned child survived its parent. Wall-clock durations
// are not part of the contract (each case only has a generous ceiling).
// The Rust replay is crates/symdesk-cli/tests/signals.rs.

const signalsFixturePath = "../../testdata/port/cli/signals-process.json"

const signalToken = "signal-test-token-0123456789abcdef"

type signalStep struct {
	Op     string `json:"op"`
	Text   string `json:"text,omitempty"`
	Ms     int    `json:"ms,omitempty"`
	Signal string `json:"signal,omitempty"`
	Data   string `json:"data,omitempty"`
	Name   string `json:"name,omitempty"`
	Path   string `json:"path,omitempty"`
	Count  int    `json:"count,omitempty"`
	// RustSettleMs replaces the wait for Go-only readiness text (log lines of
	// subsystems the Rust command does not start) with a fixed settle time.
	RustSettleMs int `json:"rust_settle_ms,omitempty"`
}

type signalRust struct {
	Steps    []signalStep `json:"steps"`
	ExitCode int          `json:"exit_code"`
	Signal   string       `json:"signal,omitempty"`
	Stdout   string       `json:"stdout"`
	Stderr   string       `json:"stderr"`
	Note     string       `json:"note"`
}

type signalCase struct {
	Name   string       `json:"name"`
	Args   []string     `json:"args"`
	Fake   string       `json:"fake,omitempty"`
	Env    []string     `json:"env,omitempty"`
	Stdout string       `json:"stdout_mode"`
	Setup  []string     `json:"setup,omitempty"`
	Steps  []signalStep `json:"steps"`
	Post   []string     `json:"post,omitempty"`
	// GoDefect names a Go defect the recorded behaviour contains; Rust then
	// replays the declared override instead of reproducing the defect.
	GoDefect string      `json:"go_defect,omitempty"`
	Rust     *signalRust `json:"rust,omitempty"`

	ExitCode     int               `json:"exit_code"`
	Signal       string            `json:"signal,omitempty"`
	Stdout2      string            `json:"stdout"`
	Stderr       string            `json:"stderr"`
	Observations map[string]string `json:"observations,omitempty"`
	Requests     []string          `json:"requests,omitempty"`
	PostState    map[string]string `json:"post_state,omitempty"`
}

type signalEventCase struct {
	Name string `json:"name"`
	// Setup files are written into the vault before the event is processed.
	Files map[string]string `json:"files,omitempty"`
	// Remove deletes the path before processing (a removal event).
	Remove string   `json:"remove,omitempty"`
	Mkdir  string   `json:"mkdir,omitempty"`
	Ops    []string `json:"ops"`
	Path   string   `json:"path"`

	Output string   `json:"output"`
	Listed []string `json:"listed"`
}

type signalsFixture struct {
	SchemaVersion int               `json:"schema_version"`
	NonComparable []string          `json:"non_comparable"`
	Cases         []signalCase      `json:"cases"`
	EventCases    []signalEventCase `json:"event_cases"`
}

var signalNonComparable = []string{
	"Wall-clock durations are not compared; each case only has a generous ceiling.",
	"Go slog lines (time=... level=INFO) are removed from stderr before comparison.",
	"Go `serve` drains idle and unused connections only after about five seconds; Rust drains them at once. Both exit 0.",
	"Go keeps a spawned child alive when the parent dies by a signal (no handler, no process-group kill); the recorded child_alive_after_parent_exit pins that, the harness always kills the child itself.",
	"The Rust `serve` prints a `LISTENING http://ADDR` readiness line on stderr that Go does not; it is dropped before comparison.",
	"The Rust `events` and `worker` commands do not start the Go inbox/mail watchers or the OCR engines; cases that depend on them use declared settle times and are limited to lifecycle behaviour.",
}

func signalStepSleep(ms int) signalStep { return signalStep{Op: "settle", Ms: ms} }

func signalCases() []signalCase {
	term := signalStep{Op: "signal", Signal: "TERM"}
	intr := signalStep{Op: "signal", Signal: "INT"}
	pipe := signalStep{Op: "signal", Signal: "PIPE"}
	closeStdin := signalStep{Op: "close_stdin"}
	ping := signalStep{Op: "send", Data: `{"jsonrpc":"2.0","id":1,"method":"ping"}`}
	servePort := []string{"serve", "--listen", "127.0.0.1:{port}", "--token", signalToken}
	workerArgs := func(extra ...string) []string {
		return append([]string{"worker", "--server", "{server}", "--token", signalToken, "--worker-id", "w1"}, extra...)
	}
	eventsReady := signalStep{Op: "wait_stderr", Text: "MailWatcher: starting", RustSettleMs: 1500}
	hermesEnv := []string{"SYMDESK_LLM_PROVIDER=hermes", "SYMDESK_OLLAMA_URL=http://127.0.0.1:1"}
	mcpAsk := signalStep{Op: "send_line", Data: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"desk_ask","arguments":{"query":"quarterly"}}}`}
	return []signalCase{
		{Name: "mcp_idle_sigterm", Args: []string{"mcp"}, Stdout: "pipe", Steps: []signalStep{signalStepSleep(800), term}},
		{Name: "mcp_idle_sigint", Args: []string{"mcp"}, Stdout: "pipe", Steps: []signalStep{signalStepSleep(800), intr}},
		{Name: "mcp_idle_stdin_eof_exits_zero", Args: []string{"mcp"}, Stdout: "pipe", Steps: []signalStep{signalStepSleep(500), closeStdin}},
		{Name: "mcp_answer_is_flushed_before_stdin_eof_exit", Args: []string{"mcp"}, Stdout: "pipe", Steps: []signalStep{ping, {Op: "wait_stdout", Text: `"result"`}, closeStdin}},
		{Name: "mcp_external_sigpipe_is_ignored", Args: []string{"mcp"}, Stdout: "pipe", Steps: []signalStep{signalStepSleep(800), pipe, {Op: "observe", Name: "running_after_sigpipe", Ms: 500}, closeStdin}},
		{Name: "mcp_closed_stdout_ends_with_sigpipe", Args: []string{"mcp"}, Stdout: "broken-pipe", Steps: []signalStep{signalStepSleep(800), {Op: "close_stdout_reader"}, ping, signalStepSleep(500), closeStdin}},

		{Name: "serve_sigterm_shuts_down_gracefully", Args: servePort, Stdout: "pipe", Steps: []signalStep{{Op: "wait_port"}, signalStepSleep(300), term}, Post: []string{"port_closed"}},
		{Name: "serve_sigint_shuts_down_gracefully", Args: servePort, Stdout: "pipe", Steps: []signalStep{{Op: "wait_port"}, signalStepSleep(300), intr}, Post: []string{"port_closed"}},
		{Name: "serve_stdin_eof_does_not_stop", Args: servePort, Stdout: "pipe", Steps: []signalStep{{Op: "wait_port"}, signalStepSleep(300), closeStdin, {Op: "observe", Name: "running_after_stdin_eof", Ms: 1000}, term}, Post: []string{"port_closed"}},
		{Name: "serve_external_sigpipe_is_ignored", Args: servePort, Stdout: "pipe", Steps: []signalStep{{Op: "wait_port"}, signalStepSleep(300), pipe, {Op: "observe", Name: "running_after_sigpipe", Ms: 500}, term}},
		{Name: "serve_sigterm_with_a_connection_that_sent_nothing", Args: servePort, Stdout: "pipe", Steps: []signalStep{{Op: "wait_port"}, signalStepSleep(300), {Op: "connect_idle"}, signalStepSleep(200), term}, Post: []string{"port_closed"}},
		{Name: "serve_sigterm_with_an_idle_keep_alive_connection", Args: servePort, Stdout: "pipe", Steps: []signalStep{{Op: "wait_port"}, signalStepSleep(300), {Op: "keepalive_get", Path: "/healthz", Name: "keep_alive_status"}, term}, Post: []string{"port_closed"}},

		{Name: "worker_once_without_a_job_exits_zero", Args: workerArgs("--once"), Fake: "worker-empty", Stdout: "pipe", Steps: []signalStep{{Op: "wait_exit"}}},
		{Name: "worker_idle_poll_sigterm_exits_zero", Args: workerArgs("--poll", "100ms"), Fake: "worker-empty", Stdout: "pipe", Steps: []signalStep{{Op: "wait_requests", Count: 2}, term}, Post: []string{"requests_prefix:2"}},
		{Name: "worker_idle_poll_sigint_exits_zero", Args: workerArgs("--poll", "100ms"), Fake: "worker-empty", Stdout: "pipe", Steps: []signalStep{{Op: "wait_requests", Count: 2}, intr}, Post: []string{"requests_prefix:2"}},
		{Name: "worker_sigterm_cancels_an_in_flight_lease", Args: workerArgs("--poll", "100ms"), Fake: "worker-stall", Stdout: "pipe", Steps: []signalStep{{Op: "wait_requests", Count: 1}, signalStepSleep(200), term}, Post: []string{"server_saw_client_gone"}},
		{Name: "worker_download_failure_keeps_polling_until_sigterm", Args: workerArgs("--poll", "200ms"), Fake: "worker-job404", Stdout: "pipe", Steps: []signalStep{{Op: "wait_requests", Count: 6}, term}, Post: []string{"requests_prefix:6"}},
		{Name: "worker_sigterm_stops_a_running_ocr_child", Args: workerArgs("--poll", "100ms", "--engine", "tesseract"), Fake: "worker-ocr-hold", Stdout: "pipe", Steps: []signalStep{{Op: "wait_file", Path: "tesseract.ready"}, signalStepSleep(200), term}, Post: []string{"tesseract_alive_after_parent_exit", "tesseract_args", "requests_prefix:2"}},
		{Name: "worker_ocr_result_is_reported_then_polling_continues", Args: workerArgs("--poll", "100ms", "--engine", "tesseract"), Fake: "worker-ocr-done", Stdout: "pipe", Steps: []signalStep{{Op: "wait_requests", Count: 3}, term}, Post: []string{"tesseract_args", "requests_prefix:3"}},
		{Name: "worker_unreachable_server_retries_silently_until_sigterm", Args: []string{"worker", "--server", "http://127.0.0.1:{closedport}", "--token", signalToken, "--worker-id", "w1", "--poll", "100ms"}, Stdout: "pipe", Steps: []signalStep{signalStepSleep(800), term}},
		{Name: "worker_stdin_eof_does_not_stop", Args: workerArgs("--poll", "100ms"), Fake: "worker-empty", Stdout: "pipe", Steps: []signalStep{{Op: "wait_requests", Count: 1}, closeStdin, {Op: "observe", Name: "running_after_stdin_eof", Ms: 800}, term}, Post: []string{"requests_prefix:1"}},

		{Name: "events_idle_sigterm_exits_zero", Args: []string{"events"}, Stdout: "pipe", Steps: []signalStep{eventsReady, signalStepSleep(500), term}},
		{Name: "events_idle_sigint_exits_zero", Args: []string{"events"}, Stdout: "pipe", Steps: []signalStep{eventsReady, signalStepSleep(500), intr}},
		{Name: "events_stdin_eof_exits_zero", Args: []string{"events"}, Stdout: "pipe", Steps: []signalStep{eventsReady, signalStepSleep(500), closeStdin}},
		{Name: "events_external_sigpipe_is_ignored", Args: []string{"events"}, Stdout: "pipe", Steps: []signalStep{eventsReady, signalStepSleep(500), pipe, {Op: "observe", Name: "running_after_sigpipe", Ms: 500}, term}},
		{
			Name: "events_first_vault_change", Args: []string{"events"}, Stdout: "pipe",
			Steps:    []signalStep{eventsReady, signalStepSleep(500), {Op: "write_file", Path: "vault/new.md", Data: "---\ntitle: New\n---\n\nbody\n"}, {Op: "wait_exit"}},
			GoDefect: "cmd/symdesk/events.go unlocks the debounce mutex without holding it, so the first filesystem event ends the Go process with `fatal error: sync: unlock of unlocked mutex` (exit 2)",
			Rust: &signalRust{
				Steps:    []signalStep{{Op: "settle", Ms: 1500}, {Op: "write_file", Path: "vault/new.md", Data: "---\ntitle: New\n---\n\nbody\n"}, {Op: "wait_stdout", Text: `"event":"index_updated"`}, term},
				ExitCode: 0,
				Stdout:   `{"event":"index_updated","path":"<ROOT>/vault/new.md","ts":"<TS>"}` + "\n",
				Stderr:   "",
				Note:     "Rust emits the NDJSON line the event processor produces (see event_cases) and then shuts down on SIGTERM instead of crashing.",
			},
		},

		{Name: "sources_watch_sigterm_ends_by_the_signal", Args: []string{"sources", "watch", "{ext}"}, Stdout: "pipe", Setup: []string{"sources-add"}, Steps: []signalStep{{Op: "wait_stderr", Text: "Watching 1 files in"}, signalStepSleep(300), term}},
		{Name: "sources_watch_sigint_ends_by_the_signal", Args: []string{"sources", "watch", "{ext}"}, Stdout: "pipe", Setup: []string{"sources-add"}, Steps: []signalStep{{Op: "wait_stderr", Text: "Watching 1 files in"}, signalStepSleep(300), intr}},
		{Name: "sources_watch_stdin_eof_does_not_stop", Args: []string{"sources", "watch", "{ext}"}, Stdout: "pipe", Setup: []string{"sources-add"}, Steps: []signalStep{{Op: "wait_stderr", Text: "Watching 1 files in"}, signalStepSleep(300), closeStdin, {Op: "observe", Name: "running_after_stdin_eof", Ms: 1000}, term}},
		{Name: "sources_watch_external_sigpipe_is_ignored", Args: []string{"sources", "watch", "{ext}"}, Stdout: "pipe", Setup: []string{"sources-add"}, Steps: []signalStep{{Op: "wait_stderr", Text: "Watching 1 files in"}, signalStepSleep(300), pipe, {Op: "observe", Name: "running_after_sigpipe", Ms: 500}, term}},
		{
			Name: "sources_watch_indexes_changes_until_sigterm", Args: []string{"sources", "watch", "{ext}"}, Stdout: "pipe", Setup: []string{"sources-add"},
			Steps: []signalStep{
				{Op: "wait_stderr", Text: "Watching 1 files in"}, signalStepSleep(500),
				{Op: "write_file", Path: "ext/y.md", Data: "# Y\nnew file quarterly\n"},
				{Op: "wait_stderr", Text: "Watch: indexed=1 removed=0"},
				{Op: "remove_file", Path: "ext/x.md"},
				{Op: "wait_stderr", Text: "Watch: indexed=0 removed=1"},
				term,
			},
			Post: []string{"search:quarterly"},
		},

		{Name: "ask_hermes_sigterm_leaves_the_child_running", Args: []string{"--json", "ask", "quarterly"}, Fake: "hermes-hold", Env: hermesEnv, Stdout: "pipe", Setup: []string{"index"}, Steps: []signalStep{{Op: "wait_file", Path: "hermes.ready"}, signalStepSleep(200), term}, Post: []string{"child_alive_after_parent_exit"}},
		{Name: "ask_hermes_sigint_leaves_the_child_running", Args: []string{"--json", "ask", "quarterly"}, Fake: "hermes-hold", Env: hermesEnv, Stdout: "pipe", Setup: []string{"index"}, Steps: []signalStep{{Op: "wait_file", Path: "hermes.ready"}, signalStepSleep(200), intr}, Post: []string{"child_alive_after_parent_exit"}},
		{Name: "ask_hermes_stdin_eof_does_not_stop", Args: []string{"--json", "ask", "quarterly"}, Fake: "hermes-hold", Env: hermesEnv, Stdout: "pipe", Setup: []string{"index"}, Steps: []signalStep{{Op: "wait_file", Path: "hermes.ready"}, signalStepSleep(200), closeStdin, {Op: "observe", Name: "running_after_stdin_eof", Ms: 800}, term}, Post: []string{"child_alive_after_parent_exit"}},
		{Name: "mcp_desk_ask_hermes_sigterm_leaves_the_child_running", Args: []string{"mcp"}, Fake: "hermes-hold", Env: hermesEnv, Stdout: "pipe", Setup: []string{"index"}, Steps: []signalStep{mcpAsk, {Op: "wait_file", Path: "hermes.ready"}, signalStepSleep(200), term}, Post: []string{"child_alive_after_parent_exit"}},
		{Name: "ask_hermes_completion_reaps_the_child", Args: []string{"--json", "ask", "quarterly"}, Fake: "hermes-ok", Env: hermesEnv, Stdout: "pipe", Setup: []string{"index"}, Steps: []signalStep{{Op: "wait_exit"}}, Post: []string{"child_alive_after_parent_exit"}},
	}
}

func signalEventCases() []signalEventCase {
	md := "---\ntitle: Test Note\n---\n\nBody content\n"
	return []signalEventCase{
		{Name: "create_md", Files: map[string]string{"note.md": md}, Ops: []string{"create"}, Path: "note.md"},
		{Name: "write_md", Files: map[string]string{"note.md": md}, Ops: []string{"write"}, Path: "note.md"},
		{Name: "create_then_write_stays_create", Files: map[string]string{"note.md": md}, Ops: []string{"create", "write"}, Path: "note.md"},
		{Name: "remove_md", Files: map[string]string{"note.md": md}, Remove: "note.md", Ops: []string{"remove"}, Path: "note.md"},
		{Name: "rename_md", Files: map[string]string{"note.md": md}, Ops: []string{"rename"}, Path: "note.md"},
		{Name: "chmod_only_md", Files: map[string]string{"note.md": md}, Ops: []string{"chmod"}, Path: "note.md"},
		{Name: "create_txt_is_silent", Files: map[string]string{"readme.txt": "hello"}, Ops: []string{"create"}, Path: "readme.txt"},
		{Name: "create_directory_is_silent", Mkdir: "subdir", Ops: []string{"create"}, Path: "subdir"},
		{Name: "md_without_frontmatter", Files: map[string]string{"plain.md": "# Plain\n\nJust text\n"}, Ops: []string{"create"}, Path: "plain.md"},
		{Name: "md_with_unparseable_frontmatter_is_not_indexed", Files: map[string]string{"bad.md": "---\ntitle: [unclosed\n---\n\nbody\n"}, Ops: []string{"create"}, Path: "bad.md"},
		{Name: "write_to_a_missing_md_is_not_indexed", Ops: []string{"write"}, Path: "gone.md"},
	}
}

type signalEnv struct {
	root, home, bin, vault, ext string
	port, closedPort            int
	token                       string
}

func newSignalEnv(t *testing.T, c signalCase) *signalEnv {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &signalEnv{root: root, home: filepath.Join(root, "home"), bin: filepath.Join(root, "bin"), vault: filepath.Join(root, "vault"), ext: filepath.Join(root, "ext"), token: signalToken}
	for _, dir := range []string{e.home, e.bin, e.vault, e.ext, filepath.Join(e.home, ".config", "symseek")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeSignalFile(t, filepath.Join(e.home, ".config", "symseek", "config.toml"), "retry_count = 1\nretry_backoff_ms = 1\n", 0o600)
	stamp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for path, content := range map[string]string{
		filepath.Join(e.vault, "a.md"): "# A\nhello quarterly\n",
		filepath.Join(e.ext, "x.md"):   "# X\nhello\n",
	} {
		writeSignalFile(t, path, content, 0o600)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	e.port, e.closedPort = freeSignalPort(t), freeSignalPort(t)
	switch c.Fake {
	case "hermes-hold":
		writeSignalFile(t, filepath.Join(e.bin, "hermes"), "#!/bin/sh\necho $$ > "+filepath.Join(e.root, "hermes.pid")+"\n: > "+filepath.Join(e.root, "hermes.ready")+"\nexec sleep 30\n", 0o700)
	case "worker-ocr-hold":
		writeSignalFile(t, filepath.Join(e.bin, "tesseract"), fakeTesseract(e.root, "exec sleep 300"), 0o700)
	case "worker-ocr-done":
		writeSignalFile(t, filepath.Join(e.bin, "tesseract"), fakeTesseract(e.root, "printf 'hello ocr\\n'"), 0o700)
	case "hermes-ok":
		writeSignalFile(t, filepath.Join(e.bin, "hermes"), "#!/bin/sh\necho $$ > "+filepath.Join(e.root, "hermes.pid")+"\nprintf 'Answer [[a.md]]\\n'\n", 0o700)
	}
	return e
}

// fakeTesseract answers the language probe and records its arguments, then
// either holds until killed or prints a result.
func fakeTesseract(root, action string) string {
	return "#!/bin/sh\ncase \"$1\" in --list-langs) printf 'List of available languages (3):\\ndeu\\neng\\nosd\\n'; exit 0;; esac\n" +
		"echo \"$@\" >> " + filepath.Join(root, "tesseract.args") + "\n" +
		"echo $$ > " + filepath.Join(root, "tesseract.pid") + "\n" +
		": > " + filepath.Join(root, "tesseract.ready") + "\n" + action + "\n"
}

// signalPNG is a 1x1 PNG the fake server hands to the worker as job input.
var signalPNG = mustDecodeBase64("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")

func mustDecodeBase64(text string) []byte {
	data, err := base64.StdEncoding.DecodeString(text)
	if err != nil {
		panic(err)
	}
	return data
}

func writeSignalFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil { //nolint:gosec // test-owned scratch tree
		t.Fatal(err)
	}
}

func freeSignalPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func (e *signalEnv) environment(extra []string) []string {
	env := []string{
		"PATH=" + e.bin + ":/usr/bin:/bin", "HOME=" + e.home, "TZ=UTC",
		"XDG_CONFIG_HOME=" + filepath.Join(e.home, "config"),
		"XDG_CACHE_HOME=" + filepath.Join(e.home, "cache"),
		"XDG_DATA_HOME=" + filepath.Join(e.home, "data"),
		"SYMDESK_OLLAMA_URL=http://127.0.0.1:1",
	}
	return append(env, extra...)
}

func (e *signalEnv) substitute(value, server string) string {
	value = strings.ReplaceAll(value, "{ext}", e.ext)
	value = strings.ReplaceAll(value, "{port}", strconv.Itoa(e.port))
	value = strings.ReplaceAll(value, "{closedport}", strconv.Itoa(e.closedPort))
	return strings.ReplaceAll(value, "{server}", server)
}

// signalServer is the fake SymDesk server a worker talks to.
type signalServer struct {
	mu         sync.Mutex
	requests   []string
	clientGone bool
	server     *httptest.Server
}

func newSignalServer(mode string) *signalServer {
	s := &signalServer{}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		auth := "none"
		if r.Header.Get("Authorization") == "Bearer "+signalToken {
			auth = "bearer"
		}
		s.mu.Lock()
		s.requests = append(s.requests, fmt.Sprintf("%s %s auth=%s body=%s", r.Method, r.URL.RequestURI(), auth, strings.TrimSpace(string(body))))
		s.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/lease"):
			switch mode {
			case "worker-stall":
				select {
				case <-r.Context().Done():
					s.mu.Lock()
					s.clientGone = true
					s.mu.Unlock()
				case <-time.After(30 * time.Second):
				}
			case "worker-ocr-hold", "worker-ocr-done":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"job1","original_name":"scan.png","content_type":"image/png","capability":"ocr"}`))
			case "worker-job404":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"job1","original_name":"a.png","content_type":"image/png","capability":"ocr"}`))
			default:
				w.WriteHeader(http.StatusNoContent)
			}
		case r.Method == http.MethodGet && strings.HasPrefix(mode, "worker-ocr-"):
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(signalPNG)
		case r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"input missing"}`))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	return s
}

func (s *signalServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func (s *signalServer) snapshot() ([]string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...), s.clientGone
}

type lockedSignalBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedSignalBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedSignalBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func signalWaitFor(limit time.Duration, ok func() bool) bool {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if ok() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return ok()
}

var (
	signalLogLine   = regexp.MustCompile(`(?m)^time=\S+ level=INFO .*\n`)
	signalScoreMask = regexp.MustCompile(`"score":[0-9][0-9.e+-]*`)
	signalTSMask    = regexp.MustCompile(`"ts":"[^"]*"`)
	signalFatalLine = regexp.MustCompile(`(?s)^.*?(fatal error: [^\n]*\n).*$`)
)

func normalizeSignalText(text string, e *signalEnv, server string) string {
	text = signalLogLine.ReplaceAllString(text, "")
	if strings.Contains(text, "fatal error: ") {
		text = signalFatalLine.ReplaceAllString(text, "$1")
	}
	text = strings.ReplaceAll(text, e.root, "<ROOT>")
	if server != "" {
		text = strings.ReplaceAll(text, strings.TrimPrefix(server, "http://"), "<SERVER>")
	}
	text = strings.ReplaceAll(text, "127.0.0.1:"+strconv.Itoa(e.port), "127.0.0.1:<PORT>")
	text = signalScoreMask.ReplaceAllString(text, `"score":"<SCORE>"`)
	return signalTSMask.ReplaceAllString(text, `"ts":"<TS>"`)
}

func signalName(sig syscall.Signal) string {
	switch sig {
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGPIPE:
		return "SIGPIPE"
	case syscall.SIGKILL:
		return "SIGKILL"
	}
	return sig.String()
}

func signalProcessRunning(pid int) bool {
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		if end := bytes.LastIndexByte(data, ')'); end >= 0 && end+2 < len(data) {
			return data[end+2] != 'Z'
		}
	}
	return syscall.Kill(pid, 0) == nil
}

func readSignalPID(path string) int {
	data, err := os.ReadFile(path) //nolint:gosec // test-owned scratch file
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return pid
}

func framedSignalBody(body string) []byte {
	return append([]byte(fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))), body...)
}

// observeSignalCase runs one case against the Go binary and fills in the
// recorded expectation.
func observeSignalCase(t *testing.T, binary string, c signalCase) signalCase {
	t.Helper()
	e := newSignalEnv(t, c)
	var fake *signalServer
	server := ""
	if strings.HasPrefix(c.Fake, "worker-") {
		fake = newSignalServer(c.Fake)
		defer fake.server.Close()
		server = fake.server.URL
	}
	environment := e.environment(c.Env)
	for _, setup := range c.Setup {
		var argv []string
		switch setup {
		case "index":
			argv = []string{"--vault", e.vault, "index"}
		case "sources-add":
			argv = []string{"--vault", e.vault, "sources", "add", e.ext}
		default:
			t.Fatalf("unknown setup %q", setup)
		}
		cmd := exec.Command(binary, argv...) //nolint:gosec // test-built CLI with controlled arguments
		cmd.Dir, cmd.Env = e.root, environment
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: setup %s: %v\n%s", c.Name, setup, err, out)
		}
	}
	argv := []string{"--vault", e.vault}
	for _, arg := range c.Args {
		argv = append(argv, e.substitute(arg, server))
	}
	cmd := exec.Command(binary, argv...) //nolint:gosec // test-built CLI with controlled arguments
	cmd.Dir, cmd.Env = e.root, environment
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, stderr := &lockedSignalBuffer{}, &lockedSignalBuffer{}
	cmd.Stderr = stderr
	var stdoutReader, stdoutWriter *os.File
	switch c.Stdout {
	case "pipe":
		cmd.Stdout = stdout
	case "broken-pipe":
		var err error
		if stdoutReader, stdoutWriter, err = os.Pipe(); err != nil {
			t.Fatal(err)
		}
		cmd.Stdout = stdoutWriter
	default:
		t.Fatalf("unknown stdout mode %q", c.Stdout)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("%s: start: %v", c.Name, err)
	}
	pgid := cmd.Process.Pid
	defer func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) }()
	if stdoutWriter != nil {
		_ = stdoutWriter.Close()
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	var waitErr error
	exited := false
	finish := func(limit time.Duration) bool {
		if exited {
			return true
		}
		select {
		case waitErr = <-waited:
			exited = true
			return true
		case <-time.After(limit):
			return false
		}
	}
	hold := []net.Conn{}
	defer func() {
		for _, conn := range hold {
			_ = conn.Close()
		}
	}()
	c.Observations = map[string]string{}
	stdinClosed := false
	for _, step := range c.Steps {
		switch step.Op {
		case "settle":
			time.Sleep(time.Duration(step.Ms) * time.Millisecond)
		case "wait_stderr":
			if !signalWaitFor(30*time.Second, func() bool { return strings.Contains(stderr.String(), step.Text) }) {
				t.Fatalf("%s: stderr never contained %q: %q", c.Name, step.Text, stderr.String())
			}
		case "wait_stdout":
			if !signalWaitFor(30*time.Second, func() bool { return strings.Contains(stdout.String(), step.Text) }) {
				t.Fatalf("%s: stdout never contained %q: %q", c.Name, step.Text, stdout.String())
			}
		case "wait_port":
			if !signalWaitFor(30*time.Second, func() bool {
				conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", e.port), 200*time.Millisecond)
				if err == nil {
					_ = conn.Close()
				}
				return err == nil
			}) {
				t.Fatalf("%s: port %d never accepted a connection; stderr=%q", c.Name, e.port, stderr.String())
			}
		case "wait_file":
			if !signalWaitFor(30*time.Second, func() bool { _, err := os.Stat(filepath.Join(e.root, step.Path)); return err == nil }) {
				t.Fatalf("%s: %s never appeared", c.Name, step.Path)
			}
		case "wait_requests":
			if !signalWaitFor(30*time.Second, func() bool { return fake.count() >= step.Count }) {
				t.Fatalf("%s: fake server saw %d requests, want %d", c.Name, fake.count(), step.Count)
			}
		case "wait_exit":
			if !finish(60 * time.Second) {
				t.Fatalf("%s: process did not exit", c.Name)
			}
		case "signal":
			sig := map[string]syscall.Signal{"TERM": syscall.SIGTERM, "INT": syscall.SIGINT, "PIPE": syscall.SIGPIPE}[step.Signal]
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatalf("%s: signal: %v", c.Name, err)
			}
		case "close_stdin":
			_ = stdin.Close()
			stdinClosed = true
		case "close_stdout_reader":
			_ = stdoutReader.Close()
		case "send":
			_, _ = stdin.Write(framedSignalBody(step.Data))
		case "send_line":
			_, _ = stdin.Write([]byte(step.Data + "\n"))
		case "observe":
			time.Sleep(time.Duration(step.Ms) * time.Millisecond)
			state := "running"
			if finish(time.Millisecond) {
				state = "exited"
			}
			c.Observations[step.Name] = state
		case "connect_idle":
			conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", e.port))
			if err != nil {
				t.Fatalf("%s: connect: %v", c.Name, err)
			}
			hold = append(hold, conn)
		case "keepalive_get":
			conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", e.port))
			if err != nil {
				t.Fatalf("%s: connect: %v", c.Name, err)
			}
			hold = append(hold, conn)
			_, _ = conn.Write([]byte("GET " + step.Path + " HTTP/1.1\r\nHost: x\r\n\r\n"))
			buf := make([]byte, 64)
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			n, _ := conn.Read(buf)
			line, _, _ := strings.Cut(string(buf[:n]), "\r\n")
			c.Observations[step.Name] = line
		case "write_file":
			writeSignalFile(t, filepath.Join(e.root, step.Path), step.Data, 0o600)
		case "remove_file":
			if err := os.Remove(filepath.Join(e.root, step.Path)); err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatalf("%s: unknown step %q", c.Name, step.Op)
		}
	}
	if !stdinClosed {
		defer func() { _ = stdin.Close() }()
	}
	if !finish(60 * time.Second) {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		t.Fatalf("%s: process did not end; stderr=%q", c.Name, stderr.String())
	}
	if stdoutReader != nil {
		_ = stdoutReader.Close()
	}
	time.Sleep(150 * time.Millisecond)

	c.ExitCode, c.Signal = 0, ""
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			c.ExitCode, c.Signal = -1, signalName(status.Signal())
		} else {
			c.ExitCode = exitErr.ExitCode()
		}
	} else if waitErr != nil {
		t.Fatalf("%s: wait: %v", c.Name, waitErr)
	}
	c.Stdout2 = normalizeSignalText(stdout.String(), e, server)
	c.Stderr = normalizeSignalText(stderr.String(), e, server)
	c.PostState = map[string]string{}
	for _, post := range c.Post {
		switch {
		case post == "port_closed":
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", e.port), 300*time.Millisecond)
			if err == nil {
				_ = conn.Close()
			}
			c.PostState[post] = strconv.FormatBool(err != nil)
		case post == "server_saw_client_gone":
			signalWaitFor(5*time.Second, func() bool { _, gone := fake.snapshot(); return gone })
			_, gone := fake.snapshot()
			c.PostState[post] = strconv.FormatBool(gone)
		case post == "tesseract_alive_after_parent_exit":
			pid := readSignalPID(filepath.Join(e.root, "tesseract.pid"))
			c.PostState[post] = strconv.FormatBool(pid > 0 && signalProcessRunning(pid))
			if pid > 0 {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		case post == "tesseract_args":
			data, _ := os.ReadFile(filepath.Join(e.root, "tesseract.args")) //nolint:gosec // test-owned scratch file
			first, _, _ := strings.Cut(string(data), "\n")
			c.PostState[post] = first
		case post == "child_alive_after_parent_exit":
			pid := readSignalPID(filepath.Join(e.root, "hermes.pid"))
			alive := pid > 0 && signalProcessRunning(pid)
			c.PostState[post] = strconv.FormatBool(alive)
			if pid > 0 {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		case strings.HasPrefix(post, "search:"):
			search := exec.Command(binary, "--vault", e.vault, "--json", "search", strings.TrimPrefix(post, "search:")) //nolint:gosec // test-built CLI with controlled arguments
			search.Dir, search.Env = e.root, environment
			out, err := search.Output()
			if err != nil {
				t.Fatalf("%s: post search: %v", c.Name, err)
			}
			c.PostState[post] = normalizeSignalText(string(out), e, server)
		case strings.HasPrefix(post, "requests_prefix:"):
			// handled below
		default:
			t.Fatalf("%s: unknown post check %q", c.Name, post)
		}
	}
	if fake != nil {
		requests, _ := fake.snapshot()
		// Polling workers send a timing-dependent number of requests; cases
		// pin only the deterministic prefix.
		limit := len(requests)
		for _, post := range c.Post {
			if rest, ok := strings.CutPrefix(post, "requests_prefix:"); ok {
				limit, _ = strconv.Atoi(rest)
			}
		}
		if len(requests) < limit {
			t.Fatalf("%s: fake server saw %d requests, want at least %d", c.Name, len(requests), limit)
		}
		c.Requests = requests[:limit]
		for i := range c.Requests {
			c.Requests[i] = normalizeSignalText(c.Requests[i], e, server)
		}
	}
	if len(c.PostState) == 0 {
		c.PostState = nil
	}
	if len(c.Observations) == 0 {
		c.Observations = nil
	}
	return c
}

func observeSignalEventCase(t *testing.T, c signalEventCase) signalEventCase {
	t.Helper()
	vaultPath, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	db, err := sidecar.Open(filepath.Join(t.TempDir(), "sidecar.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	svc := service.New(vaultPath, db)
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = watcher.Close() }()
	for name, content := range c.Files {
		writeSignalFile(t, filepath.Join(vaultPath, name), content, 0o600)
	}
	if c.Mkdir != "" {
		if err := os.Mkdir(filepath.Join(vaultPath, c.Mkdir), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if c.Remove != "" {
		if err := os.Remove(filepath.Join(vaultPath, c.Remove)); err != nil {
			t.Fatal(err)
		}
	}
	var op fsnotify.Op
	for _, name := range c.Ops {
		op |= map[string]fsnotify.Op{"create": fsnotify.Create, "write": fsnotify.Write, "remove": fsnotify.Remove, "rename": fsnotify.Rename, "chmod": fsnotify.Chmod}[name]
	}
	var out bytes.Buffer
	processEvent(filepath.Join(vaultPath, c.Path), &DebouncedEvent{Op: op, Ts: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}, watcher, svc, &out)
	c.Output = strings.ReplaceAll(out.String(), vaultPath, "<VAULT>")
	docs, err := db.ListFiles("")
	if err != nil {
		t.Fatal(err)
	}
	c.Listed = []string{}
	for _, doc := range docs {
		c.Listed = append(c.Listed, strings.ReplaceAll(doc.Path, vaultPath, "<VAULT>")+" | "+doc.Title)
	}
	sort.Strings(c.Listed)
	return c
}

func TestPortSignalsProcessContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signal contract is POSIX-only")
	}
	binary := processFixtureBinary(t)
	fixture := signalsFixture{SchemaVersion: 1, NonComparable: signalNonComparable}
	for _, c := range signalCases() {
		fixture.Cases = append(fixture.Cases, observeSignalCase(t, binary, c))
	}
	for _, c := range signalEventCases() {
		fixture.EventCases = append(fixture.EventCases, observeSignalEventCase(t, c))
	}
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source path unavailable")
	}
	path := filepath.Join(filepath.Dir(source), signalsFixturePath)
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	current, err := os.ReadFile(path) //nolint:gosec // fixed repository fixture path
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, encoded) {
		portProcessFixtureDifference(t, "signals fixture is stale; regenerate with make cli-signals-fixtures-generate", current, encoded)
	}
}
