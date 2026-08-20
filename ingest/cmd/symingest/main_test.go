package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-corekit/updatecheck"

	"github.com/danieljustus/symaira-ingest/internal/config"
	"github.com/danieljustus/symaira-ingest/internal/paperlessimport"
	"github.com/danieljustus/symaira-ingest/internal/store"
)

func TestRun_Version(t *testing.T) {
	var sb strings.Builder
	oldStdout := stdout
	stdout = &sb
	defer func() { stdout = oldStdout }()

	if err := run([]string{"version"}); err != nil {
		t.Fatalf("run(version): %v", err)
	}
	if got := strings.TrimSpace(sb.String()); got == "" {
		t.Fatal("expected version output")
	}
	if got := strings.TrimSpace(sb.String()); !strings.Contains(got, "symingest") {
		t.Errorf("expected version output to contain 'symingest', got %q", got)
	}

	sb.Reset()
	if err := run([]string{"version", "--json"}); err != nil {
		t.Fatalf("run(version --json): %v", err)
	}
	gotJSON := strings.TrimSpace(sb.String())
	if !strings.Contains(gotJSON, `"tool":"symingest"`) {
		t.Errorf("expected JSON version output to contain tool 'symingest', got %q", gotJSON)
	}
}

func TestRun_VersionCheck(t *testing.T) {
	// Stub the update check function to return a newer release.
	oldFn := checkForUpdateFn
	defer func() { checkForUpdateFn = oldFn }()

	checkForUpdateFn = func(_ context.Context, currentVersion string) (*updatecheck.Release, error) {
		if currentVersion == "" {
			return nil, nil
		}
		return &updatecheck.Release{
			TagName: "v9.9.9",
			HTMLURL: "https://github.com/danieljustus/symaira-ingest/releases/v9.9.9",
		}, nil
	}

	var sb strings.Builder
	oldStdout := stdout
	stdout = &sb
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	defer func() {
		stdout = oldStdout
		os.Stderr = oldStderr
	}()

	// Capture stderr into eb
	errCh := make(chan string, 1)
	go func() {
		var b strings.Builder
		_, _ = io.Copy(&b, r)
		errCh <- b.String()
	}()

	if err := run([]string{"version", "--check"}); err != nil {
		t.Fatalf("run(version --check): %v", err)
	}
	w.Close()
	stderrOutput := <-errCh

	if !strings.Contains(sb.String(), "symingest") {
		t.Errorf("stdout should contain version, got %q", sb.String())
	}
	if !strings.Contains(stderrOutput, "Update available: v9.9.9") {
		t.Errorf("stderr should announce update, got %q", stderrOutput)
	}
	if !strings.Contains(stderrOutput, "danieljustus/symaira-ingest/releases/v9.9.9") {
		t.Errorf("stderr should contain download URL, got %q", stderrOutput)
	}
}

func TestRun_VersionCheckUpToDate(t *testing.T) {
	oldFn := checkForUpdateFn
	defer func() { checkForUpdateFn = oldFn }()

	checkForUpdateFn = func(_ context.Context, _ string) (*updatecheck.Release, error) {
		return nil, nil // no update available
	}

	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	defer func() { os.Stderr = oldStderr }()

	errCh := make(chan string, 1)
	go func() {
		var b strings.Builder
		_, _ = io.Copy(&b, r)
		errCh <- b.String()
	}()

	if err := run([]string{"version", "--check"}); err != nil {
		t.Fatalf("run(version --check): %v", err)
	}
	w.Close()
	stderrOutput := <-errCh

	if !strings.Contains(stderrOutput, "Already up to date.") {
		t.Errorf("stderr should say 'Already up to date.', got %q", stderrOutput)
	}
}

func TestRun_Help(t *testing.T) {
	var sb strings.Builder
	oldStdout := stdout
	stdout = &sb
	defer func() { stdout = oldStdout }()

	if err := run([]string{"help"}); err != nil {
		t.Fatalf("run(help): %v", err)
	}
	out := sb.String()
	for _, want := range []string{"ingest", "mcp", "version", "watch", "service", "jobs", "retry"} {
		if !strings.Contains(out, want) {
			t.Fatalf("help output missing %q", want)
		}
	}
}

func TestRun_UnknownCommand(t *testing.T) {
	err := run([]string{"nope"})
	if err == nil {
		t.Fatal("expected error for unknown command")
	}
}

