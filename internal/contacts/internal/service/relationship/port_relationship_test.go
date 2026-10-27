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

	domain "github.com/danieljustus/symaira-desktop/internal/contacts/internal/domain/relationship"
	"github.com/danieljustus/symaira-desktop/internal/contacts/internal/errs"
	"github.com/danieljustus/symaira-desktop/internal/contacts/internal/storage/sqlite"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
	"github.com/google/uuid"
)

type portRelationshipRand struct {
	n   byte
	ids []string
}

func (r *portRelationshipRand) Read(p []byte) (int, error) {
	r.n++
	for i := range p {
		p[i] = 0
	}
	p[len(p)-1] = r.n
	u := append([]byte(nil), p...)
	u[6] = 64
	u[8] = 128
	id, e := uuid.FromBytes(u)
	if e != nil {
		return 0, e
	}
	r.ids = append(r.ids, id.String())
	return len(p), nil
}

type portRelationshipAction struct {
	Op     string         `json:"op"`
	Entity string         `json:"entity"`
	ID     string         `json:"id"`
	Input  map[string]any `json:"input"`
}

func TestPortContactsRelationshipContract(t *testing.T) {
	ctx := context.Background()
	db, e := sqlite.OpenMemory(ctx)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := db.Close(); e != nil {
			t.Error(e)
		}
	})
	const stamp = "2026-01-02T03:04:05Z"
	seed := `INSERT INTO persons(id,display_name,created_at,updated_at) VALUES('p','Ada','2026-01-02T03:04:05Z','2026-01-02T03:04:05Z'),('q','Bob','2026-01-02T03:04:05Z','2026-01-02T03:04:05Z'); INSERT INTO organizations(id,name,created_at,updated_at) VALUES('o','Example','2026-01-02T03:04:05Z','2026-01-02T03:04:05Z');`
	if _, e := db.Exec(seed); e != nil {
		t.Fatal(e)
	}
	entropy := &portRelationshipRand{}
	uuid.SetRand(entropy)
	t.Cleanup(func() { uuid.SetRand(nil) })
	svc := New(db)
	actions := []portRelationshipAction{}
	results := []any{}
	refs := map[string]string{}
	var normalize func(any) any
	normalize = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			for k, v := range x {
				if (k == "CreatedAt" || k == "UpdatedAt" || k == "CompletedAt" || k == "CancelledAt") && v != nil {
					x[k] = stamp
				} else {
					x[k] = normalize(v)
				}
			}
		case []any:
			for i := range x {
				x[i] = normalize(x[i])
			}
		}
		return v
	}
	run := func(op, entity, key string, input map[string]any) {
		actions = append(actions, portRelationshipAction{op, entity, key, input})
		id := key
		if v, ok := refs[key]; ok {
			id = v
		}
		raw, e := json.Marshal(input)
		if e != nil {
			t.Fatal(e)
		}
		var out any
		var err error
		person := entity == "person"
		switch op {
		case "relationship":
			var in domain.RelationshipInput
			_ = json.Unmarshal(raw, &in)
			out, err = svc.AddRelationship(ctx, in)
		case "outgoing":
			out, err = svc.ListOutgoingFromPerson(ctx, id)
		case "incoming_person":
			out, err = svc.ListIncomingToPerson(ctx, id)
		case "incoming_org":
			out, err = svc.ListIncomingToOrganization(ctx, id)
		case "followup":
			var in domain.FollowUpInput
			_ = json.Unmarshal(raw, &in)
			if person {
				out, err = svc.AddPersonFollowUp(ctx, id, in)
			} else {
				out, err = svc.AddOrganizationFollowUp(ctx, id, in)
			}
		case "complete":
			out, err = svc.CompleteFollowUp(ctx, id)
		case "cancel":
			out, err = svc.CancelFollowUp(ctx, id)
		case "followups":
			v, _ := input["filter"].(string)
			if person {
				out, err = svc.ListPersonFollowUps(ctx, id, FollowUpFilter(v))
			} else {
				out, err = svc.ListOrganizationFollowUps(ctx, id, FollowUpFilter(v))
			}
		case "interaction":
			var in domain.InteractionInput
			_ = json.Unmarshal(raw, &in)
			if person {
				out, err = svc.AddPersonInteraction(ctx, id, in)
			} else {
				out, err = svc.AddOrganizationInteraction(ctx, id, in)
			}
		case "interactions":
			if person {
				out, err = svc.ListPersonInteractions(ctx, id)
			} else {
				out, err = svc.ListOrganizationInteractions(ctx, id)
			}
		case "last":
			if person {
				out, err = svc.LastPersonInteraction(ctx, id)
			} else {
				out, err = svc.LastOrganizationInteraction(ctx, id)
			}
		default:
			t.Fatal(op)
		}
		b, e := json.Marshal(out)
		if e != nil {
			t.Fatal(e)
		}
		var v any
		if e := json.Unmarshal(b, &v); e != nil {
			t.Fatal(e)
		}
		v = normalize(v)
		if err == nil {
			if obj, ok := v.(map[string]any); ok {
				if id, ok := obj["ID"].(string); ok {
					if ref, ok := input["ref"].(string); ok {
						refs[ref] = id
					}
				}
			}
		}
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		results = append(results, map[string]any{"value": v, "error": msg, "kind": errs.KindOf(err)})
		for _, table := range []string{"relationships", "interactions", "follow_ups"} {
			//nolint:gosec // SQL identifiers come from the fixed fixture table list or its SQLite schema, never user input.
			if _, e := db.Exec("UPDATE "+table+" SET created_at=?", stamp); e != nil {
				t.Fatal(e)
			}
		}
		for _, table := range []string{"relationships", "follow_ups"} {
			//nolint:gosec // SQL identifiers come from the fixed fixture table list or its SQLite schema, never user input.
			if _, e := db.Exec("UPDATE "+table+" SET updated_at=?", stamp); e != nil {
				t.Fatal(e)
			}
		}
		for _, col := range []string{"completed_at", "cancelled_at"} {
			//nolint:gosec // SQL identifiers come from the fixed fixture table list or its SQLite schema, never user input.
			if _, e := db.Exec("UPDATE follow_ups SET "+col+"=? WHERE "+col+" IS NOT NULL", stamp); e != nil {
				t.Fatal(e)
			}
		}
	}
	for _, in := range []map[string]any{
		{}, {"FromPersonID": " \u2003", "Type": "friend", "ToPersonID": "q"}, {"FromPersonID": "p", "Type": " \n", "ToPersonID": "q"}, {"FromPersonID": "p", "Type": "friend"}, {"FromPersonID": "p", "Type": "friend", "ToPersonID": "q", "ToOrganizationID": "o"}, {"FromPersonID": "p", "Type": "friend", "ToPersonID": "p"}, {"FromPersonID": "p", "Type": "friend", "ToPersonID": "missing"}, {"FromPersonID": "missing", "Type": "friend", "ToOrganizationID": "o"},
		{"FromPersonID": "p", "Type": "friend", "ToPersonID": "q", "Notes": "original"}, {"FromPersonID": "p", "Type": "friend", "ToPersonID": "q", "Notes": "ignored retry"},
		{"FromPersonID": "p", "Type": "employee", "ToOrganizationID": "o", "ValidFrom": "2025-02-03T04:05:06Z", "ValidTo": "2025-01-01T00:00:00Z"},
		{"FromPersonID": "p", "Type": "employee", "ToOrganizationID": "o", "ValidFrom": "2025-02-03T04:05:06Z", "ValidTo": "2027-01-01T00:00:00Z"},
		{"FromPersonID": "p", "Type": "employee", "ToOrganizationID": "o", "ValidFrom": "2025-02-03T04:05:06Z", "Notes": "ignored retry"},
		{"FromPersonID": "p", "Type": "employee", "ToOrganizationID": "o", "ValidFrom": "2024-02-03T04:05:06.123456789Z"},
		{"FromPersonID": "q", "Type": "friend", "ToPersonID": "p"},
	} {
		run("relationship", "person", "", in)
	}
	run("outgoing", "person", "p", nil)
	run("incoming_person", "person", "q", nil)
	run("incoming_org", "organization", "o", nil)
	run("outgoing", "person", "missing", nil)
	for _, entity := range []string{"person", "organization"} {
		id := "p"
		if entity == "organization" {
			id = "o"
		}
		run("followups", entity, id, nil)
		run("last", entity, id, nil)
		run("interactions", entity, id, nil)
		run("followup", entity, id, nil)
		run("followup", entity, "missing", map[string]any{"DueAt": "2000-01-01T00:00:00Z"})
		for n, due := range []string{"2000-01-01T00:00:00Z", "9000-01-01T00:00:00Z", "2000-01-01T00:00:00.1Z"} {
			run("followup", entity, id, map[string]any{"DueAt": due, "Notes": "reviewed", "ref": fmt.Sprintf("%s-f%d", entity, n)})
		}
		for _, filter := range []string{"", "all", "open", "overdue", "upcoming", "invalid"} {
			run("followups", entity, id, map[string]any{"filter": filter})
		}
		run("complete", entity, entity+"-f0", nil)
		run("complete", entity, entity+"-f0", nil)
		run("cancel", entity, entity+"-f0", nil)
		run("cancel", entity, entity+"-f1", nil)
		run("cancel", entity, entity+"-f1", nil)
		run("complete", entity, entity+"-f1", nil)
		run("complete", entity, "missing", nil)
		run("cancel", entity, "missing", nil)
		for _, filter := range []string{"all", "open", "overdue", "upcoming"} {
			run("followups", entity, id, map[string]any{"filter": filter})
		}
		for _, in := range []map[string]any{{}, {"Kind": "unknown", "Summary": "x"}, {"Kind": "note", "Summary": " \u2003"}, {"Kind": "external_reference", "Summary": "x", "ExternalRef": " 	"}} {
			run("interaction", entity, id, in)
		}
		for n, kind := range []string{"note", "call", "email", "meeting", "external_reference"} {
			run("interaction", entity, id, map[string]any{"Kind": kind, "Summary": "private note", "ExternalRef": fmt.Sprintf("opaque-%d", n), "OccurredAt": "2025-02-03T04:05:06.123456789Z"})
		}
		run("interaction", entity, "missing", map[string]any{"Kind": "note", "Summary": "x", "OccurredAt": "2025-02-03T04:05:06Z"})
		run("interactions", entity, id, nil)
		run("last", entity, id, nil)
	}
	queries := []string{"SELECT * FROM relationships ORDER BY id", "SELECT * FROM follow_ups ORDER BY id", "SELECT * FROM interactions ORDER BY id"}
	snapshots := []any{}
	for _, q := range queries {
		rows, e := db.Query(q)
		if e != nil {
			t.Fatal(e)
		}
		cols, e := rows.Columns()
		if e != nil {
			t.Fatal(e)
		}
		snapshot := [][]any{}
		for rows.Next() {
			v := make([]any, len(cols))
			p := make([]any, len(cols))
			for i := range v {
				p[i] = &v[i]
			}
			if e := rows.Scan(p...); e != nil {
				t.Fatal(e)
			}
			snapshot = append(snapshot, v)
		}
		if e := rows.Err(); e != nil {
			t.Fatal(e)
		}
		if e := rows.Close(); e != nil {
			t.Fatal(e)
		}
		snapshots = append(snapshots, snapshot)
	}
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../../../../.."))
	hashes := map[string]string{}
	for _, rel := range []string{"internal/contacts/internal/service/relationship/relationships.go", "internal/contacts/internal/service/relationship/followups.go", "internal/contacts/internal/service/relationship/interactions.go", "internal/contacts/internal/service/relationship/service.go", "internal/contacts/internal/domain/relationship/types.go", "internal/contacts/internal/errs/errors.go"} {
		//nolint:gosec // path is derived from repository fixtures or the test-owned temporary root, not external input.
		b, e := os.ReadFile(filepath.Join(root, rel))
		if e != nil {
			t.Fatal(e)
		}
		h := sha256.Sum256(b)
		hashes[rel] = hex.EncodeToString(h[:])
	}
	fixture := map[string]any{"oracle": fixtureoracle.Current(), "source_hashes": hashes, "timestamp": stamp, "ids": entropy.ids, "seed": seed, "actions": actions, "results": results, "queries": queries, "snapshots": snapshots}
	data, e := json.MarshalIndent(fixture, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	data = append(data, '\n')
	path := filepath.Join(root, "testdata/port/contacts/relationship.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if e := os.WriteFile(path, data, 0o600); e != nil {
			t.Fatal(e)
		}
	} else {
		expected, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		if !bytes.Equal(expected, data) {
			t.Fatal("contacts relationship fixture drift")
		}
	}
	fmt.Printf("contacts relationships: %d actions, %d snapshots\n", len(actions), len(snapshots))
}
