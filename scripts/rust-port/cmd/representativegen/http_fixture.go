package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const httpFixturePath = "testdata/port/http/representative.json"

func generatedHTTP() httpSuite {
	return httpSuite{SchemaVersion: 1, Oracle: oracle{Commit: "745c08e8144971c61133c5d0e5d61c7ce405aad2", Release: "post-v0.12.2-security-880"}, Cases: []httpCase{
		{ID: "healthz", Method: "GET", Path: "/healthz"},
		{ID: "status-missing-auth", Method: "GET", Path: "/api/v1/status"},
		{ID: "status-wrong-auth", Method: "GET", Path: "/api/v1/status", Auth: "wrong"},
		{ID: "status-raw-token-without-bearer", Method: "GET", Path: "/api/v1/status", Auth: "raw"},
		{ID: "status-authorized", Method: "GET", Path: "/api/v1/status", Auth: "valid"},
		{ID: "status-wrong-auth-2", Method: "GET", Path: "/api/v1/status", Auth: "wrong"},
		{ID: "status-wrong-auth-3", Method: "GET", Path: "/api/v1/status", Auth: "wrong"},
		{ID: "status-wrong-auth-4", Method: "GET", Path: "/api/v1/status", Auth: "wrong"},
		{ID: "status-wrong-auth-5", Method: "GET", Path: "/api/v1/status", Auth: "wrong"},
		{ID: "snapshot-plain", Method: "GET", Path: "/api/v1/snapshot", Auth: "valid"},
		{ID: "snapshot-not-modified", Method: "GET", Path: "/api/v1/snapshot", Auth: "valid", Headers: map[string]string{"If-None-Match": "$LAST_ETAG"}},
		{ID: "snapshot-gzip", Method: "GET", Path: "/api/v1/snapshot", Auth: "valid", Headers: map[string]string{"Accept-Encoding": "gzip"}},
		{ID: "snapshot-gzip-q0", Method: "GET", Path: "/api/v1/snapshot", Auth: "valid", Headers: map[string]string{"Accept-Encoding": "br, gzip;q=0"}},
		{ID: "snapshot-head-gzip", Method: "HEAD", Path: "/api/v1/snapshot", Auth: "valid", Headers: map[string]string{"Accept-Encoding": "gzip;q=0"}},
		{ID: "file-exact-bytes", Method: "GET", Path: "/api/v1/files?path=Hello.md", Auth: "valid"},
		{ID: "file-internal-symlink", Method: "GET", Path: "/api/v1/files?path=internal.md", Auth: "valid"},
		{ID: "file-range", Method: "GET", Path: "/api/v1/files?path=Hello.md", Auth: "valid", Headers: map[string]string{"Range": "bytes=0-4"}},
		{ID: "file-head", Method: "HEAD", Path: "/api/v1/files?path=Hello.md", Auth: "valid"},
		{ID: "file-traversal", Method: "GET", Path: "/api/v1/files?path=../outside.md", Auth: "valid"},
		{ID: "file-absolute", Method: "GET", Path: "/api/v1/files?path=/etc/passwd", Auth: "valid"},
		{ID: "file-backslash", Method: "GET", Path: "/api/v1/files?path=escape%5Csecret.md", Auth: "valid"},
		{ID: "file-nul", Method: "GET", Path: "/api/v1/files?path=bad%00.md", Auth: "valid"},
		{ID: "file-invalid-separator", Method: "GET", Path: "/api/v1/files?path=foo//bar.md", Auth: "valid"},
		{ID: "file-dot-segment", Method: "GET", Path: "/api/v1/files?path=foo/./bar.md", Auth: "valid"},
		{ID: "file-range-overflow", Method: "GET", Path: "/api/v1/files?path=Hello.md", Auth: "valid", Headers: map[string]string{"Range": "bytes=18446744073709551616-"}},
		{ID: "file-internal", Method: "GET", Path: "/api/v1/files?path=.symdesk/server/state.db", Auth: "valid"},
		{ID: "file-symlink", Method: "GET", Path: "/api/v1/files?path=escape.md", Auth: "valid"},
		{ID: "file-missing", Method: "GET", Path: "/api/v1/files?path=missing.md", Auth: "valid"},
		{ID: "file-put-missing-auth", Method: "PUT", Path: "/api/v1/files?path=nested/Created.md", Body: "# Created\n"},
		{ID: "file-put-bad-extension", Method: "PUT", Path: "/api/v1/files?path=nested/Created.txt", Auth: "valid", Body: "plain"},
		{ID: "file-put-internal", Method: "PUT", Path: "/api/v1/files?path=.symdesk/server/secret.md", Auth: "valid", Body: "secret"},
		{ID: "file-put-traversal", Method: "PUT", Path: "/api/v1/files?path=../outside.md", Auth: "valid", Body: "outside"},
		{ID: "file-put-symlink-parent", Method: "PUT", Path: "/api/v1/files?path=escape-dir/new.md", Auth: "valid", Body: "outside"},
		{ID: "file-put-create", Method: "PUT", Path: "/api/v1/files?path=nested/Created.md", Auth: "valid", Body: "# Created\n\nfirst state\n"},
		{ID: "file-put-read-created", Method: "GET", Path: "/api/v1/files?path=nested/Created.md", Auth: "valid"},
		{ID: "file-put-update", Method: "PUT", Path: "/api/v1/files?path=nested/Created.md", Auth: "valid", Body: "# Created\n\nsecond state\n"},
		{ID: "file-put-read-updated", Method: "GET", Path: "/api/v1/files?path=nested/Created.md", Auth: "valid"},
		{ID: "file-put-over-limit", Method: "PUT", Path: "/api/v1/files?path=nested/TooBig.md", Auth: "valid", BodyRepeat: (8 << 20) + 1},
		{ID: "health-method-not-allowed", Method: "POST", Path: "/healthz"},
		{ID: "unknown-route", Method: "GET", Path: "/not-found"},
		{ID: "status-head", Method: "HEAD", Path: "/api/v1/status", Auth: "valid"},
		{ID: "notebooks-missing-auth", Method: "GET", Path: "/api/v1/notebooks"},
		{ID: "notebooks-wrong-auth", Method: "GET", Path: "/api/v1/notebooks", Auth: "wrong"},
		{ID: "notebooks-list", Method: "GET", Path: "/api/v1/notebooks", Auth: "valid"},
		{ID: "notebook-get-missing-auth", Method: "GET", Path: "/api/v1/notebooks/research"},
		{ID: "notebook-get-wrong-auth", Method: "GET", Path: "/api/v1/notebooks/research", Auth: "wrong"},
		{ID: "notebook-get-research", Method: "GET", Path: "/api/v1/notebooks/research", Auth: "valid"},
		{ID: "notebook-get-mixed-sources", Method: "GET", Path: "/api/v1/notebooks/mixed", Auth: "valid"},
		{ID: "notebook-get-unknown", Method: "GET", Path: "/api/v1/notebooks/does-not-exist", Auth: "valid"},
		{ID: "notebooks-empty", Method: "GET", Path: "/api/v1/notebooks", Auth: "valid", EmptyNotebooks: true},
	}}
}

func writeHTTPFixture(root string) {
	content := marshalHTTPFixture()
	path := filepath.Join(root, httpFixturePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // checked-in fixture directory
		fatal("create HTTP fixture directory: %v", err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil { //nolint:gosec // checked-in non-secret fixture
		fatal("write HTTP fixture: %v", err)
	}
}

func checkHTTPFixture(root string) {
	path := filepath.Join(root, httpFixturePath)
	//nolint:gosec // path is the fixed repository fixture path
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
