package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

// MCP-004 / MCP-005: the real `symroom mcp` process. In-process fixtures cannot
// show how the server ends (exit status or signal, stderr), what a failed
// stdout write does to a waiting tool, or how many calls it runs at once, so
// these cases drive the built Go binary through pipes and record the result.
const mcpProcessContractPath = "testdata/port/room/mcp-process.json"

type mcpProcessStep struct {
	Op      string         `json:"op"`
	Data    string         `json:"data,omitempty"`
	Ms      int            `json:"ms,omitempty"`
	ID      int            `json:"id,omitempty"`
	Method  string         `json:"method,omitempty"`
	Tool    string         `json:"tool,omitempty"`
	Args    map[string]any `json:"args,omitempty"`
	Count   int            `json:"count,omitempty"`
	CLIArgs []string       `json:"cli_args,omitempty"`
	Signal  string         `json:"signal,omitempty"`
}

type mcpProcessCase struct {
	Name       string           `json:"name"`
	UnixOnly   bool             `json:"unix_only,omitempty"`
	Stdout     string           `json:"stdout_mode"`
	Setup      [][]string       `json:"setup,omitempty"`
	SetupOut   []string         `json:"setup_stdout,omitempty"`
	RunID      string           `json:"run_id,omitempty"`
	Steps      []mcpProcessStep `json:"steps"`
	Unordered  bool             `json:"unordered,omitempty"`
	MinMs      int              `json:"min_ms"`
	MaxMs      int              `json:"max_ms"`
	ExitCode   int              `json:"exit_code"`
	Signal     string           `json:"signal,omitempty"`
	Stderr     string           `json:"stderr"`
	FirstID    *int             `json:"first_id,omitempty"`
	Frames     []string         `json:"frames"`
	CapSkewIDs []int            `json:"cap_sensitive_ids,omitempty"`
}

type mcpProcessContract struct {
	SchemaVersion  int               `json:"schema_version"`
	OracleRevision string            `json:"oracle_revision"`
	SourceHashes   map[string]string `json:"source_hashes"`
	IdentityKey    string            `json:"identity_key"`
	Cases          []mcpProcessCase  `json:"cases"`
}

