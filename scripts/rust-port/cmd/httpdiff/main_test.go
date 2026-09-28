package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func testExecutable(t *testing.T) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return executable
}

func TestCleanupExitExpectedIntentionalKill(t *testing.T) {
	cmd := exec.Command(testExecutable(t), "-test.run=TestCleanupHelper") //nolint:gosec // testExecutable resolves the checked test binary; arguments are fixed test-only flags
	cmd.Env = append(os.Environ(), "HTTPDIFF_CLEANUP_HELPER=block")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err := cmd.Wait()
	if err == nil || !cleanupExitExpected(err, true, false, runtime.GOOS == "windows") {
		t.Fatalf("intentional kill should be accepted, err=%v", err)
	}
}

func TestCleanupExitExpectedAlreadyExited(t *testing.T) {
	cmd := exec.Command(testExecutable(t), "-test.run=TestCleanupHelper") //nolint:gosec // testExecutable resolves the checked test binary; arguments are fixed test-only flags
	cmd.Env = append(os.Environ(), "HTTPDIFF_CLEANUP_HELPER=exit")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if cleanupExitExpected(nil, true, true, runtime.GOOS == "windows") {
		t.Fatal("already-exited process must not turn a non-zero wait into success")
	}
}

func TestCleanupExitExpectedUnexpectedFailure(t *testing.T) {
	cmd := exec.Command(testExecutable(t), "-test.run=TestCleanupHelper") //nolint:gosec // testExecutable resolves the checked test binary; arguments are fixed test-only flags
	cmd.Env = append(os.Environ(), "HTTPDIFF_CLEANUP_HELPER=fail")
	err := cmd.Run()
	if err == nil {
		t.Fatal("expected helper failure")
	}
	if cleanupExitExpected(err, false, false, runtime.GOOS == "windows") {
		t.Fatal("unexpected process failure must remain an error")
	}
}

