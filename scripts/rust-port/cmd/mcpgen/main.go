// Command mcpgen generates the Go-owned RUST-006 raw MCP fixture.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type fixture struct {
	SchemaVersion int       `json:"schema_version"`
	Oracle        oracle    `json:"oracle"`
	Cases         []mcpCase `json:"cases"`
}

type oracle struct {
	Commit  string `json:"commit"`
	Release string `json:"release"`
}

type mcpCase struct {
	ID         string `json:"id"`
	Request    string `json:"request"`
	RawInput   string `json:"raw_input,omitempty"`
	Framed     bool   `json:"framed,omitempty"`
	EmptyVault bool   `json:"empty_vault,omitempty"`
}

func main() {
	check := len(os.Args) == 2 && os.Args[1] == "--check"
	root, err := repoRoot()
	if err != nil {
		fatal("find repository root: %v", err)
	}
	content, err := json.MarshalIndent(generated(), "", "  ")
	if err != nil {
		fatal("encode fixture: %v", err)
	}
	content = append(content, '\n')
	path := filepath.Join(root, "testdata/port/mcp/representative.json")
	if check {
		//nolint:gosec // path is the fixed repository fixture path
		actual, err := os.ReadFile(path)
		if err != nil {
			fatal("read fixture: %v", err)
		}
		if !bytes.Equal(actual, content) {
			fatal("fixture drift; run make mcp-fixtures-generate")
		}
		fmt.Println("PASS MCP fixture verified: testdata/port/mcp/representative.json")
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // checked-in repository directory
		fatal("mkdir: %v", err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil { //nolint:gosec // checked-in non-secret fixture
		fatal("write fixture: %v", err)
	}
	fmt.Println("PASS generated testdata/port/mcp/representative.json")
}

func generated() fixture {
	return fixture{
		SchemaVersion: 1,
		Oracle:        oracle{Commit: "ae86331930fdfa2b128b68ae5af7437091b9949a", Release: "v0.12.2"},
		Cases: []mcpCase{
			{ID: "initialize-line", Request: `{"jsonrpc":"2.0","id":1,"method":"initialize"}`},
			{ID: "tools-list-line", Request: `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`},
			{ID: "status-call", Request: `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"desk_status","arguments":{}}}`},
			{ID: "ls-call", Request: `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"desk_ls","arguments":{}}}`},
			{ID: "ls-empty-call", Request: `{"jsonrpc":"2.0","id":40,"method":"tools/call","params":{"name":"desk_ls","arguments":{}}}`, EmptyVault: true},
			{ID: "search-call", Request: `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"desk_search","arguments":{"query":"needle"}}}`},
			{ID: "missing-search-query", Request: `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"desk_search","arguments":{}}}`},
			{ID: "unknown-method", Request: `{"jsonrpc":"2.0","id":7,"method":"unknown"}`},
			{ID: "notification-silent", Request: `{"jsonrpc":"2.0","method":"unknown"}`},
			{ID: "cancel-notification-silent", Request: `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":5}}`},
			{ID: "clean-eof", Request: ""},
			{ID: "invalid-request", Request: `[]`},
			{ID: "malformed-line", Request: `{"jsonrpc":"2.0","id":8,"method":`},
			{ID: "truncated-frame", RawInput: "Content-Length: 10\r\n\r\n{}"},
			{ID: "invalid-content-length", RawInput: "Content-Length: nope\r\n\r\n{}"},
			{ID: "initialize-framed", Request: `{"jsonrpc":"2.0","id":9,"method":"initialize"}`, Framed: true},
		},
	}
}

func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found")
		}
		dir = parent
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "FAIL "+format+"\n", args...)
	os.Exit(1)
}
