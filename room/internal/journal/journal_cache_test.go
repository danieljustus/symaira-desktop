package journal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/danieljustus/symaira-room/internal/event"
	"github.com/danieljustus/symaira-room/internal/identity"
)

// rawNote builds an unsigned note event with an explicit Lamport. Append
// never verifies signatures, so this is enough for Append-only tests.
func rawNote(id *identity.Identity, lamport uint64) *event.Event {
	return &event.Event{
		V:       1,
		ID:      fmt.Sprintf("ev_note_%d", lamport),
		Room:    "rm_test",
		Author:  id.MemberID,
		Lamport: lamport,
		TS:      event.FormatTimestamp(time.Now()),
		Kind:    event.KindNotePosted,
		Body:    json.RawMessage(`{"text":"note"}`),
	}
}

// preparedNote resolves Seq/Prev (and possibly Lamport) via PrepareEvent and
// signs afterwards, so the stored signature matches the final on-disk event.
// Required wherever Verify() runs.
func preparedNote(t *testing.T, j *Journal, id *identity.Identity, lamport uint64) *event.Event {
	t.Helper()
	ev := rawNote(id, lamport)
	if err := j.PrepareEvent(ev); err != nil {
		t.Fatalf("prepare note: %v", err)
	}
	if err := ev.Sign(id); err != nil {
		t.Fatalf("sign note: %v", err)
	}
	return ev
}

// TestAppendChainWithCache appends many events through the cached path and
// verifies the on-disk hash chain is still intact.
func TestAppendChainWithCache(t *testing.T) {
	tempDir := t.TempDir()
	ownerID, j := createTestRoom(t, tempDir)

	for i := 2; i <= 30; i++ {
		ev := preparedNote(t, j, ownerID, uint64(i))
		if err := j.Append(ev); err != nil {
			t.Fatalf("append note %d: %v", i, err)
		}
		if ev.Seq != uint64(i) {
			t.Fatalf("expected seq %d, got %d", i, ev.Seq)
		}
	}

	if err := j.VerifyChain(ownerID.MemberID); err != nil {
		t.Fatalf("chain verification failed after cached appends: %v", err)
	}

	report, err := j.Verify()
	if err != nil {
		t.Fatalf("verify error: %v", err)
	}
	if !report.Valid {
		t.Fatalf("expected valid report, got findings: %v", report.Findings)
	}
}

// TestRestartColdStart ensures a fresh Journal instance on the same directory
// loads the existing segment tails from disk (cold start) and continues the
// seq/prev chain and Lamport clock correctly.
func TestRestartColdStart(t *testing.T) {
	tempDir := t.TempDir()
	dir := filepath.Join(tempDir, "journal")
	ownerID, j1 := createTestRoom(t, tempDir)

	for i := 2; i <= 5; i++ {
		ev := preparedNote(t, j1, ownerID, uint64(i))
		if err := j1.Append(ev); err != nil {
			t.Fatalf("append note %d: %v", i, err)
		}
	}

	// "Restart": a brand-new instance over the same directory. It must not
	// assume an empty journal.
	j2 := New(dir)

	ev6 := preparedNote(t, j2, ownerID, 0) // Seq and Lamport both auto-assigned
	if err := j2.Append(ev6); err != nil {
		t.Fatalf("append after restart: %v", err)
	}
	if ev6.Seq != 6 {
		t.Fatalf("restart cold start: expected seq 6, got %d", ev6.Seq)
	}
	if ev6.Lamport != 6 {
		t.Fatalf("restart cold start: expected lamport 6, got %d", ev6.Lamport)
	}

	ev7 := preparedNote(t, j2, ownerID, 7)
	if err := j2.Append(ev7); err != nil {
		t.Fatalf("append after restart: %v", err)
	}

	if err := j2.VerifyChain(ownerID.MemberID); err != nil {
		t.Fatalf("chain verification failed after restart: %v", err)
	}

	// The cached tail must agree with a fresh on-disk read.
	segPath := j2.SegmentPath(ownerID.MemberID)
	diskSeq, diskHash, err := readLastSegmentStats(segPath)
	if err != nil {
		t.Fatalf("read last segment stats: %v", err)
	}
	cached, err := j2.tailStatForLocked(ownerID.MemberID)
	if err != nil {
		t.Fatalf("cached tail stats: %v", err)
	}
	if cached.seq != diskSeq || cached.hash != diskHash {
		t.Fatalf("cache diverged from disk: cache seq=%d hash=%s, disk seq=%d hash=%s",
			cached.seq, cached.hash, diskSeq, diskHash)
	}

	report, err := j2.Verify()
	if err != nil {
		t.Fatalf("verify error: %v", err)
	}
	if !report.Valid {
		t.Fatalf("expected valid report after restart, got findings: %v", report.Findings)
	}
}

