package symmemory

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// withFakeBinary puts a fake `symmemory` executable script on PATH for
// the duration of the test, replacing whatever real PATH entries existed.
// script is a POSIX shell script body (the shebang is added here).
func withFakeBinary(t *testing.T, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake shell-script binary not supported on windows")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "symmemory")
	content := "#!/bin/sh\n" + script + "\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("failed to write fake symmemory binary: %v", err)
	}
	// Prepend, don't replace: exec.LookPath still finds the fake
	// "symmemory" first (shadowing any real installation), but the
	// script's own use of external commands (sleep, cat, ...) still
	// resolves via the rest of the real PATH instead of failing with
	// "command not found" and silently falling through.
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func withEmptyPath(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir()) // a directory with nothing in it, and nothing else on PATH
}

func TestDiscover_MissingBinary_ReturnsErrUnavailable(t *testing.T) {
	withEmptyPath(t)
	_, err := Discover(context.Background())
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("Discover() error = %v, want ErrUnavailable", err)
	}
}

func TestDiscover_ValidResponse_Succeeds(t *testing.T) {
	withFakeBinary(t, `echo '{"tool":"symmemory","version":"v1.2.3","schema_version":7}'`)
	info, err := Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if info.Tool != "symmemory" || info.Version != "v1.2.3" || info.SchemaVersion != 7 {
		t.Errorf("Discover() = %+v, want tool=symmemory version=v1.2.3 schema_version=7", info)
	}
}

func TestDiscover_MalformedJSON_ReturnsErrUnavailable(t *testing.T) {
	withFakeBinary(t, `echo 'not json at all'`)
	_, err := Discover(context.Background())
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("Discover() error = %v, want ErrUnavailable", err)
	}
}

func TestDiscover_WrongTool_ReturnsErrUnavailable(t *testing.T) {
	withFakeBinary(t, `echo '{"tool":"something-else","version":"v1.0.0","schema_version":1}'`)
	_, err := Discover(context.Background())
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("Discover() error = %v, want ErrUnavailable", err)
	}
}

func TestDiscover_NonZeroExit_ReturnsErrUnavailable(t *testing.T) {
	withFakeBinary(t, `exit 1`)
	_, err := Discover(context.Background())
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("Discover() error = %v, want ErrUnavailable", err)
	}
}

func TestDiscover_Timeout_ReturnsErrUnavailable(t *testing.T) {
	orig := DefaultTimeout
	DefaultTimeout = 30 * time.Millisecond
	t.Cleanup(func() { DefaultTimeout = orig })

	// `exec sleep 2` replaces the script's shell process image with sleep
	// instead of forking it as a child — a forked child would inherit the
	// stdout pipe and keep it open (and .Output() blocked) even after
	// context cancellation kills the parent shell, which is a shell-
	// scripting artifact this test must not depend on, not a real risk
	// for the actual symmemory binary (a single self-contained process).
	withFakeBinary(t, `exec sleep 2`)

	start := time.Now()
	_, err := Discover(context.Background())
	elapsed := time.Since(start)

	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("Discover() error = %v, want ErrUnavailable", err)
	}
	if elapsed > time.Second {
		t.Errorf("Discover() took %v, want it to respect the ~30ms timeout, not the subprocess's 2s sleep", elapsed)
	}
}

func TestContext_MissingBinary_ReturnsErrUnavailable(t *testing.T) {
	withEmptyPath(t)
	_, err := Context(context.Background(), "entity-1")
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("Context() error = %v, want ErrUnavailable", err)
	}
}

func TestContext_EmptyEntityID_ReturnsErrUnavailable(t *testing.T) {
	withFakeBinary(t, `echo '{"id":"x"}'`)
	_, err := Context(context.Background(), "")
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("Context(\"\") error = %v, want ErrUnavailable", err)
	}
}

func TestContext_ValidResponse_Succeeds(t *testing.T) {
	withFakeBinary(t, `echo '{"id":"entity-1","name":"Some Entity"}'`)
	out, err := Context(context.Background(), "entity-1")
	if err != nil {
		t.Fatalf("Context() error = %v", err)
	}
	if out == "" {
		t.Error("Context() returned empty output for a valid response")
	}
}

func TestContext_MalformedJSON_ReturnsErrUnavailable(t *testing.T) {
	withFakeBinary(t, `echo 'not json'`)
	_, err := Context(context.Background(), "entity-1")
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("Context() error = %v, want ErrUnavailable", err)
	}
}

// TestContext_TrailingNonJSONText_IsIgnored guards the shape confirmed
// against a real symmemory v0.12.0 installation: `entity show --output
// json` prints one JSON object followed by a non-JSON "Linked memories"
// section on the same stdout stream. Context must extract the leading
// JSON value instead of rejecting the whole response.
func TestContext_TrailingNonJSONText_IsIgnored(t *testing.T) {
	withFakeBinary(t, `cat <<'EOF'
{
  "id": "entity-1",
  "name": "Example Entity"
}

Linked memories (2):
  - abc123: some memory summary text that is not JSON
EOF`)
	out, err := Context(context.Background(), "entity-1")
	if err != nil {
		t.Fatalf("Context() error = %v, want nil (trailing text should be ignored)", err)
	}
	if !strings.Contains(out, "Example Entity") {
		t.Errorf("Context() = %q, want it to contain the decoded entity JSON", out)
	}
	if strings.Contains(out, "Linked memories") {
		t.Errorf("Context() = %q, want the non-JSON trailing text excluded", out)
	}
}

// TestContext_AgainstRealBinary is a light integration check that only
// runs when a real `symmemory` is on PATH (never true in CI, since the
// standalone-first rule in ARCHITECTURE.md means no other Symaira tool is
// present there) — it exists so a developer with symmemory installed
// locally gets an early signal if the real CLI's output shape drifts from
// what this package assumes.
func TestContext_AgainstRealBinary(t *testing.T) {
	if _, err := exec.LookPath("symmemory"); err != nil {
		t.Skip("no real symmemory binary on PATH")
	}
	info, err := Discover(context.Background())
	if err != nil {
		t.Skipf("symmemory present but Discover() failed: %v", err)
	}
	t.Logf("discovered real symmemory %s (schema %d)", info.Version, info.SchemaVersion)
	// Context() itself is not asserted here beyond "does not panic and
	// returns one of the two documented outcomes" — see the KNOWN
	// LIMITATION note on Context: a real installation's `entity show`
	// resolves by name, so a synthetic id is expected to come back
	// ErrUnavailable, which is not a failure of this package.
	if _, err := Context(context.Background(), "a-synthetic-id-unlikely-to-exist"); err != nil && !errors.Is(err, ErrUnavailable) {
		t.Errorf("Context() with an unknown id returned an unexpected error type: %v", err)
	}
}
