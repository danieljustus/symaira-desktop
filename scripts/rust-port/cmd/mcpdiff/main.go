// Command mcpdiff compares raw-process representative MCP responses. With
// --record it writes the Go observations of --left as the frozen recording
// (testdata/port/mcp/representative-recording.json) that the Rust replay
// `mcp_representative` consumes without Go; --check-recording verifies that the
// committed recording still equals a fresh run of the Go oracle.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"time"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

type fixture struct {
	Cases []mcpCase `json:"cases"`
}
type mcpCase struct {
	ID                 string `json:"id"`
	Request            string `json:"request"`
	RawInput           string `json:"raw_input,omitempty"`
	Framed             bool   `json:"framed,omitempty"`
	EmptyVault         bool   `json:"empty_vault,omitempty"`
	InstructionsAbsent bool   `json:"instructions_absent,omitempty"`
}

// recording is the frozen Go observation of the fixture requests. The vault
// root of the run is masked as <ROOT> so the Rust replay can substitute its own.
type recording struct {
	SchemaVersion int           `json:"schema_version"`
	Oracle        recordOracle  `json:"oracle"`
	Cases         []observation `json:"cases"`
}
type recordOracle struct {
	Commit  string `json:"commit"`
	Release string `json:"release"`
}
type observation struct {
	ID       string `json:"id"`
	ExitCode int    `json:"exit_code"`
	Frames   []any  `json:"frames"`
}

type processResult struct {
	stdout, stderr []byte
	exitCode       int
}

