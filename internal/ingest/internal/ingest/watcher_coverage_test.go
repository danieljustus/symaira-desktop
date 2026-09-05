package ingest

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/ingest/internal/store"
)

func newTestWatcher(t *testing.T) (*Watcher, *store.Store, string, *fakeClock) {
	t.Helper()
	dir := t.TempDir()
	inbox := filepath.Join(dir, "inbox")
	if err := os.MkdirAll(inbox, 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(filepath.Join(dir, "docs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeTestResource(t, "store", s) })

	clk := newFakeClock()
	w, err := NewWatcherWithOptions(s, inbox, WatcherOptions{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeTestResource(t, "watcher", w) })

	return w, s, inbox, clk
}

// A file written and then removed before its debounce window elapses
// must not be enqueued: the Remove event cancels the pending debounce
// (cancelDebounce), and a stray timer fire would hit the deleted-file
// cleanup branch in debounceFile.
func TestWatcher_RemovedBeforeStable_DoesNotEnqueue(t *testing.T) {
	w, s, inbox, clk := newTestWatcher(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := w.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Let the event loop goroutine start and fsnotify settle.
	time.Sleep(50 * time.Millisecond)

	path := filepath.Join(inbox, "vanishing.txt")
	if err := os.WriteFile(path, []byte("here for a moment"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Wait for the create event to reach the event loop and schedule a timer.
	time.Sleep(50 * time.Millisecond)

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	// Wait for the remove event to cancel the debounce.
	time.Sleep(50 * time.Millisecond)

	// Advance past the debounce window — the timer was already stopped by
	// cancelDebounce, so nothing should be enqueued.
	clk.Advance(2 * time.Second)

	w.mu.Lock()
	_, stillPending := w.pending[path]
	w.mu.Unlock()
	if stillPending {
		t.Fatalf("expected %s to be removed from pending after deletion", path)
	}

	jobs, err := s.ListJobs(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Fatalf("expected no jobs for a removed file, got %d: %+v", len(jobs), jobs)
	}
}

// cancelDebounce must stop the pending timer and remove the entry so a
// later checkStability fire (if any were in flight) is a no-op.
func TestCancelDebounce_StopsTimerAndClearsPending(t *testing.T) {
	w, _, inbox, clk := newTestWatcher(t)

	path := filepath.Join(inbox, "doc.txt")
	if err := os.WriteFile(path, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	w.debounceFile(ctx, path)

	w.mu.Lock()
	if _, ok := w.pending[path]; !ok {
		w.mu.Unlock()
		t.Fatal("expected file to be pending after debounceFile")
	}
	w.mu.Unlock()

	w.cancelDebounce(path)

	w.mu.Lock()
	_, ok := w.pending[path]
	w.mu.Unlock()
	if ok {
		t.Fatal("expected pending entry to be cleared after cancelDebounce")
	}

	// Advance well past the debounce window. The timer was already stopped,
	// so the callback should be a no-op (file not in pending map).
	clk.Advance(2 * time.Second)

	jobs, err := w.store.ListJobs(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Fatalf("expected no jobs after cancelDebounce, got %d: %+v", len(jobs), jobs)
	}
}

// A new subdirectory created under the watched root must be picked up by
// watchDirectoryRecursive (triggered from the Start event loop's
// new-directory-detected branch), and files later written inside it must
// be debounced and enqueued like any other watched file.
func TestWatcher_NewSubdirectoryIsWatchedRecursively(t *testing.T) {
	w, s, inbox, clk := newTestWatcher(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := w.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, time.Second, func() bool {
		for _, watched := range w.watcher.WatchList() {
			if watched == inbox {
				return true
			}
		}
		return false
	})

	subdir := filepath.Join(inbox, "incoming")
	if err := os.MkdirAll(subdir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Synchronize on the fsnotify registration instead of assuming the event
	// loop observes the directory within an arbitrary wall-clock sleep.
	waitFor(t, time.Second, func() bool {
		for _, watched := range w.watcher.WatchList() {
			if watched == subdir {
				return true
			}
		}
		return false
	})

	nestedPath := filepath.Join(subdir, "nested.txt")
	if err := os.WriteFile(nestedPath, []byte("nested content"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The fake clock must not advance until the create event has installed the
	// debounce timer; otherwise parallel package load can race this assertion.
	waitFor(t, time.Second, func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		_, pending := w.pending[nestedPath]
		return pending
	})

	var jobs []*store.Job
	for attempt := 0; attempt < 3; attempt++ {
		// A create event may be followed by a write event. Depending on which
		// event established the pending state, checkStability may need one more
		// deterministic interval to observe an unchanged file.
		clk.Advance(w.stableFor)
		var err error
		jobs, err = s.ListJobs(ctx, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, job := range jobs {
			if job.SourcePath == nestedPath {
				return
			}
		}
	}
	t.Fatalf("expected a job for %s, got jobs: %+v", nestedPath, jobs)
}

// checkStability force-enqueues a file that has been pending longer than
// maxPendingAge, even if its size/modtime are still changing. Drive this
// directly rather than waiting 5 real minutes.
func TestCheckStability_ForceEnqueuesAfterMaxPendingAge(t *testing.T) {
	w, s, inbox, _ := newTestWatcher(t)
	ctx := context.Background()

	path := filepath.Join(inbox, "stuck.txt")
	if err := os.WriteFile(path, []byte("still being written"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	w.mu.Lock()
	w.pending[path] = &fileState{
		lastSize:    info.Size() - 1, // pretend the size is still changing
		lastModTime: info.ModTime(),
		createdAt:   w.clock.Now().Add(-6 * time.Minute),
	}
	w.mu.Unlock()

	w.checkStability(ctx, path)

	w.mu.Lock()
	_, stillPending := w.pending[path]
	w.mu.Unlock()
	if stillPending {
		t.Fatal("expected force-enqueued file to be removed from pending")
	}

	jobs, err := s.ListJobs(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, j := range jobs {
		if j.SourcePath == path {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a force-enqueued job for %s, got jobs: %+v", path, jobs)
	}
}

// checkStability must clean up and skip enqueuing when the file disappears
// from disk between the debounce timer firing and the stat check, even if
// the pending entry was not removed via cancelDebounce first.
func TestCheckStability_FileGoneDuringWait(t *testing.T) {
	w, s, inbox, _ := newTestWatcher(t)
	ctx := context.Background()

	path := filepath.Join(inbox, "gone.txt")
	if err := os.WriteFile(path, []byte("temporary"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	w.mu.Lock()
	w.pending[path] = &fileState{
		lastSize:    info.Size(),
		lastModTime: info.ModTime(),
		createdAt:   time.Now(),
	}
	w.mu.Unlock()

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	w.checkStability(ctx, path)

	w.mu.Lock()
	_, stillPending := w.pending[path]
	w.mu.Unlock()
	if stillPending {
		t.Fatal("expected pending entry to be cleared once the file is gone")
	}

	jobs, err := s.ListJobs(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Fatalf("expected no jobs for a file removed before stability check, got %d: %+v", len(jobs), jobs)
	}
}

// Closing the watcher (context cancellation) while a debounce timer is
// pending must not panic or leak: the event loop's deferred cleanup stops
// every pending timer and clears the map.
func TestWatcher_CloseWhileDebouncePending(t *testing.T) {
	w, _, inbox, _ := newTestWatcher(t)

	ctx, cancel := context.WithCancel(context.Background())
	if err := w.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	path := filepath.Join(inbox, "pending-on-close.txt")
	if err := os.WriteFile(path, []byte("not yet stable"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Let the create event register and start the debounce timer, but
	// cancel well before the debounce window elapses.
	time.Sleep(100 * time.Millisecond)

	cancel()
	// Give the event loop's goroutine time to run its deferred cleanup.
	time.Sleep(100 * time.Millisecond)

	w.mu.Lock()
	pendingCount := len(w.pending)
	w.mu.Unlock()
	if pendingCount != 0 {
		t.Fatalf("expected pending map to be cleared on shutdown, got %d entries", pendingCount)
	}
}
