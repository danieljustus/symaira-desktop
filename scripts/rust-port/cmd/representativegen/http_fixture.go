package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

const httpFixturePath = "testdata/port/http/representative.json"

func generatedHTTP() httpSuite {
	cases := []httpCase{
		{ID: "healthz", Method: "GET", Path: "/healthz"},
		{ID: "healthz-slow-header", Method: "GET", Path: "/healthz", HeaderDelayMS: 6000},
		{ID: "healthz-large-header", Method: "GET", Path: "/healthz", HeaderPaddingBytes: 512 << 10},
		{ID: "healthz-many-headers", Method: "GET", Path: "/healthz", HeaderRepeatCount: 101},
		{ID: "status-missing-auth", Method: "GET", Path: "/api/v1/status"},
		{ID: "status-wrong-auth", Method: "GET", Path: "/api/v1/status", Auth: "wrong"},
		{ID: "status-raw-token-without-bearer", Method: "GET", Path: "/api/v1/status", Auth: "raw"},
		{ID: "status-authorized", Method: "GET", Path: "/api/v1/status", Auth: "valid"},
		{ID: "status-worker-token", Method: "GET", Path: "/api/v1/status", Auth: "worker"},
		{ID: "status-wrong-auth-2", Method: "GET", Path: "/api/v1/status", Auth: "wrong"},
		{ID: "status-wrong-auth-3", Method: "GET", Path: "/api/v1/status", Auth: "wrong"},
		{ID: "status-wrong-auth-4", Method: "GET", Path: "/api/v1/status", Auth: "wrong"},
		{ID: "status-wrong-auth-5", Method: "GET", Path: "/api/v1/status", Auth: "wrong"},
		{ID: "snapshot-plain", Method: "GET", Path: "/api/v1/snapshot", Auth: "valid"},
		{ID: "snapshot-not-modified", Method: "GET", Path: "/api/v1/snapshot", Auth: "valid", Headers: map[string]string{"If-None-Match": "$LAST_ETAG"}},
		{ID: "snapshot-gzip", Method: "GET", Path: "/api/v1/snapshot", Auth: "valid", Headers: map[string]string{"Accept-Encoding": "gzip"}},
		{ID: "snapshot-gzip-q0", Method: "GET", Path: "/api/v1/snapshot", Auth: "valid", Headers: map[string]string{"Accept-Encoding": "br, gzip;q=0"}},
		{ID: "snapshot-head-gzip", Method: "HEAD", Path: "/api/v1/snapshot", Auth: "valid", Headers: map[string]string{"Accept-Encoding": "gzip;q=0"}},
		{ID: "file-get-worker-acl-denied", Method: "GET", Path: "/api/v1/files?path=Hello.md", Auth: "worker", PopulateWorkerACL: true},
		{ID: "file-get-worker-acl-group", Method: "GET", Path: "/api/v1/files?path=nested/Note.md", Auth: "worker"},
		{ID: "file-get-worker-acl-public", Method: "GET", Path: "/api/v1/files?path=notebooks/archive.md", Auth: "worker"},
		{ID: "file-put-worker-denied", Method: "PUT", Path: "/api/v1/files?path=Hello.md", Auth: "worker", Body: "# unauthorized update\n"},
		{ID: "snapshot-worker-acl-filter", Method: "GET", Path: "/api/v1/snapshot", Auth: "worker"},
		{ID: "notebook-worker-acl-filter", Method: "GET", Path: "/api/v1/notebooks/research", Auth: "worker"},
		{ID: "file-get-named-user-denied", Method: "GET", Path: "/api/v1/files?path=Hello.md", Auth: "named", PopulateNamedUser: true},
		{ID: "status-named-user", Method: "GET", Path: "/api/v1/status", Auth: "named"},
		{ID: "file-get-named-user-allowed", Method: "GET", Path: "/api/v1/files?path=nested/Named.md", Auth: "named"},
		{ID: "file-get-named-user-range", Method: "GET", Path: "/api/v1/files?path=nested/Named.md", Auth: "named", Headers: map[string]string{"Range": "bytes=0-5"}, PopulateNamedUser: true},
		{ID: "file-get-named-user-not-modified", Method: "GET", Path: "/api/v1/files?path=nested/Named.md", Auth: "named", Headers: map[string]string{"If-Modified-Since": "Fri, 02 Jan 2026 03:04:05 GMT"}, PopulateNamedUser: true},
		{ID: "file-get-named-user-denied-not-modified", Method: "GET", Path: "/api/v1/files?path=Hello.md", Auth: "named", Headers: map[string]string{"If-Modified-Since": "Fri, 02 Jan 2026 03:04:05 GMT"}, PopulateNamedUser: true},
		{ID: "file-get-named-user-wrong-token", Method: "GET", Path: "/api/v1/files?path=nested/Named.md", Auth: "named-wrong"},
		{ID: "snapshot-named-user-filter", Method: "GET", Path: "/api/v1/snapshot", Auth: "named"},
		{ID: "snapshot-named-user-head-gzip", Method: "HEAD", Path: "/api/v1/snapshot", Auth: "named", Headers: map[string]string{"Accept-Encoding": "gzip"}},
		{ID: "notebooks-list-named-user", Method: "GET", Path: "/api/v1/notebooks", Auth: "named"},
		{ID: "notebook-get-named-user-filter", Method: "GET", Path: "/api/v1/notebooks/research", Auth: "named"},
		{ID: "notebook-head-named-user", Method: "HEAD", Path: "/api/v1/notebooks/research", Auth: "named"},
		{ID: "jobs-named-user-forbidden", Method: "GET", Path: "/api/v1/jobs", Auth: "named"},
		{ID: "jobs-named-worker-forbidden", Method: "GET", Path: "/api/v1/jobs", Auth: "named-worker", PopulateNamedUser: true},
		{ID: "command-missing-auth", Method: "POST", Path: "/api/v1/command", Body: "{"},
		{ID: "command-worker-forbidden", Method: "POST", Path: "/api/v1/command", Auth: "worker", Body: "{"},
		{ID: "command-named-user-forbidden", Method: "POST", Path: "/api/v1/command", Auth: "named", Body: "{", PopulateNamedUser: true},
		{ID: "command-named-worker-forbidden", Method: "POST", Path: "/api/v1/command", Auth: "named-worker", Body: "{", PopulateNamedUser: true},
		{ID: "command-admin-empty", Method: "POST", Path: "/api/v1/command", Auth: "valid", Body: `{"arguments":[]}`},
		{ID: "command-method-not-allowed", Method: "GET", Path: "/api/v1/command", Auth: "valid"},
		{ID: "command-admin-null-arguments", Method: "POST", Path: "/api/v1/command", Auth: "valid", Body: `{"arguments":null}`},
		{ID: "command-admin-unavailable", Method: "POST", Path: "/api/v1/command", Auth: "valid", Body: `{"arguments":["not-real"]}`},
		{ID: "command-admin-controlled-path", Method: "POST", Path: "/api/v1/command", Auth: "valid", Body: `{"arguments":["ls","--vault=/tmp/other-vault"]}`},
		{ID: "command-admin-stream-controlled-path", Method: "POST", Path: "/api/v1/command", Auth: "valid", Body: `{"arguments":["ask","--vault=/tmp/other-vault"]}`},
		{ID: "command-admin-stream-transform", Method: "POST", Path: "/api/v1/command", Auth: "valid", Body: `{"arguments":["transform","summarize","--text","short input"]}`},
		{ID: "command-admin-stream-ask-no-provider", Method: "POST", Path: "/api/v1/command", Auth: "valid", Body: `{"arguments":["ask","hello"]}`},
		{ID: "ai-transform-missing-auth", Method: "POST", Path: "/api/v1/ai/transform", Body: `{"text":"short input"}`},
		{ID: "ai-transform-empty-text", Method: "POST", Path: "/api/v1/ai/transform", Auth: "valid", Body: `{"text":"  \n "}`},
		{ID: "ai-transform-null-strings", Method: "POST", Path: "/api/v1/ai/transform", Auth: "valid", Body: `{"text":null,"intent":null}`},
		{ID: "ai-transform-no-provider", Method: "POST", Path: "/api/v1/ai/transform", Auth: "valid", Body: `{"text":"short input"}`},
		{ID: "ai-transform-local-ollama-stream", Method: "POST", Path: "/api/v1/ai/transform", Auth: "valid", Body: `{"text":"short provider input"}`, ProviderOllama: true},
		{ID: "ai-transform-local-ollama-error", Method: "POST", Path: "/api/v1/ai/transform", Auth: "valid", Body: `{"text":"provider error input"}`, ProviderFailure: true},
		{ID: "ai-transform-local-ollama-oversized-line", Method: "POST", Path: "/api/v1/ai/transform", Auth: "valid", Body: `{"text":"oversized provider input"}`, ProviderOversized: true},
		{ID: "ai-transform-local-ollama-disconnect", Method: "POST", Path: "/api/v1/ai/transform", Auth: "valid", Body: `{"text":"disconnect provider input"}`, ProviderDisconnect: true},
		{ID: "ai-question-local-ollama-empty-results", Method: "POST", Path: "/api/v1/ai/ask", Auth: "valid", Body: `{"query":"provider ask question"}`, ProviderAskOllama: true},
		{ID: "ai-question-local-ollama-worker-denied-source-not-forwarded", Method: "POST", Path: "/api/v1/ai/ask", Auth: "worker", Body: `{"query":"Hello"}`, ProviderAskOllama: true, PopulateWorkerACL: true},
		{ID: "ai-question-local-ollama-with-citation", Method: "POST", Path: "/api/v1/ai/ask", Auth: "valid", Body: `{"query":"Body"}`, ProviderAskOllama: true, ProviderAskExpectedSource: true},
		{ID: "ai-question-local-ollama-disconnect", Method: "POST", Path: "/api/v1/ai/ask", Auth: "valid", Body: `{"query":"disconnect provider input"}`, ProviderAskOllama: true, ProviderDisconnect: true, DisconnectAfterEvents: 4},
		{ID: "ai-question-local-ollama-notebook-citation-warning", Method: "POST", Path: "/api/v1/ai/ask", Auth: "valid", Body: `{"query":"notebook citation warning","notebook":"research"}`, ProviderAskOllama: true, ProviderAskExpectedSource: true, ProviderAskNotebook: true},
		{ID: "ai-question-local-ollama-notebook-worker-acl", Method: "POST", Path: "/api/v1/ai/ask", Auth: "worker", Body: `{"query":"Hello","notebook":"research"}`, ProviderAskOllama: true, PopulateWorkerACL: true, ProviderAskNotebook: true},
		{ID: "ai-transform-openai-provider-falls-back-to-local-ollama", Method: "POST", Path: "/api/v1/ai/transform", Auth: "valid", Body: `{"text":"short provider input"}`, ProviderOpenAIFallback: true},
		{ID: "ai-transform-trailing-json", Method: "POST", Path: "/api/v1/ai/transform", Auth: "valid", Body: `{"text":"short input"}{"ignored":true}`},
		{ID: "ai-transform-named-user-no-provider", Method: "POST", Path: "/api/v1/ai/transform", Auth: "named", Body: `{"text":"short input","intent":"rewrite"}`, PopulateNamedUser: true},
		{ID: "ai-transform-method-not-allowed", Method: "GET", Path: "/api/v1/ai/transform", Auth: "valid"},
		{ID: "ai-ask-missing-auth", Method: "POST", Path: "/api/v1/ai/ask", Body: `{"query":"Body"}`},
		{ID: "ai-ask-invalid-json", Method: "POST", Path: "/api/v1/ai/ask", Auth: "valid", Body: "{"},
		{ID: "ai-ask-empty-query", Method: "POST", Path: "/api/v1/ai/ask", Auth: "valid", Body: `{"query":"  "}`},
		{ID: "ai-ask-no-provider", Method: "POST", Path: "/api/v1/ai/ask", Auth: "valid", Body: `{"query":"Body"}`},
		{ID: "ai-ask-ranked-citations", Method: "POST", Path: "/api/v1/ai/ask", Auth: "valid", Body: `{"query":"Body"}`},
		{ID: "ai-ask-trailing-json", Method: "POST", Path: "/api/v1/ai/ask", Auth: "valid", Body: `{"query":"Body"}{"ignored":true}`},
		{ID: "ai-ask-named-user-acl-filter", Method: "POST", Path: "/api/v1/ai/ask", Auth: "named", Body: `{"query":"named"}`, PopulateNamedUser: true},
		{ID: "ai-ask-worker-acl-filter", Method: "POST", Path: "/api/v1/ai/ask", Auth: "worker", Body: `{"query":"nested"}`, PopulateWorkerACL: true},
		{ID: "ai-ask-worker-acl-denied", Method: "POST", Path: "/api/v1/ai/ask", Auth: "worker", Body: `{"query":"Body"}`, PopulateWorkerACL: true},
		{ID: "ai-ask-notebook-scoped-sources", Method: "POST", Path: "/api/v1/ai/ask", Auth: "valid", Body: `{"query":"Body","notebook":"mixed"}`},
		{ID: "ai-ask-notebook-unmatched-fallback", Method: "POST", Path: "/api/v1/ai/ask", Auth: "valid", Body: `{"query":"conceptual-question-no-match","notebook":"mixed"}`},
		{ID: "ai-ask-notebook-worker-acl", Method: "POST", Path: "/api/v1/ai/ask", Auth: "worker", Body: `{"query":"Body","notebook":"mixed"}`, PopulateWorkerACL: true},
		{ID: "ai-ask-notebook-worker-acl-fallback", Method: "POST", Path: "/api/v1/ai/ask", Auth: "worker", Body: `{"query":"conceptual-question-no-match","notebook":"mixed"}`, PopulateWorkerACL: true},
		{ID: "ai-ask-method-not-allowed", Method: "GET", Path: "/api/v1/ai/ask", Auth: "valid"},
		{ID: "ai-ask-notebook-nonempty-hybrid-index", Method: "POST", Path: "/api/v1/ai/ask", Auth: "valid", Body: `{"query":"Body","notebook":"research"}`, PopulateHybridIndex: true},
		{ID: "ai-ask-unscoped-hybrid-index", Method: "POST", Path: "/api/v1/ai/ask", Auth: "valid", Body: `{"query":"Body"}`, HybridQuery: true},
		{ID: "command-admin-invalid-subcommand", Method: "POST", Path: "/api/v1/command", Auth: "valid", Body: `{"arguments":["note","not-real"]}`},
		{ID: "command-admin-trailing-data", Method: "POST", Path: "/api/v1/command", Auth: "valid", Body: `{"arguments":["not-real"]}{"ignored":true}`},
		{ID: "command-admin-over-limit-prefix", Method: "POST", Path: "/api/v1/command", Auth: "valid", Body: `{"arguments":["not-real"]}`, BodyRepeat: (2 << 20) - len(`{"arguments":["not-real"]}`) + 1},
		{ID: "file-put-worker-group", Method: "PUT", Path: "/api/v1/files?path=nested/Note.md", Auth: "worker", Body: "# authorized worker update\n"},
		{ID: "file-put-named-user", Method: "PUT", Path: "/api/v1/files?path=nested/Named.md", Auth: "named", Body: "# authorized named user update\n"},
		{ID: "file-get-named-user-updated", Method: "GET", Path: "/api/v1/files?path=nested/Named.md", Auth: "named"},
		{ID: "file-exact-bytes", Method: "GET", Path: "/api/v1/files?path=Hello.md", Auth: "valid"},
		{ID: "file-internal-symlink", Method: "GET", Path: "/api/v1/files?path=internal.md", Auth: "valid"},
		{ID: "file-range", Method: "GET", Path: "/api/v1/files?path=Hello.md", Auth: "valid", Headers: map[string]string{"Range": "bytes=0-4"}},
		{ID: "file-range-if-range-match", Method: "GET", Path: "/api/v1/files?path=Hello.md", Auth: "valid", Headers: map[string]string{"Range": "bytes=0-4", "If-Range": "Fri, 02 Jan 2026 03:04:05 GMT"}},
		{ID: "file-range-if-range-legacy-date", Method: "GET", Path: "/api/v1/files?path=Hello.md", Auth: "valid", Headers: map[string]string{"Range": "bytes=0-4", "If-Range": "Friday, 02-Jan-26 03:04:05 GMT"}},
		{ID: "file-range-if-range-stale", Method: "GET", Path: "/api/v1/files?path=Hello.md", Auth: "valid", Headers: map[string]string{"Range": "bytes=0-4", "If-Range": "Thu, 01 Jan 2026 03:04:05 GMT"}},
		{ID: "file-range-if-range-etag", Method: "GET", Path: "/api/v1/files?path=Hello.md", Auth: "valid", Headers: map[string]string{"Range": "bytes=0-4", "If-Range": "\"unavailable-etag\""}},
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
		{ID: "files-method-not-allowed", Method: "POST", Path: "/api/v1/files?path=Hello.md", Auth: "valid"},
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
		{ID: "jobs-missing-auth", Method: "GET", Path: "/api/v1/jobs"},
		{ID: "jobs-worker-token", Method: "GET", Path: "/api/v1/jobs", Auth: "worker"},
		{ID: "jobs-worker-head", Method: "HEAD", Path: "/api/v1/jobs", Auth: "worker"},
		{ID: "jobs-wrong-auth", Method: "GET", Path: "/api/v1/jobs", Auth: "wrong"},
		{ID: "worker-lease-no-jobs", Method: "POST", Path: "/api/v1/worker/lease", Auth: "worker", Body: `{"worker_id":"worker-lease","capabilities":["ocr"]}`},
		{ID: "worker-lease-valid-prefix-over-limit-trailing", Method: "POST", Path: "/api/v1/worker/lease", Auth: "valid", Body: `{"worker_id":"worker-lease","capabilities":["ocr"]}`, BodyRepeat: 65486},
		{ID: "jobs-empty", Method: "GET", Path: "/api/v1/jobs", Auth: "valid"},
		{ID: "jobs-populated", Method: "GET", Path: "/api/v1/jobs", Auth: "valid", PopulateJobs: true},
		{ID: "jobs-first-page", Method: "GET", Path: "/api/v1/jobs?limit=1", Auth: "valid"},
		{ID: "jobs-second-page", Method: "GET", Path: "/api/v1/jobs?limit=1&offset=1", Auth: "valid"},
		{ID: "jobs-beyond-end", Method: "GET", Path: "/api/v1/jobs?offset=3", Auth: "valid"},
		{ID: "jobs-invalid-limit", Method: "GET", Path: "/api/v1/jobs?limit=0", Auth: "valid"},
		{ID: "jobs-invalid-offset", Method: "GET", Path: "/api/v1/jobs?offset=-1", Auth: "valid"},
		{ID: "jobs-retry-missing-auth", Method: "POST", Path: "/api/v1/jobs/retry?id=00000000000000000000000000000003"},
		{ID: "jobs-retry-worker-token", Method: "POST", Path: "/api/v1/jobs/retry?id=00000000000000000000000000000003", Auth: "worker"},
		{ID: "jobs-retry-method-not-allowed", Method: "GET", Path: "/api/v1/jobs/retry", Auth: "valid"},
		{ID: "jobs-retry-nonfailed", Method: "POST", Path: "/api/v1/jobs/retry?id=00000000000000000000000000000001", Auth: "valid"},
		{ID: "jobs-retry-failed", Method: "POST", Path: "/api/v1/jobs/retry?id=00000000000000000000000000000003", Auth: "valid"},
		{ID: "worker-input-missing-auth", Method: "GET", Path: "/api/v1/worker/input?id=00000000000000000000000000000003"},
		{ID: "worker-input-worker-token", Method: "GET", Path: "/api/v1/worker/input?id=00000000000000000000000000000003", Auth: "worker"},
		{ID: "worker-input-missing-job", Method: "GET", Path: "/api/v1/worker/input?id=00000000000000000000000000000003", Auth: "valid"},
		{ID: "worker-input-valid", Method: "GET", Path: "/api/v1/worker/input?id=00000000000000000000000000000003", Auth: "valid", PopulateJobs: true},
		{ID: "worker-input-named-worker", Method: "GET", Path: "/api/v1/worker/input?id=00000000000000000000000000000003", Auth: "named-worker", PopulateJobs: true, PopulateNamedUser: true},
		{ID: "worker-input-head-range", Method: "HEAD", Path: "/api/v1/worker/input?id=00000000000000000000000000000003", Auth: "valid", Headers: map[string]string{"Range": "bytes=0-5"}, PopulateJobs: true},
		{ID: "worker-lease-missing-auth", Method: "POST", Path: "/api/v1/worker/lease", Body: `{"worker_id":"worker-lease","capabilities":["ocr"]}`},
		{ID: "worker-method-not-allowed", Method: "GET", Path: "/api/v1/worker/complete", Auth: "valid"},
		{ID: "worker-lease-invalid-json", Method: "POST", Path: "/api/v1/worker/lease", Auth: "valid", Body: "{"},
		{ID: "worker-lease-missing-worker", Method: "POST", Path: "/api/v1/worker/lease", Auth: "valid", Body: `{"capabilities":["ocr"]}`},
		{ID: "worker-lease-oversized", Method: "POST", Path: "/api/v1/worker/lease", Auth: "valid", BodyRepeat: (64 << 10) + 1},
		{ID: "worker-lease-no-capability", Method: "POST", Path: "/api/v1/worker/lease", Auth: "valid", Body: `{"worker_id":"worker-lease","capabilities":["pdf"]}`, PopulateJobs: true},
		{ID: "worker-lease-valid", Method: "POST", Path: "/api/v1/worker/lease", Auth: "valid", Body: `{"worker_id":"worker-lease","capabilities":["ocr"]}`, PopulateJobs: true},
		{ID: "worker-lease-named-worker", Method: "POST", Path: "/api/v1/worker/lease", Auth: "named-worker", Body: `{"worker_id":"worker-lease","capabilities":["ocr"]}`, PopulateJobs: true, PopulateNamedUser: true},
		{ID: "worker-lease-expired-reclaim", Method: "POST", Path: "/api/v1/worker/lease", Auth: "valid", Body: `{"worker_id":"worker-lease","capabilities":["ocr"]}`, PopulateExpiredJob: true},
		{ID: "worker-complete-missing-auth", Method: "POST", Path: "/api/v1/worker/complete", Body: `{"job_id":"00000000000000000000000000000004","worker_id":"worker-1","text":"ocr","engine":"tesseract"}`},
		{ID: "worker-complete-worker-token", Method: "POST", Path: "/api/v1/worker/complete", Auth: "worker", Body: `{"job_id":"00000000000000000000000000000004","worker_id":"worker-other","text":"ignored","engine":"tesseract"}`, PopulateWorkerJob: true},
		{ID: "worker-complete-missing-job", Method: "POST", Path: "/api/v1/worker/complete", Auth: "valid", Body: `{"job_id":"00000000000000000000000000000099","worker_id":"worker-1","text":"ocr","engine":"tesseract"}`},
		{ID: "worker-complete-null-strings", Method: "POST", Path: "/api/v1/worker/complete", Auth: "valid", Body: `{"job_id":null,"worker_id":null,"text":null,"engine":null,"model":null}`},
		{ID: "worker-complete-trailing-json", Method: "POST", Path: "/api/v1/worker/complete", Auth: "valid", Body: `{"job_id":"00000000000000000000000000000099"}{"ignored":true}`},
		{ID: "worker-complete-not-leased", Method: "POST", Path: "/api/v1/worker/complete", Auth: "valid", Body: `{"job_id":"00000000000000000000000000000004","worker_id":"worker-other","text":"ignored","engine":"tesseract"}`, PopulateWorkerJob: true},
		{ID: "worker-complete-invalid-json", Method: "POST", Path: "/api/v1/worker/complete", Auth: "valid", Body: "{"},
		{ID: "worker-complete-oversized", Method: "POST", Path: "/api/v1/worker/complete", Auth: "valid", Body: `{"job_id":"00000000000000000000000000000004","worker_id":"worker-1","text":"`, BodyRepeat: (24 << 20) + 1 - 76},
		{ID: "worker-complete-valid", Method: "POST", Path: "/api/v1/worker/complete", Auth: "valid", Body: `{"job_id":"00000000000000000000000000000004","worker_id":"worker-1","text":"  Invoice total: 42 EUR  ","engine":"yes","model":"123"}`, PopulateWorkerJob: true},
		{ID: "worker-complete-named-worker", Method: "POST", Path: "/api/v1/worker/complete", Auth: "named-worker", Body: `{"job_id":"00000000000000000000000000000004","worker_id":"worker-1","text":"  Invoice total: 42 EUR  ","engine":"yes","model":"123"}`, PopulateWorkerJob: true, PopulateNamedUser: true},
		{ID: "worker-fail-missing-auth", Method: "POST", Path: "/api/v1/worker/fail", Body: `{"job_id":"00000000000000000000000000000004","worker_id":"worker-1","error":"scan failed"}`},
		{ID: "worker-fail-worker-token", Method: "POST", Path: "/api/v1/worker/fail", Auth: "worker", Body: `{"job_id":"00000000000000000000000000000004","worker_id":"worker-other","error":"scan failed"}`, PopulateWorkerJob: true},
		{ID: "worker-fail-not-leased", Method: "POST", Path: "/api/v1/worker/fail", Auth: "valid", Body: `{"job_id":"00000000000000000000000000000004","worker_id":"worker-other","error":"scan failed"}`, PopulateWorkerJob: true},
		{ID: "worker-fail-valid", Method: "POST", Path: "/api/v1/worker/fail", Auth: "valid", Body: `{"job_id":"00000000000000000000000000000004","worker_id":"worker-1","error":"  scan failed \n","retry":false}`, PopulateWorkerJob: true},
		{ID: "worker-fail-named-worker", Method: "POST", Path: "/api/v1/worker/fail", Auth: "named-worker", Body: `{"job_id":"00000000000000000000000000000004","worker_id":"worker-1","error":"  scan failed \n","retry":false}`, PopulateWorkerJob: true, PopulateNamedUser: true},
		{ID: "worker-fail-retry", Method: "POST", Path: "/api/v1/worker/fail", Auth: "valid", Body: `{"job_id":"00000000000000000000000000000004","worker_id":"worker-1","error":"  retry me \n","retry":true}`, PopulateWorkerJob: true},
		{ID: "ingest-missing-auth", Method: "POST", Path: "/api/v1/ingest", MultipartFile: "report.pdf", Body: "%PDF-1.7\nfixture\n"},
		{ID: "ingest-method-not-allowed", Method: "GET", Path: "/api/v1/ingest", Auth: "valid"},
		{ID: "ingest-worker-token", Method: "POST", Path: "/api/v1/ingest", Auth: "worker", MultipartFile: "report.pdf", Body: "%PDF-1.7\nfixture\n"},
		{ID: "ingest-invalid-multipart", Method: "POST", Path: "/api/v1/ingest", Auth: "valid", Headers: map[string]string{"Content-Type": "multipart/form-data; boundary=broken"}, Body: "not multipart"},
		{ID: "ingest-missing-file", Method: "POST", Path: "/api/v1/ingest", Auth: "valid", Headers: map[string]string{"Content-Type": "multipart/form-data; boundary=empty"}, Body: "--empty--\r\n"},
		{ID: "ingest-valid", Method: "POST", Path: "/api/v1/ingest", Auth: "valid", MultipartFile: "report.pdf", Body: "%PDF-1.7\nfixture\n"},
		{ID: "shares-missing-auth", Method: "GET", Path: "/api/v1/shares"},
		{ID: "shares-wrong-auth", Method: "GET", Path: "/api/v1/shares", Auth: "wrong"},
		{ID: "shares-empty", Method: "GET", Path: "/api/v1/shares", Auth: "valid"},
		{ID: "shares-populated", Method: "GET", Path: "/api/v1/shares", Auth: "valid", PopulateShares: true},
		{ID: "shares-worker-owned", Method: "GET", Path: "/api/v1/shares", Auth: "worker", PopulateShares: true},
		{ID: "share-revoke-missing-auth", Method: "DELETE", Path: "/api/v1/share/share-old"},
		{ID: "share-revoke-wrong-auth", Method: "DELETE", Path: "/api/v1/share/share-old", Auth: "wrong"},
		{ID: "share-revoke-unknown", Method: "DELETE", Path: "/api/v1/share/does-not-exist", Auth: "valid"},
		{ID: "share-revoke-worker-nonowned", Method: "DELETE", Path: "/api/v1/share/share-old", Auth: "worker", PopulateShares: true},
		{ID: "share-revoke-worker-owned", Method: "DELETE", Path: "/api/v1/share/share-worker", Auth: "worker", PopulateShares: true},
		{ID: "share-revoke-already", Method: "DELETE", Path: "/api/v1/share/share-revoked", Auth: "valid"},
		{ID: "share-revoke-valid", Method: "DELETE", Path: "/api/v1/share/share-old", Auth: "valid"},
		{ID: "share-revoke-repeat", Method: "DELETE", Path: "/api/v1/share/share-old", Auth: "valid"},
		{ID: "share-method-not-allowed", Method: "GET", Path: "/api/v1/share", Auth: "valid"},
		{ID: "share-item-method-not-allowed", Method: "GET", Path: "/api/v1/share/does-not-exist", Auth: "valid"},
		{ID: "share-create-missing-auth", Method: "POST", Path: "/api/v1/share", Body: `{"path":"Hello.md","expiry":24}`},
		{ID: "share-create-wrong-auth", Method: "POST", Path: "/api/v1/share", Auth: "wrong", Body: `{"path":"Hello.md","expiry":24}`},
		{ID: "share-create-worker-token", Method: "POST", Path: "/api/v1/share", Auth: "worker", Body: `{"path":"Hello.md","expiry":24}`},
		{ID: "share-create-invalid-json", Method: "POST", Path: "/api/v1/share", Auth: "valid", Body: "{not-json"},
		{ID: "share-create-missing-path", Method: "POST", Path: "/api/v1/share", Auth: "valid", Body: `{"expiry":24}`},
		{ID: "share-create-traversal", Method: "POST", Path: "/api/v1/share", Auth: "valid", Body: `{"path":"../outside.md","expiry":24}`},
		{ID: "share-create-internal", Method: "POST", Path: "/api/v1/share", Auth: "valid", Body: `{"path":".symdesk/server/shares.json","expiry":24}`},
		{ID: "share-create-dataset", Method: "POST", Path: "/api/v1/share", Auth: "valid", Body: `{"path":"datasets/raw.md","expiry":24}`},
		{ID: "share-create-expiry-zero", Method: "POST", Path: "/api/v1/share", Auth: "valid", Body: `{"path":"Hello.md","expiry":0}`},
		{ID: "share-create-expiry-high", Method: "POST", Path: "/api/v1/share", Auth: "valid", Body: `{"path":"Hello.md","expiry":169}`},
		{ID: "share-create-missing-document", Method: "POST", Path: "/api/v1/share", Auth: "valid", Body: `{"path":"missing.md","expiry":24}`},
		{ID: "share-create-valid", Method: "POST", Path: "/api/v1/share", Auth: "valid", Body: `{"path":"Hello.md","expiry":24}`},
		{ID: "share-access-valid", Method: "GET", Path: "/s/share-valid-token", PopulateShareAccess: true},
		{ID: "share-access-head", Method: "HEAD", Path: "/s/share-valid-token"},
		{ID: "share-access-range", Method: "GET", Path: "/s/share-valid-token", Headers: map[string]string{"Range": "bytes=0-4"}},
		{ID: "share-access-not-modified", Method: "GET", Path: "/s/share-valid-token", Headers: map[string]string{"If-Modified-Since": "Fri, 02 Jan 2026 03:04:05 GMT"}},
		{ID: "share-access-dataset", Method: "GET", Path: "/s/share-dataset-token"},
		{ID: "share-access-missing-file", Method: "GET", Path: "/s/share-missing-token"},
		{ID: "share-access-symlink-escape", Method: "GET", Path: "/s/share-escape-token"},
		{ID: "share-access-large-file", Method: "GET", Path: "/s/share-large-token"},
		{ID: "share-access-large-range", Method: "GET", Path: "/s/share-large-token", Headers: map[string]string{"Range": "bytes=1-8388609"}},
		{ID: "share-access-invalid-first", Method: "GET", Path: "/s/unknown-share-token"},
		{ID: "share-access-expired", Method: "GET", Path: "/s/share-expired-token"},
		{ID: "share-access-revoked-threshold", Method: "GET", Path: "/s/share-revoked-token"},
		{ID: "share-access-invalid-blocked", Method: "GET", Path: "/s/unknown-share-token"},
		{ID: "shares-named-user-owned", Method: "GET", Path: "/api/v1/shares", Auth: "named", PopulateNamedUser: true, PopulateShares: true},
		{ID: "share-revoke-named-nonowned", Method: "DELETE", Path: "/api/v1/share/share-worker", Auth: "named", PopulateNamedUser: true, PopulateShares: true},
		{ID: "share-revoke-named-owned", Method: "DELETE", Path: "/api/v1/share/share-old", Auth: "named", PopulateNamedUser: true, PopulateShares: true},
		{ID: "share-create-named-user-denied", Method: "POST", Path: "/api/v1/share", Auth: "named", Body: `{"path":"Hello.md","expiry":24}`, PopulateNamedUser: true, PopulateShares: true},
		{ID: "share-create-named-worker-denied", Method: "POST", Path: "/api/v1/share", Auth: "named-worker", Body: `{"path":"nested/Named.md","expiry":24}`, PopulateNamedUser: true, PopulateShares: true},
		{ID: "share-create-named-user-valid", Method: "POST", Path: "/api/v1/share", Auth: "named", Body: `{"path":"nested/Named.md","expiry":24}`, PopulateNamedUser: true, PopulateShares: true},
	}
	cases = append(cases, ingestMultipartCases()...)
	cases = append(cases, httpCase{ID: "command-admin-exec-ls", Method: "POST", Path: "/api/v1/command", Auth: "valid", Body: `{"arguments":["ls","--dir","nested"]}`, RemoveSymlinkEscapes: true})
	return httpSuite{SchemaVersion: 1, Oracle: oracle{Commit: fixtureoracle.Current().Commit, Release: fixtureoracle.Current().Release}, Cases: cases}
}

