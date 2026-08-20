package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danieljustus/symaira-ingest/internal/store"
	"github.com/danieljustus/symaira-ingest/internal/writer"
)

func TestWatcher_DebouncesAndEnqueues(t *testing.T) {
	dir := t.TempDir()
	inbox := filepath.Join(dir, "inbox")
	if err := os.MkdirAll(inbox, 0o755); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(dir, "docs.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	clk := newFakeClock()
	w, err := NewWatcherWithOptions(s, inbox, WatcherOptions{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := w.Start(ctx); err != nil {
		t.Fatalf("Start watcher: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	ignoredPath := filepath.Join(inbox, "test.tmp")
	if err := os.WriteFile(ignoredPath, []byte("ignored"), 0o644); err != nil {
		t.Fatal(err)
	}

	realPath := filepath.Join(inbox, "doc.txt")
	if err := os.WriteFile(realPath, []byte("init"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Write multiple times in quick succession to exercise debounce reset.
	// The event loop calls debounceFile for each write, which stops the
	// previous timer and schedules a new one.  No Advance between writes
	// ensures the timer never fires prematurely.
	time.Sleep(50 * time.Millisecond)
	if err := os.WriteFile(realPath, []byte("init updated"), 0o644); err != nil {
		t.Fatal(err)
	}

	time.Sleep(50 * time.Millisecond)
	if err := os.WriteFile(realPath, []byte("final content"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Advance past the debounce window — file is stable, one job enqueued.
	time.Sleep(50 * time.Millisecond)
	clk.Advance(2 * time.Second)

	jobs, err := s.ListJobs(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}

	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d. Jobs: %+v", len(jobs), jobs)
	}

	if jobs[0].SourcePath != realPath {
		t.Fatalf("expected job for %s, got %s", realPath, jobs[0].SourcePath)
	}
}

func TestWatcher_MovesStableFileToProcessing(t *testing.T) {
	dir := t.TempDir()
	inbox := filepath.Join(dir, "inbox")
	processing := filepath.Join(dir, "processing")
	if err := os.MkdirAll(inbox, 0o755); err != nil {
		t.Fatal(err)
	}

	s, err := store.Open(filepath.Join(dir, "docs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	w, err := NewWatcherWithOptions(s, inbox, WatcherOptions{StableFor: 50 * time.Millisecond, ProcessingDir: processing})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := w.Start(ctx); err != nil {
		t.Fatalf("Start watcher: %v", err)
	}

	source := filepath.Join(inbox, "doc.txt")
	if err := os.WriteFile(source, []byte("ready"), 0o644); err != nil {
		t.Fatal(err)
	}

	processedPath := filepath.Join(processing, "doc.txt")
	var jobs []*store.Job
	waitFor(t, time.Second, func() bool {
		var err error
		jobs, err = s.ListJobs(ctx, 0)
		return err == nil && len(jobs) == 1
	})
	if _, err := os.Stat(source); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source should have moved out of inbox, stat err=%v", err)
	}

	if len(jobs) != 1 || jobs[0].SourcePath != processedPath {
		t.Fatalf("expected queued processing path %s, got %+v", processedPath, jobs)
	}
}

func TestWatcher_MovesUnsupportedFileToFailedAndContinues(t *testing.T) {
	dir := t.TempDir()
	inbox := filepath.Join(dir, "inbox")
	failed := filepath.Join(dir, "failed")
	if err := os.MkdirAll(inbox, 0o755); err != nil {
		t.Fatal(err)
	}

	s, err := store.Open(filepath.Join(dir, "docs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	w, err := NewWatcherWithOptions(s, inbox, WatcherOptions{StableFor: 50 * time.Millisecond, FailedDir: failed})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := w.Start(ctx); err != nil {
		t.Fatalf("Start watcher: %v", err)
	}

	bad := filepath.Join(inbox, "broken.bin")
	if err := os.WriteFile(bad, []byte{0x01, 0x02, 0x03}, 0o644); err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(inbox, "good.txt")
	if err := os.WriteFile(good, []byte("keep going"), 0o644); err != nil {
		t.Fatal(err)
	}

	failedFile := filepath.Join(failed, "broken.bin")
	waitFor(t, time.Second, func() bool {
		_, err := os.Stat(failedFile + ".error.json")
		return err == nil
	})
	var sidecar failureSidecar
	data, err := os.ReadFile(failedFile + ".error.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &sidecar); err != nil {
		t.Fatalf("sidecar JSON: %v", err)
	}
	if sidecar.Stage != "detect" || sidecar.Error == "" || sidecar.SourcePath != failedFile {
		t.Fatalf("unexpected sidecar: %+v", sidecar)
	}

	waitFor(t, time.Second, func() bool {
		jobs, err := s.ListJobs(ctx, 0)
		return err == nil && len(jobs) == 1 && jobs[0].SourcePath == good
	})
}

func TestWorker_MovesCompletedAndFailedSources(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "docs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	processed := filepath.Join(dir, "processed")
	failed := filepath.Join(dir, "failed")
	good := filepath.Join(dir, "good.txt")
	bad := filepath.Join(dir, "bad.png")
	if err := os.WriteFile(good, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	for _, path := range []string{good, bad} {
		kind := "text/plain"
		if filepath.Ext(path) == ".png" {
			kind = "image/png"
		}
		hash, err := hashFile(path)
		if err != nil {
			t.Fatal(err)
		}
		doc, _, err := s.CreateOrGet(ctx, path, hash, kind)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.EnqueueJob(ctx, doc.ID, kind); err != nil {
			t.Fatal(err)
		}
	}

	workerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &Pipeline{
		Engine:       &fakePipelineEngine{err: errors.New("ocr failed")},
		Store:        s,
		Writer:       &writer.NoteWriter{Vault: filepath.Join(dir, "vault")},
		ArchiveDir:   filepath.Join(dir, "archive"),
		ProcessedDir: processed,
		FailedDir:    failed,
	}
	go StartWorker(workerCtx, p)

	waitFor(t, 2*time.Second, func() bool {
		_, goodErr := os.Stat(filepath.Join(processed, "good.txt"))
		_, badErr := os.Stat(filepath.Join(failed, "bad.png.error.json"))
		return goodErr == nil && badErr == nil
	})

	jobs, err := s.ListJobs(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	var completed, failedCount int
	for _, job := range jobs {
		switch job.Status {
		case "completed":
			completed++
		case "failed":
			failedCount++
		}
	}
	if completed != 1 || failedCount != 1 {
		t.Fatalf("expected one completed and one failed job, got %+v", jobs)
	}
}

func waitFor(t *testing.T, timeout time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	if ok() {
		return
	}
	t.Fatalf("condition not met within %s", timeout)
}
