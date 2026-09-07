package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const httpFixturePath = "testdata/port/http/representative.json"

type httpFixtureCase struct {
	ID      string            `json:"id"`
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Auth    string            `json:"auth,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

func generatedHTTP() httpSuite {
	return httpSuite{SchemaVersion: 1, Oracle: oracle{Commit: "ae86331930fdfa2b128b68ae5af7437091b9949a", Release: "v0.12.2"}, Cases: []httpCase{
		{ID: "healthz", Method: "GET", Path: "/healthz"},
		{ID: "status-missing-auth", Method: "GET", Path: "/api/v1/status"},
		{ID: "status-wrong-auth", Method: "GET", Path: "/api/v1/status", Auth: "wrong"},
		{ID: "status-raw-token-without-bearer", Method: "GET", Path: "/api/v1/status", Auth: "raw"},
		{ID: "status-authorized", Method: "GET", Path: "/api/v1/status", Auth: "valid"},
		{ID: "snapshot-plain", Method: "GET", Path: "/api/v1/snapshot", Auth: "valid"},
		{ID: "snapshot-not-modified", Method: "GET", Path: "/api/v1/snapshot", Auth: "valid", Headers: map[string]string{"If-None-Match": "$LAST_ETAG"}},
		{ID: "snapshot-gzip", Method: "GET", Path: "/api/v1/snapshot", Auth: "valid", Headers: map[string]string{"Accept-Encoding": "gzip"}},
		{ID: "file-exact-bytes", Method: "GET", Path: "/api/v1/files?path=Hello.md", Auth: "valid"},
		{ID: "file-range", Method: "GET", Path: "/api/v1/files?path=Hello.md", Auth: "valid", Headers: map[string]string{"Range": "bytes=0-4"}},
		{ID: "file-head", Method: "HEAD", Path: "/api/v1/files?path=Hello.md", Auth: "valid"},
		{ID: "file-traversal", Method: "GET", Path: "/api/v1/files?path=../outside.md", Auth: "valid"},
		{ID: "file-absolute", Method: "GET", Path: "/api/v1/files?path=/etc/passwd", Auth: "valid"},
		{ID: "file-backslash", Method: "GET", Path: "/api/v1/files?path=escape%5Csecret.md", Auth: "valid"},
		{ID: "file-nul", Method: "GET", Path: "/api/v1/files?path=bad%00.md", Auth: "valid"},
		{ID: "file-internal", Method: "GET", Path: "/api/v1/files?path=.symdesk/server/state.db", Auth: "valid"},
		{ID: "file-symlink", Method: "GET", Path: "/api/v1/files?path=escape.md", Auth: "valid"},
		{ID: "file-missing", Method: "GET", Path: "/api/v1/files?path=missing.md", Auth: "valid"},
		{ID: "health-method-not-allowed", Method: "POST", Path: "/healthz"},
		{ID: "unknown-route", Method: "GET", Path: "/not-found"},
		{ID: "status-head", Method: "HEAD", Path: "/api/v1/status", Auth: "valid"},
	}}
}

func writeHTTPFixture(root string) {
	content := marshalHTTPFixture()
	path := filepath.Join(root, httpFixturePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fatal("create HTTP fixture directory: %v", err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		fatal("write HTTP fixture: %v", err)
	}
}

func checkHTTPFixture(root string) {
	path := filepath.Join(root, httpFixturePath)
	actual, err := os.ReadFile(path)
	if err != nil {
		fatal("read %s: %v (run representative-fixtures-generate)", httpFixturePath, err)
	}
	if !bytes.Equal(actual, marshalHTTPFixture()) {
		fatal("fixture drift in %s; run representative-fixtures-generate", httpFixturePath)
	}
	fmt.Printf("PASS representative HTTP fixture verified: %s\n", httpFixturePath)
}

func marshalHTTPFixture() []byte {
	content, err := json.MarshalIndent(generatedHTTP(), "", "  ")
	if err != nil {
		fatal("encode HTTP fixture: %v", err)
	}
	return append(content, '\n')
}
