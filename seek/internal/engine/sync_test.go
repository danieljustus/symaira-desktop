package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-seek/internal/db"
)

type countingStore struct {
	db.Store
	getDocCalls int
}

func (c *countingStore) GetDocument(path string) (*db.Document, error) {
	c.getDocCalls++
	return c.Store.GetDocument(path)
}

// Regression test for issue #159: one GetDocument per IndexFile call.
func TestIndexFileSingleGetDocumentCall(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-159-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	t.Setenv("HOME", tempDir)

	dbClient, err := db.Open()
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer dbClient.Close()

	cs := &countingStore{Store: dbClient}
	embedder := &fakeEmbedder{dim: 768}

	file := filepath.Join(tempDir, "doc.md")
	if err := os.WriteFile(file, []byte("original content"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// First index: new file — expect 1 GetDocument (returns nil)
	cs.getDocCalls = 0
	if _, err := IndexFile(cs, embedder, file); err != nil {
		t.Fatalf("first IndexFile: %v", err)
	}
	if cs.getDocCalls != 1 {
		t.Errorf("first index: expected 1 GetDocument call, got %d", cs.getDocCalls)
	}

	// Second index: unchanged file — expect 1 GetDocument (skip)
	cs.getDocCalls = 0
	if _, err := IndexFile(cs, embedder, file); err != nil {
		t.Fatalf("second IndexFile: %v", err)
	}
	if cs.getDocCalls != 1 {
		t.Errorf("unchanged index: expected 1 GetDocument call, got %d", cs.getDocCalls)
	}

	// Third index: changed file — expect 1 GetDocument (prepareIndex), 0 in commitIndex
	cs.getDocCalls = 0
	if err := os.WriteFile(file, []byte("updated content"), 0644); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := IndexFile(cs, embedder, file); err != nil {
		t.Fatalf("third IndexFile: %v", err)
	}
	if cs.getDocCalls != 1 {
		t.Errorf("changed index: expected 1 GetDocument call, got %d", cs.getDocCalls)
	}
}

// Regression test for issue #159: one GetDocument per indexContent call.
func TestIndexContentSingleGetDocumentCall(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-159-content-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	t.Setenv("HOME", tempDir)

	dbClient, err := db.Open()
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer dbClient.Close()

	cs := &countingStore{Store: dbClient}
	embedder := &fakeEmbedder{dim: 768}

	// First index: new content — expect 1 GetDocument
	cs.getDocCalls = 0
	if err := indexContent(cs, embedder, "test://issue159", "first version"); err != nil {
		t.Fatalf("first indexContent: %v", err)
	}
	if cs.getDocCalls != 1 {
		t.Errorf("first content index: expected 1 GetDocument call, got %d", cs.getDocCalls)
	}

	// Second index: unchanged content — expect 1 GetDocument (skip)
	cs.getDocCalls = 0
	if err := indexContent(cs, embedder, "test://issue159", "first version"); err != nil {
		t.Fatalf("second indexContent: %v", err)
	}
	if cs.getDocCalls != 1 {
		t.Errorf("unchanged content index: expected 1 GetDocument call, got %d", cs.getDocCalls)
	}

	// Third index: changed content — expect 1 GetDocument, 0 in commitIndex
	cs.getDocCalls = 0
	if err := indexContent(cs, embedder, "test://issue159", "second version"); err != nil {
		t.Fatalf("third indexContent: %v", err)
	}
	if cs.getDocCalls != 1 {
		t.Errorf("changed content index: expected 1 GetDocument call, got %d", cs.getDocCalls)
	}
}

// TestParallelIndexSkipsUnchangedFiles is a regression test for issue #70.
// The parallel indexing path must check the file hash before generating
// embeddings, so unchanged files are not re-embedded on every index run.
func TestParallelIndexSkipsUnchangedFiles(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-parallel-hash-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	t.Setenv("HOME", tempDir)

	dbClient, err := db.Open()
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer dbClient.Close()

	embedder := &countingEmbedder{dim: 768}

	docsDir := filepath.Join(tempDir, "docs")
	if err := os.MkdirAll(docsDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	file1 := filepath.Join(docsDir, "a.md")
	file2 := filepath.Join(docsDir, "b.md")
	if err := os.WriteFile(file1, []byte("content one"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(file2, []byte("content two"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	paths := map[string]bool{file1: true, file2: true}
	processFilesInParallel(dbClient, embedder, paths)

	if embedder.calls != 2 {
		t.Fatalf("first run: expected 2 embedding calls, got %d", embedder.calls)
	}

	embedder.calls = 0
	processFilesInParallel(dbClient, embedder, paths)

	if embedder.calls != 0 {
		t.Errorf("second run: expected 0 embedding calls for unchanged files, got %d", embedder.calls)
	}
}

// countingEmbedder wraps fakeEmbedder and counts GenerateVectors calls.
type countingEmbedder struct {
	dim   int
	calls int
}

func (c *countingEmbedder) GenerateVector(text string) []float32 {
	c.calls++
	return (&fakeEmbedder{dim: c.dim}).GenerateVector(text)
}

func (c *countingEmbedder) GenerateVectors(texts []string) [][]float32 {
	c.calls++
	return (&fakeEmbedder{dim: c.dim}).GenerateVectors(texts)
}

func (c *countingEmbedder) GenerateVectorsWithModel(texts []string) []EmbeddingResult {
	c.calls++
	return (&fakeEmbedder{dim: c.dim}).GenerateVectorsWithModel(texts)
}

func (c *countingEmbedder) GenerateVectorNoRetry(text string) []float32 {
	c.calls++
	return (&fakeEmbedder{dim: c.dim}).GenerateVectorNoRetry(text)
}

func (c *countingEmbedder) GenerateVectorNoRetryWithModel(text string) EmbeddingResult {
	c.calls++
	return (&fakeEmbedder{dim: c.dim}).GenerateVectorNoRetryWithModel(text)
}

func (c *countingEmbedder) Dim() int {
	return c.dim
}

func (c *countingEmbedder) ModelName() string {
	return "counting-model"
}

// TestIndexDirectorySiblingPrefix is a regression test for issue #66.
// Re-indexing a directory must never delete documents that live in a
// sibling directory whose name shares a string prefix (e.g. /docs vs /docs2).
func TestIndexDirectorySiblingPrefix(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-sibling-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	t.Setenv("HOME", tempDir)

	dbClient, err := db.Open()
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer dbClient.Close()

	embedder := &fakeEmbedder{dim: 768}

	docsDir := filepath.Join(tempDir, "docs")
	docs2Dir := filepath.Join(tempDir, "docs2")
	if err := os.MkdirAll(docsDir, 0755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	if err := os.MkdirAll(docs2Dir, 0755); err != nil {
		t.Fatalf("mkdir docs2: %v", err)
	}

	file1 := filepath.Join(docsDir, "a.md")
	file2 := filepath.Join(docs2Dir, "b.md")
	if err := os.WriteFile(file1, []byte("content in docs"), 0644); err != nil {
		t.Fatalf("write a.md: %v", err)
	}
	if err := os.WriteFile(file2, []byte("content in docs2"), 0644); err != nil {
		t.Fatalf("write b.md: %v", err)
	}

	if err := IndexDirectory(dbClient, embedder, docsDir); err != nil {
		t.Fatalf("index docs: %v", err)
	}
	if err := IndexDirectory(dbClient, embedder, docs2Dir); err != nil {
		t.Fatalf("index docs2: %v", err)
	}

	doc2, err := dbClient.GetDocument(file2)
	if err != nil || doc2 == nil {
		t.Fatalf("docs2/b.md not indexed after initial pass")
	}

	if err := IndexDirectory(dbClient, embedder, docsDir); err != nil {
		t.Fatalf("re-index docs: %v", err)
	}

	doc2After, err := dbClient.GetDocument(file2)
	if err != nil {
		t.Fatalf("GetDocument after re-index: %v", err)
	}
	if doc2After == nil {
		t.Error("docs2/b.md was deleted from index after re-indexing sibling docs/ — orphan cleanup used plain HasPrefix")
	}
}

// TestApplyIncrementalChangesTouchesOnlyChangedFiles is a regression
// test for issue #46.
func TestApplyIncrementalChangesTouchesOnlyChangedFiles(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-inc-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)

	dbClient, err := db.Open()
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer dbClient.Close()

	embedder := newTestEmbeddingsGenerator()

	docsDir := filepath.Join(tempDir, "docs")
	if err := os.MkdirAll(docsDir, 0755); err != nil {
		t.Fatalf("failed to create docs dir: %v", err)
	}
	untouched := filepath.Join(docsDir, "untouched.md")
	if err := os.WriteFile(untouched, []byte("untouched original content"), 0644); err != nil {
		t.Fatalf("failed to write untouched.md: %v", err)
	}
	changed := filepath.Join(docsDir, "changed.md")
	if err := os.WriteFile(changed, []byte("changed original content"), 0644); err != nil {
		t.Fatalf("failed to write changed.md: %v", err)
	}

	if err := IndexDirectory(dbClient, embedder, docsDir); err != nil {
		t.Fatalf("initial IndexDirectory failed: %v", err)
	}

	untouchedDoc, err := dbClient.GetDocument(untouched)
	if err != nil || untouchedDoc == nil {
		t.Fatalf("expected untouched.md to be indexed before edit, err=%v doc=%v", err, untouchedDoc)
	}
	originalUntouchedHash := untouchedDoc.Hash

	if err := os.WriteFile(changed, []byte("changed updated content"), 0644); err != nil {
		t.Fatalf("failed to update changed.md: %v", err)
	}

	changes := map[string]struct{}{changed: {}}
	if err := applyIncrementalChanges(dbClient, embedder, docsDir, changes); err != nil {
		t.Fatalf("applyIncrementalChanges failed: %v", err)
	}

	untouchedAfter, err := dbClient.GetDocument(untouched)
	if err != nil || untouchedAfter == nil {
		t.Fatalf("untouched.md missing after incremental change: err=%v doc=%v", err, untouchedAfter)
	}
	if untouchedAfter.Hash != originalUntouchedHash {
		t.Errorf("incremental change touched the unrelated file: hash %q -> %q",
			originalUntouchedHash, untouchedAfter.Hash)
	}

	changedAfter, err := dbClient.GetDocument(changed)
	if err != nil || changedAfter == nil {
		t.Fatalf("changed.md missing after incremental change: err=%v doc=%v", err, changedAfter)
	}
	if changedAfter.Hash == originalUntouchedHash {
		t.Errorf("expected changed.md to be re-indexed with a new hash")
	}
}

// TestApplyIncrementalChangesDropsMissingFiles is a regression test
// for issue #46.
func TestApplyIncrementalChangesDropsMissingFiles(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-inc-orphan-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)

	dbClient, err := db.Open()
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer dbClient.Close()

	embedder := newTestEmbeddingsGenerator()

	docsDir := filepath.Join(tempDir, "docs")
	if err := os.MkdirAll(docsDir, 0755); err != nil {
		t.Fatalf("failed to create docs dir: %v", err)
	}
	doomed := filepath.Join(docsDir, "doomed.md")
	if err := os.WriteFile(doomed, []byte("doomed content"), 0644); err != nil {
		t.Fatalf("failed to write doomed.md: %v", err)
	}
	if err := IndexDirectory(dbClient, embedder, docsDir); err != nil {
		t.Fatalf("IndexDirectory failed: %v", err)
	}

	if doc, err := dbClient.GetDocument(doomed); err != nil || doc == nil {
		t.Fatalf("expected doomed.md to be indexed: err=%v doc=%v", err, doc)
	}

	if err := os.Remove(doomed); err != nil {
		t.Fatalf("failed to delete doomed.md: %v", err)
	}
	changes := map[string]struct{}{doomed: {}}
	if err := applyIncrementalChanges(dbClient, embedder, docsDir, changes); err != nil {
		t.Fatalf("applyIncrementalChanges failed: %v", err)
	}

	if doc, err := dbClient.GetDocument(doomed); err != nil {
		t.Fatalf("GetDocument after delete: %v", err)
	} else if doc != nil {
		t.Errorf("expected doomed.md to be removed from index, still present: %+v", doc)
	}
}

// TestProcessFilesInParallelMatchesSequential is a regression test
// for issue #50. The bounded worker pool must produce the same
// database state as a sequential sweep: every input path is
// indexed exactly once and no goroutine panics or leaks the
// worker-count budget.
func TestProcessFilesInParallelMatchesSequential(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-par-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)

	dbClient, err := db.Open()
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer dbClient.Close()

	embedder := newTestEmbeddingsGenerator()

	const nFiles = 20
	docsDir := filepath.Join(tempDir, "docs")
	if err := os.MkdirAll(docsDir, 0755); err != nil {
		t.Fatalf("failed to create docs dir: %v", err)
	}
	paths := make(map[string]bool, nFiles)
	for i := 0; i < nFiles; i++ {
		p := filepath.Join(docsDir, fmt.Sprintf("file_%02d.md", i))
		if err := os.WriteFile(p, []byte(fmt.Sprintf("content of file %d", i)), 0644); err != nil {
			t.Fatalf("failed to write %s: %v", p, err)
		}
		paths[p] = true
	}

	processFilesInParallel(dbClient, embedder, paths)

	stats, err := dbClient.GetStats()
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	if stats.DocumentCount != nFiles {
		t.Errorf("expected %d documents indexed, got %d", nFiles, stats.DocumentCount)
	}
	for p := range paths {
		doc, err := dbClient.GetDocument(p)
		if err != nil {
			t.Errorf("GetDocument(%s): %v", p, err)
			continue
		}
		if doc == nil {
			t.Errorf("expected %s to be indexed, found nil", p)
		}
	}
}

// BenchmarkIndexDirectorySequential vs BenchmarkIndexDirectoryParallel
// measures the impact of issue #50's bounded worker pool.
//
// Run with:
//
//	go test -bench=BenchmarkIndexDirectory -benchtime=1x ./internal/engine
func BenchmarkIndexDirectorySequential(b *testing.B) {
	benchmarkIndexDirectory(b, false)
}

func BenchmarkIndexDirectoryParallel(b *testing.B) {
	benchmarkIndexDirectory(b, true)
}

func benchmarkIndexDirectory(b *testing.B, parallel bool) {
	b.Helper()
	tempDir, err := os.MkdirTemp("", "seek-par-bench")
	if err != nil {
		b.Fatalf("tempdir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)

	dbClient, err := db.Open()
	if err != nil {
		b.Fatalf("db.Open: %v", err)
	}
	defer dbClient.Close()

	embedder := newTestEmbeddingsGenerator()

	const nFiles = 20
	docsDir := filepath.Join(tempDir, "docs")
	if err := os.MkdirAll(docsDir, 0755); err != nil {
		b.Fatalf("mkdir: %v", err)
	}
	paths := make(map[string]bool, nFiles)
	for i := 0; i < nFiles; i++ {
		p := filepath.Join(docsDir, fmt.Sprintf("file_%02d.md", i))
		if err := os.WriteFile(p, []byte(fmt.Sprintf("content of file %d", i)), 0644); err != nil {
			b.Fatalf("write: %v", err)
		}
		paths[p] = true
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if parallel {
			processFilesInParallel(dbClient, embedder, paths)
		} else {
			for p := range paths {
				if _, err := IndexFile(dbClient, embedder, p); err != nil {
					b.Fatalf("IndexFile(%s): %v", p, err)
				}
			}
		}
	}
}

func TestWatchDirectory_CreatesAndIndexes(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-watch-create-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	t.Setenv("HOME", tempDir)

	dbClient, err := db.Open()
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer dbClient.Close()

	embedder := &fakeEmbedder{dim: 768}

	docsDir := filepath.Join(tempDir, "docs")
	if err := os.MkdirAll(docsDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- WatchDirectory(ctx, dbClient, embedder, docsDir)
	}()

	time.Sleep(200 * time.Millisecond)

	newFile := filepath.Join(docsDir, "new.md")
	if err := os.WriteFile(newFile, []byte("watched content"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	time.Sleep(1500 * time.Millisecond)

	doc, err := dbClient.GetDocument(newFile)
	if err != nil {
		t.Fatalf("GetDocument: %v", err)
	}
	if doc == nil {
		t.Errorf("expected new.md to be indexed by watcher, got nil")
	}

	cancel()
	<-errCh
}

func TestWatchDirectory_ModifiesFile(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-watch-modify-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	t.Setenv("HOME", tempDir)

	dbClient, err := db.Open()
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer dbClient.Close()

	embedder := &fakeEmbedder{dim: 768}

	docsDir := filepath.Join(tempDir, "docs")
	if err := os.MkdirAll(docsDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	existingFile := filepath.Join(docsDir, "existing.md")
	if err := os.WriteFile(existingFile, []byte("original content"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- WatchDirectory(ctx, dbClient, embedder, docsDir)
	}()

	// Wait for the initial sync to finish. Poll instead of sleeping a fixed
	// 200ms: on a loaded CI runner the initial index can take longer, which
	// makes the fixed-sleep variant flaky.
	var doc *db.Document
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		doc, err = dbClient.GetDocument(existingFile)
		if err == nil && doc != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil || doc == nil {
		t.Fatalf("initial index: doc=%v err=%v", doc, err)
	}
	originalHash := doc.Hash

	if err := os.WriteFile(existingFile, []byte("updated content for watcher"), 0644); err != nil {
		t.Fatalf("update: %v", err)
	}

	time.Sleep(1500 * time.Millisecond)

	docAfter, err := dbClient.GetDocument(existingFile)
	if err != nil {
		t.Fatalf("GetDocument after modify: %v", err)
	}
	if docAfter == nil {
		t.Fatal("expected existing.md to still be in DB after modify")
	}
	if docAfter.Hash == originalHash {
		t.Errorf("expected hash to change after file modification, still %q", originalHash)
	}

	cancel()
	<-errCh
}

func TestWatchDirectory_RemovesFile(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-watch-remove-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	t.Setenv("HOME", tempDir)

	dbClient, err := db.Open()
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer dbClient.Close()

	embedder := &fakeEmbedder{dim: 768}

	docsDir := filepath.Join(tempDir, "docs")
	if err := os.MkdirAll(docsDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	doomedFile := filepath.Join(docsDir, "doomed.md")
	if err := os.WriteFile(doomedFile, []byte("about to be deleted"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- WatchDirectory(ctx, dbClient, embedder, docsDir)
	}()

	time.Sleep(200 * time.Millisecond)

	doc, err := dbClient.GetDocument(doomedFile)
	if err != nil || doc == nil {
		t.Fatalf("initial index: doc=%v err=%v", doc, err)
	}

	if err := os.Remove(doomedFile); err != nil {
		t.Fatalf("remove: %v", err)
	}

	time.Sleep(1500 * time.Millisecond)

	docAfter, err := dbClient.GetDocument(doomedFile)
	if err != nil {
		t.Fatalf("GetDocument after remove: %v", err)
	}
	if docAfter != nil {
		t.Errorf("expected doomed.md to be removed from index, still present")
	}

	cancel()
	<-errCh
}

func TestIndexDirectory(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-sync-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	t.Setenv("HOME", tempDir)

	dbClient, err := db.Open()
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer dbClient.Close()

	embedder := newTestEmbeddingsGenerator()

	// 1. Create a dummy folder with markdown files
	docsDir := filepath.Join(tempDir, "docs")
	err = os.MkdirAll(docsDir, 0755)
	if err != nil {
		t.Fatalf("failed to create docs dir: %v", err)
	}

	file1 := filepath.Join(docsDir, "first.md")
	err = os.WriteFile(file1, []byte("# First Document\nThis is the content of the first file."), 0644)
	if err != nil {
		t.Fatalf("failed to write first.md: %v", err)
	}

	file2 := filepath.Join(docsDir, "second.txt")
	err = os.WriteFile(file2, []byte("Second document contains simple plain text data."), 0644)
	if err != nil {
		t.Fatalf("failed to write second.txt: %v", err)
	}

	// 2. Perform index
	err = IndexDirectory(dbClient, embedder, docsDir)
	if err != nil {
		t.Fatalf("IndexDirectory failed: %v", err)
	}

	stats, err := dbClient.GetStats()
	if err != nil {
		t.Fatalf("failed to get stats: %v", err)
	}
	if stats.DocumentCount != 2 {
		t.Errorf("expected 2 documents in index, got %d", stats.DocumentCount)
	}

	// 3. Test update (incremental change)
	time.Sleep(10 * time.Millisecond) // Ensure file modification times would update if checked
	err = os.WriteFile(file1, []byte("# First Document\nThis is updated content of the first file with extra information."), 0644)
	if err != nil {
		t.Fatalf("failed to update first.md: %v", err)
	}

	err = IndexDirectory(dbClient, embedder, docsDir)
	if err != nil {
		t.Fatalf("re-index failed: %v", err)
	}

	stats2, _ := dbClient.GetStats()
	if stats2.DocumentCount != 2 {
		t.Errorf("expected 2 documents in index after update, got %d", stats2.DocumentCount)
	}

	// Verify update content by searching
	res, err := SearchHybrid(dbClient, dbClient, embedder, "extra information", 10)
	if err != nil {
		t.Fatalf("hybrid search failed: %v", err)
	}
	if len(res) == 0 || res[0].Chunk.DocumentPath != file1 {
		t.Errorf("expected updated content to be searchable, got: %v", res)
	}

	// 4. Test orphan detection (deleting file on disk)
	err = os.Remove(file2)
	if err != nil {
		t.Fatalf("failed to delete file2: %v", err)
	}

	err = IndexDirectory(dbClient, embedder, docsDir)
	if err != nil {
		t.Fatalf("index after deletion failed: %v", err)
	}

	stats3, _ := dbClient.GetStats()
	if stats3.DocumentCount != 1 {
		t.Errorf("expected 1 document after orphan cleanup, got %d", stats3.DocumentCount)
	}
}

// TestDeriveChunkIDDeterministic is a regression test for issue #252.
// Chunk IDs must be deterministic functions of document path, content hash,
// and character start offset so that unchanged chunks keep their IDs across
// reindex runs.
func TestDeriveChunkIDDeterministic(t *testing.T) {
	id1 := deriveChunkID("/docs/a.md", "hash1", 42)
	id2 := deriveChunkID("/docs/a.md", "hash1", 42)
	if id1 != id2 {
		t.Errorf("expected deterministic IDs for identical inputs, got %q and %q", id1, id2)
	}

	if deriveChunkID("/docs/a.md", "hash1", 42) == deriveChunkID("/docs/a.md", "hash2", 42) {
		t.Error("different content hashes must produce different IDs")
	}
	if deriveChunkID("/docs/a.md", "hash1", 42) == deriveChunkID("/docs/a.md", "hash1", 43) {
		t.Error("different char starts must produce different IDs")
	}
	if deriveChunkID("/docs/a.md", "hash1", 42) == deriveChunkID("/docs/b.md", "hash1", 42) {
		t.Error("different document paths must produce different IDs")
	}

	// Sanity: output must be a valid UUID string.
	if len(id1) != 36 {
		t.Errorf("expected UUID string length 36, got %d (%q)", len(id1), id1)
	}
}

// TestBuildChunksStableIDs is a regression test for issue #252. Reindexing an
// unchanged document must produce the same chunk IDs. Editing a chunk must change
// that chunk's ID while leaving textually and positionally unchanged chunks
// with their original IDs.
func TestBuildChunksStableIDs(t *testing.T) {
	embedder := &fakeEmbedder{dim: 768}
	path := "/docs/stable.md"

	// Build content that splits deterministically into two chunks: the first
	 // chunk is 1000 'a's, the second chunk is the trailing 200 'a's plus the
	 // final 200 'b's (with 200-char overlap). Changing only the 'b' suffix
	 // affects only the second chunk.
	content := strings.Repeat("a", 1000) + strings.Repeat("b", 200)

	chunks1 := buildChunks(embedder, path, content)
	if len(chunks1) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks1))
	}

	chunks2 := buildChunks(embedder, path, content)
	if len(chunks2) != len(chunks1) {
		t.Fatalf("expected same chunk count on reindex, got %d and %d", len(chunks1), len(chunks2))
	}

	for i := range chunks1 {
		if chunks1[i].UUID != chunks2[i].UUID {
			t.Errorf("chunk %d: expected stable UUID on reindex, got %q then %q", i, chunks1[i].UUID, chunks2[i].UUID)
		}
	}

	// Modify only the trailing 'b' suffix. The first chunk remains exactly the
	// same 1000 'a's, so its ID must be unchanged. The second chunk's content
	// changes, so its ID must change.
	modifiedContent := strings.Repeat("a", 1000) + strings.Repeat("c", 200)
	chunks3 := buildChunks(embedder, path, modifiedContent)
	if len(chunks3) < len(chunks1) {
		t.Fatalf("expected at least as many chunks after edit, got %d", len(chunks3))
	}

	if chunks3[0].UUID != chunks1[0].UUID {
		t.Errorf("unchanged first chunk should keep its ID, got %q then %q", chunks1[0].UUID, chunks3[0].UUID)
	}
	if chunks3[len(chunks3)-1].UUID == chunks1[len(chunks1)-1].UUID {
		t.Error("modified trailing chunk should get a new ID")
	}
}

// mixedModelEmbedder returns a deterministic embedding and reports the
// configured model for even-indexed texts and the local hash fallback model
// for odd-indexed texts. It lets buildChunks tests verify per-chunk provenance
// without an Ollama dependency.
type mixedModelEmbedder struct {
	dim int
}

func (m *mixedModelEmbedder) GenerateVector(text string) []float32 {
	vec := make([]float32, m.dim)
	for i, b := range []byte(text) {
		vec[i%m.dim] += float32(b) / 255.0
	}
	return vec
}

func (m *mixedModelEmbedder) GenerateVectors(texts []string) [][]float32 {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = m.GenerateVector(t)
	}
	return out
}

func (m *mixedModelEmbedder) GenerateVectorsWithModel(texts []string) []EmbeddingResult {
	out := make([]EmbeddingResult, len(texts))
	for i, t := range texts {
		model := m.ModelName()
		if i%2 == 1 {
			model = localHashModelName
		}
		out[i] = EmbeddingResult{Vector: m.GenerateVector(t), Model: model}
	}
	return out
}

func (m *mixedModelEmbedder) GenerateVectorNoRetry(text string) []float32 {
	return m.GenerateVector(text)
}

func (m *mixedModelEmbedder) GenerateVectorNoRetryWithModel(text string) EmbeddingResult {
	return EmbeddingResult{Vector: m.GenerateVector(text), Model: m.ModelName()}
}

func (m *mixedModelEmbedder) Dim() int {
	return m.dim
}

func (m *mixedModelEmbedder) ModelName() string {
	return "ollama-model"
}

// TestBuildChunks_RecordsActualEmbeddingModel verifies that each chunk is
// stamped with the model that actually produced its vector, so mixed spaces
// can be detected later (issue #268).
func TestBuildChunks_RecordsActualEmbeddingModel(t *testing.T) {
	embedder := &mixedModelEmbedder{dim: 768}
	content := strings.Repeat("alpha ", 200) + " " + strings.Repeat("beta ", 200)

	chunks := buildChunks(embedder, "doc.md", content)
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}

	var ollamaCount, fallbackCount int
	for _, c := range chunks {
		switch c.Model {
		case "ollama-model":
			ollamaCount++
		case localHashModelName:
			fallbackCount++
		default:
			t.Errorf("unexpected model %q for chunk %d", c.Model, c.ChunkIndex)
		}
	}
	if ollamaCount == 0 {
		t.Error("expected at least one ollama-model chunk")
	}
	if fallbackCount == 0 {
		t.Error("expected at least one local-hash chunk")
	}
}
