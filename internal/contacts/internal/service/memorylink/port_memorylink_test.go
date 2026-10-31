package memorylink

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

	domain "github.com/danieljustus/symaira-desktop/internal/contacts/internal/domain/memorylink"
	"github.com/danieljustus/symaira-desktop/internal/contacts/internal/errs"
	"github.com/danieljustus/symaira-desktop/internal/contacts/internal/storage/sqlite"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
	"github.com/google/uuid"
)

// CONTACT-002: reviewed contact -> SymMemory entity links (reference-only
// storage, one link per contact, idempotent unlink) against the real Go
// service.
//
// Regenerate with:
// PORT_GENERATE=1 go test ./internal/contacts/internal/service/memorylink -run '^TestPortContactsMemoryLinkContract$'

type portLinkRand struct {
	n   byte
	ids []string
}

func (r *portLinkRand) Read(p []byte) (int, error) {
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

type portLinkAction struct {
	Op    string         `json:"op"`
	ID    string         `json:"id"`
	Input map[string]any `json:"input"`
}

func TestPortContactsMemoryLinkContract(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	const stamp = "2026-01-02T03:04:05Z"
	seed := `INSERT INTO persons(id,display_name,created_at,updated_at) VALUES('p','Ada','2026-01-02T03:04:05Z','2026-01-02T03:04:05Z'),('q','Bob','2026-01-02T03:04:05Z','2026-01-02T03:04:05Z'),('r','Cy','2026-01-02T03:04:05Z','2026-01-02T03:04:05Z'); INSERT INTO organizations(id,name,created_at,updated_at) VALUES('o','Example','2026-01-02T03:04:05Z','2026-01-02T03:04:05Z'),('o2','Other','2026-01-02T03:04:05Z','2026-01-02T03:04:05Z');`
	if _, err := db.Exec(seed); err != nil {
		t.Fatal(err)
	}
	entropy := &portLinkRand{}
	uuid.SetRand(entropy)
	t.Cleanup(func() { uuid.SetRand(nil) })
	svc := New(db)
	actions := []portLinkAction{}
	results := []any{}

	var normalize func(any) any
	normalize = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			for k, item := range x {
				if k == "CreatedAt" {
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
	run := func(op, id string, input map[string]any) {
		actions = append(actions, portLinkAction{op, id, input})
		in := domain.LinkInput{MemoryEntityID: str(input, "MemoryEntityID"), MemoryEntityType: str(input, "MemoryEntityType"), LinkedBy: str(input, "LinkedBy")}
		var out any
		var callErr error
		switch op {
		case "link_person":
			out, callErr = svc.LinkPerson(ctx, id, in)
		case "link_org":
			out, callErr = svc.LinkOrganization(ctx, id, in)
		case "get_person":
			out, callErr = svc.GetPersonLink(ctx, id)
		case "get_org":
			out, callErr = svc.GetOrganizationLink(ctx, id)
		case "unlink_person":
			callErr = svc.UnlinkPerson(ctx, id)
		case "unlink_org":
			callErr = svc.UnlinkOrganization(ctx, id)
		case "delete_person":
			_, callErr = db.Exec("DELETE FROM persons WHERE id = ?", id)
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
		msg := ""
		if callErr != nil {
			msg = callErr.Error()
		}
		results = append(results, map[string]any{"value": value, "error": msg, "kind": errs.KindOf(callErr)})
		if _, err := db.Exec("UPDATE memory_links SET created_at=?", stamp); err != nil {
			t.Fatal(err)
		}
	}

	valid := map[string]any{"MemoryEntityID": "ent-1", "MemoryEntityType": "person", "LinkedBy": "reviewer"}
	run("get_person", "p", nil)
	run("get_org", "o", nil)
	run("link_person", "p", map[string]any{})
	run("link_person", "p", map[string]any{"MemoryEntityID": "  ", "LinkedBy": "x"})
	run("link_person", "p", map[string]any{"MemoryEntityID": "ent-1", "LinkedBy": " \t"})
	run("link_person", "p", valid)
	run("link_person", "p", map[string]any{"MemoryEntityID": "ent-2", "LinkedBy": "reviewer"})
	run("link_person", "q", map[string]any{"MemoryEntityID": "ent-1", "LinkedBy": "reviewer"})
	run("link_person", "missing", valid)
	run("link_org", "o", map[string]any{"MemoryEntityID": "ent-9", "MemoryEntityType": "organization", "LinkedBy": "reviewer"})
	run("link_org", "o", valid)
	run("link_org", "missing", valid)
	run("link_org", "o2", map[string]any{"MemoryEntityID": " padded ", "LinkedBy": " by "})
	run("get_person", "p", nil)
	run("get_person", "q", nil)
	run("get_person", "r", nil)
	run("get_org", "o", nil)
	run("get_org", "o2", nil)
	run("get_org", "p", nil)
	run("unlink_person", "p", nil)
	run("unlink_person", "p", nil)
	run("unlink_person", "missing", nil)
	run("get_person", "p", nil)
	run("link_person", "p", map[string]any{"MemoryEntityID": "ent-2", "MemoryEntityType": "project", "LinkedBy": "reviewer"})
	run("get_person", "p", nil)
	run("delete_person", "q", nil)
	run("get_person", "q", nil)
	run("unlink_org", "o", nil)
	run("get_org", "o", nil)
	run("unlink_org", "o", nil)

	queries := []string{"SELECT * FROM memory_links ORDER BY id"}
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
	for _, rel := range []string{"internal/contacts/internal/service/memorylink/service.go", "internal/contacts/internal/domain/memorylink/types.go"} {
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
	path := filepath.Join(root, "testdata/port/contacts/memorylink.json")
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
			t.Fatal("contacts memory-link fixture drift")
		}
	}
	fmt.Printf("contacts memory links: %d actions, %d snapshots\n", len(actions), len(snapshots))
}

func str(input map[string]any, key string) string {
	s, _ := input[key].(string)
	return s
}