func TestRun_ServiceInstallDryRunDoesNotEmbedSecrets(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("service management supports macOS LaunchAgents only")
	}
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("SYMINGEST_VAULT", filepath.Join(dir, "vault"))
	t.Setenv("SYMINGEST_ARCHIVE_PATH", filepath.Join(dir, "archive"))
	t.Setenv("SYMINGEST_DB_PATH", filepath.Join(dir, "symingest.db"))
	t.Setenv("SYMINGEST_INBOX", filepath.Join(dir, "inbox"))
	t.Setenv("PAPERLESS_TOKEN", "secret-token-must-not-leak")
	if err := os.MkdirAll(filepath.Join(dir, "inbox"), 0o700); err != nil {
		t.Fatal(err)
	}

	var sb strings.Builder
	oldStdout := stdout
	stdout = &sb
	defer func() { stdout = oldStdout }()

	if err := run([]string{"service", "--dry-run", "--json", "--vault", filepath.Join(dir, "vault"), "--archive", filepath.Join(dir, "archive"), "--db", filepath.Join(dir, "symingest.db"), "--inbox", filepath.Join(dir, "inbox"), "install"}); err != nil {
		t.Fatalf("run(service install dry-run): %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(sb.String()), &got); err != nil {
		t.Fatalf("decode dry-run JSON: %v\n%s", err, sb.String())
	}
	plist, _ := got["plist"].(string)
	for _, forbidden := range []string{"secret-token-must-not-leak", "PAPERLESS_TOKEN", "token"} {
		if strings.Contains(strings.ToLower(plist), strings.ToLower(forbidden)) {
			t.Fatalf("plist leaked forbidden string %q:\n%s", forbidden, plist)
		}
	}
	for _, want := range []string{"dev.symaira.symingest.watch", "--processing-dir", "--processed-dir", "--failed-dir", "StandardOutPath", "Library/Logs/symingest"} {
		if !strings.Contains(plist, want) {
			t.Fatalf("plist missing %q:\n%s", want, plist)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "Library", "LaunchAgents", serviceLabel+".plist")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run wrote plist unexpectedly: %v", err)
	}
}

func TestRun_ImportPaperless_TokenResolvesEnvScheme(t *testing.T) {
	t.Setenv("TEST_IMPORT_PAPERLESS_TOKEN", "resolved-secret-token")

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/api/documents/":
			json.NewEncoder(w).Encode(map[string]any{"count": 0, "results": []any{}, "next": nil})
		default:
			json.NewEncoder(w).Encode(map[string]any{"count": 0, "results": []any{}, "next": nil})
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	var sb strings.Builder
	oldStdout := stdout
	stdout = &sb
	defer func() { stdout = oldStdout }()

	err := run([]string{
		"import", "paperless",
		"-db", filepath.Join(dir, "test.db"),
		"-base-url", srv.URL,
		"-token", "env://TEST_IMPORT_PAPERLESS_TOKEN",
		"-plan",
	})
	if err != nil {
		t.Fatalf("run(import paperless -plan): %v", err)
	}
	if gotAuth != "Token resolved-secret-token" {
		t.Fatalf("expected resolved token in Authorization header, got %q", gotAuth)
	}
	if strings.Contains(sb.String(), "resolved-secret-token") {
		t.Fatalf("output leaked the resolved token: %s", sb.String())
	}
}