func main() {
	left := flag.String("left", "", "Go oracle binary")
	right := flag.String("right", "", "Rust binary")
	recordPath := flag.String("record", "", "write the Go observations of --left to this recording instead of comparing")
	checkPath := flag.String("check-recording", "", "verify that this recording equals a fresh run of --left instead of comparing")
	fixturePath := flag.String("fixture", "testdata/port/mcp/representative.json", "fixture path")
	casePrefix := flag.String("case-prefix", "", "run only fixture cases with this ID prefix")
	flag.Parse()
	recordMode := *recordPath != "" || *checkPath != ""
	if *recordPath != "" && *checkPath != "" {
		fatal("--record and --check-recording are exclusive")
	}
	if !recordMode && (*left == "" || *right == "") {
		fatal("--left and --right are required (--right is not used with --record/--check-recording)")
	}
	if recordMode && *left == "" {
		// Recording and its drift check build the pinned Go oracle themselves, so
		// portgen can verify the recording from an immutable source snapshot.
		built, cleanup, err := buildOracle()
		if err != nil {
			fatal("build Go oracle: %v", err)
		}
		defer cleanup()
		*left = built
	}
	data, err := os.ReadFile(*fixturePath)
	if err != nil {
		fatal("read fixture: %v", err)
	}
	var suite fixture
	if err := json.Unmarshal(data, &suite); err != nil {
		fatal("decode fixture: %v", err)
	}
	root, err := os.MkdirTemp("", "symdesk-mcp-diff-")
	if err != nil {
		fatal("temp root: %v", err)
	}
	defer func() { _ = os.RemoveAll(root) }()
	if err := os.WriteFile(filepath.Join(root, "alpha.md"), []byte("---\ntitle: Alpha Note\ncreated: 2026-01-02T03:04:05Z\n---\nneedle alpha body\n"), 0o600); err != nil {
		fatal("fixture file: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o700); err != nil {
		fatal("fixture dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested/beta.md"), []byte("---\ntitle: Nested Beta\n---\nneedle beta body\n"), 0o600); err != nil {
		fatal("fixture file: %v", err)
	}

	maskRoots := []string{root}
	if resolved, err := filepath.EvalSymlinks(root); err == nil && resolved != root {
		maskRoots = []string{resolved, root}
	}
	recorded := recording{SchemaVersion: 1, Oracle: recordOracle{Commit: fixtureoracle.Current().Commit, Release: fixtureoracle.Current().Release}}
	// desk_ls reports file modification times: pin them so the recording and its
	// Rust replay are clock independent.
	fixedTime := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, name := range []string{"alpha.md", "nested/beta.md"} {
		if err := os.Chtimes(filepath.Join(root, name), fixedTime, fixedTime); err != nil {
			fatal("pin mtime of %s: %v", name, err)
		}
	}

	runCount := 0
	for _, tc := range suite.Cases {
		if *casePrefix != "" && !strings.HasPrefix(tc.ID, *casePrefix) {
			continue
		}
		runCount++
		vault := root
		if tc.EmptyVault {
			vault = filepath.Join(root, "empty")
			if err := os.MkdirAll(vault, 0o700); err != nil {
				fatal("%s empty vault: %v", tc.ID, err)
			}
		}
		if tc.ID == "search-call" {
			if err := prepare(*left, vault, filepath.Join(root, "go.db")); err != nil {
				fatal("%s prepare Go: %v", tc.ID, err)
			}
			if !recordMode {
				if err := prepare(*right, vault, filepath.Join(root, "rust.db")); err != nil {
					fatal("%s prepare Rust: %v", tc.ID, err)
				}
			}
		}
		goDB := filepath.Join(root, "go.db")
		rustDB := filepath.Join(root, "rust.db")
		if tc.EmptyVault {
			goDB = filepath.Join(vault, "go.db")
			rustDB = filepath.Join(vault, "rust.db")
		}
		leftResult := run(*left, vault, goDB, tc)
		if recordMode {
			stdout := leftResult.stdout
			for _, mask := range maskRoots {
				stdout = bytes.ReplaceAll(stdout, []byte(mask), []byte("<ROOT>"))
			}
			frames, err := decodeFrames(stdout, tc.Framed)
			if err != nil {
				fatal("%s Go output: %v (%q)", tc.ID, err, stdout)
			}
			if tc.InstructionsAbsent {
				if err := checkInstructionsAbsent(frames); err != nil {
					fatal("%s Go %v", tc.ID, err)
				}
			}
			if frames == nil {
				frames = []any{}
			}
			recorded.Cases = append(recorded.Cases, observation{ID: tc.ID, ExitCode: leftResult.exitCode, Frames: frames})
			continue
		}
		rightResult := run(*right, vault, rustDB, tc)
		if leftResult.exitCode != rightResult.exitCode {
			fatal("%s exit mismatch: Go=%d Rust=%d\nGo stderr=%s\nRust stderr=%s", tc.ID, leftResult.exitCode, rightResult.exitCode, leftResult.stderr, rightResult.stderr)
		}
		leftFrames, err := decodeFrames(leftResult.stdout, tc.Framed)
		if err != nil {
			fatal("%s Go output: %v (%q)", tc.ID, err, leftResult.stdout)
		}
		rightFrames, err := decodeFrames(rightResult.stdout, tc.Framed)
		if err != nil {
			fatal("%s Rust output: %v (%q)", tc.ID, err, rightResult.stdout)
		}
		if tc.InstructionsAbsent {
			if err := checkInstructionsAbsent(leftFrames); err != nil {
				fatal("%s Go %v", tc.ID, err)
			}
			if err := checkInstructionsAbsent(rightFrames); err != nil {
				fatal("%s Rust %v", tc.ID, err)
			}
		}
		if !reflect.DeepEqual(leftFrames, rightFrames) {
			fatal("%s response mismatch\nGo: %s\nRust: %s", tc.ID, compact(leftFrames), compact(rightFrames))
		}
		fmt.Printf("PASS %s (%d response(s))\n", tc.ID, len(leftFrames))
	}
	if runCount == 0 {
		fatal("no MCP fixture cases match prefix %q", *casePrefix)
	}
	if recordMode {
		content, err := json.MarshalIndent(recorded, "", "  ")
		if err != nil {
			fatal("encode recording: %v", err)
		}
		content = append(content, '\n')
		if *recordPath != "" {
			if err := os.WriteFile(*recordPath, content, 0o644); err != nil { //nolint:gosec // checked-in non-secret fixture
				fatal("write recording: %v", err)
			}
			fmt.Printf("PASS recorded %d MCP cases to %s\n", runCount, *recordPath)
			return
		}
		//nolint:gosec // path is the recording named on the command line
		actual, err := os.ReadFile(*checkPath)
		if err != nil {
			fatal("read recording: %v", err)
		}
		if !bytes.Equal(actual, content) {
			fatal("recording drift in %s; run make mcp-representative-recording-generate", *checkPath)
		}
		fmt.Printf("PASS MCP recording verified: %d cases in %s\n", runCount, *checkPath)
		return
	}
	fmt.Printf("PASS MCP differential: %d cases\n", runCount)
}

// buildOracle compiles the Go symdesk of the current checkout with the version
// stamp "devel", which is what a plain Rust test binary reports. The Go tool is the one this command
// was built with, never a PATH lookup.
func buildOracle() (string, func(), error) {
	dir, err := os.MkdirTemp("", "symdesk-mcp-oracle-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	binary := filepath.Join(dir, "symdesk-go")
	//nolint:staticcheck // the harness deliberately uses the GOROOT it was built with
	goTool := filepath.Join(runtime.GOROOT(), "bin", "go")
	cmd := exec.Command(goTool, "build", "-ldflags=-X main.version=devel", "-o", binary, "./cmd/symdesk") //nolint:gosec // fixed arguments
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := cmd.CombinedOutput(); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("%w\n%s", err, output)
	}
	return binary, cleanup, nil
}

func checkInstructionsAbsent(frames []any) error {
	if len(frames) != 1 {
		return fmt.Errorf("expected one initialize response, got %d", len(frames))
	}
	frame, ok := frames[0].(map[string]any)
	if !ok {
		return fmt.Errorf("response frame has type %T", frames[0])
	}
	result, ok := frame["result"].(map[string]any)
	if !ok {
		return fmt.Errorf("initialize result has type %T", frame["result"])
	}
	if _, ok := result["instructions"]; ok {
		return fmt.Errorf("initialize result unexpectedly has instructions field")
	}
	return nil
}

func prepare(binary, vault, db string) error {
	result := run(binary, vault, db, mcpCase{Request: `{"jsonrpc":"2.0","id":99,"method":"tools/call","params":{"name":"desk_ls","arguments":{}}}`})
	if result.exitCode != 0 {
		return fmt.Errorf("exit %d stderr %s", result.exitCode, result.stderr)
	}
	return nil
}

func run(binary, vault, db string, testCase mcpCase) processResult {
	home := filepath.Join(vault, ".harness-home")
	if err := os.MkdirAll(filepath.Join(home, "tmp"), 0o700); err != nil {
		return processResult{stderr: []byte(err.Error()), exitCode: -1}
	}
	input := testCase.Request
	if testCase.RawInput != "" {
		input = testCase.RawInput
	} else if testCase.Framed {
		input = fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len([]byte(testCase.Request)), testCase.Request)
	} else {
		input += "\n"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "mcp")
	cmd.Stdin = strings.NewReader(input)
	cmd.Env = isolatedEnv(vault, db)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			code = -1
		}
	}
	if ctx.Err() != nil {
		code = -1
		stderr.WriteString("MCP process timed out")
	}
	return processResult{stdout: stdout.Bytes(), stderr: stderr.Bytes(), exitCode: code}
}