// multipartBody joins raw parts (headers, blank line, content) with the given
// boundary and CRLF framing, so a case controls every byte of the request.
func multipartBody(boundary string, parts ...string) string {
	var b strings.Builder
	for _, part := range parts {
		b.WriteString("--" + boundary + "\r\n" + part + "\r\n")
	}
	b.WriteString("--" + boundary + "--\r\n")
	return b.String()
}

func formFile(name, filename, contentType, content string) string {
	header := fmt.Sprintf("Content-Disposition: form-data; name=%q; filename=%q\r\n", name, filename)
	if contentType != "" {
		header += "Content-Type: " + contentType + "\r\n"
	}
	return header + "\r\n" + content
}

func ingestMultipartCases() []httpCase {
	const b = "port-boundary"
	mp := map[string]string{"Content-Type": "multipart/form-data; boundary=" + b}
	post := func(id, auth, body string, headers map[string]string) httpCase {
		return httpCase{ID: id, Method: "POST", Path: "/api/v1/ingest", Auth: auth, Headers: headers, Body: body}
	}
	file := func(id, filename, contentType, content string) httpCase {
		return post(id, "valid", multipartBody(b, formFile("file", filename, contentType, content)), mp)
	}
	return []httpCase{
		file("ingest-filename-traversal", "../../etc/passwd.pdf", "application/pdf", "%PDF-1.7\n"),
		file("ingest-filename-backslash-traversal", `..\..\evil.pdf`, "application/pdf", "%PDF-1.7\n"),
		file("ingest-filename-windows-path", `C:\Users\x\scan.pdf`, "application/pdf", "%PDF-1.7\n"),
		file("ingest-filename-dot", ".", "text/plain", "dot"),
		file("ingest-filename-dotdot", "..", "text/plain", "dotdot"),
		file("ingest-filename-spaces", "   ", "text/plain", "spaces"),
		file("ingest-filename-trimmed", "  padded name.txt  ", "text/plain", "padded"),
		file("ingest-filename-unicode", "Rechnung ÄÖÜ 日本語.pdf", "application/pdf", "%PDF-1.7\n"),
		file("ingest-filename-quote", `a"b.txt`, "text/plain", "quote"),
		file("ingest-filename-control", "tab\tname.txt", "text/plain", "control"),
		file("ingest-filename-long", strings.Repeat("a", 300)+".pdf", "application/pdf", "%PDF-1.7\n"),
		file("ingest-filename-trailing-slash", "dir/", "text/plain", "slash"),
		file("ingest-filename-nested", "a/b/c.txt", "text/plain", "nested"),
		post("ingest-filename-star-encoded", "valid", multipartBody(b, "Content-Disposition: form-data; name=\"file\"; filename*=UTF-8''r%C3%A4.pdf\r\nContent-Type: application/pdf\r\n\r\n%PDF-1.7\n"), mp),
		post("ingest-filename-empty-is-not-a-file", "valid", multipartBody(b, formFile("file", "", "text/plain", "no filename")), mp),
		post("ingest-field-without-filename", "valid", multipartBody(b, "Content-Disposition: form-data; name=\"file\"\r\n\r\nplain value"), mp),
		post("ingest-wrong-field-name", "valid", multipartBody(b, formFile("upload", "report.pdf", "application/pdf", "%PDF-1.7\n")), mp),
		post("ingest-two-files-first-wins", "valid", multipartBody(b, formFile("file", "first.txt", "text/plain", "first"), formFile("file", "second.txt", "text/plain", "second")), mp),
		post("ingest-fields-before-file", "valid", multipartBody(b, "Content-Disposition: form-data; name=\"note\"\r\n\r\nhello", formFile("file", "after.txt", "text/plain", "after")), mp),
		post("ingest-other-file-then-file", "valid", multipartBody(b, formFile("other", "other.txt", "text/plain", "other"), formFile("file", "wanted.txt", "text/plain", "wanted")), mp),
		file("ingest-part-content-type-charset", "charset.txt", "text/plain; charset=iso-8859-1", "caf\xe9"),
		file("ingest-part-content-type-odd-case", "odd.txt", "TEXT/Plain", "odd"),
		file("ingest-part-no-content-type", "untyped.bin", "", "untyped"),
		file("ingest-empty-file", "empty.txt", "text/plain", ""),
		file("ingest-binary-content", "binary.bin", "application/octet-stream", "\x00\r\n--port-bound\r\n\xff\xfe--"),
		file("ingest-content-looks-like-boundary", "boundary.txt", "text/plain", "--port-boundary-not-really"),
		post("ingest-lf-only-framing", "valid", "--port-boundary\nContent-Disposition: form-data; name=\"file\"; filename=\"lf.txt\"\nContent-Type: text/plain\n\nlf body\n--port-boundary--\n", mp),
		post("ingest-quoted-boundary", "valid", multipartBody("quoted", formFile("file", "q.txt", "text/plain", "quoted")), map[string]string{"Content-Type": `multipart/form-data; boundary="quoted"`}),
		post("ingest-boundary-with-spaces", "valid", multipartBody("a b", formFile("file", "s.txt", "text/plain", "spaced")), map[string]string{"Content-Type": `multipart/form-data; boundary="a b"`}),
		post("ingest-preamble-and-epilogue", "valid", "preamble line\r\n"+multipartBody(b, formFile("file", "pre.txt", "text/plain", "pre"))+"epilogue", mp),
		post("ingest-missing-closing-boundary", "valid", "--"+b+"\r\n"+formFile("file", "open.txt", "text/plain", "never closed"), mp),
		post("ingest-missing-boundary-parameter", "valid", multipartBody(b, formFile("file", "nb.txt", "text/plain", "x")), map[string]string{"Content-Type": "multipart/form-data"}),
		post("ingest-multipart-mixed", "valid", multipartBody(b, formFile("file", "mixed.txt", "text/plain", "mixed")), map[string]string{"Content-Type": "multipart/mixed; boundary=" + b}),
		post("ingest-content-type-json", "valid", `{"file":"x"}`, map[string]string{"Content-Type": "application/json"}),
		post("ingest-content-type-missing", "valid", multipartBody(b, formFile("file", "nt.txt", "text/plain", "x")), nil),
		post("ingest-content-type-urlencoded", "valid", "file=x", map[string]string{"Content-Type": "application/x-www-form-urlencoded"}),
		post("ingest-part-header-folded", "valid", multipartBody(b, "Content-Disposition: form-data;\r\n name=\"file\"; filename=\"folded.txt\"\r\nContent-Type: text/plain\r\n\r\nfolded"), mp),
		post("ingest-part-header-lowercase", "valid", multipartBody(b, "content-disposition: form-data; name=file; filename=lower.txt\r\ncontent-type: text/plain\r\n\r\nlower"), mp),
		post("ingest-part-transfer-encoding-ignored", "valid", multipartBody(b, "Content-Disposition: form-data; name=\"file\"; filename=\"b64.txt\"\r\nContent-Transfer-Encoding: base64\r\n\r\naGVsbG8="), mp),
		{ID: "ingest-query-string-ignored", Method: "POST", Path: "/api/v1/ingest?x=1&y=%20z+w", Auth: "valid", Headers: mp, Body: multipartBody(b, formFile("file", "query.txt", "text/plain", "query"))},
		{ID: "ingest-query-bad-escape", Method: "POST", Path: "/api/v1/ingest?a=%zz", Auth: "valid", Headers: mp, Body: multipartBody(b, formFile("file", "query.txt", "text/plain", "query"))},
		{ID: "ingest-query-semicolon", Method: "POST", Path: "/api/v1/ingest?a=1;b=2", Auth: "valid", Headers: mp, Body: multipartBody(b, formFile("file", "query.txt", "text/plain", "query"))},
		{ID: "ingest-query-truncated-escape", Method: "POST", Path: "/api/v1/ingest?a=%2", Auth: "valid", Headers: mp, Body: multipartBody(b, formFile("file", "query.txt", "text/plain", "query"))},
		{ID: "ingest-over-body-limit", Method: "POST", Path: "/api/v1/ingest", Auth: "valid", Headers: mp, Body: "--" + b + "\r\n" + formFile("file", "huge.bin", "application/octet-stream", ""), BodyRepeat: 101 << 20},
		{ID: "ingest-file-over-upload-limit", Method: "POST", Path: "/api/v1/ingest", Auth: "valid", MultipartFile: "limit.bin", BodyRepeat: 100<<20 + 1},
		{ID: "ingest-file-at-upload-limit", Method: "POST", Path: "/api/v1/ingest", Auth: "valid", MultipartFile: "limit.bin", BodyRepeat: 100 << 20},
		{ID: "ingest-named-user-forbidden", Method: "POST", Path: "/api/v1/ingest", Auth: "named", MultipartFile: "report.pdf", Body: "%PDF-1.7\nfixture\n", PopulateNamedUser: true},
		{ID: "ingest-named-worker-forbidden", Method: "POST", Path: "/api/v1/ingest", Auth: "named-worker", MultipartFile: "report.pdf", Body: "%PDF-1.7\nfixture\n", PopulateNamedUser: true},
		{ID: "ingest-wrong-auth", Method: "POST", Path: "/api/v1/ingest", Auth: "wrong", MultipartFile: "report.pdf", Body: "%PDF-1.7\nfixture\n"},
		{ID: "ingest-head-method", Method: "HEAD", Path: "/api/v1/ingest", Auth: "valid"},
		{ID: "ingest-put-method", Method: "PUT", Path: "/api/v1/ingest", Auth: "valid", MultipartFile: "report.pdf", Body: "x"},
		{ID: "ingest-trailing-slash-path", Method: "POST", Path: "/api/v1/ingest/", Auth: "valid", MultipartFile: "report.pdf", Body: "x"},
		{ID: "jobs-retry-named-user-forbidden", Method: "POST", Path: "/api/v1/jobs/retry?id=00000000000000000000000000000003", Auth: "named", PopulateNamedUser: true, PopulateJobs: true},
		{ID: "jobs-retry-named-worker-forbidden", Method: "POST", Path: "/api/v1/jobs/retry?id=00000000000000000000000000000003", Auth: "named-worker", PopulateNamedUser: true, PopulateJobs: true},
		{ID: "worker-lease-named-user-allowed", Method: "POST", Path: "/api/v1/worker/lease", Auth: "named", Body: `{"worker_id":"worker-lease","capabilities":["ocr"]}`, PopulateNamedUser: true, PopulateJobs: true},
		{ID: "worker-input-named-user-allowed", Method: "GET", Path: "/api/v1/worker/input?id=00000000000000000000000000000003", Auth: "named", PopulateNamedUser: true, PopulateJobs: true},
		{ID: "worker-complete-named-user-allowed", Method: "POST", Path: "/api/v1/worker/complete", Auth: "named", Body: `{"job_id":"00000000000000000000000000000004","worker_id":"worker-1","text":"  Invoice total: 42 EUR  ","engine":"yes","model":"123"}`, PopulateNamedUser: true, PopulateWorkerJob: true},
		{ID: "worker-fail-named-user-allowed", Method: "POST", Path: "/api/v1/worker/fail", Auth: "named", Body: `{"job_id":"00000000000000000000000000000004","worker_id":"worker-1","error":"  scan failed \n","retry":false}`, PopulateNamedUser: true, PopulateWorkerJob: true},
	}
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