func TestRun_ImportPaperlessDeepRequiresVerify(t *testing.T) {
	err := run([]string{"import", "paperless", "-base-url", "https://paperless.example", "-token", "test-token", "-deep"})
	if err == nil || exitcodes.ExitCodeFromError(err) != exitcodes.ExitData {
		t.Fatalf("expected data error for --deep without --verify, err=%v code=%d", err, exitcodes.ExitCodeFromError(err))
	}
	if !strings.Contains(err.Error(), "--deep is only valid with --verify") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRun_CutoverCheckJSONBlocksMissingEvidence(t *testing.T) {
	var sb strings.Builder
	oldStdout := stdout
	stdout = &sb
	defer func() { stdout = oldStdout }()

	err := run([]string{"cutover-check", "-json"})
	if err == nil || exitcodes.ExitCodeFromError(err) != exitcodes.ExitConflict {
		t.Fatalf("expected conflict for missing evidence, err=%v code=%d", err, exitcodes.ExitCodeFromError(err))
	}
	out := sb.String()
	for _, want := range []string{`"ready": false`, `"dry-run report"`, `"import report"`, `"verify report"`, `"vault validation"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("cutover JSON missing %q:\n%s", want, out)
		}
	}
}

func TestRun_ReportValidateJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migration.json")
	data, err := json.Marshal(&paperlessimport.MigrationReport{
		SchemaVersion: paperlessimport.ReportSchemaVersion,
		ToolVersion:   "test-version",
		Mode:          "import",
		Total:         1,
		Imported:      1,
		Documents:     []paperlessimport.DocumentResult{{ID: 1, Status: "imported"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	var sb strings.Builder
	oldStdout := stdout
	stdout = &sb
	defer func() { stdout = oldStdout }()

	if err := run([]string{"report", "-json", "validate", path}); err != nil {
		t.Fatalf("run(report validate): %v", err)
	}
	out := sb.String()
	for _, want := range []string{`"kind": "migration"`, `"valid": true`, `"schema_version": 1`} {
		if !strings.Contains(out, want) {
			t.Fatalf("report validation JSON missing %q:\n%s", want, out)
		}
	}
}

func TestRun_JobsEmpty(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")
	var sb strings.Builder
	oldStdout := stdout
	stdout = &sb
	defer func() { stdout = oldStdout }()

	if err := run([]string{"jobs", "-db", tempDB}); err != nil {
		t.Fatalf("run(jobs): %v", err)
	}
	if got := strings.TrimSpace(sb.String()); got != "No jobs in queue." {
		t.Fatalf("expected 'No jobs in queue.', got %q", got)
	}
}

func TestRun_JobsJSON(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")
	var sb strings.Builder
	oldStdout := stdout
	stdout = &sb
	defer func() { stdout = oldStdout }()

	if err := run([]string{"jobs", "-db", tempDB, "-json"}); err != nil {
		t.Fatalf("run(jobs -json): %v", err)
	}
	if got := strings.TrimSpace(sb.String()); got != "[]" {
		t.Fatalf("expected '[]', got %q", got)
	}
}

func TestRun_ImportPaperless_PlanWritesReportWithoutVault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/documents/":
			json.NewEncoder(w).Encode(map[string]any{
				"count":   1,
				"results": []map[string]any{{"id": 5, "title": "Plan Doc", "created_date": "2026-01-15", "file_type": ".pdf", "mime_type": "application/pdf"}},
				"next":    nil,
			})
		case "/api/tags/", "/api/correspondents/", "/api/document_types/", "/api/storage_paths/":
			json.NewEncoder(w).Encode(map[string]any{"count": 0, "results": []any{}, "next": nil})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	reportPath := filepath.Join(dir, "plan.json")
	var sb strings.Builder
	oldStdout := stdout
	stdout = &sb
	defer func() { stdout = oldStdout }()

	err := run([]string{
		"import", "paperless",
		"-db", filepath.Join(dir, "test.db"),
		"-base-url", srv.URL,
		"-token", "test-token",
		"-plan",
		"-report", reportPath,
	})
	if err != nil {
		t.Fatalf("run(import paperless -plan): %v", err)
	}
	if !strings.Contains(sb.String(), "Import plan complete") {
		t.Fatalf("plan output missing completion message: %s", sb.String())
	}
	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("read plan report: %v", err)
	}
	var report struct {
		Mode      string `json:"mode"`
		Documents []struct {
			Status    string `json:"status"`
			SourceURI string `json:"source_uri"`
		} `json:"documents"`
		RequiredTools []string `json:"required_tools"`
	}
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("plan report JSON: %v", err)
	}
	if report.Mode != "plan" || len(report.Documents) != 1 || report.Documents[0].Status != "planned" || report.Documents[0].SourceURI != "paperless://documents/5" {
		t.Fatalf("unexpected plan report: %+v", report)
	}
	if len(report.RequiredTools) == 0 {
		t.Fatalf("plan report should identify required tools for PDF/OCR workload: %+v", report)
	}
}

func TestRun_ImportPaperless_StatusEmpty(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")
	var sb strings.Builder
	oldStdout := stdout
	stdout = &sb
	defer func() { stdout = oldStdout }()

	err := run([]string{
		"import", "paperless",
		"-db", tempDB,
		"-base-url", "https://paperless.example",
		"-status",
	})
	if err != nil {
		t.Fatalf("run(import paperless -status): %v", err)
	}
	if got := strings.TrimSpace(sb.String()); !strings.Contains(got, "No recorded import status") {
		t.Fatalf("expected no-status message, got %q", got)
	}
}

func TestRun_ImportPaperless_StatusUsesConfigBaseURL(t *testing.T) {
	home := t.TempDir()
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", home)
	defer os.Setenv("HOME", oldHome)
	config.Loader.ResetCache()
	defer config.Loader.ResetCache()

	cfgDir := filepath.Join(home, ".config", "symingest")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte("paperless_base_url = \"https://paperless.from-config\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var sb strings.Builder
	oldStdout := stdout
	stdout = &sb
	defer func() { stdout = oldStdout }()

	err := run([]string{
		"import", "paperless",
		"-db", filepath.Join(home, "test.db"),
		"-status",
	})
	if err != nil {
		t.Fatalf("run(import paperless -status): %v", err)
	}
	if got := sb.String(); !strings.Contains(got, "https://paperless.from-config") {
		t.Fatalf("status output did not use config base URL: %q", got)
	}
}

func TestRun_ImportPaperless_StatusJSONAfterUpsert(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")
	st, err := store.Open(tempDB)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if err := st.UpsertPaperlessImportState(context.Background(), "https://paperless.example", 42, "failed", "boom"); err != nil {
		t.Fatalf("UpsertPaperlessImportState: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	var sb strings.Builder
	oldStdout := stdout
	stdout = &sb
	defer func() { stdout = oldStdout }()

	err = run([]string{
		"import", "paperless",
		"-db", tempDB,
		"-base-url", "https://paperless.example",
		"-status", "-json",
	})
	if err != nil {
		t.Fatalf("run(import paperless -status -json): %v", err)
	}
	out := sb.String()
	for _, want := range []string{`"paperless_document_id": 42`, `"status": "failed"`, `"last_error": "boom"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRun_ImportPaperless_StatusSummaryAndFailedFilter(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")
	st, err := store.Open(tempDB)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if err := st.UpsertPaperlessImportState(context.Background(), "https://paperless.example", 8, "imported", ""); err != nil {
		t.Fatalf("Upsert imported: %v", err)
	}
	if err := st.UpsertPaperlessImportState(context.Background(), "https://paperless.example", 9, "failed", "ocr failed"); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	var sb strings.Builder
	oldStdout := stdout
	stdout = &sb
	defer func() { stdout = oldStdout }()

	if err := run([]string{"import", "paperless", "-db", tempDB, "-base-url", "https://paperless.example", "-status", "-summary"}); err != nil {
		t.Fatalf("run(status summary): %v", err)
	}
	out := sb.String()
	for _, want := range []string{"total=2", "imported=1", "failed=1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("summary output missing %q: %s", want, out)
		}
	}

	sb.Reset()
	if err := run([]string{"import", "paperless", "-db", tempDB, "-base-url", "https://paperless.example", "-status", "-failed"}); err != nil {
		t.Fatalf("run(status failed): %v", err)
	}
	out = sb.String()
	if !strings.Contains(out, "document 9: failed") || strings.Contains(out, "document 8") {
		t.Fatalf("failed filter output wrong: %s", out)
	}
}

func TestRun_SetupWritesConfigWithoutTokenAndDoctorPasses(t *testing.T) {
	home := t.TempDir()
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", home)
	defer os.Setenv("HOME", oldHome)
	config.Loader.ResetCache()
	defer config.Loader.ResetCache()

	toolsDir := t.TempDir()
	writeTestBin(t, toolsDir, "tesseract", `#!/bin/sh
if [ "$1" = "--list-langs" ]; then
  echo "List of available languages (2):"
  echo "eng"
  echo "deu"
  exit 0
fi
exit 0
`)
	writeTestBin(t, toolsDir, "pdftoppm", "#!/bin/sh\nexit 0\n")
	writeTestBin(t, toolsDir, "sips", "#!/bin/sh\nexit 0\n")
	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", toolsDir+string(os.PathListSeparator)+oldPath)
	defer os.Setenv("PATH", oldPath)
	oldToken := os.Getenv("PAPERLESS_TOKEN")
	os.Setenv("PAPERLESS_TOKEN", "super-secret-token")
	defer os.Setenv("PAPERLESS_TOKEN", oldToken)

	vault := filepath.Join(home, "vault")
	archive := filepath.Join(home, "archive")
	db := filepath.Join(home, "data", "symingest.db")
	inbox := filepath.Join(home, "inbox")
	var sb strings.Builder
	oldStdout := stdout
	stdout = &sb
	defer func() { stdout = oldStdout }()

	if err := run([]string{
		"setup",
		"--vault", vault,
		"--archive", archive,
		"--db", db,
		"--inbox", inbox,
		"--ocr-lang", "eng",
		"--paperless-base-url", "https://paperless.example",
	}); err != nil {
		t.Fatalf("run(setup): %v", err)
	}
	configPath := filepath.Join(home, ".config", "symingest", "config.toml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	content := string(data)
	for _, want := range []string{fmt.Sprintf("vault = %q", vault), fmt.Sprintf("archive_path = %q", archive), fmt.Sprintf("db_path = %q", db), fmt.Sprintf("inbox = %q", inbox), `ocr_lang = "eng"`, `paperless_base_url = "https://paperless.example"`} {
		if !strings.Contains(content, want) {
			t.Fatalf("config missing %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, "super-secret-token") || strings.Contains(sb.String(), "super-secret-token") {
		t.Fatalf("setup leaked Paperless token")
	}

	sb.Reset()
	if err := run([]string{"setup", "--vault", vault, "--archive", archive, "--db", db, "--inbox", inbox, "--ocr-lang", "eng", "--paperless-base-url", "https://paperless.example"}); err != nil {
		t.Fatalf("idempotent setup: %v", err)
	}
	if !strings.Contains(sb.String(), "already up to date") {
		t.Fatalf("expected idempotent message, got %q", sb.String())
	}

	sb.Reset()
	if err := run([]string{"doctor", "--json"}); err != nil {
		t.Fatalf("doctor should pass generated config: %v\n%s", err, sb.String())
	}
	var report doctorReport
	if err := json.Unmarshal([]byte(sb.String()), &report); err != nil {
		t.Fatalf("doctor JSON: %v\n%s", err, sb.String())
	}
	if report.Status != doctorOK || report.Failures != 0 || report.Warnings != 0 {
		t.Fatalf("doctor report = %+v", report)
	}
	for _, name := range []string{"tool.optional.textutil", "tool.optional.pandoc", "tool.optional.libreoffice", "tool.optional.soffice", "tool.optional.pdfinfo", "tool.optional.pdfseparate", "tool.optional.pdfunite", "tool.optional.qpdf"} {
		if !doctorReportHasCheck(report, name) {
			t.Fatalf("doctor report missing optional converter check %q: %+v", name, report.Checks)
		}
	}
}

func doctorReportHasCheck(report doctorReport, name string) bool {
	for _, check := range report.Checks {
		if check.Name == name {
			return true
		}
	}
	return false
}

func TestRun_SetupDryRunShowsDiffAndDoesNotWrite(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	var sb strings.Builder
	oldStdout := stdout
	stdout = &sb
	defer func() { stdout = oldStdout }()

	if err := run([]string{
		"setup",
		"--config", configPath,
		"--vault", filepath.Join(dir, "vault"),
		"--inbox", filepath.Join(dir, "inbox"),
		"--paperless-base-url", "https://paperless.example",
		"--dry-run",
	}); err != nil {
		t.Fatalf("dry-run setup: %v", err)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("dry-run should not write config, stat err=%v", err)
	}
	if out := sb.String(); !strings.Contains(out, "+++ proposed") || !strings.Contains(out, "+ vault =") {
		t.Fatalf("dry-run output missing diff:\n%s", out)
	}
}

func TestRun_DoctorPaperlessJSONDoesNotLeakToken(t *testing.T) {
	toolsDir := t.TempDir()
	writeTestBin(t, toolsDir, "tesseract", `#!/bin/sh
if [ "$1" = "--list-langs" ]; then
  echo "List of available languages (1):"
  echo "eng"
  exit 0
fi
exit 0
`)
	writeTestBin(t, toolsDir, "pdftoppm", "#!/bin/sh\nexit 0\n")
	writeTestBin(t, toolsDir, "sips", "#!/bin/sh\nexit 0\n")
	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", toolsDir+string(os.PathListSeparator)+oldPath)
	defer os.Setenv("PATH", oldPath)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Token secret-token" {
			t.Fatalf("Authorization = %q", got)
		}
		if r.URL.Path != "/api/documents/" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		fmt.Fprintln(w, `{"count": 12, "results": []}`)
	}))
	defer srv.Close()

	dir := t.TempDir()
	var sb strings.Builder
	oldStdout := stdout
	stdout = &sb
	defer func() { stdout = oldStdout }()
	err := run([]string{
		"doctor", "--paperless", "--json",
		"--vault", filepath.Join(dir, "vault"),
		"--archive", filepath.Join(dir, "archive"),
		"--db", filepath.Join(dir, "db.sqlite"),
		"--inbox", filepath.Join(dir, "inbox"),
		"--ocr-lang", "eng",
		"--base-url", srv.URL,
		"--token", "secret-token",
	})
	if err != nil {
		t.Fatalf("doctor paperless: %v\n%s", err, sb.String())
	}
	if strings.Contains(sb.String(), "secret-token") {
		t.Fatalf("doctor leaked token:\n%s", sb.String())
	}
	var report doctorReport
	if err := json.Unmarshal([]byte(sb.String()), &report); err != nil {
		t.Fatalf("doctor JSON: %v", err)
	}
	if report.Status != doctorOK {
		t.Fatalf("report status = %s, want ok: %+v", report.Status, report)
	}
}

func TestRun_DoctorExitCodesForWarningsAndFailures(t *testing.T) {
	toolsDir := t.TempDir()
	writeTestBin(t, toolsDir, "tesseract", `#!/bin/sh
if [ "$1" = "--list-langs" ]; then
  echo "eng"
  exit 0
fi
exit 0
`)
	writeTestBin(t, toolsDir, "pdftoppm", "#!/bin/sh\nexit 0\n")
	writeTestBin(t, toolsDir, "sips", "#!/bin/sh\nexit 0\n")
	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", toolsDir+string(os.PathListSeparator)+oldPath)
	defer os.Setenv("PATH", oldPath)

	dir := t.TempDir()
	oldStdout := stdout
	stdout = &strings.Builder{}
	defer func() { stdout = oldStdout }()

	warnErr := run([]string{"doctor", "--vault", filepath.Join(dir, "vault"), "--archive", filepath.Join(dir, "archive"), "--db", filepath.Join(dir, "db.sqlite"), "--ocr-lang", "eng"})
	if warnErr == nil || exitcodes.ExitCodeFromError(warnErr) != exitcodes.ExitNoInput {
		t.Fatalf("warning-only doctor error = %v, code=%d", warnErr, exitcodes.ExitCodeFromError(warnErr))
	}

	oldPath = os.Getenv("PATH")
	os.Setenv("PATH", t.TempDir())
	defer os.Setenv("PATH", oldPath)
	failErr := run([]string{"doctor", "--vault", filepath.Join(dir, "vault2"), "--archive", filepath.Join(dir, "archive2"), "--db", filepath.Join(dir, "db2.sqlite"), "--inbox", filepath.Join(dir, "inbox"), "--ocr-lang", "eng"})
	if failErr == nil || exitcodes.ExitCodeFromError(failErr) != exitcodes.ExitGeneric {
		t.Fatalf("failure doctor error = %v, code=%d", failErr, exitcodes.ExitCodeFromError(failErr))
	}
}

func writeTestBin(t *testing.T, dir, name, script string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake %s: %v", name, err)
	}
	return path
}

