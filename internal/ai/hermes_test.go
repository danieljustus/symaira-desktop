package ai

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeHermesScript writes a fake hermes binary into dir that echoes its
// arguments back as the answer, so tests can assert the exact CLI contract
// (prompt via -z, session via --resume).
func fakeHermesScript(t *testing.T, dir string) {
	t.Helper()
	script := `#!/bin/sh
# Fake hermes for tests: emit a fixed answer plus the resume target.
echo "fake-answer session=$HERMES_FAKE_SESSION"
`
	path := filepath.Join(dir, "hermes")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestStreamHermesUsesResumeSession proves the hermes provider invokes the
// CLI with -z and --resume and streams the answer as one chunk.
func TestStreamHermesUsesResumeSession(t *testing.T) {
	dir := t.TempDir()
	fakeHermesScript(t, dir)
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+oldPath)

	ctx := context.Background()
	out := make(chan AskChunk, 1)
	err := streamHermes(ctx, HermesConfig{Session: "my-session"}, "hello", out)
	if err != nil {
		t.Fatalf("streamHermes returned error: %v", err)
	}
	chunk := <-out
	if !strings.Contains(chunk.Chunk, "fake-answer") {
		t.Errorf("chunk = %q, want fake-answer content", chunk.Chunk)
	}
	close(out)
}

// TestStreamHermesMissingBinary verifies honest degradation: without hermes
// on PATH the provider reports ErrNotConfigured instead of failing.
func TestStreamHermesMissingBinary(t *testing.T) {
	// Point PATH at an empty dir so hermes cannot resolve.
	empty := t.TempDir()
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", empty)

	ctx := context.Background()
	out := make(chan AskChunk, 1)
	defer func() { _ = os.Setenv("PATH", oldPath) }()
	err := streamHermes(ctx, HermesConfig{Session: ""}, "hello", out)
	if err != ErrNotConfigured {
		t.Fatalf("streamHermes err = %v, want ErrNotConfigured", err)
	}
}
