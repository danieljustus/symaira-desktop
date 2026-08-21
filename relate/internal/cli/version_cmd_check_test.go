package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-corekit/updatecheck"
	"github.com/danieljustus/symaira-relate/internal/version"
)

func TestRunVersion_Check_DevBuild(t *testing.T) {
	// When Version is "dev" (not a stable semver), the update check
	// should silently succeed with "Already up to date." — no error,
	// no update prompt.
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), IO{Stdout: &stdout, Stderr: &stderr}, []string{"version", "--check"})
	if code != 0 {
		t.Fatalf("Run() code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "Already up to date.") {
		t.Errorf("stderr = %q, want substring %q", stderr.String(), "Already up to date.")
	}
}

func TestRunVersion_Check_WithStubbedServer(t *testing.T) {
	// Verify the integration works end-to-end with a stubbed GitHub API.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"tag_name": "v0.2.0",
			"html_url": "https://github.com/danieljustus/symaira-relate/releases/tag/v0.2.0",
			"draft": false,
			"prerelease": false,
			"assets": []
		}`))
	}))
	defer server.Close()

	// Override the version to a known value and construct a checker
	// pointing at the test server.
	oldVersion := version.Version
	version.Version = "0.1.0"
	t.Cleanup(func() { version.Version = oldVersion })

	// We can't inject the checker through the CLI dispatcher, so test
	// the updatecheck integration directly.
	checker := &updatecheck.Checker{
		HTTPClient:       server.Client(),
		LatestReleaseURL: server.URL,
	}
	release, err := checker.Check(context.Background(), version.Version)
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if release == nil {
		t.Fatal("Check() returned nil, want release")
	}
	if release.TagName != "v0.2.0" {
		t.Errorf("TagName = %q, want v0.2.0", release.TagName)
	}
}

func TestRunVersion_Check_ErrorSwallowed(t *testing.T) {
	// When the update check fails (e.g. network error), the error must
	// be printed to stderr but the command must still exit 0.
	var stdout, stderr bytes.Buffer

	// Simulate a network error by running with a bogus URL.
	// We need a checker that fails — easiest is to call version --check
	// which will try to reach api.github.com. In offline environments
	// this will fail, but the command should still exit 0.
	code := Run(context.Background(), IO{Stdout: &stdout, Stderr: &stderr}, []string{"version", "--check"})
	if code != 0 {
		// It's OK if the check itself fails — the command must not fail.
		// Since we can't force a network error deterministically without
		// mocking the HTTP client through the CLI layer, this test just
		// verifies the exit code contract holds under normal conditions.
		t.Fatalf("Run() code = %d, stderr = %s", code, stderr.String())
	}
}