func TestResolveConfig_FlagOverridesEnv(t *testing.T) {
	config.Loader.ResetCache()
	save := setEnv(t, map[string]string{
		"SYMINGEST_VAULT":        "/env/vault",
		"SYMINGEST_ARCHIVE_PATH": "/env/archive",
		"SYMINGEST_DB_PATH":      "/env/test.db",
		"SYMINGEST_OCR_LANG":     "deu",
	})
	defer save()

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	ocrLang, vault, archive, db := registerSharedFlags(fs)
	args := []string{
		"-vault", "/flag/vault",
		"-archive", "/flag/archive",
		"-db", "/flag/test.db",
		"-ocr-lang", "fra",
	}
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse: %v", err)
	}

	cfg, err := resolveConfig(fs, ocrLang, vault, archive, db)
	if err != nil {
		t.Fatalf("resolveConfig: %v", err)
	}

	if cfg.vault != "/flag/vault" {
		t.Errorf("vault = %q, want /flag/vault", cfg.vault)
	}
	if cfg.archive != "/flag/archive" {
		t.Errorf("archive = %q, want /flag/archive", cfg.archive)
	}
	if cfg.db != "/flag/test.db" {
		t.Errorf("db = %q, want /flag/test.db", cfg.db)
	}
	if cfg.ocrLang != "fra" {
		t.Errorf("ocrLang = %q, want fra", cfg.ocrLang)
	}
}

