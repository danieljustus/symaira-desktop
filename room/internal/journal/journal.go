package journal

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/danieljustus/symaira-room/internal/event"
)

var (
	ErrChainBroken = errors.New("journal hash chain broken")
	ErrSeqMismatch = errors.New("journal sequence number mismatch")
)

const zeroHash = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

// tailStat is the cached tail of one author's segment: the sequence number and
// line hash of the last event written, plus the segment file size the cache
// was populated at. The size lets Append detect external modifications with a
// cheap stat and re-sync from disk instead of silently diverging.
type tailStat struct {
	seq  uint64
	hash string
	size int64
}

type Journal struct {
	Dir string
	mu  sync.RWMutex

	// tails caches per-author segment tail stats. Entries are populated on
	// first use (cold start, reading from disk) and kept in lockstep with
	// Append. Guarded by mu.
	tails map[string]tailStat

	// lamportMax/lamportKnown cache the global maximum Lamport across all
	// segments. lamportKnown is only set once the whole journal has been
	// scanned at least once, so the cached value can never under-report the
	// true maximum. Guarded by mu.
	lamportMax   uint64
	lamportKnown bool
}

func New(dir string) *Journal {
	return &Journal{Dir: dir, tails: make(map[string]tailStat)}
}

func (j *Journal) SegmentPath(author string) string {
	return filepath.Join(j.Dir, author+".jsonl")
}

func ComputeLineHash(line []byte) string {
	h := sha256.Sum256(line)
	return "sha256:" + hex.EncodeToString(h[:])
}

func (j *Journal) PrepareEvent(ev *event.Event) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	// Resolve the segment tail first: if the file changed on disk since the
	// cache was populated, the re-sync also invalidates the Lamport cache so
	// the Lamport assignment below cannot under-report.
	st, err := j.tailStatForLocked(ev.Author)
	if err != nil {
		return err
	}

	maxL, _ := j.maxLamportUnlocked()
	if ev.Lamport <= maxL {
		ev.Lamport = maxL + 1
	}

	ev.Seq = st.seq + 1
	ev.Prev = st.hash
	return nil
}

func (j *Journal) Append(ev *event.Event) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	if err := os.MkdirAll(j.Dir, 0755); err != nil {
		return fmt.Errorf("mkdir journal dir: %w", err)
	}

	segPath := j.SegmentPath(ev.Author)
	if ev.Seq == 0 {
		st, err := j.tailStatForLocked(ev.Author)
		if err != nil {
			return err
		}
		ev.Seq = st.seq + 1
		ev.Prev = st.hash
	}
	if ev.Lamport == 0 {
		maxL, _ := j.maxLamportUnlocked()
		ev.Lamport = maxL + 1
	}

	line, err := ev.MarshalJSONLine()
	if err != nil {
		return fmt.Errorf("marshal event line: %w", err)
	}

	f, err := os.OpenFile(segPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("open segment file: %w", err)
	}
	defer func() { _ = f.Close() }()

	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("write segment line: %w", err)
	}

	// Keep the tail cache in lockstep with what was just written. This runs
	// under j.mu, so the cached seq/prev chain can never diverge from the
	// on-disk segment for appends made through this instance.
	if j.tails == nil {
		j.tails = make(map[string]tailStat)
	}
	st := j.tails[ev.Author]
	st.seq = ev.Seq
	// The marshaled line carries a trailing newline; the hash chain covers
	// the JSON content without it, matching readLastSegmentStats and
	// VerifyChain, which read lines via bufio.Scanner.
	st.hash = ComputeLineHash(bytes.TrimSuffix(line, []byte("\n")))
	st.size += int64(len(line))
	j.tails[ev.Author] = st

	if j.lamportKnown && ev.Lamport > j.lamportMax {
		j.lamportMax = ev.Lamport
	}

	return nil
}

// tailStatForLocked returns the tail stats for author's segment, loading them
// from disk on first use (cold start) and re-syncing when the segment file
// changed on disk since the cache was populated. Caller must hold j.mu.
func (j *Journal) tailStatForLocked(author string) (tailStat, error) {
	if st, ok := j.tails[author]; ok {
		fi, err := os.Stat(j.SegmentPath(author))
		if err == nil {
			if fi.Size() == st.size {
				return st, nil
			}
			// Segment grew or shrank outside this instance: re-sync below.
		} else if os.IsNotExist(err) {
			if st.size == 0 {
				return st, nil
			}
			// Segment was deleted externally; the tail is empty again.
			st = tailStat{}
			j.tails[author] = st
			return st, nil
		} else {
			return tailStat{}, err
		}
	}
	return j.loadTailLocked(author)
}

