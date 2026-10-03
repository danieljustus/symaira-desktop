package authorization

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestTransactionPreservesSentinelAndReleasesOnError(t *testing.T) {
	root := t.TempDir()
	sentinel := errors.New("operation rejected")
	if err := WithRoom(root, func() error { return sentinel }); err != sentinel {
		t.Fatalf("error identity changed: %v", err)
	}
	release, err := TryAcquire(root)
	if err != nil {
		t.Fatalf("failed operation retained its lock: %v", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatalf("release is not idempotent: %v", err)
	}
}

func TestRootAliasSharesKernelLock(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "room")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(parent, "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Skipf("root symlink unavailable: %v", err)
	}
	if err := WithRoom(root, func() error {
		release, err := TryAcquire(alias)
		if err == nil {
			_ = release()
			t.Fatal("root alias bypassed held kernel lock")
		}
		if !errors.Is(err, ErrBusy) {
			t.Fatalf("alias contention: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestLocalAuthorizationStateCannotEscapeRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".symroom")); err != nil {
		t.Skipf("directory symlink unavailable: %v", err)
	}
	called := false
	if err := WithRoom(root, func() error { called = true; return nil }); err == nil || called {
		t.Fatalf("external state accepted: called=%v err=%v", called, err)
	}
	if _, err := os.Stat(filepath.Join(outside, "authorization.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created a lock outside the room: %v", err)
	}
}
