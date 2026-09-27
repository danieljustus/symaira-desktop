// Command httpdiff compares the representative HTTP transcript against Go and Rust.
package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
	_ "modernc.org/sqlite"
)

const token = "0123456789abcdef0123456789abcdef"

type fixture struct {
	Cases []httpCase `json:"cases"`
}

type httpCase struct {
	ID                  string            `json:"id"`
	Method              string            `json:"method"`
	Path                string            `json:"path"`
	Auth                string            `json:"auth,omitempty"`
	Headers             map[string]string `json:"headers,omitempty"`
	Body                string            `json:"body,omitempty"`
	BodyRepeat          int               `json:"body_repeat,omitempty"`
	MultipartFile       string            `json:"multipart_file,omitempty"`
	EmptyNotebooks      bool              `json:"empty_notebooks,omitempty"`
	PopulateJobs        bool              `json:"populate_jobs,omitempty"`
	PopulateWorkerJob   bool              `json:"populate_worker_job,omitempty"`
	PopulateExpiredJob  bool              `json:"populate_expired_job,omitempty"`
	PopulateShares      bool              `json:"populate_shares,omitempty"`
	PopulateShareAccess bool              `json:"populate_share_access,omitempty"`
}

type transcript struct {
	Status  int
	Headers map[string]string
	Body    []byte
}

type boundedBuffer struct {
	mu       sync.Mutex
	data     []byte
	overflow bool
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	const limit = 1 << 20
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.data) < limit {
		remaining := limit - len(b.data)
		if len(data) > remaining {
			b.data = append(b.data, data[:remaining]...)
			b.overflow = true
		} else {
			b.data = append(b.data, data...)
		}
	} else if len(data) > 0 {
		b.overflow = true
	}
	return len(data), nil
}

func (b *boundedBuffer) snapshot() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data), b.overflow
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL %v\n", err)
		os.Exit(1)
	}
}

