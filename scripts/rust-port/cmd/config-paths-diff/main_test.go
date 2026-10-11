package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

func TestRetainPreflightFailureKeepsRawStreamsAndSourceClosure(t *testing.T) {
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(sourcePath), "../../../../"))
	evidence := t.TempDir()
	stdout := []byte{}
	stderr := []byte("FAIL capture failed; retained evidence: missing source\n")
	captureErr := errors.New("read source scripts/rust-port/missing.go: no such file")

	if err := retainPreflightFailure(evidence, "", "", root, filepath.Join(evidence, "missing-corekit"), report{}, captureErr, stdout, stderr); err != nil {
		t.Fatalf("retain failure: %v", err)
	}
	gotStdout, err := os.ReadFile(filepath.Join(evidence, "preflight.runner.stdout"))
	if err != nil {
		t.Fatal(err)
	}
	gotStderr, err := os.ReadFile(filepath.Join(evidence, "preflight.runner.stderr"))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotStdout) != string(stdout) || string(gotStderr) != string(stderr) {
		t.Fatalf("raw runner streams changed: stdout=%q stderr=%q", gotStdout, gotStderr)
	}

	content, err := os.ReadFile(filepath.Join(evidence, "preflight.failure.json"))
	if err != nil {
		t.Fatal(err)
	}
	var receipt preflightFailure
	if err := json.Unmarshal(content, &receipt); err != nil {
		t.Fatalf("decode receipt: %v", err)
	}
	if receipt.Status != "preflight_failed" || receipt.Phase != "source_preflight" || receipt.Error != captureErr.Error() || receipt.ExitCode != 1 {
		t.Fatalf("failure identity = %#v", receipt)
	}
	if receipt.HostOS != runtime.GOOS || receipt.HostArch != runtime.GOARCH {
		t.Fatalf("failure receipt platform = %s/%s, want %s/%s", receipt.HostOS, receipt.HostArch, runtime.GOOS, runtime.GOARCH)
	}
	if receipt.DeclaredCaseCount != 0 || receipt.ExecutedCaseCount != 0 || receipt.NativeCaptureClaimed {
		t.Fatalf("preflight receipt falsely claims CLI capture: %#v", receipt)
	}
	if receipt.StdoutBytes != len(stdout) || receipt.StdoutSHA256 != digest(stdout) || receipt.StderrBytes != len(stderr) || receipt.StderrSHA256 != digest(stderr) {
		t.Fatalf("runner stream metadata does not match raw files: %#v", receipt)
	}
	for _, required := range []string{"go.mod", "go.sum", "cmd/symdesk/main.go", "cmd/symdesk/commands.go", "cmd/symdesk/config.go"} {
		if !slices.Contains(receipt.GoSource.Inputs, required) {
			t.Errorf("Go source closure omitted %q", required)
		}
	}
	for _, forbidden := range []string{"scripts/rust-port/go.mod", "scripts/rust-port/go.sum"} {
		if slices.Contains(receipt.GoSource.Inputs, forbidden) || slices.Contains(receipt.HarnessSource.Inputs, forbidden) {
			t.Errorf("Go source closure unexpectedly contains removed nested module %q", forbidden)
		}
	}
	if len(receipt.GoSource.Missing) != 0 {
		t.Fatalf("Go source inputs are missing: %v", receipt.GoSource.Missing)
	}
	if len(receipt.RustSource.Missing) != 0 || len(receipt.HarnessSource.Missing) != 0 {
		t.Fatalf("source closure has missing Rust/harness inputs: rust=%v harness=%v", receipt.RustSource.Missing, receipt.HarnessSource.Missing)
	}
	for _, required := range []string{
		"scripts/rust-port/cmd/config-paths-diff/preflight.go",
		"scripts/rust-port/cmd/config-paths-diff/main_test.go",
		"scripts/rust-port/cmd/config-paths-diff/cases.go",
	} {
		if !slices.Contains(receipt.HarnessSource.Inputs, required) {
			t.Errorf("harness source closure omitted %q", required)
		}
	}
}

func TestWriteNewPrivateRefusesToOverwriteExistingEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "retained.stdout")
	original := []byte("original bytes\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeNewPrivate(path, []byte("replacement bytes\n")); err == nil {
		t.Fatal("overwrote existing evidence")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("existing evidence changed: %q", got)
	}
}