func TestResolveConfig_EnvUsedWhenFlagsEmpty(t *testing.T) {
	config.Loader.ResetCache()
	save := setEnv(t, map[string]string{
		"SYMINGEST_VAULT":        "/env/vault",
		"SYMINGEST_ARCHIVE_PATH": "/env/archive",
		"SYMINGEST_DB_PATH":      "/env/test.db",
		"SYMINGEST_OCR_LANG":     "deu",
	})
	defer save()

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	ocrLang, vault, archive, db := registerSharedFlags(fs)
	if err := fs.Parse([]string{}); err != nil {
		t.Fatalf("parse: %v", err)
	}

	cfg, err := resolveConfig(fs, ocrLang, vault, archive, db)
	if err != nil {
		t.Fatalf("resolveConfig: %v", err)
	}

	if cfg.vault != "/env/vault" {
		t.Errorf("vault = %q, want /env/vault", cfg.vault)
	}
	if cfg.archive != "/env/archive" {
		t.Errorf("archive = %q, want /env/archive", cfg.archive)
	}
	if cfg.db != "/env/test.db" {
		t.Errorf("db = %q, want /env/test.db", cfg.db)
	}
	if cfg.ocrLang != "deu" {
		t.Errorf("ocrLang = %q, want deu", cfg.ocrLang)
	}
}