func run() (runErr error) {
	defer func() {
		if value := recover(); value != nil {
			panicErr := fmt.Errorf("%v", value)
			if runErr == nil {
				runErr = panicErr
			} else {
				runErr = errors.Join(runErr, panicErr)
			}
		}
	}()
	left := flag.String("left", "", "Go oracle binary")
	right := flag.String("right", "", "Rust candidate binary")
	fixturePath := flag.String("fixture", "testdata/port/http/representative.json", "fixture path")
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
	if len(suite.Cases) == 0 {
		fatal("HTTP fixture is empty")
	}

	harnessRoot, err := os.MkdirTemp("", "symdesk-http-diff-")
	if err != nil {
		fatal("temp root: %v", err)
	}
	defer func() { _ = os.RemoveAll(harnessRoot) }()
	leftVault := createFixtureVault(filepath.Join(harnessRoot, "go"))
	rightVault := createFixtureVault(filepath.Join(harnessRoot, "rust"))
	leftServer := startServer(*left, leftVault)
	defer func() {
		if err := leftServer.stop(); err != nil {
			cleanupErr := fmt.Errorf("Go cleanup failed: %w", err) //nolint:staticcheck // Go is the implementation label
			if runErr == nil {
				runErr = cleanupErr
			} else {
				runErr = errors.Join(runErr, cleanupErr)
			}
		}
	}()
	rightServer := startServer(*right, rightVault)
	defer func() {
		if err := rightServer.stop(); err != nil {
			cleanupErr := fmt.Errorf("Rust cleanup failed: %w", err) //nolint:staticcheck // Rust is the implementation label
			if runErr == nil {
				runErr = cleanupErr
			} else {
				runErr = errors.Join(runErr, cleanupErr)
			}
		}
	}()
	if err := leftServer.ready(); err != nil {
		fatal("Go readiness: %v", err)
	}
	if err := rightServer.ready(); err != nil {
		fatal("Rust readiness: %v", err)
	}
	leftETag, rightETag := "", ""
	for _, tc := range suite.Cases {
		if tc.PopulateShares {
			for _, vault := range []string{leftVault, rightVault} {
				if err := populateShares(vault); err != nil {
					fatal("populate share fixture: %v", err)
				}
			}
		}
		if tc.PopulateShareAccess {
			for _, vault := range []string{leftVault, rightVault} {
				if err := populateShareAccess(vault); err != nil {
					fatal("populate share access fixture: %v", err)
				}
			}
		}
		if tc.PopulateJobs {
			for _, vault := range []string{leftVault, rightVault} {
				if err := populateJobs(vault); err != nil {
					fatal("populate job fixture: %v", err)
				}
			}
		}
		if tc.PopulateWorkerJob {
			for _, vault := range []string{leftVault, rightVault} {
				if err := populateWorkerJob(vault); err != nil {
					fatal("populate worker job fixture: %v", err)
				}
			}
		}
		if tc.PopulateExpiredJob {
			for _, vault := range []string{leftVault, rightVault} {
				if err := populateExpiredJob(vault); err != nil {
					fatal("populate expired worker job: %v", err)
				}
			}
		}
		if tc.EmptyNotebooks {
			for _, vault := range []string{leftVault, rightVault} {
				notebooks := filepath.Join(vault, "notebooks")
				if err := os.RemoveAll(notebooks); err != nil {
					fatal("clear notebook fixture: %v", err)
				}
				if err := os.Mkdir(notebooks, 0o700); err != nil {
					fatal("empty notebook directory: %v", err)
				}
			}
		}
		leftResult, nextLeftETag, err := leftServer.request(tc, leftETag)
		if err != nil {
			fatal("%s Go request: %v", tc.ID, err)
		}
		rightResult, nextRightETag, err := rightServer.request(tc, rightETag)
		if err != nil {
			fatal("%s Rust request: %v", tc.ID, err)
		}
		if err := compare(tc.ID, leftResult, rightResult); err != nil {
			fatal("%s: %v", tc.ID, err)
		}
		if tc.ID == "shares-populated" {
			for _, item := range []struct {
				name, vault string
				body        []byte
			}{{"Go", leftVault, leftResult.Body}, {"Rust", rightVault, rightResult.Body}} {
				if bytes.Contains(item.body, []byte("fixture-hash")) || bytes.Contains(item.body, []byte(`"token":`)) {
					fatal("%s %s leaked a share token or hash", tc.ID, item.name)
				}
				if err := assertSharesUnchanged(item.vault); err != nil {
					fatal("%s %s persistence: %v", tc.ID, item.name, err)
				}
			}
		}
		if tc.ID == "share-revoke-valid" {
			leftStore, err := assertedRevokedShare(leftVault)
			if err != nil {
				fatal("%s Go persistence: %v", tc.ID, err)
			}
			rightStore, err := assertedRevokedShare(rightVault)
			if err != nil {
				fatal("%s Rust persistence: %v", tc.ID, err)
			}
			if !reflect.DeepEqual(leftStore, rightStore) {
				fatal("%s persisted share stores differ", tc.ID)
			}
		}
		if tc.ID == "share-create-valid" {
			leftStore, err := assertedCreatedShare(leftVault, leftResult.Body)
			if err != nil {
				fatal("%s Go persistence: %v", tc.ID, err)
			}
			rightStore, err := assertedCreatedShare(rightVault, rightResult.Body)
			if err != nil {
				fatal("%s Rust persistence: %v", tc.ID, err)
			}
			if !reflect.DeepEqual(leftStore, rightStore) {
				fatal("%s persisted share stores differ", tc.ID)
			}
		}
		if tc.ID == "jobs-retry-failed" {
			leftJob, err := retriedJobFile(leftVault)
			if err != nil {
				fatal("%s Go persistence: %v", tc.ID, err)
			}
			rightJob, err := retriedJobFile(rightVault)
			if err != nil {
				fatal("%s Rust persistence: %v", tc.ID, err)
			}
			if !bytes.Equal(leftJob, rightJob) {
				fatal("%s persisted job differs: Go=%q Rust=%q", tc.ID, leftJob, rightJob)
			}
		}
		if tc.ID == "worker-fail-valid" || tc.ID == "worker-fail-retry" {
			retry := tc.ID == "worker-fail-retry"
			leftJob, err := failedWorkerJob(leftVault, leftResult.Body, retry)
			if err != nil {
				fatal("%s Go persistence: %v", tc.ID, err)
			}
			rightJob, err := failedWorkerJob(rightVault, rightResult.Body, retry)
			if err != nil {
				fatal("%s Rust persistence: %v", tc.ID, err)
			}
			leftJob, err = normalizeJobUpdatedAt(leftJob)
			if err != nil {
				fatal("%s Go timestamp: %v", tc.ID, err)
			}
			rightJob, err = normalizeJobUpdatedAt(rightJob)
			if err != nil {
				fatal("%s Rust timestamp: %v", tc.ID, err)
			}
			if !bytes.Equal(leftJob, rightJob) {
				fatal("%s persisted job differs: Go=%q Rust=%q", tc.ID, leftJob, rightJob)
			}
		}
		if tc.ID == "worker-lease-valid" || tc.ID == "worker-lease-expired-reclaim" {
			jobID := "00000000000000000000000000000001"
			if tc.ID == "worker-lease-expired-reclaim" {
				jobID = "00000000000000000000000000000005"
			}
			leftJob, err := leasedWorkerJob(leftVault, leftResult.Body, jobID)
			if err != nil {
				fatal("%s Go persistence: %v", tc.ID, err)
			}
			rightJob, err := leasedWorkerJob(rightVault, rightResult.Body, jobID)
			if err != nil {
				fatal("%s Rust persistence: %v", tc.ID, err)
			}
			leftJob, err = normalizeJobLeaseTimes(leftJob)
			if err != nil {
				fatal("%s Go timestamps: %v", tc.ID, err)
			}
			rightJob, err = normalizeJobLeaseTimes(rightJob)
			if err != nil {
				fatal("%s Rust timestamps: %v", tc.ID, err)
			}
			if !bytes.Equal(leftJob, rightJob) {
				fatal("%s persisted job differs: Go=%q Rust=%q", tc.ID, leftJob, rightJob)
			}
		}
		if tc.ID == "worker-complete-valid" {
			leftJob, err := completedWorkerJob(leftVault, leftResult.Body)
			if err != nil {
				fatal("%s Go persistence: %v", tc.ID, err)
			}
			rightJob, err := completedWorkerJob(rightVault, rightResult.Body)
			if err != nil {
				fatal("%s Rust persistence: %v", tc.ID, err)
			}
			if !bytes.Equal(leftJob, rightJob) {
				fatal("%s persisted job differs: Go=%q Rust=%q", tc.ID, leftJob, rightJob)
			}
			leftNote, err := completedNote(leftVault, leftResult.Body)
			if err != nil {
				fatal("%s Go note: %v", tc.ID, err)
			}
			rightNote, err := completedNote(rightVault, rightResult.Body)
			if err != nil {
				fatal("%s Rust note: %v", tc.ID, err)
			}
			if !bytes.Equal(leftNote, rightNote) {
				fatal("%s note semantics differ: Go=%q Rust=%q", tc.ID, leftNote, rightNote)
			}
		}
		if tc.ID == "ingest-valid" {
			for _, item := range []struct {
				name, vault string
				response    transcript
			}{{"Go", leftVault, leftResult}, {"Rust", rightVault, rightResult}} {
				if err := assertIngestWrite(item.vault, item.response.Body, tc.Body); err != nil {
					fatal("%s %s persistence: %v", tc.ID, item.name, err)
				}
			}
		}
		if tc.ID == "file-put-symlink-parent" {
			for _, root := range []string{filepath.Dir(leftVault), filepath.Dir(rightVault)} {
				if _, err := os.Stat(filepath.Join(root, "new.md")); !errors.Is(err, os.ErrNotExist) {
					fatal("%s wrote outside vault %s: %v", tc.ID, root, err)
				}
			}
		}
		if tc.ID == "file-put-create" || tc.ID == "file-put-update" {
			for _, vault := range []string{leftVault, rightVault} {
				if err := assertIndexedWrite(vault, "nested/Created.md", tc.Body); err != nil {
					fatal("%s side effect: %v", tc.ID, err)
				}
			}
		}
		leftETag, rightETag = nextLeftETag, nextRightETag
		fmt.Printf("PASS %s\n", tc.ID)
	}
	fmt.Printf("PASS HTTP differential: %d cases; isolated roots, loopback ports, readiness, harness bounds and shutdown verified\n", len(suite.Cases))
	return nil
}

func createFixtureVault(root string) string {
	vault := filepath.Join(root, "vault")
	for _, dir := range []string{vault, filepath.Join(vault, "notebooks"), filepath.Join(vault, "nested"), filepath.Join(vault, "inbox")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			fatal("fixture directory: %v", err)
		}
	}
	files := map[string]string{
		"notebooks/research.md": "---\ntype: notebook\ntitle: Research\ncreated: 2026-01-02T03:04:05Z\nnotebook_id: research\ndescription: Research notes\nsources:\n  - Hello.md\n---\n",
		"notebooks/archive.md":  "---\ntype: notebook\ntitle: Archive\ncreated: 2026-01-03T04:05:06Z\nnotebook_id: archive\nsources: []\n---\n",
		"notebooks/mixed.md":    "---\ntype: notebook\ntitle: Mixed\ncreated: 2026-01-04T05:06:07Z\nnotebook_id: mixed\nsources:\n  - Hello.md\n  - missing.md\n  - escape.md\n  - ../outside.md\n---\n",
		"notebooks/ignored.md":  "---\ntype: note\ntitle: Not a notebook\n---\n",
		"Hello.md":              "---\ntitle: Hello\n---\nBody",
		"nested/Note.md":        "nested",
		"inbox/c.png":           "worker input bytes",
	}
	modified := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	for name, body := range files {
		path := filepath.Join(vault, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			fatal("fixture file: %v", err)
		}
		if err := os.Chtimes(path, modified, modified); err != nil {
			fatal("fixture timestamp: %v", err)
		}
	}
	outside := filepath.Join(root, "outside.md")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		fatal("outside fixture: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(vault, "escape.md")); err != nil {
		fatal("fixture symlink: %v", err)
	}
	if err := os.Symlink(root, filepath.Join(vault, "escape-dir")); err != nil {
		fatal("fixture parent symlink: %v", err)
	}
	if err := os.Symlink("Hello.md", filepath.Join(vault, "internal.md")); err != nil {
		fatal("fixture internal symlink: %v", err)
	}
	return vault
}

