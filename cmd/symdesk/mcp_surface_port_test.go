package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// This process oracle deliberately records the raw frames, not reconstructed
// registry structs: RawMessage schemas retain the production encoder's order.
type mcpSurfaceCase struct {
	ID         string `json:"id"`
	AllowWrite bool   `json:"allow_write,omitempty"`
	Framed     bool   `json:"framed,omitempty"`
	Request    string `json:"request"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ExitCode   int    `json:"exit_code"`
}

type mcpSurfaceFixture struct {
	SchemaVersion int              `json:"schema_version"`
	Cases         []mcpSurfaceCase `json:"cases"`
}

func TestMCPSurfaceProcessPortFixture(t *testing.T) {
	binary := buildIndexRetrievalBinary(t, t.TempDir())
	fixture := mcpSurfaceFixture{SchemaVersion: 1}
	for _, write := range []bool{false, true} {
		for _, framed := range []bool{false, true} {
			mode := "read-only"
			if write {
				mode = "read-write"
			}
			id := mode + "-line"
			if framed {
				id = mode + "-framed"
			}
			c := mcpSurfaceCase{ID: id, AllowWrite: write, Framed: framed, Request: `{"jsonrpc":"2.0","id":"catalog","method":"tools/list"}`}
			root := t.TempDir()
			for _, dir := range []string{"home", "config", "data", "cache", "tmp", "vault", "cwd"} {
				if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			input := c.Request + "\n"
			if framed {
				input = "Content-Length: " + fmtMCPSize(len(c.Request)) + "\r\n\r\n" + c.Request
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			args := []string{"--vault", filepath.Join(root, "vault"), "mcp"}
			if write {
				args = append(args, "--allow-write")
			}
			//nolint:gosec // binary is the locally built symdesk fixture; arguments come from fixed test cases.
			cmd := exec.CommandContext(ctx, binary, args...)
			cmd.Dir = filepath.Join(root, "cwd")
			cmd.Env = []string{"HOME=" + filepath.Join(root, "home"), "USERPROFILE=" + filepath.Join(root, "home"), "XDG_CONFIG_HOME=" + filepath.Join(root, "config"), "XDG_DATA_HOME=" + filepath.Join(root, "data"), "XDG_CACHE_HOME=" + filepath.Join(root, "cache"), "TMPDIR=" + filepath.Join(root, "tmp"), "TMP=" + filepath.Join(root, "tmp"), "TEMP=" + filepath.Join(root, "tmp"), "TZ=UTC", "LANG=C", "LC_ALL=C", "PATH="}
			if systemRoot := os.Getenv("SYSTEMROOT"); systemRoot != "" {
				cmd.Env = append(cmd.Env, "SYSTEMROOT="+systemRoot)
			}
			var stdout, stderr bytes.Buffer
			cmd.Stdin = strings.NewReader(input)
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			cancel()
			if err != nil {
				t.Fatalf("%s: %v: %s", id, err, stderr.String())
			}
			c.Stdout, c.Stderr = stdout.String(), stderr.String()
			fixture.Cases = append(fixture.Cases, c)
			if !framed {
				var response struct {
					Result json.RawMessage `json:"result"`
				}
				if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				var catalog struct {
					Tools []json.RawMessage `json:"tools"`
				}
				if err := json.Unmarshal(response.Result, &catalog); err != nil {
					t.Fatal(err)
				}
				expectedCount := 29
				if write {
					expectedCount = 57
				}
				if len(catalog.Tools) != expectedCount {
					t.Fatalf("%s catalog count: %d", id, len(catalog.Tools))
				}
				checkMCPSurfaceBytes(t, "../../crates/symdesk-cli/src/mcp/assets/tools-"+mode+".json", append(response.Result, '\n'))
			}
		}
	}
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	checkMCPSurfaceBytes(t, "../../testdata/port/mcp/tools-process.json", append(data, '\n'))
}

func fmtMCPSize(size int) string { return strconv.Itoa(size) }

func checkMCPSurfaceBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, want, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	//nolint:gosec // path is derived from repository fixtures or the test-owned temporary root, not external input.
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("MCP surface fixture drift: %s", path)
	}
}
