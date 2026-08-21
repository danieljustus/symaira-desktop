package journal

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/danieljustus/symaira-room/internal/event"
	"github.com/danieljustus/symaira-room/internal/identity"
)

func TestQueryLogFilter(t *testing.T) {
	tempDir := t.TempDir()
	ownerID, j := createTestRoom(t, tempDir)

	// Append note 1
	ev1 := &event.Event{
		V:      1,
		ID:     "ev_n1",
		Room:   "rm_test",
		Author: ownerID.MemberID,
		Kind:   event.KindNotePosted,
		TS:     "2026-07-27T10:00:00.000Z",
		Body:   json.RawMessage(`{"text":"first note"}`),
	}
	if err := j.PrepareEvent(ev1); err != nil {
		t.Fatalf("prepare ev1: %v", err)
	}
	if err := ev1.Sign(ownerID); err != nil {
		t.Fatalf("sign ev1: %v", err)
	}
	if err := j.Append(ev1); err != nil {
		t.Fatalf("append ev1: %v", err)
	}

	// Append decision 1
	ev2 := &event.Event{
		V:      1,
		ID:     "ev_d1",
		Room:   "rm_test",
		Author: ownerID.MemberID,
		Kind:   event.KindDecisionRecorded,
		TS:     "2026-07-27T11:00:00.000Z",
		Body:   json.RawMessage(`{"text":"decision 1"}`),
	}
	if err := j.PrepareEvent(ev2); err != nil {
		t.Fatalf("prepare ev2: %v", err)
	}
	if err := ev2.Sign(ownerID); err != nil {
		t.Fatalf("sign ev2: %v", err)
	}
	if err := j.Append(ev2); err != nil {
		t.Fatalf("append ev2: %v", err)
	}

	// Filter by kind=note.posted
	res, err := j.QueryLog(LogFilter{Kind: event.KindNotePosted})
	if err != nil {
		t.Fatalf("QueryLog failed: %v", err)
	}
	if len(res.Events) != 1 || res.Events[0].ID != ev1.ID {
		t.Errorf("expected 1 note event %s, got %v", ev1.ID, res.Events)
	}

	// Filter by limit=1
	resLimit, err := j.QueryLog(LogFilter{Limit: 1})
	if err != nil {
		t.Fatalf("QueryLog with limit failed: %v", err)
	}
	if len(resLimit.Events) != 1 {
		t.Errorf("expected 1 event with limit=1, got %d", len(resLimit.Events))
	}
}

func TestFormatEventHuman(t *testing.T) {
	id, _ := identity.Generate("user")
	ev := &event.Event{
		V:      1,
		ID:     "ev_test",
		Author: id.MemberID,
		Kind:   event.KindNotePosted,
		TS:     event.FormatTimestamp(time.Now()),
		Body:   json.RawMessage(`{"text":"sample text"}`),
	}
	formatted := FormatEventHuman(ev)
	if !testing.Verbose() && formatted == "" {
		t.Errorf("expected non-empty formatted string")
	}
}
