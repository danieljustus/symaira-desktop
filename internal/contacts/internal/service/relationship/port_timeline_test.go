package relationship

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	domain "github.com/danieljustus/symaira-desktop/internal/contacts/internal/domain/relationship"
	"github.com/danieljustus/symaira-desktop/internal/contacts/internal/errs"
	"github.com/danieljustus/symaira-desktop/internal/contacts/internal/storage/sqlite"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
	"github.com/google/uuid"
)

// CONTACT-001/002: the combined interaction/follow-up timeline and the
// idempotent reviewed-meeting import against the real Go service.
//
// Regenerate with:
// PORT_GENERATE=1 go test ./internal/contacts/internal/service/relationship -run '^TestPortContactsTimelineMeetingContract$'

type portTimelineRand struct {
	n   byte
	ids []string
}

func (r *portTimelineRand) Read(p []byte) (int, error) {
	r.n++
	for i := range p {
		p[i] = 0
	}
	p[len(p)-1] = r.n
	u := append([]byte(nil), p...)
	u[6] = (u[6] & 15) | 64
	u[8] = (u[8] & 63) | 128
	id, err := uuid.FromBytes(u)
	if err != nil {
		return 0, err
	}
	r.ids = append(r.ids, id.String())
	return len(p), nil
}

type portTimelineAction struct {
	Op     string         `json:"op"`
	Entity string         `json:"entity"`
	ID     string         `json:"id"`
	Input  map[string]any `json:"input"`
}

