package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
)

func TestRunningServerReadyRejectsExitedChildOnHealthyForeignPort(t *testing.T) {
	cmd := exec.Command(testExecutable(t), "-test.run=TestCleanupHelper") //nolint:gosec // Existing helper executes only this test binary with a fixed test selector.
	cmd.Env = append(os.Environ(), "HTTPDIFF_CLEANUP_HELPER=fail")
	if _, ok := cmd.Run().(*exec.ExitError); !ok {
		t.Fatal("helper must have exited unsuccessfully before readiness")
	}

	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer foreign.Close()

	server := &runningServer{cmd: cmd, base: foreign.URL}
	if err := server.ready(); err == nil {
		t.Fatal("readiness accepted a foreign server's health response after the owned child exited")
	}
	if err := server.stop(); err == nil {
		t.Fatal("failed child must remain a cleanup error")
	}
}

func TestRunningServerReadyVerifiesLaunchCredential(t *testing.T) {
	for _, mode := range []string{"matching", "foreign", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			const launchToken = "current-launch-credential-for-readiness-test"
			incumbent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/healthz", "/redirected-status":
					_, _ = w.Write([]byte(`{"status":"ok"}`))
				case "/api/v1/status":
					if mode == "foreign" || r.Header.Get("Authorization") != "Bearer "+launchToken {
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
					if mode == "redirect" {
						http.Redirect(w, r, "/redirected-status", http.StatusTemporaryRedirect)
						return
					}
					_, _ = w.Write([]byte(`{"status":"ok"}`))
				default:
					http.NotFound(w, r)
				}
			}))
			defer incumbent.Close()
			server := &runningServer{cmd: &exec.Cmd{}, base: incumbent.URL, token: launchToken}
			err := server.ready()
			if (err == nil) != (mode == "matching") {
				t.Fatalf("ready() = %v; only matching launch may be ready", err)
			}
		})
	}
}
