package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		fmt.Fprintln(os.Stderr, "stub startup failure")
		os.Exit(17)
	}
	os.Exit(m.Run())
}

func TestReadyReportsImmediateServerExit(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	server, err := startServer(binary, root, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := server.stop(); err != nil {
			t.Error(err)
		}
	})
	started := time.Now()
	err = server.ready(&http.Client{Timeout: time.Second})
	if err == nil || !strings.Contains(err.Error(), "server exited") || strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected early-exit diagnostic, got %v", err)
	}
	if !strings.Contains(err.Error(), "17") || !strings.Contains(err.Error(), "stub startup failure") {
		t.Fatalf("missing exit status or stderr: %v", err)
	}
	if elapsed := time.Since(started); elapsed >= 5*time.Second {
		t.Fatalf("early exit took %s", elapsed)
	}
	// Readiness and repeated stop calls must all observe the same completed waiter.
	for range 2 {
		if err := server.stop(); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-server.done:
	default:
		t.Fatal("server was not reaped")
	}
}