func isolatedEnv(vault, db string) []string {
	home := filepath.Join(vault, ".harness-home")
	env := []string{
		"HOME=" + home,
		"USERPROFILE=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"TMPDIR=" + filepath.Join(home, "tmp"),
		"TMP=" + filepath.Join(home, "tmp"),
		"TEMP=" + filepath.Join(home, "tmp"),
		"LANG=C",
		"LC_ALL=C",
		"TZ=UTC",
		"TERM=dumb",
		"NO_COLOR=1",
		"SYMDESK_VAULT=" + vault,
		"SYMDESK_SIDECAR=" + db,
	}
	for _, key := range []string{"PATH", "SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT"} {
		if value := os.Getenv(key); value != "" {
			env = append(env, key+"="+value)
		}
	}
	if runtime.GOOS == "windows" {
		env = append(env, "SystemDrive="+os.Getenv("SystemDrive"))
	}
	return env
}

func decodeFrames(data []byte, framed bool) ([]any, error) {
	if !framed {
		var frames []any
		for _, line := range bytes.Split(data, []byte{'\n'}) {
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			var value any
			if err := json.Unmarshal(line, &value); err != nil {
				return nil, err
			}
			frames = append(frames, value)
		}
		return frames, nil
	}
	var frames []any
	rest := data
	for len(bytes.TrimSpace(rest)) > 0 {
		lineEnd := bytes.IndexByte(rest, '\n')
		if lineEnd < 0 {
			return nil, io.ErrUnexpectedEOF
		}
		header := strings.TrimSpace(string(rest[:lineEnd]))
		rest = rest[lineEnd+1:]
		for len(rest) > 0 && (rest[0] == '\r' || rest[0] == '\n') {
			rest = rest[1:]
		}
		var length int
		if _, err := fmt.Sscanf(header, "Content-Length: %d", &length); err != nil || length < 1 || length > 1<<20 {
			return nil, fmt.Errorf("bad header %q", header)
		}
		if len(rest) < length {
			return nil, io.ErrUnexpectedEOF
		}
		var value any
		if err := json.Unmarshal(rest[:length], &value); err != nil {
			return nil, err
		}
		frames = append(frames, value)
		rest = rest[length:]
	}
	return frames, nil
}

func compact(frames []any) string { data, _ := json.Marshal(frames); return string(data) }
func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "FAIL "+format+"\n", args...)
	os.Exit(1)
}