func populateJobs(vault string) error {
	dir := filepath.Join(vault, ".symdesk", "server", "jobs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for _, job := range []struct{ id, body string }{
		{"00000000000000000000000000000001", `{"id":"00000000000000000000000000000001","schema_version":1,"status":"pending","source_path":"inbox/a.png","original_name":"a.png","capability":"ocr","created_at":"2026-01-02T03:04:05Z","updated_at":"2026-01-02T03:04:05Z"}`},
		{"00000000000000000000000000000002", `{"id":"00000000000000000000000000000002","schema_version":1,"status":"completed","source_path":"inbox/b.png","original_name":"b.png","capability":"ocr","created_at":"2026-01-03T03:04:05Z","updated_at":"2026-01-03T04:05:06Z"}`},
		{"00000000000000000000000000000003", `{"id":"00000000000000000000000000000003","schema_version":1,"status":"failed","source_path":"inbox/c.png","original_name":"c.png","capability":"ocr","worker_id":"worker-a","error":"temporary failure","lease_until":"2026-01-04T04:05:06Z","created_at":"2026-01-04T03:04:05Z","updated_at":"2026-01-04T04:05:06Z"}`},
	} {
		if err := os.WriteFile(filepath.Join(dir, job.id+".json"), []byte(job.body), 0o600); err != nil {
			return err
		}
	}
	return nil
}

func populateWorkerJob(vault string) error {
	dir := filepath.Join(vault, ".symdesk", "server", "jobs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	const id = "00000000000000000000000000000004"
	const body = `{"id":"00000000000000000000000000000004","schema_version":1,"status":"processing","source_path":"inbox/c.png","original_name":"c.png","capability":"ocr","worker_id":"worker-1","lease_until":"2099-01-02T03:04:05Z","created_at":"2026-01-05T03:04:05Z","updated_at":"2026-01-05T03:04:05Z"}`
	return os.WriteFile(filepath.Join(dir, id+".json"), []byte(body), 0o600)
}

func populateExpiredJob(vault string) error {
	dir := filepath.Join(vault, ".symdesk", "server", "jobs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	const id = "00000000000000000000000000000005"
	const body = `{"id":"00000000000000000000000000000005","schema_version":1,"status":"processing","source_path":"inbox/expired.png","original_name":"expired.png","capability":"ocr","worker_id":"worker-old","error":"stale failure","lease_until":"2000-01-02T03:04:05Z","created_at":"2026-01-01T03:04:05Z","updated_at":"2026-01-01T03:04:05Z"}`
	return os.WriteFile(filepath.Join(dir, id+".json"), []byte(body), 0o600)
}

const shareFixture = `[{"id":"share-old","path":"Hello.md","created_by":"alice","created_at":"2026-01-02T03:04:05Z","expires_at":"2099-01-02T03:04:05Z","token_hash":"fixture-hash-old"},{"id":"share-expired","path":"nested/Note.md","created_by":"bob","created_at":"2026-01-03T03:04:05Z","expires_at":"2026-01-04T03:04:05Z","token_hash":"fixture-hash-expired"},{"id":"share-revoked","path":"Hello.md","created_by":"alice","created_at":"2026-01-04T03:04:05Z","expires_at":"2099-01-04T03:04:05Z","token_hash":"fixture-hash-revoked","expired":true,"revoked_at":"2026-01-05T03:04:05Z"}]`

func shareFixturePath(vault string) string {
	return filepath.Join(vault, ".symdesk", "server", "shares.json")
}

func populateShares(vault string) error {
	path := shareFixturePath(vault)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(shareFixture), 0o600)
}

const (
	validShareToken   = "share-valid-token"
	expiredShareToken = "share-expired-token"
	revokedShareToken = "share-revoked-token"
	datasetShareToken = "share-dataset-token"
	missingShareToken = "share-missing-token"
	escapeShareToken  = "share-escape-token"
	largeShareToken   = "share-large-token"
)

func populateShareAccess(vault string) error {
	tokenHash := func(token string) string {
		sum := sha256.Sum256([]byte(token))
		return hex.EncodeToString(sum[:])
	}
	links := []map[string]any{
		{"id": "access-valid", "path": "Hello.md", "created_by": "admin", "created_at": "2026-01-01T00:00:00Z", "expires_at": "2099-01-01T00:00:00Z", "token_hash": tokenHash(validShareToken)},
		{"id": "access-expired", "path": "Hello.md", "created_by": "admin", "created_at": "2026-01-01T00:00:00Z", "expires_at": "2026-01-02T00:00:00Z", "token_hash": tokenHash(expiredShareToken)},
		{"id": "access-revoked", "path": "Hello.md", "created_by": "admin", "created_at": "2026-01-01T00:00:00Z", "expires_at": "2099-01-01T00:00:00Z", "token_hash": tokenHash(revokedShareToken), "expired": true, "revoked_at": "2026-01-02T00:00:00Z"},
		{"id": "access-dataset", "path": "datasets/restricted.md", "created_by": "admin", "created_at": "2026-01-01T00:00:00Z", "expires_at": "2099-01-01T00:00:00Z", "token_hash": tokenHash(datasetShareToken)},
		{"id": "access-missing", "path": "missing.md", "created_by": "admin", "created_at": "2026-01-01T00:00:00Z", "expires_at": "2099-01-01T00:00:00Z", "token_hash": tokenHash(missingShareToken)},
		{"id": "access-escape", "path": "escape.md", "created_by": "admin", "created_at": "2026-01-01T00:00:00Z", "expires_at": "2099-01-01T00:00:00Z", "token_hash": tokenHash(escapeShareToken)},
		{"id": "access-large", "path": "large.bin", "created_by": "admin", "created_at": "2026-01-01T00:00:00Z", "expires_at": "2099-01-01T00:00:00Z", "token_hash": tokenHash(largeShareToken)},
	}
	data, err := json.Marshal(links)
	if err != nil {
		return err
	}
	dir := filepath.Dir(shareFixturePath(vault))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(vault, "datasets"), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(vault, "datasets", "restricted.md"), []byte("private dataset"), 0o600); err != nil {
		return err
	}
	largeFile := filepath.Join(vault, "large.bin")
	if err := os.WriteFile(largeFile, []byte{1}, 0o600); err != nil {
		return err
	}
	if err := os.Truncate(largeFile, (8<<20)+2); err != nil {
		return err
	}
	return os.WriteFile(shareFixturePath(vault), data, 0o600)
}

