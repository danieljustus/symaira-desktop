package ingest

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danieljustus/symaira-ingest/internal/store"
)

func TestNewWatcher_DelegatesToNewWatcherWithOptions(t *testing.T) {
	dir := t.TempDir()
	inbox := filepath.Join(dir, "inbox")
	if err := os.MkdirAll(inbox, 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(filepath.Join(dir, "db.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	w, err := NewWatcher(s, inbox)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	if w == nil {
		t.Fatal("expected non-nil watcher")
	}
	w.Close()
}

func TestFakeClock_StopTimer(t *testing.T) {
	c := newFakeClock()
	fired := false
	timer := c.AfterFunc(time.Second, func() { fired = true })
	if c.StopTimer(timer) {
		c.Advance(2 * time.Second)
		if fired {
			t.Error("timer should have been stopped")
		}
	} else {
		t.Error("StopTimer should return true for pending timer")
	}
}

func TestFakeClock_StopTimer_NotFound(t *testing.T) {
	c := newFakeClock()
	fake := timer{id: 999}
	if c.StopTimer(fake) {
		t.Error("StopTimer should return false for unknown timer")
	}
}

func TestFakeClock_FireAll(t *testing.T) {
	c := newFakeClock()
	count := 0
	c.AfterFunc(time.Hour, func() { count++ })
	c.AfterFunc(2*time.Hour, func() { count++ })
	c.fireAll()
	if count != 2 {
		t.Errorf("fireAll should fire all timers, got %d", count)
	}
}

func TestRealClock_StopTimer(t *testing.T) {
	c := realClock{}
	if !c.StopTimer(timer{id: -1}) {
		t.Error("realClock.StopTimer should always return true")
	}
}

func TestRealClock_AfterFunc(t *testing.T) {
	c := realClock{}
	done := make(chan bool, 1)
	c.AfterFunc(time.Millisecond, func() { done <- true })
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Error("realClock.AfterFunc did not fire within timeout")
	}
}

func TestMoveProcessedSource_EmptyDir(t *testing.T) {
	moveProcessedSource("/tmp/file.txt", "")
	moveProcessedSource("/tmp/file.txt", ".")
}

func TestMoveFailedSource_EmptyDir(t *testing.T) {
	moveFailedSource("/tmp/file.txt", "", "stage", nil)
	moveFailedSource("/tmp/file.txt", ".", "stage", nil)
}