func mcpProcessCases() []mcpProcessCase {
	wait := func(id int, seconds float64) mcpProcessStep {
		return mcpProcessStep{Op: "call", ID: id, Tool: "room_run_wait", Args: map[string]any{"run_id": "run_missing", "timeout_seconds": seconds}}
	}
	ping := func(id int) mcpProcessStep { return mcpProcessStep{Op: "request", ID: id, Method: "ping"} }
	sleep := func(ms int) mcpProcessStep { return mcpProcessStep{Op: "sleep", Ms: ms} }
	closeStdin := mcpProcessStep{Op: "close_stdin"}
	waits := func(first, count int, seconds float64) mcpProcessStep {
		return mcpProcessStep{Op: "calls", ID: first, Count: count, Tool: "room_run_wait", Args: map[string]any{"run_id": "run_missing", "timeout_seconds": seconds}}
	}
	return []mcpProcessCase{
		{Name: "stdin-closed-before-any-request", Stdout: "pipe", Steps: []mcpProcessStep{closeStdin}, MaxMs: 20000},
		{Name: "eof-lets-an-in-flight-wait-finish", Stdout: "pipe", Steps: []mcpProcessStep{wait(1, 0.8), sleep(150), closeStdin}, MinMs: 700, MaxMs: 20000},
		{
			Name: "eof-lets-an-in-flight-wait-see-a-later-approval", Stdout: "pipe",
			Setup: [][]string{{"run", "request", "--title", "t", "--identity", "x"}},
			Steps: []mcpProcessStep{
				{Op: "call", ID: 1, Tool: "room_run_wait", Args: map[string]any{"run_id": "{run_id}", "timeout_seconds": 20}},
				sleep(200), closeStdin, sleep(300),
				{Op: "cli", CLIArgs: []string{"run", "approve", "--identity", "x", "{run_id}"}},
			},
			MaxMs: 15000,
		},
		{
			Name: "cancelled-notification-does-not-cancel-a-wait", Stdout: "pipe",
			Steps: []mcpProcessStep{
				wait(1, 0.8),
				{Op: "send", Data: `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1,"reason":"user"}}`},
				sleep(100), closeStdin,
			},
			MinMs: 700, MaxMs: 20000,
		},
		{
			Name: "cancelled-with-an-id-is-an-unknown-method", Stdout: "pipe",
			Steps: []mcpProcessStep{
				wait(1, 0.6),
				{Op: "request", ID: 2, Method: "notifications/cancelled"},
				sleep(100), closeStdin,
			},
			MinMs: 500, MaxMs: 20000,
		},
		{
			Name: "truncated-frame-cancels-a-wait", Stdout: "pipe",
			Steps: []mcpProcessStep{
				wait(1, 30), sleep(300),
				{Op: "send", Data: "Content-Length: 500\r\n\r\n{\"jsonrpc\""},
				closeStdin,
			},
			MaxMs: 10000,
		},
		{
			Name: "invalid-content-length-cancels-a-wait", Stdout: "pipe",
			Steps: []mcpProcessStep{
				wait(1, 30), sleep(300),
				{Op: "send", Data: "Content-Length: abc\r\n\r\n"},
				sleep(300), closeStdin,
			},
			MaxMs: 10000,
		},
		{
			Name: "oversized-frame-cancels-a-wait", Stdout: "pipe",
			Steps: []mcpProcessStep{
				wait(1, 30), sleep(300),
				{Op: "send", Data: "Content-Length: 1048577\r\n\r\n"},
				sleep(300), closeStdin,
			},
			MaxMs: 10000,
		},
		{
			Name: "failed-response-write-cancels-a-wait", UnixOnly: true, Stdout: "readonly",
			Steps: []mcpProcessStep{wait(1, 30), sleep(300), ping(2), sleep(1200), closeStdin},
			MaxMs: 12000,
		},
		{
			Name: "failed-tool-response-write-cancels-a-wait", UnixOnly: true, Stdout: "readonly",
			Steps: []mcpProcessStep{wait(1, 30), wait(2, 0.3), sleep(1500), closeStdin},
			MaxMs: 12000,
		},
		{
			Name: "broken-pipe-ends-with-sigpipe", UnixOnly: true, Stdout: "broken-pipe",
			Steps: []mcpProcessStep{wait(1, 30), sleep(300), {Op: "close_stdout_reader"}, ping(2), sleep(1500), closeStdin},
			MaxMs: 12000,
		},
		{
			Name: "sigterm-ends-a-waiting-server", UnixOnly: true, Stdout: "pipe",
			Steps: []mcpProcessStep{wait(1, 30), sleep(300), {Op: "signal", Signal: "TERM"}},
			MaxMs: 10000,
		},
		{
			Name: "sigint-ends-a-waiting-server", UnixOnly: true, Stdout: "pipe",
			Steps: []mcpProcessStep{wait(1, 30), sleep(300), {Op: "signal", Signal: "INT"}},
			MaxMs: 10000,
		},
		{
			Name: "one-hundred-concurrent-waits-answer-ping-first", Stdout: "pipe", Unordered: true,
			Steps: []mcpProcessStep{waits(1, 100, 1.5), ping(101), sleep(200), closeStdin},
			MinMs: 1400, MaxMs: 20000,
		},
		{
			// Above the Rust worker cap (512) Go still runs every call at once:
			// the ping and the idle tool call answer before any wait times out.
			Name: "six-hundred-concurrent-waits-answer-ping-first", Stdout: "pipe", Unordered: true,
			Steps: []mcpProcessStep{
				waits(1, 600, 3),
				ping(601),
				{Op: "call", ID: 602, Tool: "room_artifact_list", Args: map[string]any{}},
				sleep(200), closeStdin,
			},
			// The 5 s ceiling also fails a queue that restarts each waiting
			// call's deadline when a worker frees up (about 6 s).
			MinMs: 2900, MaxMs: 5000,
			// Rust queues calls beyond its worker cap, so its answer to call 602
			// follows the first wait that finishes (see MCP-004 in the matrix).
			CapSkewIDs: []int{602},
		},
	}
}

func mcpFrame(body []byte) []byte {
	return append([]byte(fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))), body...)
}

func mcpRequestBody(id int, method string, params any) []byte {
	request := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		request["params"] = params
	}
	body, err := json.Marshal(request)
	if err != nil {
		panic(err)
	}
	return body
}

func mcpSubstitute(value string, runID string) string {
	return strings.ReplaceAll(value, "{run_id}", runID)
}