func TestPortContactsTimelineMeetingContract(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	const stamp = "2026-01-02T03:04:05Z"
	seed := `INSERT INTO persons(id,display_name,created_at,updated_at) VALUES('p','Ada','2026-01-02T03:04:05Z','2026-01-02T03:04:05Z'),('q','Bob','2026-01-02T03:04:05Z','2026-01-02T03:04:05Z'); INSERT INTO organizations(id,name,created_at,updated_at) VALUES('o','Example','2026-01-02T03:04:05Z','2026-01-02T03:04:05Z');`
	if _, err := db.Exec(seed); err != nil {
		t.Fatal(err)
	}
	entropy := &portTimelineRand{}
	uuid.SetRand(entropy)
	t.Cleanup(func() { uuid.SetRand(nil) })
	svc := New(db)
	actions := []portTimelineAction{}
	results := []any{}
	refs := map[string]string{}

	var normalize func(any) any
	normalize = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			for k, item := range x {
				if (k == "CreatedAt" || k == "UpdatedAt" || k == "CompletedAt" || k == "CancelledAt") && item != nil {
					x[k] = stamp
				} else {
					x[k] = normalize(item)
				}
			}
		case []any:
			for i := range x {
				x[i] = normalize(x[i])
			}
		}
		return v
	}
	when := func(input map[string]any, key string) time.Time {
		s, _ := input[key].(string)
		if s == "" {
			return time.Time{}
		}
		tm, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			t.Fatal(err)
		}
		return tm
	}
	run := func(op, entity, key string, input map[string]any) {
		actions = append(actions, portTimelineAction{op, entity, key, input})
		id := key
		if v, ok := refs[key]; ok {
			id = v
		}
		person := entity == "person"
		var out any
		var callErr error
		switch op {
		case "interaction":
			in := domain.InteractionInput{Kind: domain.InteractionKind(str(input, "Kind")), OccurredAt: when(input, "OccurredAt"), Summary: str(input, "Summary"), ExternalRef: str(input, "ExternalRef")}
			if person {
				out, callErr = svc.AddPersonInteraction(ctx, id, in)
			} else {
				out, callErr = svc.AddOrganizationInteraction(ctx, id, in)
			}
		case "followup":
			in := domain.FollowUpInput{DueAt: when(input, "DueAt"), Notes: str(input, "Notes")}
			if person {
				out, callErr = svc.AddPersonFollowUp(ctx, id, in)
			} else {
				out, callErr = svc.AddOrganizationFollowUp(ctx, id, in)
			}
		case "timeline":
			if person {
				out, callErr = svc.PersonTimeline(ctx, id)
			} else {
				out, callErr = svc.OrganizationTimeline(ctx, id)
			}
		case "meeting":
			in := MeetingImportInput{MeetingID: str(input, "MeetingID"), Title: str(input, "Title"), Summary: str(input, "Summary"), OccurredAt: when(input, "OccurredAt")}
			var created bool
			var interaction *domain.Interaction
			if person {
				interaction, created, callErr = svc.ImportPersonMeeting(ctx, id, in)
			} else {
				interaction, created, callErr = svc.ImportOrganizationMeeting(ctx, id, in)
			}
			out = map[string]any{"interaction": interaction, "created": created}
		case "complete":
			out, callErr = svc.CompleteFollowUp(ctx, id)
		default:
			t.Fatal(op)
		}
		data, err := json.Marshal(out)
		if err != nil {
			t.Fatal(err)
		}
		var value any
		if err := json.Unmarshal(data, &value); err != nil {
			t.Fatal(err)
		}
		value = normalize(value)
		if callErr == nil {
			if obj, ok := value.(map[string]any); ok {
				if inner, ok := obj["ID"].(string); ok {
					if ref, ok := input["ref"].(string); ok {
						refs[ref] = inner
					}
				}
			}
		}
		msg := ""
		if callErr != nil {
			msg = callErr.Error()
		}
		results = append(results, map[string]any{"value": value, "error": msg, "kind": errs.KindOf(callErr)})
		for _, table := range []string{"interactions", "follow_ups"} {
			//nolint:gosec // fixed table names
			if _, err := db.Exec("UPDATE "+table+" SET created_at=?", stamp); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.Exec("UPDATE follow_ups SET updated_at=?", stamp); err != nil {
			t.Fatal(err)
		}
		for _, col := range []string{"completed_at", "cancelled_at"} {
			//nolint:gosec // fixed column names
			if _, err := db.Exec("UPDATE follow_ups SET "+col+"=? WHERE "+col+" IS NOT NULL", stamp); err != nil {
				t.Fatal(err)
			}
		}
	}

	for _, entity := range []string{"person", "organization"} {
		id := "p"
		if entity == "organization" {
			id = "o"
		}
		run("timeline", entity, id, nil)
		// Out of order inserts, equal instants across kinds, fractional seconds
		// and non-UTC offsets: Go orders by instant, newest first, stably.
		run("interaction", entity, id, map[string]any{"Kind": "note", "Summary": "a-oldest", "OccurredAt": "2025-01-01T00:00:00Z"})
		run("interaction", entity, id, map[string]any{"Kind": "call", "Summary": "b-same-instant-as-followup", "OccurredAt": "2025-06-01T12:00:00Z"})
		run("interaction", entity, id, map[string]any{"Kind": "email", "Summary": "c-fraction-after", "OccurredAt": "2025-06-01T12:00:00.5Z"})
		run("interaction", entity, id, map[string]any{"Kind": "meeting", "Summary": "d-offset-same-instant", "OccurredAt": "2025-06-01T14:00:00+02:00"})
		run("interaction", entity, id, map[string]any{"Kind": "note", "Summary": "e-tie-first", "OccurredAt": "2025-03-03T03:03:03Z"})
		run("interaction", entity, id, map[string]any{"Kind": "note", "Summary": "f-tie-second", "OccurredAt": "2025-03-03T03:03:03Z"})
		run("interaction", entity, id, map[string]any{"Kind": "external_reference", "Summary": "g-ext", "ExternalRef": "opaque-1", "OccurredAt": "2030-01-01T00:00:00Z"})
		run("followup", entity, id, map[string]any{"DueAt": "2025-06-01T12:00:00Z", "Notes": "same instant", "ref": entity + "-f1"})
		run("followup", entity, id, map[string]any{"DueAt": "2026-12-31T23:59:59.999999999Z", "Notes": "nanos"})
		run("followup", entity, id, map[string]any{"DueAt": "2025-03-03T03:03:03Z", "Notes": "tie with e/f"})
		run("followup", entity, id, map[string]any{"DueAt": "2024-01-01T00:00:00Z", "Notes": "oldest followup", "ref": entity + "-f4"})
		run("complete", entity, entity+"-f4", nil)
		run("timeline", entity, id, nil)
	}
	run("timeline", "person", "q", nil)
	run("timeline", "person", "missing", nil)
	run("timeline", "organization", "missing", nil)

	for _, entity := range []string{"person", "organization"} {
		id := "q"
		if entity == "organization" {
			id = "o"
		}
		run("meeting", entity, id, map[string]any{"MeetingID": "m-1", "Title": "Kickoff", "Summary": "Reviewed notes", "OccurredAt": "2025-07-01T09:00:00Z"})
		run("meeting", entity, id, map[string]any{"MeetingID": "m-1", "Title": "Ignored retitle", "Summary": "Ignored", "OccurredAt": "2025-08-01T09:00:00Z"})
		run("meeting", entity, id, map[string]any{"MeetingID": "m-2", "Title": "Title only", "OccurredAt": "2025-07-02T09:00:00Z"})
		run("meeting", entity, id, map[string]any{"MeetingID": "m-3", "OccurredAt": "2025-07-03T09:00:00Z"})
		run("meeting", entity, id, map[string]any{"MeetingID": "  ", "Summary": "no id", "OccurredAt": "2025-07-03T09:00:00Z"})
		run("meeting", entity, id, map[string]any{"MeetingID": " m-4 ", "Summary": "padded id", "OccurredAt": "2025-07-04T09:00:00Z"})
		run("meeting", entity, id, map[string]any{"MeetingID": " m-4 ", "Summary": "padded id again", "OccurredAt": "2025-07-04T09:00:00Z"})
		run("meeting", entity, id, map[string]any{"MeetingID": "m-4", "Summary": "trimmed differs", "OccurredAt": "2025-07-05T09:00:00Z"})
		run("timeline", entity, id, nil)
	}
	// The same meeting id for a different entity is a different interaction.
	run("meeting", "person", "p", map[string]any{"MeetingID": "m-1", "Summary": "other person", "OccurredAt": "2025-07-01T09:00:00Z"})
	run("meeting", "person", "missing", map[string]any{"MeetingID": "m-1", "Summary": "ghost", "OccurredAt": "2025-07-01T09:00:00Z"})
	run("meeting", "organization", "missing", map[string]any{"MeetingID": "m-9", "Summary": "ghost", "OccurredAt": "2025-07-01T09:00:00Z"})

	queries := []string{"SELECT * FROM interactions ORDER BY id", "SELECT * FROM follow_ups ORDER BY id"}
	snapshots := []any{}
	for _, q := range queries {
		rows, err := db.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		snapshot := [][]any{}
		for rows.Next() {
			values := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range values {
				ptrs[i] = &values[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			snapshot = append(snapshot, values)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		_ = rows.Close()
		snapshots = append(snapshots, snapshot)
	}
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../../../../.."))
	hashes := map[string]string{}
	for _, rel := range []string{"internal/contacts/internal/service/relationship/timeline.go", "internal/contacts/internal/service/relationship/meetings.go", "internal/contacts/internal/service/relationship/interactions.go", "internal/contacts/internal/domain/relationship/types.go"} {
		b, err := os.ReadFile(filepath.Join(root, rel)) //nolint:gosec // fixed repository paths
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(b)
		hashes[rel] = hex.EncodeToString(sum[:])
	}
	fixture := map[string]any{"oracle": fixtureoracle.Current(), "source_hashes": hashes, "timestamp": stamp, "ids": entropy.ids, "seed": seed, "actions": actions, "results": results, "queries": queries, "snapshots": snapshots}
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join(root, "testdata/port/contacts/timeline-meeting.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	} else {
		expected, err := os.ReadFile(path) //nolint:gosec // fixed fixture path
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(expected, data) {
			t.Fatal("contacts timeline/meeting fixture drift")
		}
	}
	fmt.Printf("contacts timeline/meeting: %d actions, %d snapshots\n", len(actions), len(snapshots))
}

func str(input map[string]any, key string) string {
	s, _ := input[key].(string)
	return s
}
