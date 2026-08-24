package symmeet

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// withFakeBinary puts a fake `symmeet` executable script on PATH,
// prepended (not replacing) the real PATH so the script's own use of
// external commands still resolves — see the equivalent helper and its
// commit history in internal/integration/symmemory/discover_test.go for
// why replacing PATH outright breaks scripts using external `sleep`.
func withFakeBinary(t *testing.T, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake shell-script binary not supported on windows")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "symmeet")
	content := "#!/bin/sh\n" + script + "\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("failed to write fake symmeet binary: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func withEmptyPath(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

func TestDiscover_MissingBinary_ReturnsErrUnavailable(t *testing.T) {
	withEmptyPath(t)
	_, err := Discover(context.Background())
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("Discover() error = %v, want ErrUnavailable", err)
	}
}

func TestDiscover_ValidResponse_Succeeds(t *testing.T) {
	withFakeBinary(t, `echo '{"tool":"symmeet","version":"v1.0.0","schema_version":1}'`)
	caps, err := Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if caps.Tool != "symmeet" || caps.Version != "v1.0.0" || caps.SchemaVersion != 1 {
		t.Errorf("Discover() = %+v, want tool=symmeet version=v1.0.0 schema_version=1", caps)
	}
}

func TestDiscover_ExtraUndocumentedFields_AreIgnored(t *testing.T) {
	// The real capabilities payload is documented to carry substantially
	// more fields (engine capabilities, supported formats, ...) than this
	// package parses — those must not break decoding.
	withFakeBinary(t, `echo '{"tool":"symmeet","version":"v1.0.0","schema_version":1,"engine":{"whisper":true},"formats":["mp4","wav"]}'`)
	caps, err := Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v, want extra fields to be tolerated", err)
	}
	if caps.Tool != "symmeet" {
		t.Errorf("Discover() = %+v", caps)
	}
}

func TestDiscover_MalformedJSON_ReturnsErrUnavailable(t *testing.T) {
	withFakeBinary(t, `echo 'not json'`)
	_, err := Discover(context.Background())
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("Discover() error = %v, want ErrUnavailable", err)
	}
}

func TestDiscover_WrongTool_ReturnsErrUnavailable(t *testing.T) {
	withFakeBinary(t, `echo '{"tool":"something-else","version":"v1","schema_version":1}'`)
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

	// exec (not fork) so context cancellation kills the actual sleeping
	// process instead of an orphaned grandchild still holding the stdout
	// pipe open — see the symmemory package's equivalent test for why.
	withFakeBinary(t, `exec sleep 2`)

	start := time.Now()
	_, err := Discover(context.Background())
	elapsed := time.Since(start)

	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("Discover() error = %v, want ErrUnavailable", err)
	}
	if elapsed > time.Second {
		t.Errorf("Discover() took %v, want it to respect the ~30ms timeout", elapsed)
	}
}