type mcpProcessEnv struct {
	dir  string
	env  []string
	room string
}

func newMCPProcessEnv(t *testing.T, binary, key, name string) mcpProcessEnv {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	home, tmp := filepath.Join(dir, "home"), filepath.Join(dir, "tmp")
	for _, path := range []string{home, tmp} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	env := []string{
		"HOME=" + home, "USERPROFILE=" + home, "XDG_DATA_HOME=" + filepath.Join(dir, "xdg"),
		"TMPDIR=" + tmp, "TMP=" + tmp, "TEMP=" + tmp, "TZ=UTC",
		"SYMROOM_IDENTITY_KEY=" + key, "PATH=" + os.Getenv("PATH"),
	}
	if root := os.Getenv("SystemRoot"); root != "" {
		env = append(env, "SystemRoot="+root)
	}
	room := filepath.Join(dir, "room")
	init := exec.Command(binary, "init", "--name", "T", "--identity", "x", room) //nolint:gosec // test-only fixed helper
	init.Env = env
	if output, err := init.CombinedOutput(); err != nil {
		t.Fatalf("init room: %v\n%s", err, output)
	}
	return mcpProcessEnv{dir: dir, env: env, room: room}
}

// mcpProcessResult is what one run of `symroom mcp` produced, normalised.
type mcpProcessResult struct {
	setupOut []string
	runID    string
	exitCode int
	signal   string
	stderr   string
	frames   []string
	arrival  []string
	elapsed  time.Duration
}

var (
	mcpApprovalMask = regexp.MustCompile(`(\\"approval_id\\":\\")app_[0-9a-f]+`)
	mcpExpiresMask  = regexp.MustCompile(`(\\"expires_at\\":\\")[^\\]*`)
)

func maskMCPFrame(body string) string {
	body = mcpApprovalMask.ReplaceAllString(body, "${1}<approval>")
	return mcpExpiresMask.ReplaceAllString(body, "${1}<expires>")
}

// parseMCPFrames splits Content-Length framed output; any other byte is an error.
func parseMCPFrames(data []byte) ([]string, error) {
	var frames []string
	for len(data) > 0 {
		const prefix = "Content-Length: "
		if !bytes.HasPrefix(data, []byte(prefix)) {
			return nil, fmt.Errorf("stdout is not a frame: %q", data)
		}
		end := bytes.Index(data, []byte("\r\n\r\n"))
		if end < 0 {
			return nil, errors.New("frame header not terminated")
		}
		length, err := strconv.Atoi(string(data[len(prefix):end]))
		if err != nil || length < 0 || end+4+length > len(data) {
			return nil, fmt.Errorf("bad frame length %q", data[len(prefix):end])
		}
		frames = append(frames, string(data[end+4:end+4+length]))
		data = data[end+4+length:]
	}
	return frames, nil
}

func mcpFrameID(frame string) int {
	var decoded struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal([]byte(frame), &decoded); err != nil {
		return -1
	}
	id, err := strconv.Atoi(string(decoded.ID))
	if err != nil {
		return -1
	}
	return id
}