func TestResolveConfig_DefaultsWhenNothingSet(t *testing.T) {
	config.Loader.ResetCache()
	save := setEnv(t, map[string]string{
		"SYMINGEST_VAULT":        "",
		"SYMINGEST_ARCHIVE_PATH": "",
		"SYMINGEST_DB_PATH":      "",
		"SYMINGEST_OCR_LANG":     "",
	})
	defer save()

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	ocrLang, vault, archive, db := registerSharedFlags(fs)
	if err := fs.Parse([]string{}); err != nil {
		t.Fatalf("parse: %v", err)
	}

	cfg, err := resolveConfig(fs, ocrLang, vault, archive, db)
	if err != nil {
		t.Fatalf("resolveConfig: %v", err)
	}

	if cfg.ocrLang != "eng" {
		t.Errorf("ocrLang = %q, want eng (hardcoded default)", cfg.ocrLang)
	}
	if cfg.vault != "" {
		t.Errorf("vault = %q, want empty (no default)", cfg.vault)
	}
	if cfg.archive == "" {
		t.Error("archive should have XDG default, got empty")
	}
	if cfg.db == "" {
		t.Error("db should have XDG default, got empty")
	}
}

func TestParseDocumentIDs(t *testing.T) {
	t.Run("valid list tolerates whitespace and empty entries", func(t *testing.T) {
		ids, err := parseDocumentIDs(" 123, 456 ,,789 ")
		if err != nil {
			t.Fatalf("parseDocumentIDs: %v", err)
		}
		want := []int{123, 456, 789}
		if len(ids) != len(want) {
			t.Fatalf("ids = %v, want %v", ids, want)
		}
		for i, v := range want {
			if ids[i] != v {
				t.Errorf("ids[%d] = %d, want %d", i, ids[i], v)
			}
		}
	})
	t.Run("empty input yields no ids", func(t *testing.T) {
		ids, err := parseDocumentIDs("   ")
		if err != nil {
			t.Fatalf("parseDocumentIDs: %v", err)
		}
		if ids != nil {
			t.Errorf("ids = %v, want nil", ids)
		}
	})
	t.Run("non-numeric entry errors", func(t *testing.T) {
		if _, err := parseDocumentIDs("123,abc"); err == nil {
			t.Error("expected error for non-numeric ID")
		}
	})
	t.Run("non-positive entry errors", func(t *testing.T) {
		if _, err := parseDocumentIDs("123,0"); err == nil {
			t.Error("expected error for zero ID")
		}
		if _, err := parseDocumentIDs("-5"); err == nil {
			t.Error("expected error for negative ID")
		}
	})
}

func setEnv(t *testing.T, vars map[string]string) func() {
	t.Helper()
	origins := make(map[string]string, len(vars))
	for k, v := range vars {
		origins[k] = os.Getenv(k)
		os.Setenv(k, v)
	}
	return func() {
		for k, orig := range origins {
			if orig == "" {
				os.Unsetenv(k)
			} else {
				os.Setenv(k, orig)
			}
		}
	}
}

type doctorFakeIMAPClient struct {
	loginErr    error
	selectErr   error
	loggedIn    bool
	selectedDir string
	loggedOut   bool
}

func (f *doctorFakeIMAPClient) Login(username, password string) error {
	f.loggedIn = true
	return f.loginErr
}

func (f *doctorFakeIMAPClient) Select(folder string) error {
	f.selectedDir = folder
	return f.selectErr
}

func (f *doctorFakeIMAPClient) Logout() error {
	f.loggedOut = true
	return nil
}

