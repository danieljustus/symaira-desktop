package journal

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/danieljustus/symaira-room/internal/event"
	"github.com/danieljustus/symaira-room/internal/identity"
)

func BenchmarkAppend(b *testing.B) {
	for _, prefill := range []int{100, 1000, 5000} {
		b.Run(fmt.Sprintf("prefill_%d", prefill), func(b *testing.B) {
			tempDir := b.TempDir()
			j := New(filepath.Join(tempDir, "journal"))
			id, err := identity.Generate("alice")
			if err != nil {
				b.Fatal(err)
			}

			for i := 1; i <= prefill; i++ {
				ev := benchEvent(id, uint64(i))
				if err := j.Append(ev); err != nil {
					b.Fatal(err)
				}
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				ev := benchEvent(id, uint64(prefill+i+1))
				if err := j.Append(ev); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func benchEvent(id *identity.Identity, lamport uint64) *event.Event {
	return &event.Event{
		V:       1,
		ID:      fmt.Sprintf("ev_bench_%d", lamport),
		Room:    "rm_bench",
		Author:  id.MemberID,
		Lamport: lamport,
		TS:      event.FormatTimestamp(time.Now()),
		Kind:    event.KindNotePosted,
		Body:    json.RawMessage(`{"text":"bench"}`),
	}
}