func TestRunningServerStopLifecycle(t *testing.T) {
	cmd := exec.Command(testExecutable(t), "-test.run=TestCleanupHelper") //nolint:gosec // testExecutable resolves the checked test binary; arguments are fixed test-only flags
	cmd.Env = append(os.Environ(), "HTTPDIFF_CLEANUP_HELPER=block")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	server := &runningServer{cmd: cmd}
	started := time.Now()
	if err := server.stop(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed >= serverStopTimeout {
		t.Fatalf("stop took %s, exceeded bound", elapsed)
	}
}

func TestRunningServerStopAlreadyExitedNonZero(t *testing.T) {
	cmd := exec.Command(testExecutable(t), "-test.run=TestCleanupHelper") //nolint:gosec // testExecutable resolves the checked test binary; arguments are fixed test-only flags
	cmd.Env = append(os.Environ(), "HTTPDIFF_CLEANUP_HELPER=fail")
	if err := cmd.Run(); err == nil {
		t.Fatal("expected helper failure")
	}
	server := &runningServer{cmd: cmd}
	if err := server.stop(); err == nil {
		t.Fatal("already-exited non-zero process must remain a cleanup error")
	}
}

func TestCleanupHelper(t *testing.T) {
	switch os.Getenv("HTTPDIFF_CLEANUP_HELPER") {
	case "block":
		time.Sleep(time.Minute)
	case "fail":
		os.Exit(1)
	case "exit":
		return
	}
}

func TestBoundedBufferCapsCapturedOutput(t *testing.T) {
	var buffer boundedBuffer
	input := strings.Repeat("x", (1<<20)+8)
	written, err := buffer.Write([]byte(input))
	if err != nil || written != len(input) {
		t.Fatalf("Write() = (%d, %v), want (%d, nil)", written, err, len(input))
	}
	output, overflow := buffer.snapshot()
	if len(output) != 1<<20 || !overflow {
		t.Fatalf("snapshot() = (%d bytes, overflow=%v), want (1 MiB, true)", len(output), overflow)
	}
	if _, err := buffer.Write([]byte("more")); err != nil {
		t.Fatal(err)
	}
	output, overflow = buffer.snapshot()
	if len(output) != 1<<20 || !overflow {
		t.Fatalf("second snapshot() = (%d bytes, overflow=%v), want (1 MiB, true)", len(output), overflow)
	}
}

func TestNormalizeBodyReplacesOnlyGeneratedTimestamp(t *testing.T) {
	input := []byte(`{"generated_at": "2026-09-27T12:34:56Z", "name":"keep"}`)
	want := []byte(`{"generated_at": "<dynamic>", "name":"keep"}`)
	if got := normalizeBody(input); string(got) != string(want) {
		t.Fatalf("normalizeBody() = %s, want %s", got, want)
	}
	for _, unchanged := range [][]byte{
		[]byte(`{"name":"keep"}`),
		[]byte(`{"generated_at":42}`),
		[]byte(`{"generated_at":"unterminated}`),
	} {
		if got := normalizeBody(unchanged); string(got) != string(unchanged) {
			t.Fatalf("normalizeBody(%s) = %s, want unchanged", unchanged, got)
		}
	}
}

func TestNormalizeWorkerTimestampsValidatesAndNormalizes(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	lease := time.Now().UTC().Add(15 * time.Minute).Format(time.RFC3339Nano)
	job := []byte(`{"updated_at":"` + now + `","lease_until":"` + lease + `","status":"processing"}`)
	normalized, err := normalizeJobLeaseTimes(job)
	if err != nil {
		t.Fatalf("normalizeJobLeaseTimes() error = %v", err)
	}
	if string(normalized) != `{"updated_at":"<dynamic>","lease_until":"<dynamic>","status":"processing"}` {
		t.Fatalf("normalized lease = %s", normalized)
	}
	if _, err := normalizeJobUpdatedAt([]byte(`{"updated_at":"` + now + `"}`)); err != nil {
		t.Fatalf("normalizeJobUpdatedAt() error = %v", err)
	}
	if _, err := normalizeJobUpdatedAt([]byte(`{"updated_at":"old"}`)); err == nil {
		t.Fatal("normalizeJobUpdatedAt accepted a stale timestamp")
	}
	if _, err := normalizeJobLeaseTimes([]byte(`{"updated_at":"` + now + `","lease_until":"` + now + `"}`)); err == nil {
		t.Fatal("normalizeJobLeaseTimes accepted an out-of-range lease")
	}
	duplicate := []byte(`{"updated_at":"` + now + `","lease_until":"` + lease + `","echo":"` + now + `"}`)
	if _, err := normalizeJobLeaseTimes(duplicate); err == nil {
		t.Fatal("normalizeJobLeaseTimes accepted a duplicated timestamp value")
	}
}

func TestNormalizeCommandListTimestampsAndHeaders(t *testing.T) {
	normalized, err := normalizeCommandListTimestamps([]byte(`[{"path":"note.md","title":"Note","type":"markdown","modified":"2026-09-27T12:34:56Z"}]`))
	if err != nil {
		t.Fatalf("normalizeCommandListTimestamps() error = %v", err)
	}
	if string(normalized) != `[{"path":"note.md","title":"Note","type":"markdown","modified":""}]` {
		t.Fatalf("normalized list = %s", normalized)
	}
	if _, err := normalizeCommandListTimestamps([]byte(`[{"path":"note.md","modified":"invalid"}]`)); err == nil {
		t.Fatal("normalizeCommandListTimestamps accepted an invalid time")
	}
	left := map[string]string{"content-type": "application/json", "etag": "v1"}
	right := cloneWithout(left, "etag")
	if right["etag"] != "" || len(right) != 1 {
		t.Fatalf("cloneWithout() = %#v", right)
	}
	if err := compareHeaders(right, map[string]string{"content-type": "application/json"}); err != nil {
		t.Fatalf("compareHeaders() error = %v", err)
	}
	if err := compareHeaders(left, right); err == nil {
		t.Fatal("compareHeaders accepted different headers")
	}
}

func TestReadVaultFixtureFileConfinesReads(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".symdesk"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".symdesk", "fixture.json"), []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	contents, err := readVaultFixtureFile(root, ".symdesk/fixture.json")
	if err != nil || string(contents) != "inside" {
		t.Fatalf("readVaultFixtureFile() = (%q, %v)", contents, err)
	}
	if _, err := readVaultFixtureFile(root, "../outside"); err == nil {
		t.Fatal("readVaultFixtureFile accepted a path outside its root")
	}
}

