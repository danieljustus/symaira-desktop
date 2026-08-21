package journal

import (
	"math/rand"
	"path/filepath"
	"testing"
	"time"

	"github.com/danieljustus/symaira-room/internal/event"
	"github.com/danieljustus/symaira-room/internal/identity"
)

func TestMergePropertyRandomOrder(t *testing.T) {
	id1, _ := identity.Generate("a")
	id2, _ := identity.Generate("b")
	id3, _ := identity.Generate("c")

	e1 := &event.Event{V: 1, ID: "ev_1", Author: id1.MemberID, Seq: 1, Lamport: 1, TS: "2026-07-27T10:00:00.000Z", Kind: event.KindNotePosted}
	e2 := &event.Event{V: 1, ID: "ev_2", Author: id2.MemberID, Seq: 1, Lamport: 2, TS: "2026-07-27T10:00:01.000Z", Kind: event.KindNotePosted}
	e3 := &event.Event{V: 1, ID: "ev_3", Author: id3.MemberID, Seq: 1, Lamport: 2, TS: "2026-07-27T10:00:01.000Z", Kind: event.KindNotePosted}
	e4 := &event.Event{V: 1, ID: "ev_4", Author: id1.MemberID, Seq: 2, Lamport: 3, TS: "2026-07-27T10:00:02.000Z", Kind: event.KindNotePosted}

	segments := map[string][]*event.Event{
		id1.MemberID: {e1, e4},
		id2.MemberID: {e2},
		id3.MemberID: {e3},
	}

	canonicalMerged := Merge(segments)

	// Randomly shuffle input slices 100 times and verify identical merge output
	r := rand.New(rand.NewSource(42))
	for i := 0; i < 100; i++ {
		var all []*event.Event
		all = append(all, e1, e2, e3, e4)
		r.Shuffle(len(all), func(i, j int) { all[i], all[j] = all[j], all[i] })

		SortEventsTotalOrder(all)

		for k := range canonicalMerged {
			if canonicalMerged[k].ID != all[k].ID {
				t.Fatalf("run %d: expected event ID at pos %d to be %s, got %s", i, k, canonicalMerged[k].ID, all[k].ID)
			}
		}
	}
}

func TestCausality(t *testing.T) {
	tempDir := t.TempDir()
	j := New(filepath.Join(tempDir, "journal"))

	id1, _ := identity.Generate("user1")
	id2, _ := identity.Generate("user2")

	// User1 appends ev1 (Lamport 1)
	ev1 := &event.Event{V: 1, ID: "ev_1", Author: id1.MemberID, Lamport: 1, Kind: event.KindNotePosted}
	if err := ev1.Sign(id1); err != nil {
		t.Fatalf("sign ev1: %v", err)
	}
	if err := j.Append(ev1); err != nil {
		t.Fatalf("append ev1: %v", err)
	}

	// User2 observes max lamport (1), so user2's new event gets Lamport 2
	maxL, err := j.MaxLamport()
	if err != nil {
		t.Fatalf("MaxLamport: %v", err)
	}
	ev2 := &event.Event{V: 1, ID: "ev_2", Author: id2.MemberID, Lamport: maxL + 1, Kind: event.KindNotePosted}
	if err := ev2.Sign(id2); err != nil {
		t.Fatalf("sign ev2: %v", err)
	}
	if err := j.Append(ev2); err != nil {
		t.Fatalf("append ev2: %v", err)
	}

	merged, err := j.MergeAll()
	if err != nil {
		t.Fatalf("merge all: %v", err)
	}

	if len(merged) != 2 {
		t.Fatalf("expected 2 merged events, got %d", len(merged))
	}

	if merged[0].ID != ev1.ID || merged[1].ID != ev2.ID {
		t.Errorf("causality order failed: expected [%s, %s], got [%s, %s]", ev1.ID, ev2.ID, merged[0].ID, merged[1].ID)
	}
}

func TestOrderingStabilityWithNewSegment(t *testing.T) {
	id1, _ := identity.Generate("a")
	id2, _ := identity.Generate("b")

	e1 := &event.Event{V: 1, ID: "ev_1", Author: id1.MemberID, Seq: 1, Lamport: 1, TS: event.FormatTimestamp(time.Now()), Kind: event.KindNotePosted}
	e2 := &event.Event{V: 1, ID: "ev_2", Author: id1.MemberID, Seq: 2, Lamport: 2, TS: event.FormatTimestamp(time.Now()), Kind: event.KindNotePosted}

	initialSegments := map[string][]*event.Event{
		id1.MemberID: {e1, e2},
	}
	initialMerged := Merge(initialSegments)

	// Now a new segment from id2 arrives with Lamport 3
	e3 := &event.Event{V: 1, ID: "ev_3", Author: id2.MemberID, Seq: 1, Lamport: 3, TS: event.FormatTimestamp(time.Now()), Kind: event.KindNotePosted}
	updatedSegments := map[string][]*event.Event{
		id1.MemberID: {e1, e2},
		id2.MemberID: {e3},
	}
	updatedMerged := Merge(updatedSegments)

	// Relative order of e1 and e2 must stay unchanged
	if updatedMerged[0].ID != initialMerged[0].ID || updatedMerged[1].ID != initialMerged[1].ID {
		t.Errorf("relative order of existing events changed after new segment added")
	}
	if updatedMerged[2].ID != e3.ID {
		t.Errorf("new event should be placed at end")
	}
}