// loadTailLocked cold-starts the tail cache entry for author from disk.
// Caller must hold j.mu.
func (j *Journal) loadTailLocked(author string) (tailStat, error) {
	segPath := j.SegmentPath(author)
	seq, hash, err := readLastSegmentStats(segPath)
	if err != nil {
		return tailStat{}, err
	}
	var size int64
	if fi, err := os.Stat(segPath); err == nil {
		size = fi.Size()
	}
	st := tailStat{seq: seq, hash: hash, size: size}
	if j.tails == nil {
		j.tails = make(map[string]tailStat)
	}
	j.tails[author] = st
	// A segment was read from disk; the cached global max Lamport may be
	// stale if the file was written outside this instance, so drop it. The
	// next MaxLamport call rescans once and repopulates it.
	j.lamportKnown = false
	return st, nil
}

func readLastSegmentStats(path string) (uint64, string, error) {
	f, err := osOpen(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, zeroHash, nil
		}
		return 0, "", err
	}
	defer func() { _ = f.Close() }()

	var lastLine []byte
	var count uint64
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) > 0 {
			count++
			lastLine = append([]byte(nil), line...)
		}
	}

	if count == 0 {
		return 0, zeroHash, nil
	}

	return count, ComputeLineHash(lastLine), nil
}

func (j *Journal) ReadSegment(author string) ([]*event.Event, error) {
	j.mu.RLock()
	defer j.mu.RUnlock()

	return j.readSegmentUnlocked(author)
}

func (j *Journal) readSegmentUnlocked(author string) ([]*event.Event, error) {
	segPath := j.SegmentPath(author)
	f, err := osOpen(segPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []*event.Event{}, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var events []*event.Event
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		ev, err := event.UnmarshalJSONLine(line)
		if err != nil {
			return nil, fmt.Errorf("unmarshal line: %w", err)
		}
		events = append(events, ev)
	}

	return events, nil
}

func (j *Journal) VerifyChain(author string) error {
	j.mu.RLock()
	defer j.mu.RUnlock()

	events, err := j.readSegmentUnlocked(author)
	if err != nil {
		return err
	}
	segPath := j.SegmentPath(author)
	f, err := osOpen(segPath)
	if err != nil {
		if os.IsNotExist(err) && len(events) == 0 {
			return nil
		}
		return err
	}
	defer func() { _ = f.Close() }()

	lines, err := readLines(f)
	if err != nil {
		return err
	}

	var prevHash = zeroHash
	var expectedSeq uint64 = 1

	for idx, lineBytes := range lines {
		if idx >= len(events) {
			break
		}
		ev := events[idx]
		if ev.Seq != expectedSeq {
			return fmt.Errorf("%w: author %s expected seq %d, got %d", ErrSeqMismatch, author, expectedSeq, ev.Seq)
		}
		if ev.Prev != prevHash {
			return fmt.Errorf("%w: author %s seq %d expected prev %s, got %s", ErrChainBroken, author, ev.Seq, prevHash, ev.Prev)
		}
		prevHash = ComputeLineHash(lineBytes)
		expectedSeq++
	}
	return nil
}

func (j *Journal) ReadAllSegments() (map[string][]*event.Event, error) {
	j.mu.RLock()
	defer j.mu.RUnlock()

	return j.readAllSegmentsUnlocked()
}

func (j *Journal) readAllSegmentsUnlocked() (map[string][]*event.Event, error) {
	entries, err := os.ReadDir(j.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string][]*event.Event{}, nil
		}
		return nil, err
	}

	result := make(map[string][]*event.Event)
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jsonl") {
			author := strings.TrimSuffix(entry.Name(), ".jsonl")
			events, err := j.readSegmentUnlocked(author)
			if err != nil {
				return nil, fmt.Errorf("read segment %s: %w", author, err)
			}
			result[author] = events
		}
	}
	return result, nil
}