func TestNormalizeIngestJobPreservesOnlyStableFields(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	id := strings.Repeat("a", 32)
	period := time.Now().UTC().Format("2006/01")
	job := map[string]any{
		"id": id, "source_path": filepath.Join("archive", filepath.FromSlash(period), id+"-report.pdf"),
		"schema_version": 1, "status": "pending", "original_name": "report.pdf",
		"content_type": "application/octet-stream", "capability": "ocr",
		"created_at": now, "updated_at": now,
	}
	body, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := normalizeIngestJob(body)
	if err != nil {
		t.Fatalf("normalizeIngestJob() error = %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(normalized, &got); err != nil {
		t.Fatal(err)
	}
	if got["id"] != "<job-id>" || got["source_path"] != "archive/<month>/<upload-id>-report.pdf" || got["created_at"] != "<dynamic>" {
		t.Fatalf("normalized ingest job = %#v", got)
	}
	job["source_path"] = filepath.Join("archive", "..", "escape.pdf")
	body, _ = json.Marshal(job)
	if _, err := normalizeIngestJob(body); err == nil {
		t.Fatal("normalizeIngestJob accepted an unsafe archive path")
	}
}

func TestFixtureSeedersBuildExpectedWorkerAndNamedUserState(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture vault includes symlinks that need elevated privileges on Windows")
	}
	vault := createFixtureVault(t.TempDir())
	seeders := []func(string) error{
		populateWorkerACL,
		populateNamedUser,
		populateJobs,
		populateWorkerJob,
		populateExpiredJob,
		populateShares,
		populateShareAccess,
	}
	for _, seed := range seeders {
		if err := seed(vault); err != nil {
			t.Fatalf("fixture seeder failed: %v", err)
		}
	}

	for name, want := range map[string]string{
		"Hello.md":        "---\ntitle: Hello\n---\nBody",
		"nested/Named.md": "named user initial",
		".symdesk/server/jobs/00000000000000000000000000000004.json": `"worker_id":"worker-1"`,
		".symdesk/server/jobs/00000000000000000000000000000005.json": `"worker_id":"worker-old"`,
	} {
		contents, err := readVaultFixtureFile(vault, name)
		if err != nil || !strings.Contains(string(contents), want) {
			t.Fatalf("fixture %s = (%q, %v), want substring %q", name, contents, err, want)
		}
	}
	if err := assertNamedSnapshotFiltered([]byte(`{"notes":[{"path":"nested/Named.md"}]}`)); err != nil {
		t.Fatalf("expected named-user snapshot was rejected: %v", err)
	}
	if err := assertNamedSnapshotFiltered([]byte(`{"notes":[{"path":"Hello.md"}]}`)); err == nil {
		t.Fatal("named-user snapshot accepted a denied document")
	}
	if err := assertNamedNotebookFiltered([]byte(`{"sources":[]}`)); err != nil {
		t.Fatalf("empty readable notebook sources were rejected: %v", err)
	}
}

func TestNormalizeCreatedShareChecksAndReplacesDynamicFields(t *testing.T) {
	created := time.Now().UTC().Truncate(time.Second)
	expires := created.Add(24 * time.Hour)
	share := map[string]string{
		"id": strings.Repeat("a", 24), "token": strings.Repeat("b", 64),
		"path": "nested/Note.md", "created_at": created.Format(time.RFC3339),
		"expires_at": expires.Format(time.RFC3339),
		"url":        "/s/" + strings.Repeat("b", 64),
	}
	body, err := json.Marshal(share)
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := normalizeCreatedShareFor(body, "nested/Note.md")
	if err != nil {
		t.Fatalf("normalizeCreatedShareFor() error = %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(normalized, &got); err != nil {
		t.Fatal(err)
	}
	if got["id"] != "<dynamic>" || got["token"] != "<dynamic>" || got["path"] != "nested/Note.md" || got["url"] != "/s/<dynamic>" {
		t.Fatalf("normalized share = %#v", got)
	}
	share["path"] = "escape.md"
	body, _ = json.Marshal(share)
	if _, err := parseCreatedShareFor(body, "nested/Note.md"); err == nil {
		t.Fatal("parseCreatedShareFor accepted the wrong shared path")
	}
}
