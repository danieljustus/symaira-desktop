package ingest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/danieljustus/symaira-ingest/internal/store"
	"github.com/danieljustus/symaira-ingest/internal/writer"
)

func TestStressIngest(t *testing.T) {
	if os.Getenv("SYM_TEST_STRESS") != "true" {
		t.Skip("Skipping high-volume stress test. Set SYM_TEST_STRESS=true to run.")
	}

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "stress.db")
	vaultPath := filepath.Join(dir, "vault")

	// 1. Generate 1,000 synthetic files. 800 unique, 200 duplicates.
	type docInfo struct {
		path    string
		content string
	}
	var docs []docInfo

	// Create unique files
	for i := 0; i < 800; i++ {
		path := filepath.Join(dir, fmt.Sprintf("doc_%d.txt", i))
		content := fmt.Sprintf("content for unique document %d", i)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		docs = append(docs, docInfo{path: path, content: content})
	}

	// Create duplicate files (mapping to some of the unique contents but with different paths)
	for i := 0; i < 200; i++ {
		path := filepath.Join(dir, fmt.Sprintf("dup_%d.txt", i))
		content := fmt.Sprintf("content for unique document %d", i)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		docs = append(docs, docInfo{path: path, content: content})
	}

	// 2. Open store & pipeline
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	p := &Pipeline{
		Engine:     nil,
		Store:      s,
		Writer:     &writer.NoteWriter{Vault: vaultPath},
		ArchiveDir: filepath.Join(dir, "archive"),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 3. Enqueue the first half of the jobs
	for i := 0; i < 500; i++ {
		kind := "text/plain"
		hash, err := hashFile(docs[i].path)
		if err != nil {
			t.Fatal(err)
		}
		doc, created, err := s.CreateOrGet(ctx, docs[i].path, hash, kind)
		if err != nil {
			t.Fatal(err)
		}
		if !created {
			_, _ = s.EnqueueSkippedJob(ctx, doc.ID, kind, "duplicate")
		} else {
			_, _ = s.EnqueueJob(ctx, doc.ID, kind)
		}
	}

	// Start 4 concurrent workers to process them
	var wg sync.WaitGroup
	workerCtx, cancelWorkers := context.WithCancel(ctx)
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			StartWorker(workerCtx, p)
		}()
	}

	// Let them process for a bit, then simulate a restart by cancelling workers and closing db
	time.Sleep(100 * time.Millisecond)
	cancelWorkers()
	wg.Wait()
	_ = s.Close()

	// 4. Reopen store & pipeline
	s, err = store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ResetRunningJobs(ctx); err != nil {
		t.Fatal(err)
	}
	p.Store = s

	// Enqueue all remaining jobs (the rest of the 1,000)
	for i := 500; i < 1000; i++ {
		kind := "text/plain"
		hash, err := hashFile(docs[i].path)
		if err != nil {
			t.Fatal(err)
		}
		doc, created, err := s.CreateOrGet(ctx, docs[i].path, hash, kind)
		if err != nil {
			t.Fatal(err)
		}
		if !created {
			_, _ = s.EnqueueSkippedJob(ctx, doc.ID, kind, "duplicate")
		} else {
			_, _ = s.EnqueueJob(ctx, doc.ID, kind)
		}
	}

	// Restart workers
	workerCtx2, cancelWorkers2 := context.WithCancel(ctx)
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			StartWorker(workerCtx2, p)
		}()
	}

	// Wait until all jobs in the database queue are no longer pending/running
	for {
		jobs, err := s.ListJobs(ctx, 0)
		if err != nil {
			t.Fatal(err)
		}

		allFinished := true
		for _, j := range jobs {
			if j.Status == "pending" || j.Status == "running" {
				allFinished = false
				break
			}
		}
		if allFinished {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Stop workers
	cancelWorkers2()
	wg.Wait()

	// 5. Verify results
	jobs, err := s.ListJobs(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1000 {
		t.Fatalf("expected 1000 jobs, got %d", len(jobs))
	}

	completed := 0
	skipped := 0
	failed := 0
	for _, j := range jobs {
		switch j.Status {
		case "completed":
			completed++
		case "skipped":
			skipped++
		case "failed":
			failed++
		}
	}

	t.Logf("Completed jobs: %d, Skipped (duplicates): %d, Failed: %d", completed, skipped, failed)

	if failed > 0 {
		t.Fatalf("expected 0 failed jobs, got %d", failed)
	}

	if completed != 800 {
		t.Fatalf("expected 800 completed jobs, got %d", completed)
	}
	if skipped != 200 {
		t.Fatalf("expected 200 skipped/duplicate jobs, got %d", skipped)
	}

	files, err := filepath.Glob(filepath.Join(vaultPath, "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 800 {
		t.Fatalf("expected 800 markdown notes in vault, got %d", len(files))
	}

	_ = s.Close()
}