func runMCPProcessCase(t *testing.T, binary, key string, c mcpProcessCase) mcpProcessResult {
	t.Helper()
	env := newMCPProcessEnv(t, binary, key, c.Name)
	result := mcpProcessResult{}
	runID := ""
	for _, argv := range c.Setup {
		cmd := exec.Command(binary, argv...) //nolint:gosec // test-only fixed helper
		cmd.Dir, cmd.Env = env.room, env.env
		output, err := cmd.Output()
		if err != nil {
			t.Fatalf("setup %v: %v", argv, err)
		}
		result.setupOut = append(result.setupOut, string(output))
		if len(argv) >= 2 && argv[0] == "run" && argv[1] == "request" {
			runID = "run_" + strings.TrimPrefix(strings.TrimSpace(string(output)), "ev_")
		}
	}
	result.runID = runID

	cmd := exec.Command(binary, "mcp", "--room", env.room, "--identity", "x") //nolint:gosec // test-only fixed helper
	cmd.Env = env.env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stdoutBuf, stderrBuf bytes.Buffer
	var group sync.WaitGroup
	var stdoutReader *os.File
	var stdoutWriter *os.File
	var readonly *os.File
	switch c.Stdout {
	case "pipe":
		pipe, pipeErr := cmd.StdoutPipe()
		if pipeErr != nil {
			t.Fatal(pipeErr)
		}
		group.Add(1)
		go func() { defer group.Done(); _, _ = io.Copy(&stdoutBuf, pipe) }()
	case "readonly":
		// A descriptor opened for reading: every write fails with EBADF.
		readonly, err = os.Open(os.DevNull)
		if err != nil {
			t.Fatal(err)
		}
		cmd.Stdout = readonly
	case "broken-pipe":
		stdoutReader, stdoutWriter, err = os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		cmd.Stdout = stdoutWriter
	default:
		t.Fatalf("unknown stdout mode %q", c.Stdout)
	}
	cmd.Stderr = &stderrBuf
	started := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start mcp: %v", err)
	}
	if stdoutWriter != nil {
		_ = stdoutWriter.Close()
	}
	if readonly != nil {
		_ = readonly.Close()
	}
	stdinClosed := false
	for _, step := range c.Steps {
		switch step.Op {
		case "send":
			if step.Data != "" {
				body := mcpSubstitute(step.Data, runID)
				data := []byte(body)
				if !strings.HasPrefix(body, "Content-Length:") {
					data = mcpFrame(data)
				}
				_, _ = stdin.Write(data)
			}
		case "request":
			_, _ = stdin.Write(mcpFrame(mcpRequestBody(step.ID, step.Method, nil)))
		case "call", "calls":
			count := 1
			if step.Op == "calls" {
				count = step.Count
			}
			var batch bytes.Buffer
			for i := 0; i < count; i++ {
				args := map[string]any{}
				for k, v := range step.Args {
					if s, ok := v.(string); ok {
						v = mcpSubstitute(s, runID)
					}
					args[k] = v
				}
				batch.Write(mcpFrame(mcpRequestBody(step.ID+i, "tools/call", map[string]any{"name": step.Tool, "arguments": args})))
			}
			_, _ = stdin.Write(batch.Bytes())
		case "sleep":
			time.Sleep(time.Duration(step.Ms) * time.Millisecond)
		case "close_stdin":
			_ = stdin.Close()
			stdinClosed = true
		case "close_stdout_reader":
			_ = stdoutReader.Close()
		case "signal":
			signal := syscall.SIGTERM
			if step.Signal == "INT" {
				signal = syscall.SIGINT
			}
			if err := cmd.Process.Signal(signal); err != nil {
				t.Fatalf("signal: %v", err)
			}
		case "cli":
			argv := make([]string, len(step.CLIArgs))
			for i, a := range step.CLIArgs {
				argv[i] = mcpSubstitute(a, runID)
			}
			cli := exec.Command(binary, argv...) //nolint:gosec // test-only fixed helper
			cli.Dir, cli.Env = env.room, env.env
			if output, err := cli.CombinedOutput(); err != nil {
				t.Fatalf("cli %v: %v\n%s", argv, err, output)
			}
		default:
			t.Fatalf("unknown step %q", step.Op)
		}
	}
	if !stdinClosed {
		defer func() { _ = stdin.Close() }()
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var waitErr error
	select {
	case waitErr = <-done:
	case <-time.After(90 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("%s: mcp process did not end", c.Name)
	}
	group.Wait()
	if stdoutReader != nil {
		_ = stdoutReader.Close()
	}
	result.elapsed = time.Since(started)
	result.stderr = stderrBuf.String()
	result.exitCode = 0
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		status, ok := exitErr.Sys().(syscall.WaitStatus)
		switch {
		case ok && status.Signaled():
			result.exitCode = -1
			result.signal = map[syscall.Signal]string{syscall.SIGPIPE: "SIGPIPE", syscall.SIGTERM: "SIGTERM", syscall.SIGINT: "SIGINT"}[status.Signal()]
		default:
			result.exitCode = exitErr.ExitCode()
		}
	} else if waitErr != nil {
		t.Fatalf("%s: wait: %v", c.Name, waitErr)
	}
	frames, err := parseMCPFrames(stdoutBuf.Bytes())
	if err != nil {
		t.Fatalf("%s: %v", c.Name, err)
	}
	for i := range frames {
		frames[i] = maskMCPFrame(frames[i])
	}
	result.arrival = append([]string(nil), frames...)
	if c.Unordered {
		sort.SliceStable(frames, func(i, j int) bool { return mcpFrameID(frames[i]) < mcpFrameID(frames[j]) })
	}
	result.frames = frames
	return result
}

