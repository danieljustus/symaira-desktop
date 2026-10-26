package security

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

	"github.com/danieljustus/symaira-desktop/internal/contacts/internal/errs"
	"github.com/danieljustus/symaira-desktop/internal/contacts/internal/storage/sqlite"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
	"github.com/google/uuid"
)

func TestPortContactsEraseContract(t *testing.T) {
	ctx := context.Background()
	const stamp = "2026-01-02T03:04:05Z"
	seed := `INSERT INTO persons(id,display_name,notes) VALUES('p','PRIVATE_NAME','PRIVATE_NOTES'),('q','Other','Keep');
 INSERT INTO organizations(id,name) VALUES('o','PRIVATE_ORG');
 INSERT INTO contact_points(id,person_id,kind,raw_value,normalized_value) VALUES('cp','p','email','PRIVATE_EMAIL','private_email');
 INSERT INTO contact_points(id,organization_id,kind,raw_value,normalized_value) VALUES('co','o','phone','PRIVATE_PHONE','private_phone');
 INSERT INTO aliases(id,person_id,alias) VALUES('ap','p','PRIVATE_ALIAS'); INSERT INTO aliases(id,organization_id,alias) VALUES('ao','o','PRIVATE_ORG_ALIAS');
 INSERT INTO tags(id,name) VALUES('t','PRIVATE_TAG'); INSERT INTO entity_tags(id,tag_id,person_id) VALUES('tp','t','p'); INSERT INTO entity_tags(id,tag_id,organization_id) VALUES('to','t','o');
 INSERT INTO entity_classifications(id,person_id,classification) VALUES('ep','p','personal'); INSERT INTO entity_classifications(id,organization_id,classification) VALUES('eo','o','business');
 INSERT INTO organization_memberships(id,person_id,organization_id,role) VALUES('m','p','o','PRIVATE_ROLE');
 INSERT INTO relationships(id,from_person_id,to_person_id,relationship_type,notes) VALUES('rp','p','q','friend','PRIVATE_LINK'),('rq','q','p','friend','PRIVATE_LINK');
 INSERT INTO relationships(id,from_person_id,to_organization_id,relationship_type,notes) VALUES('ro','p','o','employee','PRIVATE_LINK');
 INSERT INTO interactions(id,person_id,kind,occurred_at,summary,external_ref) VALUES('ip','p','meeting','2025-01-01T00:00:00Z','PRIVATE_SUMMARY','opaque-meeting');
 INSERT INTO interactions(id,organization_id,kind,occurred_at,summary) VALUES('io','o','note','2025-01-01T00:00:00Z','PRIVATE_ORG_SUMMARY');
 INSERT INTO follow_ups(id,person_id,due_at,notes) VALUES('fp','p','2027-01-01T00:00:00Z','PRIVATE_FOLLOWUP');
 INSERT INTO follow_ups(id,organization_id,due_at,notes) VALUES('fo','o','2027-01-01T00:00:00Z','PRIVATE_FOLLOWUP');
 INSERT INTO memory_links(id,person_id,memory_entity_id,linked_by) VALUES('mp','p','opaque-person','manual');
 INSERT INTO memory_links(id,organization_id,memory_entity_id,linked_by) VALUES('mo','o','opaque-org','manual');
 INSERT INTO import_sources(id,source_kind,source_ref,person_id) VALUES('s','csv','opaque-source','p');`
	tables := []string{"persons", "organizations", "contact_points", "aliases", "tags", "entity_tags", "entity_classifications", "organization_memberships", "relationships", "interactions", "follow_ups", "memory_links", "import_sources", "audit_events"}
	queries := []string{}
	for _, table := range tables {
		queries = append(queries, "SELECT * FROM "+table+" ORDER BY id")
	}
	cases := []any{}
	for _, entity := range []string{"person", "organization"} {
		for _, mode := range []string{"erase", "missing", "audit_failure"} {
			db, e := sqlite.OpenMemory(ctx)
			if e != nil {
				t.Fatal(e)
			}
			if _, e := db.Exec(seed); e != nil {
				t.Fatal(e)
			}
			for _, table := range tables {
				rows, e := db.Query("PRAGMA table_info(" + table + ")")
				if e != nil {
					t.Fatal(e)
				}
				cols := []string{}
				for rows.Next() {
					var n, notnull, pk int
					var name, kind string
					var def any
					if e := rows.Scan(&n, &name, &kind, &notnull, &def, &pk); e != nil {
						t.Fatal(e)
					}
					if name == "created_at" || name == "updated_at" || name == "imported_at" {
						cols = append(cols, name)
					}
				}
				if e := rows.Err(); e != nil {
					t.Fatal(e)
				}
				if e := rows.Close(); e != nil {
					t.Fatal(e)
				}
				for _, col := range cols {
					if _, e := db.Exec("UPDATE "+table+" SET "+col+"=?", stamp); e != nil {
						t.Fatal(e)
					}
				}
			}
			setup := ""
			if mode == "audit_failure" {
				setup = "CREATE TRIGGER reject_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT, 'audit denied'); END;"
				if _, e := db.Exec(setup); e != nil {
					t.Fatal(e)
				}
			}
			// Inject UUID entropy; clock and OS username are explicitly normalized.
			uuid.SetRand(bytes.NewReader(make([]byte, 16)))
			id := "p"
			if entity == "organization" {
				id = "o"
			}
			if mode == "missing" {
				id = "missing"
			}
			svc := New(db)
			var out any
			var callErr error
			if entity == "person" {
				out, callErr = svc.EraseContact(ctx, id)
			} else {
				out, callErr = svc.EraseOrganization(ctx, id)
			}
			uuid.SetRand(nil)
			if _, e := db.Exec("UPDATE audit_events SET occurred_at=?,actor=?", stamp, "<actor>"); e != nil {
				t.Fatal(e)
			}
			audits, e := svc.ListAuditEvents(ctx)
			if e != nil {
				t.Fatal(e)
			}
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
			msg := ""
			if callErr != nil {
				msg = callErr.Error()
			}
			cases = append(cases, map[string]any{"entity": entity, "mode": mode, "id": id, "setup": setup, "value": out, "error": msg, "kind": errs.KindOf(callErr), "audits": audits, "snapshots": snapshots})
			if e := db.Close(); e != nil {
				t.Fatal(e)
			}
		}
	}
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../../../../.."))
	hashes := map[string]string{}
	for _, rel := range []string{"internal/contacts/internal/service/security/erase.go", "internal/contacts/internal/service/security/audit.go", "internal/contacts/internal/service/security/service.go", "internal/contacts/internal/domain/security/types.go", "internal/contacts/internal/errs/errors.go"} {
		b, e := os.ReadFile(filepath.Join(root, rel))
		if e != nil {
			t.Fatal(e)
		}
		h := sha256.Sum256(b)
		hashes[rel] = hex.EncodeToString(h[:])
	}
	fixture := map[string]any{"oracle": fixtureoracle.Current(), "source_hashes": hashes, "timestamp": stamp, "actor": "<actor>", "audit_id": "00000000-0000-4000-8000-000000000000", "seed": seed, "queries": queries, "cases": cases}
	data, e := json.MarshalIndent(fixture, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	data = append(data, '\n')
	path := filepath.Join(root, "testdata/port/contacts/erase.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if e := os.WriteFile(path, data, 0644); e != nil {
			t.Fatal(e)
		}
	} else {
		expected, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		if !bytes.Equal(expected, data) {
			t.Fatal("contacts erase fixture drift")
		}
	}
	fmt.Printf("contacts erase: %d cases, %d tables per case\n", len(cases), len(queries))
}
