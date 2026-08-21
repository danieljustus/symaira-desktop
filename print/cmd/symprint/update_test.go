package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danieljustus/symaira-corekit/updatecheck"
)

// stubDoer is an httpDoer-compatible stub for the update checker. It counts
// requests so tests can assert the cache TTL prevents repeated network calls.
type stubDoer struct {
	calls  atomic.Int32
	status int
	body   string
	err    error
}

func (s *stubDoer) Do(_ *http.Request) (*http.Response, error) {
	s.calls.Add(1)
	if s.err != nil {
		return nil, s.err
	}
	return &http.Response{
		StatusCode: s.status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(s.body)),
	}, nil
}

func newStubChecker(doer *stubDoer) *updatecheck.Checker {
	return &updatecheck.Checker{
		HTTPClient:       doer,
		LatestReleaseURL: "https://example.invalid/releases/latest",
		CacheTTL:         updatecheck.DefaultCacheTTL,
	}
}

const newerReleaseJSON = `{
	"tag_name": "v0.3.0",
	"html_url": "https://github.com/danieljustus/symaira-print/releases/tag/v0.3.0",
	"draft": false,
	"prerelease": false
}`

func TestUpdateHint_NewerRelease(t *testing.T) {
	doer := &stubDoer{status: http.StatusOK, body: newerReleaseJSON}
	hint := updateHint(context.Background(), newStubChecker(doer), "0.2.0")
	if hint == "" {
		t.Fatal("expected a hint for a newer release, got empty string")
	}
	if !strings.Contains(hint, "v0.3.0") {
		t.Errorf("hint %q does not contain the new version", hint)
	}
	if !strings.Contains(hint, "https://github.com/danieljustus/symaira-print/releases/tag/v0.3.0") {
		t.Errorf("hint %q does not contain the release URL", hint)
	}
}

func TestUpdateHint_UpToDate(t *testing.T) {
	doer := &stubDoer{status: http.StatusOK, body: newerReleaseJSON}
	if hint := updateHint(context.Background(), newStubChecker(doer), "0.3.0"); hint != "" {
		t.Errorf("expected no hint when up to date, got %q", hint)
	}
}

func TestUpdateHint_DevBuildSkipsNetwork(t *testing.T) {
	for _, devVersion := range []string{"dev", "0.2.0-dev", "0.2.0-rc.1", ""} {
		doer := &stubDoer{status: http.StatusOK, body: newerReleaseJSON}
		if hint := updateHint(context.Background(), newStubChecker(doer), devVersion); hint != "" {
			t.Errorf("version %q: expected no hint for a dev build, got %q", devVersion, hint)
		}
		if got := doer.calls.Load(); got != 0 {
			t.Errorf("version %q: expected no network call for a dev build, got %d calls", devVersion, got)
		}
	}
}

func TestUpdateHint_ErrorsSwallowed(t *testing.T) {
	cases := []struct {
		name string
		doer *stubDoer
	}{
		{"network error (offline)", &stubDoer{err: errors.New("dial tcp: no route to host")}},
		{"http error", &stubDoer{status: http.StatusInternalServerError, body: "{}"}},
		{"malformed json", &stubDoer{status: http.StatusOK, body: "not json"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if hint := updateHint(context.Background(), newStubChecker(tc.doer), "0.2.0"); hint != "" {
				t.Errorf("expected no hint on check failure, got %q", hint)
			}
		})
	}
}

func TestUpdateHint_CacheTTLPreventsRepeatedNetwork(t *testing.T) {
	doer := &stubDoer{status: http.StatusOK, body: newerReleaseJSON}
	checker := newStubChecker(doer)
	checker.CacheTTL = time.Hour

	first := updateHint(context.Background(), checker, "0.2.0")
	second := updateHint(context.Background(), checker, "0.2.0")
	if first == "" || second == "" {
		t.Fatalf("expected hints from both calls, got %q and %q", first, second)
	}
	if got := doer.calls.Load(); got != 1 {
		t.Errorf("expected 1 network call within the cache TTL, got %d", got)
	}
}

func TestVersionCmd_CheckFlagExists(t *testing.T) {
	cmd := newVersionCmd()
	if cmd.Flags().Lookup("check") == nil {
		t.Fatal("version command is missing the --check flag")
	}
}