func TestCheckIMAP_SecretResolutionFailure(t *testing.T) {
	origDial := doctorDialIMAP
	defer func() { doctorDialIMAP = origDial }()

	var dialCalled bool
	doctorDialIMAP = func(addr, host string) (doctorIMAPClient, error) {
		dialCalled = true
		return nil, errors.New("should not reach dial")
	}

	report := &doctorReport{Status: doctorOK}
	accounts := []config.IMAPAccount{{
		Username:       "user@example.com",
		PasswordSecret: "env://CHECKIMAP_NONEXISTENT_VAR_12345",
		Host:           "imap.example.com",
		Port:           993,
	}}
	checkIMAP(context.Background(), report, accounts)

	if dialCalled {
		t.Fatal("dial should not be called when secret resolution fails")
	}
	if report.Failures != 1 {
		t.Fatalf("expected 1 failure, got %d", report.Failures)
	}
	if report.Checks[0].Status != doctorFail {
		t.Fatalf("expected fail status, got %s", report.Checks[0].Status)
	}
	if !strings.Contains(report.Checks[0].Message, "cannot resolve password") {
		t.Fatalf("unexpected message: %s", report.Checks[0].Message)
	}
}

func TestCheckIMAP_WarnsOnPlaintextPasswordSecret(t *testing.T) {
	origDial := doctorDialIMAP
	defer func() { doctorDialIMAP = origDial }()

	fake := &doctorFakeIMAPClient{}
	doctorDialIMAP = func(addr, host string) (doctorIMAPClient, error) {
		return fake, nil
	}

	report := &doctorReport{Status: doctorOK}
	accounts := []config.IMAPAccount{{
		Username:       "user@example.com",
		PasswordSecret: "hunter2",
		Host:           "imap.example.com",
		Port:           993,
	}}
	checkIMAP(context.Background(), report, accounts)

	if report.Warnings != 1 {
		t.Fatalf("expected 1 warning, got %d", report.Warnings)
	}
	if report.Checks[0].Status != doctorWarn {
		t.Fatalf("expected first check to warn, got %s", report.Checks[0].Status)
	}
	if !strings.Contains(report.Checks[0].Message, "plaintext") {
		t.Fatalf("expected warning message to mention plaintext, got: %s", report.Checks[0].Message)
	}
	if strings.Contains(report.Checks[0].Message, "hunter2") {
		t.Fatalf("warning message must not leak the plaintext secret, got: %s", report.Checks[0].Message)
	}
}

func TestCheckIMAP_NoWarningForResolvedSecret(t *testing.T) {
	origDial := doctorDialIMAP
	defer func() { doctorDialIMAP = origDial }()

	fake := &doctorFakeIMAPClient{}
	doctorDialIMAP = func(addr, host string) (doctorIMAPClient, error) {
		return fake, nil
	}

	os.Setenv("TEST_CHECKIMAP_NO_WARN", "value")
	defer os.Unsetenv("TEST_CHECKIMAP_NO_WARN")

	report := &doctorReport{Status: doctorOK}
	accounts := []config.IMAPAccount{{
		Username:       "user@example.com",
		PasswordSecret: "env://TEST_CHECKIMAP_NO_WARN",
		Host:           "imap.example.com",
		Port:           993,
	}}
	checkIMAP(context.Background(), report, accounts)

	if report.Warnings != 0 {
		t.Fatalf("expected 0 warnings, got %d", report.Warnings)
	}
}

func TestCheckIMAP_DialFailure(t *testing.T) {
	origDial := doctorDialIMAP
	defer func() { doctorDialIMAP = origDial }()

	doctorDialIMAP = func(addr, host string) (doctorIMAPClient, error) {
		return nil, errors.New("connection refused")
	}

	report := &doctorReport{Status: doctorOK}
	accounts := []config.IMAPAccount{{
		Username:       "user@example.com",
		PasswordSecret: "plaintext-pw",
		Host:           "imap.example.com",
		Port:           993,
	}}
	checkIMAP(context.Background(), report, accounts)

	if report.Failures != 1 {
		t.Fatalf("expected 1 failure, got %d", report.Failures)
	}
	if !strings.Contains(report.Checks[1].Message, "cannot connect") {
		t.Fatalf("unexpected message: %s", report.Checks[1].Message)
	}
}

func TestCheckIMAP_LoginFailure(t *testing.T) {
	origDial := doctorDialIMAP
	defer func() { doctorDialIMAP = origDial }()

	fake := &doctorFakeIMAPClient{loginErr: errors.New("invalid credentials")}
	doctorDialIMAP = func(addr, host string) (doctorIMAPClient, error) {
		return fake, nil
	}

	report := &doctorReport{Status: doctorOK}
	accounts := []config.IMAPAccount{{
		Username:       "user@example.com",
		PasswordSecret: "plaintext-pw",
		Host:           "imap.example.com",
		Port:           993,
	}}
	checkIMAP(context.Background(), report, accounts)

	if report.Failures != 1 {
		t.Fatalf("expected 1 failure, got %d", report.Failures)
	}
	if !fake.loggedOut {
		t.Fatal("expected client to be logged out after login failure")
	}
	if !strings.Contains(report.Checks[1].Message, "login failed") {
		t.Fatalf("unexpected message: %s", report.Checks[1].Message)
	}
}

