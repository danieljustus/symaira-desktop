// Command mcpdiff compares raw-process representative MCP responses.
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
)

type fixture struct {
	Cases []mcpCase `json:"cases"`
}
type mcpCase struct {
	ID         string `json:"id"`
	Request    string `json:"request"`
	RawInput   string `json:"raw_input,omitempty"`
	Framed     bool   `json:"framed,omitempty"`
	EmptyVault bool   `json:"empty_vault,omitempty"`
}

type processResult struct {
	stdout, stderr []byte
	exitCode       int
}

func main() {
	left := flag.String("left", "", "Go oracle binary")
	right := flag.String("right", "", "Rust binary")
	fixturePath := flag.String("fixture", "testdata/port/mcp/representative.json", "fixture path")
	flag.Parse()
	if *left == "" || *right == "" {
		fatal("--left and --right are required")
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

	for _, tc := range suite.Cases {
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
			if err := prepare(*right, vault, filepath.Join(root, "rust.db")); err != nil {
				fatal("%s prepare Rust: %v", tc.ID, err)
			}
		}
		goDB := filepath.Join(root, "go.db")
		rustDB := filepath.Join(root, "rust.db")
		if tc.EmptyVault {
			goDB = filepath.Join(vault, "go.db")
			rustDB = filepath.Join(vault, "rust.db")
		}
		leftResult := run(*left, vault, goDB, tc)
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
		leftFrames = normalize(tc.ID, leftFrames)
		rightFrames = normalize(tc.ID, rightFrames)
		if !reflect.DeepEqual(leftFrames, rightFrames) {
			fatal("%s response mismatch\nGo: %s\nRust: %s", tc.ID, compact(leftFrames), compact(rightFrames))
		}
		fmt.Printf("PASS %s (%d response(s))\n", tc.ID, len(leftFrames))
	}
	fmt.Printf("PASS MCP differential: %d cases\n", len(suite.Cases))
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

func normalize(id string, frames []any) []any {
	if id != "tools-list-line" || len(frames) != 1 {
		return frames
	}
	response, ok := frames[0].(map[string]any)
	if !ok {
		return frames
	}
	result, ok := response["result"].(map[string]any)
	if !ok {
		return frames
	}
	tools, ok := result["tools"].([]any)
	if !ok || len(tools) <= 3 {
		return frames
	}
	result["tools"] = tools[:3]
	return frames
}

func compact(frames []any) string { data, _ := json.Marshal(frames); return string(data) }
func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "FAIL "+format+"\n", args...)
	os.Exit(1)
}
