package journal

import (
	"sort"

	"github.com/danieljustus/symaira-room/internal/event"
)

// SortEventsTotalOrder sorts events deterministically into a total order.
// Sort key: Lamport asc -> TS asc -> Author asc -> Seq asc -> ID asc.
func SortEventsTotalOrder(events []*event.Event) {
	sort.SliceStable(events, func(i, j int) bool {
		a, b := events[i], events[j]
		if a.Lamport != b.Lamport {
			return a.Lamport < b.Lamport
		}
		if a.TS != b.TS {
			return a.TS < b.TS
		}
		if a.Author != b.Author {
			return a.Author < b.Author
		}
		if a.Seq != b.Seq {
			return a.Seq < b.Seq
		}
		return a.ID < b.ID
	})
}

// Merge merges all events from all segment maps into a single deterministic total-ordered slice.
func Merge(segments map[string][]*event.Event) []*event.Event {
	var total []*event.Event
	for _, events := range segments {
		total = append(total, events...)
	}
	SortEventsTotalOrder(total)
	return total
}

func (j *Journal) MergeAll() ([]*event.Event, error) {
	segments, err := j.ReadAllSegments()
	if err != nil {
		return nil, err
	}
	return Merge(segments), nil
}

func (j *Journal) MaxLamport() (uint64, error) {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.maxLamportUnlocked()
}

func (j *Journal) maxLamportUnlocked() (uint64, error) {
	if j.lamportKnown {
		return j.lamportMax, nil
	}
	segments, err := j.readAllSegmentsUnlocked()
	if err != nil {
		return 0, err
	}
	events := Merge(segments)
	var maxL uint64
	for _, ev := range events {
		if ev.Lamport > maxL {
			maxL = ev.Lamport
		}
	}
	j.lamportMax = maxL
	j.lamportKnown = true
	return maxL, nil
}
