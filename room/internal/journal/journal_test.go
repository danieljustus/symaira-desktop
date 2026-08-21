package journal

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danieljustus/symaira-room/internal/event"
	"github.com/danieljustus/symaira-room/internal/identity"
)

func TestJournalAppendReadChain(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tempDir)

	id, err := identity.Generate("alice")
	if err != nil {
		t.Fatalf("failed to generate identity: %v", err)
	}

	j := New(filepath.Join(tempDir, "journal"))

	// Append 3 events
	for i := 1; i <= 3; i++ {
		ev := &event.Event{
			V:       1,
			ID:      "ev_" + string(rune('0'+i)),
			Room:    "rm_test",
			Author:  id.MemberID,
			Lamport: uint64(i),
			TS:      event.FormatTimestamp(time.Now()),
			Kind:    event.KindNotePosted,
			Body:    json.RawMessage(`{"text":"msg"}`),
		}
		if err := ev.Sign(id); err != nil {
			t.Fatalf("sign failed: %v", err)
		}
		if err := j.Append(ev); err != nil {
			t.Fatalf("append failed: %v", err)
		}
	}

	events, err := j.ReadSegment(id.MemberID)
	if err != nil {
		t.Fatalf("read segment failed: %v", err)
	}

	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}

	if events[0].Seq != 1 || events[1].Seq != 2 || events[2].Seq != 3 {
		t.Errorf("sequence numbers incorrect")
	}
}

func TestChainTamperingDetection(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tempDir)

	id, _ := identity.Generate("bob")
	j := New(filepath.Join(tempDir, "journal"))

	for i := 1; i <= 3; i++ {
		ev := &event.Event{
			V:       1,
			ID:      "ev_bob",
			Room:    "rm_test",
			Author:  id.MemberID,
			Lamport: uint64(i),
			TS:      event.FormatTimestamp(time.Now()),
			Kind:    event.KindNotePosted,
			Body:    json.RawMessage(`{"text":"hello"}`),
		}
		if err := ev.Sign(id); err != nil {
			t.Fatalf("sign failed: %v", err)
		}
		if err := j.Append(ev); err != nil {
			t.Fatalf("append failed: %v", err)
		}
	}

	// Tamper line 2 in file
	segPath := j.SegmentPath(id.MemberID)
	data, err := os.ReadFile(segPath)
	if err != nil {
		t.Fatalf("failed to read segment: %v", err)
	}

	lines := strings.Split(string(data), "\n")
	// Modify line 2
	lines[1] = strings.Replace(lines[1], `"hello"`, `"tampered"`, 1)

	if err := os.WriteFile(segPath, []byte(strings.Join(lines, "\n")), 0644); err != nil {
		t.Fatalf("failed to write tampered segment: %v", err)
	}

	err = j.VerifyChain(id.MemberID)
	if err == nil {
		t.Fatalf("expected chain tampering detection, got nil error")
	}
	if !errors.Is(err, ErrChainBroken) && !errors.Is(err, ErrSeqMismatch) {
		t.Errorf("expected ErrChainBroken or ErrSeqMismatch, got %v", err)
	}
}

func TestConcurrentAppendsDifferentSegments(t *testing.T) {
	tempDir := t.TempDir()
	j := New(filepath.Join(tempDir, "journal"))

	id1, _ := identity.Generate("user1")
	id2, _ := identity.Generate("user2")

	var wg sync.WaitGroup
	wg.Add(2)

	appendWorker := func(id *identity.Identity) {
		defer wg.Done()
		for i := 1; i <= 10; i++ {
			ev := &event.Event{
				V:       1,
				ID:      "ev_conc",
				Room:    "rm_conc",
				Author:  id.MemberID,
				Lamport: uint64(i),
				TS:      event.FormatTimestamp(time.Now()),
				Kind:    event.KindNotePosted,
				Body:    json.RawMessage(`{"text":"work"}`),
			}
			if err := ev.Sign(id); err != nil {
				t.Errorf("worker sign error: %v", err)
				continue
			}
			if err := j.Append(ev); err != nil {
				t.Errorf("worker append error: %v", err)
			}
		}
	}

	go appendWorker(id1)
	go appendWorker(id2)
	wg.Wait()

	events1, err := j.ReadSegment(id1.MemberID)
	if err != nil || len(events1) != 10 {
		t.Errorf("user1 segment failed: len=%d, err=%v", len(events1), err)
	}

	events2, err := j.ReadSegment(id2.MemberID)
	if err != nil || len(events2) != 10 {
		t.Errorf("user2 segment failed: len=%d, err=%v", len(events2), err)
	}
}