func TestCheckIMAP_FolderSelectionFailure(t *testing.T) {
	origDial := doctorDialIMAP
	defer func() { doctorDialIMAP = origDial }()

	fake := &doctorFakeIMAPClient{selectErr: errors.New("folder not found")}
	doctorDialIMAP = func(addr, host string) (doctorIMAPClient, error) {
		return fake, nil
	}

	report := &doctorReport{Status: doctorOK}
	accounts := []config.IMAPAccount{{
		Username:       "user@example.com",
		PasswordSecret: "plaintext-pw",
		Host:           "imap.example.com",
		Port:           993,
		Folder:         "BadFolder",
	}}
	checkIMAP(context.Background(), report, accounts)

	if report.Failures != 1 {
		t.Fatalf("expected 1 failure, got %d", report.Failures)
	}
	if !fake.loggedOut {
		t.Fatal("expected client to be logged out after select failure")
	}
	if !strings.Contains(report.Checks[1].Message, "cannot select folder") {
		t.Fatalf("unexpected message: %s", report.Checks[1].Message)
	}
}

func TestCheckIMAP_Success(t *testing.T) {
	origDial := doctorDialIMAP
	defer func() { doctorDialIMAP = origDial }()

	fake := &doctorFakeIMAPClient{}
	doctorDialIMAP = func(addr, host string) (doctorIMAPClient, error) {
		return fake, nil
	}

	report := &doctorReport{Status: doctorOK}
	accounts := []config.IMAPAccount{{
		Username:       "user@example.com",
		PasswordSecret: "plaintext-pw",
		Host:           "imap.example.com",
		Port:           993,
	}}
	checkIMAP(context.Background(), report, accounts)

	if report.Failures != 0 {
		t.Fatalf("expected 0 failures, got %d", report.Failures)
	}
	if report.Checks[1].Status != doctorOK {
		t.Fatalf("expected ok status, got %s", report.Checks[1].Status)
	}
	if !fake.loggedOut {
		t.Fatal("expected client to be logged out")
	}
	if fake.selectedDir != "INBOX" {
		t.Fatalf("expected default folder INBOX, got %s", fake.selectedDir)
	}
}

func TestCheckIMAP_EmptyFolderDefaultsToINBOX(t *testing.T) {
	origDial := doctorDialIMAP
	defer func() { doctorDialIMAP = origDial }()

	fake := &doctorFakeIMAPClient{}
	doctorDialIMAP = func(addr, host string) (doctorIMAPClient, error) {
		return fake, nil
	}

	report := &doctorReport{Status: doctorOK}
	accounts := []config.IMAPAccount{{
		Username:       "user@example.com",
		PasswordSecret: "secret",
		Host:           "imap.example.com",
		Port:           993,
		Folder:         "",
	}}
	checkIMAP(context.Background(), report, accounts)

	if fake.selectedDir != "INBOX" {
		t.Fatalf("expected INBOX default, got %s", fake.selectedDir)
	}
}

func TestCheckIMAP_MultipleAccounts(t *testing.T) {
	origDial := doctorDialIMAP
	defer func() { doctorDialIMAP = origDial }()

	callCount := 0
	doctorDialIMAP = func(addr, host string) (doctorIMAPClient, error) {
		callCount++
		if callCount == 1 {
			return nil, errors.New("first fails")
		}
		return &doctorFakeIMAPClient{}, nil
	}

	report := &doctorReport{Status: doctorOK}
	accounts := []config.IMAPAccount{
		{Username: "a@test.com", PasswordSecret: "pw1", Host: "h1", Port: 993},
		{Username: "b@test.com", PasswordSecret: "pw2", Host: "h2", Port: 993},
	}
	checkIMAP(context.Background(), report, accounts)

	if report.Failures != 1 {
		t.Fatalf("expected 1 failure, got %d", report.Failures)
	}
	if len(report.Checks) != 4 {
		t.Fatalf("expected 4 checks, got %d", len(report.Checks))
	}
	if report.Checks[0].Status != doctorWarn {
		t.Fatalf("first check should warn about plaintext secret, got %s", report.Checks[0].Status)
	}
	if report.Checks[1].Status != doctorFail {
		t.Fatalf("second check should fail, got %s", report.Checks[1].Status)
	}
	if report.Checks[2].Status != doctorWarn {
		t.Fatalf("third check should warn about plaintext secret, got %s", report.Checks[2].Status)
	}
	if report.Checks[3].Status != doctorOK {
		t.Fatalf("fourth check should pass, got %s", report.Checks[3].Status)
	}
}

func TestRunWatch_InvalidIMAPPollInterval(t *testing.T) {
	_, err := time.ParseDuration("5xyz")
	if err == nil {
		t.Fatal("expected parse to fail")
	}

	err = exitcodes.Wrapf(nil, exitcodes.ExitConfig, exitcodes.KindConfig,
		"imap_poll_interval: invalid duration %q", "5xyz")
	if err == nil {
		t.Fatal("expected error")
	}
	var exitErr *exitcodes.CLIError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected exitcodes.CLIError, got %T", err)
	}
	if exitErr.Kind != exitcodes.KindConfig {
		t.Fatalf("expected KindConfig, got %s", exitErr.Kind)
	}
	if !strings.Contains(exitErr.Error(), "5xyz") {
		t.Fatalf("expected error to contain the invalid value, got: %s", exitErr.Error())
	}
}
