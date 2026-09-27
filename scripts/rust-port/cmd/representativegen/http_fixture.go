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
	return httpSuite{SchemaVersion: 1, Oracle: oracle{Commit: "8fc4b67fcd84468f91ede73774ca6adf3e1fec99", Release: "post-v0.12.2-security-880+share-token-8fc4b67f"}, Cases: []httpCase{
		{ID: "healthz", Method: "GET", Path: "/healthz"},
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
		{ID: "jobs-retry-nonfailed", Method: "POST", Path: "/api/v1/jobs/retry?id=00000000000000000000000000000001", Auth: "valid"},
		{ID: "jobs-retry-failed", Method: "POST", Path: "/api/v1/jobs/retry?id=00000000000000000000000000000003", Auth: "valid"},
		{ID: "worker-input-missing-auth", Method: "GET", Path: "/api/v1/worker/input?id=00000000000000000000000000000003"},
		{ID: "worker-input-worker-token", Method: "GET", Path: "/api/v1/worker/input?id=00000000000000000000000000000003", Auth: "worker"},
		{ID: "worker-input-missing-job", Method: "GET", Path: "/api/v1/worker/input?id=00000000000000000000000000000003", Auth: "valid"},
		{ID: "worker-input-valid", Method: "GET", Path: "/api/v1/worker/input?id=00000000000000000000000000000003", Auth: "valid", PopulateJobs: true},
		{ID: "worker-input-named-worker", Method: "GET", Path: "/api/v1/worker/input?id=00000000000000000000000000000003", Auth: "named-worker", PopulateJobs: true, PopulateNamedUser: true},
		{ID: "worker-input-head-range", Method: "HEAD", Path: "/api/v1/worker/input?id=00000000000000000000000000000003", Auth: "valid", Headers: map[string]string{"Range": "bytes=0-5"}, PopulateJobs: true},
		{ID: "worker-lease-missing-auth", Method: "POST", Path: "/api/v1/worker/lease", Body: `{"worker_id":"worker-lease","capabilities":["ocr"]}`},
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