// TestExternalAppendResyncsCache simulates another writer appending to the
// segment behind this instance's back: the next append must re-sync the cache
// from disk and continue the chain instead of forking it.
func TestExternalAppendResyncsCache(t *testing.T) {
	tempDir := t.TempDir()
	ownerID, j1 := createTestRoom(t, tempDir)

	note2 := preparedNote(t, j1, ownerID, 2)
	if err := j1.Append(note2); err != nil {
		t.Fatalf("append note: %v", err)
	}

	// External writer: a separate instance prepares an event from a fresh
	// read, and the line is written straight to the file (j1 never sees it).
	dir := filepath.Join(tempDir, "journal")
	j2 := New(dir)
	ext := preparedNote(t, j2, ownerID, 3)
	extLine, err := ext.MarshalJSONLine()
	if err != nil {
		t.Fatalf("marshal external event: %v", err)
	}
	segPath := j1.SegmentPath(ownerID.MemberID)
	f, err := os.OpenFile(segPath, os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		t.Fatalf("open segment: %v", err)
	}
	if _, err := f.Write(extLine); err != nil {
		_ = f.Close()
		t.Fatalf("write external line: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close segment: %v", err)
	}

	// j1's next append must detect the size change, re-sync, and continue
	// the chain from the externally appended line.
	note4 := preparedNote(t, j1, ownerID, 4)
	if err := j1.Append(note4); err != nil {
		t.Fatalf("append after external write: %v", err)
	}
	if note4.Seq != 4 {
		t.Fatalf("expected seq 4 after external write, got %d", note4.Seq)
	}
	if note4.Prev != ComputeLineHash(bytes.TrimSuffix(extLine, []byte("\n"))) {
		t.Fatalf("expected prev to chain off the externally appended line")
	}

	if err := j1.VerifyChain(ownerID.MemberID); err != nil {
		t.Fatalf("chain verification failed after external write: %v", err)
	}

	report, err := j1.Verify()
	if err != nil {
		t.Fatalf("verify error: %v", err)
	}
	if !report.Valid {
		t.Fatalf("expected valid report after external write, got findings: %v", report.Findings)
	}
}

// TestAppendDoesNotRescanSegment proves appends and MaxLamport no longer
// re-read segment files after the cache is warm. osOpen is instrumented to
// count every segment file open: the whole test must perform at most 2 opens
// (one tail cold-start read + one MaxLamport cold scan) regardless of how
// many events are appended.
func TestAppendDoesNotRescanSegment(t *testing.T) {
	tempDir := t.TempDir()
	j := New(filepath.Join(tempDir, "journal"))
	id, err := identity.Generate("alice")
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}
	author := id.MemberID
	segPath := j.SegmentPath(author)

	var mu sync.Mutex
	opens := make(map[string]int)
	origOpen := osOpen
	osOpen = func(path string) (*os.File, error) {
		mu.Lock()
		opens[path]++
		mu.Unlock()
		return origOpen(path)
	}
	t.Cleanup(func() { osOpen = origOpen })

	countOpens := func() int {
		mu.Lock()
		defer mu.Unlock()
		return opens[segPath]
	}

	// Warm-up: 100 appends. Only the first may read the (missing) segment to
	// compute seq 1; the rest must hit the cache.
	for i := 1; i <= 100; i++ {
		if err := j.Append(rawNote(id, uint64(i))); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	if got := countOpens(); got != 1 {
		t.Fatalf("expected exactly 1 segment read during warm-up, got %d", got)
	}

	// MaxLamport: one cold scan, then cached forever.
	if _, err := j.MaxLamport(); err != nil {
		t.Fatalf("max lamport: %v", err)
	}
	if _, err := j.MaxLamport(); err != nil {
		t.Fatalf("max lamport (cached): %v", err)
	}
	if got := countOpens(); got != 2 {
		t.Fatalf("expected 2 reads after MaxLamport (1 tail + 1 scan), got %d", got)
	}

	// 1000 more appends: zero additional reads.
	for i := 101; i <= 1100; i++ {
		if err := j.Append(rawNote(id, uint64(i))); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	if got := countOpens(); got != 2 {
		t.Fatalf("appends rescan the segment: expected 2 reads, got %d", got)
	}

	// PrepareEvent + Append with a fully warm cache: zero additional reads.
	evPrep := rawNote(id, 1101)
	if err := j.PrepareEvent(evPrep); err != nil {
		t.Fatalf("prepare event: %v", err)
	}
	if err := j.Append(evPrep); err != nil {
		t.Fatalf("append prepared event: %v", err)
	}
	if got := countOpens(); got != 2 {
		t.Fatalf("prepare/append rescan the segment: expected 2 reads, got %d", got)
	}

	if err := j.VerifyChain(author); err != nil {
		t.Fatalf("chain verification failed: %v", err)
	}
}