// recordMCPProcessCase runs the Go binary and returns the case with the
// recorded expectation filled in. Unordered cases keep the frame order in
// first_id (the id of the first frame written) before sorting by id.
func recordMCPProcessCase(t *testing.T, binary, key string, c mcpProcessCase) mcpProcessCase {
	t.Helper()
	result := runMCPProcessCase(t, binary, key, c)
	checkMCPProcessBounds(t, c, result)
	c.SetupOut, c.RunID = result.setupOut, result.runID
	c.ExitCode, c.Signal, c.Stderr, c.Frames = result.exitCode, result.signal, result.stderr, result.frames
	if c.Unordered && len(result.arrival) > 0 {
		c.FirstID = new(int)
		*c.FirstID = mcpFrameID(result.arrival[0])
	}
	if c.Frames == nil {
		c.Frames = []string{}
	}
	return c
}

func checkMCPProcessBounds(t *testing.T, c mcpProcessCase, result mcpProcessResult) {
	t.Helper()
	if ms := int(result.elapsed / time.Millisecond); ms < c.MinMs || ms > c.MaxMs {
		t.Fatalf("%s: elapsed %dms outside [%d, %d]", c.Name, ms, c.MinMs, c.MaxMs)
	}
}

func TestPortMCPProcessContract(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	seed := sha256.Sum256([]byte("symroom-mcp-process-owner"))
	key := hex.EncodeToString(seed[:])
	binary := oracleExecutablePath(t, "symroom-go-mcp-process-oracle")
	build := exec.Command("go", "build", "-o", binary, "./cmd/symroom") //nolint:gosec // test-only fixed helper
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build Go symroom oracle: %v\n%s", err, output)
	}
	path := filepath.Join(root, mcpProcessContractPath)
	generate := os.Getenv("PORT_GENERATE") == "1"
	if generate && runtime.GOOS == "windows" {
		t.Skip("Unix-only cases are generated on Unix")
	}
	var frozen mcpProcessContract
	if !generate {
		data, readErr := os.ReadFile(path) //nolint:gosec // fixed fixture path
		if readErr != nil {
			t.Fatalf("read %s: %v (set PORT_GENERATE=1 to create it)", mcpProcessContractPath, readErr)
		}
		if err := json.Unmarshal(data, &frozen); err != nil {
			t.Fatal(err)
		}
	}
	fixture := mcpProcessContract{SchemaVersion: 1, OracleRevision: fixtureoracle.Current().Commit, IdentityKey: key, SourceHashes: map[string]string{}}
	for _, rel := range []string{
		"cmd/symroom/cmd_mcp.go", "internal/room/mcp/server.go", "internal/room/run/wait.go",
	} {
		data, err := os.ReadFile(filepath.Join(root, rel)) //nolint:gosec // fixed source path
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		fixture.SourceHashes[rel] = hex.EncodeToString(sum[:])
	}
	cases := mcpProcessCases()
	if !generate && len(frozen.Cases) != len(cases) {
		t.Fatalf("fixture has %d cases, generator defines %d", len(frozen.Cases), len(cases))
	}
	replayed, skipped := 0, 0
	for i, c := range cases {
		if c.UnixOnly && runtime.GOOS == "windows" {
			t.Logf("SKIP %s: needs POSIX descriptors or signals", c.Name)
			skipped++
			fixture.Cases = append(fixture.Cases, frozen.Cases[i])
			continue
		}
		recorded := recordMCPProcessCase(t, binary, key, c)
		fixture.Cases = append(fixture.Cases, recorded)
		replayed++
	}
	t.Logf("MCP process cases: %d total, %d replayed, %d SKIPPED", len(cases), replayed, skipped)
	if replayed == 0 {
		t.Fatal("no MCP process case ran")
	}
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if generate {
		if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec // generated contract fixture is intentionally world-readable
			t.Fatal(err)
		}
		t.Logf("wrote %s", mcpProcessContractPath)
		return
	}
	want, err := os.ReadFile(path) //nolint:gosec // fixed fixture path
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, want) {
		t.Fatal("Go MCP process fixture is stale; regenerate deliberately with PORT_GENERATE=1")
	}
}
