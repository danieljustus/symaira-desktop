package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

func TestRunVersion_JSON(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), IO{Stdout: &stdout, Stderr: &stderr}, []string{"version", "--json"})
	if code != 0 {
		t.Fatalf("Run() code = %d, stderr = %s", code, stderr.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("stdout is not valid JSON: %v (%s)", err, stdout.String())
	}
	if payload["tool"] != "symrelate" {
		t.Errorf("tool = %v, want symrelate", payload["tool"])
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty for --json output", stderr.String())
	}
}

func TestRunVersion_Human(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), IO{Stdout: &stdout, Stderr: &stderr}, []string{"version"})
	if code != 0 {
		t.Fatalf("Run() code = %d, stderr = %s", code, stderr.String())
	}
	if stdout.Len() == 0 {
		t.Error("expected human-readable output on stdout")
	}
}

func TestRun_UnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), IO{Stdout: &stdout, Stderr: &stderr}, []string{"bogus"})
	if code != 2 {
		t.Errorf("Run() code = %d, want 2", code)
	}
}
