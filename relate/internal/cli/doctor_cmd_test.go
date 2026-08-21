package cli

import (
	"bytes"
	"context"
	"testing"
)

func TestRunDoctor_DiagnosticsOnStderr(t *testing.T) {
	t.Setenv("SYMRELATE_CONFIG_HOME", t.TempDir())
	t.Setenv("SYMRELATE_DATA_HOME", t.TempDir())
	t.Setenv("SYMRELATE_CACHE_HOME", t.TempDir())

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), IO{Stdout: &stdout, Stderr: &stderr}, []string{"doctor"})
	if code != 0 {
		t.Fatalf("Run() code = %d, stderr = %s", code, stderr.String())
	}
	if stderr.Len() == 0 {
		t.Error("expected doctor diagnostics on stderr")
	}
	if stdout.String() != "ok\n" {
		t.Errorf("stdout = %q, want \"ok\\n\"", stdout.String())
	}
}