func assertSharesUnchanged(vault string) error {
	actual, err := os.ReadFile(shareFixturePath(vault))
	if err != nil {
		return err
	}
	if string(actual) != shareFixture {
		return fmt.Errorf("share store changed during listing")
	}
	return nil
}

func assertedRevokedShare(vault string) ([]map[string]any, error) {
	path := shareFixturePath(vault)
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("share store mode = %o, want 600", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var links []map[string]any
	if err := json.Unmarshal(data, &links); err != nil {
		return nil, err
	}
	if len(links) != 3 || links[0]["id"] != "share-old" || links[0]["expired"] != true || links[0]["token_hash"] != "fixture-hash-old" {
		return nil, fmt.Errorf("unexpected revoked share state")
	}
	revoked, _ := links[0]["revoked_at"].(string)
	when, err := time.Parse(time.RFC3339Nano, revoked)
	if err != nil || time.Since(when) > time.Minute || time.Until(when) > time.Minute {
		return nil, fmt.Errorf("revocation time is not current RFC3339: %q", revoked)
	}
	links[0]["revoked_at"] = "<dynamic>"
	return links, nil
}

type createdShare struct {
	ID        string `json:"id"`
	Token     string `json:"token"`
	Path      string `json:"path"`
	CreatedAt string `json:"created_at"`
	ExpiresAt string `json:"expires_at"`
	URL       string `json:"url"`
}

func parseCreatedShare(body []byte) (createdShare, error) {
	var share createdShare
	if err := json.Unmarshal(body, &share); err != nil {
		return share, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || len(fields) != 6 {
		return share, fmt.Errorf("unexpected share response fields")
	}
	if len(share.ID) != 24 || len(share.Token) != 64 || share.Path != "Hello.md" || share.URL != "/s/"+share.Token {
		return share, fmt.Errorf("invalid share response: %q", body)
	}
	if _, err := hex.DecodeString(share.ID); err != nil {
		return share, err
	}
	if _, err := hex.DecodeString(share.Token); err != nil {
		return share, err
	}
	created, err := time.Parse(time.RFC3339Nano, share.CreatedAt)
	if err != nil || time.Since(created) > time.Minute || time.Until(created) > time.Minute {
		return share, fmt.Errorf("invalid share created_at %q", share.CreatedAt)
	}
	expires, err := time.Parse(time.RFC3339Nano, share.ExpiresAt)
	if err != nil || expires.Sub(created) != 24*time.Hour {
		return share, fmt.Errorf("invalid share expires_at %q", share.ExpiresAt)
	}
	return share, nil
}

func normalizeCreatedShare(body []byte) ([]byte, error) {
	if _, err := parseCreatedShare(body); err != nil {
		return nil, err
	}
	return []byte(`{"id":"<dynamic>","token":"<dynamic>","path":"Hello.md","created_at":"<dynamic>","expires_at":"<dynamic>","url":"/s/<dynamic>"}`), nil
}

func assertedCreatedShare(vault string, response []byte) ([]map[string]any, error) {
	share, err := parseCreatedShare(response)
	if err != nil {
		return nil, err
	}
	path := shareFixturePath(vault)
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("share store mode = %o, want 600", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var links []map[string]any
	if err := json.Unmarshal(data, &links); err != nil {
		return nil, err
	}
	if len(links) != 4 {
		return nil, fmt.Errorf("share store count = %d, want 4", len(links))
	}
	link := links[3]
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(share.Token)))
	if link["id"] != share.ID || link["path"] != share.Path || link["created_by"] != "admin" || link["created_at"] != share.CreatedAt || link["expires_at"] != share.ExpiresAt || link["token_hash"] != hash || link["token"] != nil || link["expired"] != nil {
		return nil, fmt.Errorf("share store has unexpected created link")
	}
	links[0]["revoked_at"] = "<dynamic>"
	for _, field := range []string{"id", "created_at", "expires_at", "token_hash"} {
		link[field] = "<dynamic>"
	}
	return links, nil
}

func retriedJobFile(vault string) ([]byte, error) {
	path := filepath.Join(vault, ".symdesk", "server", "jobs", "00000000000000000000000000000003.json")
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("job mode = %o, want 600", info.Mode().Perm())
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var job map[string]any
	if err := json.Unmarshal(body, &job); err != nil {
		return nil, err
	}
	if job["status"] != "pending" || job["worker_id"] != nil || job["lease_until"] != nil || job["error"] != nil {
		return nil, fmt.Errorf("retry left wrong state: %q", body)
	}
	return normalizeJobUpdatedAt(body)
}

func failedWorkerJob(vault string, response []byte, retry bool) ([]byte, error) {
	path := filepath.Join(vault, ".symdesk", "server", "jobs", "00000000000000000000000000000004.json")
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("job mode = %o, want 600", info.Mode().Perm())
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var responseJob, persistedJob map[string]any
	if err := json.Unmarshal(response, &responseJob); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(persisted, &persistedJob); err != nil {
		return nil, err
	}
	wantStatus, wantError := "failed", "scan failed"
	wantWorker := any("worker-1")
	if retry {
		wantStatus, wantError, wantWorker = "pending", "retry me", nil
	}
	for _, job := range []map[string]any{responseJob, persistedJob} {
		if job["status"] != wantStatus || job["error"] != wantError || job["worker_id"] != wantWorker || job["lease_until"] != nil {
			return nil, fmt.Errorf("wrong worker failure state: %q", response)
		}
	}
	if !reflect.DeepEqual(responseJob, persistedJob) {
		return nil, fmt.Errorf("response and persisted worker job differ")
	}
	return persisted, nil
}

func leasedWorkerJob(vault string, response []byte, jobID string) ([]byte, error) {
	path := filepath.Join(vault, ".symdesk", "server", "jobs", jobID+".json")
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("job mode = %o, want 600", info.Mode().Perm())
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var responseJob, persistedJob map[string]any
	if err := json.Unmarshal(response, &responseJob); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(persisted, &persistedJob); err != nil {
		return nil, err
	}
	for _, job := range []map[string]any{responseJob, persistedJob} {
		if job["id"] != jobID || job["status"] != "processing" || job["worker_id"] != "worker-lease" || job["capability"] != "ocr" {
			return nil, fmt.Errorf("wrong worker lease state: %q", response)
		}
		if jobID == "00000000000000000000000000000005" && job["error"] != nil {
			return nil, fmt.Errorf("expired worker error was not cleared")
		}
		updatedText, ok := job["updated_at"].(string)
		if !ok {
			return nil, fmt.Errorf("updated_at is not a string: %v", job["updated_at"])
		}
		updated, err := time.Parse(time.RFC3339Nano, updatedText)
		if err != nil || time.Since(updated) > time.Minute || time.Until(updated) > time.Minute {
			return nil, fmt.Errorf("updated_at is not current RFC3339: %v", job["updated_at"])
		}
		leaseUntilText, ok := job["lease_until"].(string)
		if !ok {
			return nil, fmt.Errorf("lease_until is not a string: %v", job["lease_until"])
		}
		leaseUntil, err := time.Parse(time.RFC3339Nano, leaseUntilText)
		if err != nil || time.Until(leaseUntil) < 14*time.Minute || time.Until(leaseUntil) > 16*time.Minute {
			return nil, fmt.Errorf("lease_until is not 15 minutes ahead: %v", job["lease_until"])
		}
	}
	if !reflect.DeepEqual(responseJob, persistedJob) {
		return nil, fmt.Errorf("response and persisted worker job differ")
	}
	return persisted, nil
}

func completedWorkerJob(vault string, response []byte) ([]byte, error) {
	path := filepath.Join(vault, ".symdesk", "server", "jobs", "00000000000000000000000000000004.json")
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("job mode = %o, want 600", info.Mode().Perm())
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var responseJob, persistedJob map[string]any
	if err := json.Unmarshal(response, &responseJob); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(persisted, &persistedJob); err != nil {
		return nil, err
	}
	for _, job := range []map[string]any{responseJob, persistedJob} {
		if job["id"] != "00000000000000000000000000000004" || job["status"] != "completed" || job["worker_id"] != "worker-1" || job["engine"] != "ollama" || job["model"] != "gemma3" || job["note_path"] != "inbox/c-00000000.md" || job["lease_until"] != nil || job["error"] != nil {
			return nil, fmt.Errorf("wrong worker completion state: %q", response)
		}
		updatedText, ok := job["updated_at"].(string)
		if !ok {
			return nil, fmt.Errorf("updated_at is not a string: %v", job["updated_at"])
		}
		updated, err := time.Parse(time.RFC3339Nano, updatedText)
		if err != nil || time.Since(updated) > time.Minute || time.Until(updated) > time.Minute {
			return nil, fmt.Errorf("updated_at is not current RFC3339: %q", updatedText)
		}
	}
	if !reflect.DeepEqual(responseJob, persistedJob) {
		return nil, fmt.Errorf("response and persisted worker job differ")
	}
	return normalizeJobUpdatedAt(persisted)
}

func completedNote(vault string, response []byte) ([]byte, error) {
	var job struct {
		NotePath string `json:"note_path"`
	}
	if err := json.Unmarshal(response, &job); err != nil {
		return nil, err
	}
	if job.NotePath != "inbox/c-00000000.md" {
		return nil, fmt.Errorf("note_path = %q", job.NotePath)
	}
	path := filepath.Join(vault, filepath.FromSlash(job.NotePath))
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o644 {
		return nil, fmt.Errorf("note mode = %o, want 644", info.Mode().Perm())
	}
	note, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	parts := bytes.SplitN(note, []byte("\n---\n"), 2)
	if len(parts) != 2 || !bytes.HasPrefix(parts[0], []byte("---\n")) {
		return nil, fmt.Errorf("note has invalid frontmatter: %q", note)
	}
	frontmatter := make(map[string]any)
	if err := yaml.Unmarshal(bytes.TrimPrefix(parts[0], []byte("---\n")), &frontmatter); err != nil {
		return nil, err
	}
	created := frontmatter["created"]
	var createdAt time.Time
	switch value := created.(type) {
	case time.Time:
		createdAt = value
	case string:
		createdAt, err = time.Parse(time.RFC3339, value)
	default:
		return nil, fmt.Errorf("created has unexpected YAML type %T", created)
	}
	if err != nil || time.Since(createdAt) > 5*time.Minute || time.Until(createdAt) > 5*time.Minute {
		return nil, fmt.Errorf("created is not a current timestamp: %v", created)
	}
	if frontmatter["title"] != "c" || frontmatter["archive_path"] != "inbox/c.png" || frontmatter["confidence"] != 0 || frontmatter["ocr_engine"] != "ollama" || frontmatter["ocr_model"] != "gemma3" || frontmatter["status"] != "needs_review" {
		return nil, fmt.Errorf("unexpected note frontmatter: %v", frontmatter)
	}
	frontmatter["created"] = "<dynamic>"
	body := parts[1]
	wantBody := []byte("\n![[inbox/c.png]]\n\n## OCR text\n\nInvoice total: 42 EUR\n")
	if !bytes.Equal(body, wantBody) {
		return nil, fmt.Errorf("unexpected note body: %q", body)
	}
	if err := assertIndexedWrite(vault, job.NotePath, string(note)); err != nil {
		return nil, err
	}
	metadata, err := json.Marshal(frontmatter)
	if err != nil {
		return nil, err
	}
	return append(metadata, body...), nil
}

func normalizeIngestJob(body []byte) ([]byte, error) {
	var job map[string]any
	if err := json.Unmarshal(body, &job); err != nil {
		return nil, err
	}
	id, _ := job["id"].(string)
	if len(id) != 32 {
		return nil, fmt.Errorf("invalid job id %q", id)
	}
	if _, err := hex.DecodeString(id); err != nil {
		return nil, fmt.Errorf("invalid job id %q", id)
	}
	source, _ := job["source_path"].(string)
	parts := strings.Split(source, "/")
	if len(parts) != 4 || parts[0] != "archive" || len(parts[1]) != 4 || len(parts[2]) != 2 {
		return nil, fmt.Errorf("invalid archive path %q", source)
	}
	month := time.Now().UTC().Format("2006/01")
	previousMinute := time.Now().UTC().Add(-time.Minute).Format("2006/01")
	if period := parts[1] + "/" + parts[2]; period != month && period != previousMinute {
		return nil, fmt.Errorf("archive month is not current: %q", source)
	}
	uploadID, filename, ok := strings.Cut(parts[3], "-")
	if !ok || len(uploadID) != 32 || filename != "report.pdf" {
		return nil, fmt.Errorf("invalid archive filename %q", source)
	}
	if _, err := hex.DecodeString(uploadID); err != nil {
		return nil, fmt.Errorf("invalid upload id %q", uploadID)
	}
	if job["schema_version"] != float64(1) || job["status"] != "pending" || job["original_name"] != "report.pdf" || job["content_type"] != "application/octet-stream" || job["capability"] != "ocr" {
		return nil, fmt.Errorf("wrong ingested job state: %q", body)
	}
	for _, field := range []string{"created_at", "updated_at"} {
		value, _ := job[field].(string)
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil || time.Since(parsed) > time.Minute || time.Until(parsed) > time.Minute {
			return nil, fmt.Errorf("invalid %s %q", field, value)
		}
	}
	if job["created_at"] != job["updated_at"] {
		return nil, fmt.Errorf("ingest timestamps differ")
	}
	job["id"] = "<job-id>"
	job["source_path"] = "archive/<month>/<upload-id>-report.pdf"
	job["created_at"] = "<dynamic>"
	job["updated_at"] = "<dynamic>"
	return json.Marshal(job)
}

func assertIngestWrite(vault string, response []byte, body string) error {
	var job struct {
		ID         string `json:"id"`
		SourcePath string `json:"source_path"`
	}
	if err := json.Unmarshal(response, &job); err != nil {
		return err
	}
	if !filepath.IsLocal(job.SourcePath) || !strings.HasPrefix(job.SourcePath, "archive/") {
		return fmt.Errorf("unsafe archive path %q", job.SourcePath)
	}
	uploadPath := filepath.Join(vault, filepath.FromSlash(job.SourcePath))
	uploadInfo, err := os.Stat(uploadPath)
	if err != nil {
		return err
	}
	if runtime.GOOS != "windows" && uploadInfo.Mode().Perm() != 0o640 {
		return fmt.Errorf("upload mode = %o, want 640", uploadInfo.Mode().Perm())
	}
	content, err := os.ReadFile(uploadPath)
	if err != nil {
		return err
	}
	if string(content) != body {
		return fmt.Errorf("archived bytes differ")
	}
	jobPath := filepath.Join(vault, ".symdesk", "server", "jobs", job.ID+".json")
	jobInfo, err := os.Stat(jobPath)
	if err != nil {
		return err
	}
	if runtime.GOOS != "windows" && jobInfo.Mode().Perm() != 0o600 {
		return fmt.Errorf("job mode = %o, want 600", jobInfo.Mode().Perm())
	}
	persisted, err := os.ReadFile(jobPath)
	if err != nil {
		return err
	}
	var responseJob, persistedJob map[string]any
	if err := json.Unmarshal(response, &responseJob); err != nil {
		return err
	}
	if err := json.Unmarshal(persisted, &persistedJob); err != nil {
		return err
	}
	if !reflect.DeepEqual(responseJob, persistedJob) {
		return fmt.Errorf("response and persisted job differ")
	}
	return nil
}

func normalizeJobUpdatedAt(body []byte) ([]byte, error) {
	var job struct {
		UpdatedAt string `json:"updated_at"`
	}
	if err := json.Unmarshal(body, &job); err != nil {
		return nil, err
	}
	updated, err := time.Parse(time.RFC3339Nano, job.UpdatedAt)
	if err != nil || time.Since(updated) > time.Minute || time.Until(updated) > time.Minute {
		return nil, fmt.Errorf("job timestamp is not current RFC3339: %q", job.UpdatedAt)
	}
	marker := []byte(`"` + job.UpdatedAt + `"`)
	if bytes.Count(body, marker) != 1 {
		return nil, fmt.Errorf("job timestamp field is not unique")
	}
	return bytes.Replace(body, marker, []byte(`"<dynamic>"`), 1), nil
}

func normalizeJobLeaseTimes(body []byte) ([]byte, error) {
	var job struct {
		UpdatedAt  string `json:"updated_at"`
		LeaseUntil string `json:"lease_until"`
	}
	if err := json.Unmarshal(body, &job); err != nil {
		return nil, err
	}
	updated, err := time.Parse(time.RFC3339Nano, job.UpdatedAt)
	if err != nil || time.Since(updated) > time.Minute || time.Until(updated) > time.Minute {
		return nil, fmt.Errorf("job updated_at is not current RFC3339: %q", job.UpdatedAt)
	}
	leaseUntil, err := time.Parse(time.RFC3339Nano, job.LeaseUntil)
	if err != nil || time.Until(leaseUntil) < 14*time.Minute || time.Until(leaseUntil) > 16*time.Minute {
		return nil, fmt.Errorf("job lease_until is not 15 minutes ahead: %q", job.LeaseUntil)
	}
	for _, value := range []string{job.UpdatedAt, job.LeaseUntil} {
		marker := []byte(`"` + value + `"`)
		if bytes.Count(body, marker) != 1 {
			return nil, fmt.Errorf("job timestamp field is not unique")
		}
		body = bytes.Replace(body, marker, []byte(`"<dynamic>"`), 1)
	}
	return body, nil
}

func assertIndexedWrite(vault, relative, body string) error {
	canonical, err := filepath.EvalSymlinks(vault)
	if err != nil {
		return err
	}
	path := filepath.Join(canonical, relative)
	actual, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if string(actual) != body {
		return fmt.Errorf("%s has unexpected bytes", path)
	}
	checksum := fmt.Sprintf("%x", sha256.Sum256(actual))
	dbPath := filepath.Join(vault, ".symdesk", "server", "sidecar.db")
	if _, err := os.Stat(dbPath); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	var indexed string
	if err := db.QueryRow("SELECT sha256 FROM files WHERE path = ?", path).Scan(&indexed); err != nil {
		var storedPath string
		_ = db.QueryRow("SELECT path FROM files LIMIT 1").Scan(&storedPath)
		return fmt.Errorf("query index path %q (first stored path %q): %w", path, storedPath, err)
	}
	if indexed != checksum {
		return fmt.Errorf("%s index hash = %s, want %s", path, indexed, checksum)
	}
	return nil
}

const serverStopTimeout = 5 * time.Second

type runningServer struct {
	cmd  *exec.Cmd
	base string
	logs boundedBuffer
}

func startServer(binary, vault string) *runningServer {
	absoluteBinary, err := filepath.Abs(binary)
	if err != nil {
		fatal("resolve %s: %v", binary, err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fatal("choose loopback port: %v", err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	home := filepath.Join(vault, ".http-harness", filepath.Base(binary))
	if err := os.MkdirAll(filepath.Join(home, "tmp"), 0o700); err != nil {
		fatal("isolation root: %v", err)
	}
	//nolint:gosec // absoluteBinary is the explicit Go/Rust harness operand
	cmd := exec.Command(absoluteBinary, "serve", "--listen", address, "--vault", vault, "--token", token)
	cmd.Dir = home
	cmd.Env = isolatedEnv(vault, home)
	server := &runningServer{cmd: cmd, base: "http://" + address}
	cmd.Stdout = io.Discard
	cmd.Stderr = &server.logs
	// Bound os/exec cleanup if a descendant retains an inherited handle.
	cmd.WaitDelay = serverStopTimeout
	if err := cmd.Start(); err != nil {
		fatal("start %s: %v", binary, err)
	}
	return server
}

func (s *runningServer) ready() error {
	client := &http.Client{Timeout: 250 * time.Millisecond}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get(s.base + "/healthz")
		if err == nil {
			body, readErr := io.ReadAll(io.LimitReader(response.Body, 1024))
			_ = response.Body.Close()
			if readErr == nil && response.StatusCode == http.StatusOK && string(body) == `{"status":"ok"}` {
				return nil
			}
		}
		if s.cmd.ProcessState != nil {
			logs, overflow := s.logs.snapshot()
			if overflow {
				return fmt.Errorf("process exited with more than 1 MiB stderr; stderr=%s", logs)
			}
			return fmt.Errorf("process exited: %s; stderr=%s", s.cmd.ProcessState, logs)
		}
		time.Sleep(25 * time.Millisecond)
	}
	logs, overflow := s.logs.snapshot()
	if overflow {
		return fmt.Errorf("timed out waiting for %s: stderr exceeded 1 MiB; stderr=%s", s.base, logs)
	}
	return fmt.Errorf("timed out waiting for %s; stderr=%s", s.base, logs)
}

func (s *runningServer) request(tc httpCase, previousETag string) (transcript, string, error) {
	if tc.BodyRepeat < 0 || tc.BodyRepeat > (24<<20)+1 {
		return transcript{}, previousETag, fmt.Errorf("fixture body_repeat exceeds 8 MiB + 1 bound")
	}
	requestBody := tc.Body
	if tc.BodyRepeat > 0 {
		requestBody = tc.Body + strings.Repeat("a", tc.BodyRepeat)
	}
	var requestReader io.Reader = strings.NewReader(requestBody)
	if tc.MultipartFile != "" {
		var encoded bytes.Buffer
		writer := multipart.NewWriter(&encoded)
		if err := writer.SetBoundary("symdesk-http-diff"); err != nil {
			return transcript{}, previousETag, err
		}
		file, err := writer.CreateFormFile("file", tc.MultipartFile)
		if err != nil {
			return transcript{}, previousETag, err
		}
		if _, err := io.WriteString(file, requestBody); err != nil {
			return transcript{}, previousETag, err
		}
		if err := writer.Close(); err != nil {
			return transcript{}, previousETag, err
		}
		requestReader = &encoded
	}
	request, err := http.NewRequestWithContext(context.Background(), tc.Method, s.base+tc.Path, requestReader)
	if err != nil {
		return transcript{}, previousETag, err
	}
	for key, value := range tc.Headers {
		if value == "$LAST_ETAG" {
			value = previousETag
		}
		request.Header.Set(key, value)
	}
	if tc.MultipartFile != "" {
		request.Header.Set("Content-Type", "multipart/form-data; boundary=symdesk-http-diff")
	}
	switch tc.Auth {
	case "valid":
		request.Header.Set("Authorization", "Bearer "+token)
	case "wrong":
		request.Header.Set("Authorization", "Bearer 0000000000000000000000000000wrong")
	case "raw":
		request.Header.Set("Authorization", token)
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableCompression: true}}
	response, err := client.Do(request)
	if err != nil {
		return transcript{}, previousETag, err
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (16<<20)+1))
	closeErr := response.Body.Close()
	if err != nil {
		return transcript{}, previousETag, err
	}
	if closeErr != nil {
		return transcript{}, previousETag, fmt.Errorf("close response body: %w", closeErr)
	}
	if len(body) > 16<<20 {
		return transcript{}, previousETag, fmt.Errorf("response exceeded 16 MiB harness cap")
	}
	if response.Header.Get("Content-Encoding") == "gzip" && len(body) > 0 {
		reader, gzipErr := gzip.NewReader(bytes.NewReader(body))
		if gzipErr != nil {
			return transcript{}, previousETag, gzipErr
		}
		body, err = io.ReadAll(io.LimitReader(reader, (16<<20)+1))
		closeErr := reader.Close()
		if err != nil {
			return transcript{}, previousETag, err
		}
		if closeErr != nil {
			return transcript{}, previousETag, closeErr
		}
		if len(body) > 16<<20 {
			return transcript{}, previousETag, fmt.Errorf("decompressed response exceeded 16 MiB harness cap")
		}
	}
	headers := make(map[string]string)
	for _, name := range []string{"Accept-Ranges", "Allow", "Cache-Control", "Content-Disposition", "Content-Encoding", "Content-Length", "Content-Range", "Content-Security-Policy", "Content-Type", "ETag", "Last-Modified", "Referrer-Policy", "WWW-Authenticate", "X-Content-Type-Options"} {
		if value := response.Header.Get(name); value != "" {
			headers[strings.ToLower(name)] = value
		}
	}
	return transcript{Status: response.StatusCode, Headers: headers, Body: normalizeBody(body)}, response.Header.Get("ETag"), nil
}

func normalizeBody(body []byte) []byte {
	key := []byte(`"generated_at":`)
	index := bytes.Index(body, key)
	if index < 0 {
		return body
	}
	start := index + len(key)
	for start < len(body) && (body[start] == ' ' || body[start] == '	' || body[start] == '\r' || body[start] == '\n') {
		start++
	}
	if start >= len(body) || body[start] != '"' {
		return body
	}
	end := start + 1
	escaped := false
	for end < len(body) {
		switch {
		case escaped:
			escaped = false
		case body[end] == '\\':
			escaped = true
		case body[end] == '"':
			result := make([]byte, 0, len(body)-end+len(`<dynamic>`))
			result = append(result, body[:start]...)
			result = append(result, `"<dynamic>"`...)
			result = append(result, body[end+1:]...)
			return result
		}
		end++
	}
	return body
}

func compare(id string, left, right transcript) error {
	if left.Status != right.Status {
		return fmt.Errorf("status mismatch: Go=%d Rust=%d", left.Status, right.Status)
	}
	if id == "file-put-symlink-parent" {
		if left.Status != http.StatusInternalServerError {
			return fmt.Errorf("symlink parent was not rejected: status=%d", left.Status)
		}
		for _, response := range []transcript{left, right} {
			var body map[string]string
			if err := json.Unmarshal(response.Body, &body); err != nil || body["error"] == "" {
				return fmt.Errorf("symlink parent lacked JSON error: %q", response.Body)
			}
		}
		left.Headers = cloneWithout(left.Headers, "content-length")
		right.Headers = cloneWithout(right.Headers, "content-length")
		return compareHeaders(left.Headers, right.Headers)
	}
	if id == "jobs-retry-failed" {
		var err error
		left.Body, err = normalizeJobUpdatedAt(left.Body)
		if err != nil {
			return fmt.Errorf("Go retry response: %w", err)
		}
		right.Body, err = normalizeJobUpdatedAt(right.Body)
		if err != nil {
			return fmt.Errorf("Rust retry response: %w", err)
		}
		left.Headers = cloneWithout(left.Headers, "content-length")
		right.Headers = cloneWithout(right.Headers, "content-length")
	}
	if id == "worker-fail-valid" || id == "worker-fail-retry" {
		var err error
		left.Body, err = normalizeJobUpdatedAt(left.Body)
		if err != nil {
			return fmt.Errorf("Go worker fail response: %w", err)
		}
		right.Body, err = normalizeJobUpdatedAt(right.Body)
		if err != nil {
			return fmt.Errorf("Rust worker fail response: %w", err)
		}
		left.Headers = cloneWithout(left.Headers, "content-length")
		right.Headers = cloneWithout(right.Headers, "content-length")
	}
	if id == "worker-lease-valid" || id == "worker-lease-expired-reclaim" {
		var err error
		left.Body, err = normalizeJobLeaseTimes(left.Body)
		if err != nil {
			return fmt.Errorf("Go worker lease response: %w", err)
		}
		right.Body, err = normalizeJobLeaseTimes(right.Body)
		if err != nil {
			return fmt.Errorf("Rust worker lease response: %w", err)
		}
		left.Headers = cloneWithout(left.Headers, "content-length")
		right.Headers = cloneWithout(right.Headers, "content-length")
	}
	if id == "worker-complete-invalid-json" {
		for name, response := range map[string]transcript{"Go": left, "Rust": right} {
			var body map[string]string
			if response.Status != http.StatusBadRequest || json.Unmarshal(response.Body, &body) != nil || !strings.HasPrefix(body["error"], "invalid JSON:") {
				return fmt.Errorf("%s invalid completion JSON response mismatch: status=%d body=%q", name, response.Status, response.Body)
			}
		}
		left.Headers = cloneWithout(left.Headers, "content-length")
		right.Headers = cloneWithout(right.Headers, "content-length")
		return compareHeaders(left.Headers, right.Headers)
	}
	if id == "worker-complete-valid" {
		var err error
		left.Body, err = normalizeJobUpdatedAt(left.Body)
		if err != nil {
			return fmt.Errorf("Go worker completion response: %w", err)
		}
		right.Body, err = normalizeJobUpdatedAt(right.Body)
		if err != nil {
			return fmt.Errorf("Rust worker completion response: %w", err)
		}
		left.Headers = cloneWithout(left.Headers, "content-length")
		right.Headers = cloneWithout(right.Headers, "content-length")
	}
	if id == "ingest-valid" {
		if left.Status != http.StatusAccepted {
			return fmt.Errorf("ingest status = %d, want 202", left.Status)
		}
		var err error
		left.Body, err = normalizeIngestJob(left.Body)
		if err != nil {
			return fmt.Errorf("Go ingest response: %w", err)
		}
		right.Body, err = normalizeIngestJob(right.Body)
		if err != nil {
			return fmt.Errorf("Rust ingest response: %w", err)
		}
		left.Headers = cloneWithout(left.Headers, "content-length")
		right.Headers = cloneWithout(right.Headers, "content-length")
	}
	if id == "share-create-valid" {
		if left.Status != http.StatusCreated {
			return fmt.Errorf("share create status = %d, want 201", left.Status)
		}
		var err error
		left.Body, err = normalizeCreatedShare(left.Body)
		if err != nil {
			return fmt.Errorf("Go share create response: %w", err)
		}
		right.Body, err = normalizeCreatedShare(right.Body)
		if err != nil {
			return fmt.Errorf("Rust share create response: %w", err)
		}
		left.Headers = cloneWithout(left.Headers, "content-length")
		right.Headers = cloneWithout(right.Headers, "content-length")
	}
	if strings.HasPrefix(id, "snapshot-") {
		if strings.HasPrefix(id, "snapshot-gzip") || id == "snapshot-head-gzip" {
			if left.Headers["content-encoding"] != "gzip" || right.Headers["content-encoding"] != "gzip" || left.Headers["content-length"] == "" || right.Headers["content-length"] == "" {
				return fmt.Errorf("gzip response lacks encoding or length contract")
			}
		}
		// generated_at is intentionally normalized in the body; its
		// fractional timestamp width can still vary and therefore changes
		// Content-Length without changing the response contract.
		left.Headers = cloneWithout(left.Headers, "content-length")
		right.Headers = cloneWithout(right.Headers, "content-length")
	}
	if err := compareHeaders(left.Headers, right.Headers); err != nil {
		return fmt.Errorf("%w; bodies Go=%q Rust=%q", err, left.Body, right.Body)
	}
	if !reflect.DeepEqual(left.Body, right.Body) {
		return fmt.Errorf("body mismatch: Go=%q Rust=%q", left.Body, right.Body)
	}
	return nil
}

func compareHeaders(left, right map[string]string) error {
	if !reflect.DeepEqual(left, right) {
		return fmt.Errorf("headers mismatch: Go=%v Rust=%v", left, right)
	}
	return nil
}

func cloneWithout(headers map[string]string, omitted string) map[string]string {
	clone := make(map[string]string, len(headers))
	for key, value := range headers {
		if key != omitted {
			clone[key] = value
		}
	}
	return clone
}

func isolatedEnv(vault, home string) []string {
	tmp := filepath.Join(home, "tmp")
	env := []string{
		"HOME=" + home, "USERPROFILE=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"TMPDIR=" + tmp, "TMP=" + tmp, "TEMP=" + tmp,
		"LANG=C", "LC_ALL=C", "TZ=UTC", "TERM=dumb", "NO_COLOR=1",
		"SYMDESK_VAULT=" + vault, "SYMDESK_VERSION=0.12.2",
	}
	for _, key := range []string{"PATH", "SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT"} {
		if value := os.Getenv(key); value != "" {
			env = append(env, key+"="+value)
		}
	}
	return env
}

func (s *runningServer) stop() error {
	if s.cmd == nil || s.cmd.Process == nil {
		return nil
	}
	if s.cmd.ProcessState != nil {
		if s.cmd.ProcessState.Success() {
			return nil
		}
		return fmt.Errorf("process cleanup failed: already exited with status %d", s.cmd.ProcessState.ExitCode())
	}
	terminateErr := terminateProcessTree(s.cmd)
	intentionalTermination := terminateErr == nil || errors.Is(terminateErr, os.ErrProcessDone)
	alreadyExited := errors.Is(terminateErr, os.ErrProcessDone)
	wait := make(chan error, 1)
	go func() { wait <- s.cmd.Wait() }()
	select {
	case err := <-wait:
		if err != nil && !cleanupExitExpected(err, intentionalTermination, alreadyExited, isWindowsProcess()) {
			logs, overflow := s.logs.snapshot()
			if overflow {
				return fmt.Errorf("process cleanup stderr exceeded 1 MiB: %w; stderr=%s", err, logs)
			}
			return fmt.Errorf("process cleanup failed: %w; stderr=%s", err, logs)
		}
		if _, overflow := s.logs.snapshot(); overflow {
			return fmt.Errorf("process cleanup stderr exceeded 1 MiB")
		}
		if terminateErr != nil && !errors.Is(terminateErr, os.ErrProcessDone) {
			return fmt.Errorf("terminate process: %w", terminateErr)
		}
		return nil
	case <-time.After(serverStopTimeout):
		killErr := s.cmd.Process.Kill()
		select {
		case waitErr := <-wait:
			if killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
				return fmt.Errorf("process did not exit within %s: kill: %w", serverStopTimeout, killErr)
			}
			if waitErr != nil && !cleanupExitExpected(waitErr, true, false, isWindowsProcess()) {
				return fmt.Errorf("process cleanup failed after timeout: %w", waitErr)
			}
		case <-time.After(serverStopTimeout):
			return fmt.Errorf("process did not exit within %s after kill", serverStopTimeout)
		}
		return fmt.Errorf("process did not exit within %s", serverStopTimeout)
	}
}

// cleanupExitExpected accepts only statuses caused by the cleanup action.
// Windows reports a forcibly terminated process as exit status 1, while Unix
// reports a signal termination as -1. Other non-zero exits remain failures.
func cleanupExitExpected(err error, intentionalTermination, alreadyExited, windows bool) bool {
	if !intentionalTermination || alreadyExited {
		return false
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	code := exitErr.ExitCode()
	return code == -1 || (windows && code == 1)
}

func fatal(format string, args ...any) {
	panic(fmt.Errorf(format, args...))
}
