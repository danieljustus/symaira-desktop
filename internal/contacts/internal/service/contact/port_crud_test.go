package contact

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

	domain "github.com/danieljustus/symaira-desktop/internal/contacts/internal/domain/contact"
	"github.com/danieljustus/symaira-desktop/internal/contacts/internal/domain/page"
	"github.com/danieljustus/symaira-desktop/internal/contacts/internal/errs"
	"github.com/danieljustus/symaira-desktop/internal/contacts/internal/storage/sqlite"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
	"github.com/google/uuid"
)

// UUID entropy is injected through google/uuid's supported test seam. The
// production clock has no seam: only CreatedAt/UpdatedAt and DB timestamp
// columns are normalized, explicitly, to the same instant after each call.
// No HOME-backed stores, OS credentials or external adapters are accessed.
type portCRUDRand struct {
	n   byte
	ids []string
}

func (r *portCRUDRand) Read(p []byte) (int, error) {
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

type portCRUDAction struct {
	Op     string         `json:"op"`
	Entity string         `json:"entity"`
	ID     string         `json:"id"`
	Input  map[string]any `json:"input"`
}

func TestPortContactsCRUDContract(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	entropy := &portCRUDRand{}
	uuid.SetRand(entropy)
	t.Cleanup(func() { uuid.SetRand(nil) })
	svc := New(db)
	actions := []portCRUDAction{}
	results := []any{}
	refs := map[string]string{}
	const stamp = "2026-01-02T03:04:05Z"
	var normalize func(any) any
	normalize = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			for k, v := range x {
				if k == "CreatedAt" || k == "UpdatedAt" {
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
	run := func(op, entity, id string, input map[string]any) {
		a := portCRUDAction{op, entity, id, input}
		actions = append(actions, a)
		target := id
		if v, ok := refs[id]; ok {
			target = v
		}
		raw, e := json.Marshal(input)
		if e != nil {
			t.Fatal(e)
		}
		var output any
		var callErr error
		person := entity == "person"
		switch op {
		case "create":
			if person {
				var in domain.PersonInput
				_ = json.Unmarshal(raw, &in)
				output, callErr = svc.CreatePerson(ctx, in)
			} else {
				var in domain.OrganizationInput
				_ = json.Unmarshal(raw, &in)
				output, callErr = svc.CreateOrganization(ctx, in)
			}
		case "get":
			if person {
				output, callErr = svc.GetPerson(ctx, target)
			} else {
				output, callErr = svc.GetOrganization(ctx, target)
			}
		case "update":
			if person {
				var in domain.PersonUpdate
				_ = json.Unmarshal(raw, &in)
				output, callErr = svc.UpdatePerson(ctx, target, in)
			} else {
				var in domain.OrganizationUpdate
				_ = json.Unmarshal(raw, &in)
				output, callErr = svc.UpdateOrganization(ctx, target, in)
			}
		case "delete":
			if person {
				callErr = svc.DeletePerson(ctx, target)
			} else {
				callErr = svc.DeleteOrganization(ctx, target)
			}
		case "list":
			var in struct {
				Query          string
				Classification domain.Classification
				Limit, Offset  int
			}
			_ = json.Unmarshal(raw, &in)
			if person {
				output, callErr = svc.ListPersons(ctx, ListPersonsOptions{Query: in.Query, Classification: in.Classification, Page: page.Request{Limit: in.Limit, Offset: in.Offset}})
			} else {
				output, callErr = svc.ListOrganizations(ctx, ListOrganizationsOptions{Query: in.Query, Classification: in.Classification, Page: page.Request{Limit: in.Limit, Offset: in.Offset}})
			}
		case "alias":
			v, _ := input["value"].(string)
			if person {
				callErr = svc.AddPersonAlias(ctx, target, v)
			} else {
				callErr = svc.AddOrganizationAlias(ctx, target, v)
			}
		case "tag":
			v, _ := input["value"].(string)
			if person {
				callErr = svc.AddPersonTag(ctx, target, v)
			} else {
				callErr = svc.AddOrganizationTag(ctx, target, v)
			}
		case "classify":
			v, _ := input["value"].(string)
			if person {
				callErr = svc.SetPersonClassification(ctx, target, domain.Classification(v))
			} else {
				callErr = svc.SetOrganizationClassification(ctx, target, domain.Classification(v))
			}
		case "unclassify":
			v, _ := input["value"].(string)
			if person {
				callErr = svc.RemovePersonClassification(ctx, target, domain.Classification(v))
			} else {
				callErr = svc.RemoveOrganizationClassification(ctx, target, domain.Classification(v))
			}
		case "point":
			var in domain.ContactPointInput
			_ = json.Unmarshal(raw, &in)
			if person {
				output, callErr = svc.AddPersonContactPoint(ctx, target, in)
			} else {
				output, callErr = svc.AddOrganizationContactPoint(ctx, target, in)
			}
		case "remove_point":
			v, _ := input["value"].(string)
			if actual, ok := refs[v]; ok {
				v = actual
			}
			if person {
				callErr = svc.RemovePersonContactPoint(ctx, target, v)
			} else {
				callErr = svc.RemoveOrganizationContactPoint(ctx, target, v)
			}
		default:
			t.Fatal(op)
		}
		data, e := json.Marshal(output)
		if e != nil {
			t.Fatal(e)
		}
		var value any
		if e = json.Unmarshal(data, &value); e != nil {
			t.Fatal(e)
		}
		value = normalize(value)
		if callErr == nil {
			if obj, ok := value.(map[string]any); ok {
				if key, ok := obj["ID"].(string); ok {
					if op == "create" {
						refs[id] = key
					}
					if op == "point" {
						refs["point-"+id] = key
					}
				}
			}
		}
		message := ""
		if callErr != nil {
			message = callErr.Error()
		}
		results = append(results, map[string]any{"value": value, "error": message, "kind": errs.KindOf(callErr)})
		for _, table := range []string{"persons", "organizations", "aliases", "entity_classifications", "contact_points"} {
			if _, e := db.Exec("UPDATE "+table+" SET created_at = ?", stamp); e != nil {
				t.Fatal(e)
			}
		}
		for _, table := range []string{"persons", "organizations", "contact_points"} {
			if _, e := db.Exec("UPDATE "+table+" SET updated_at = ?", stamp); e != nil {
				t.Fatal(e)
			}
		}
	}
	for _, entity := range []string{"person", "organization"} {
		name := "DisplayName"
		if entity == "organization" {
			name = "Name"
		}
		id := entity
		run("list", entity, "", nil)
		run("create", entity, "bad", map[string]any{name: " \t\u0085"})
		run("create", entity, id, map[string]any{name: " Ada_% ", "Notes": "private", "Source": "manual", "SourceRef": "source-1", "GivenName": "Ada", "FamilyName": "Lovelace"})
		run("create", entity, id+"-2", map[string]any{name: "bob"})
		run("create", entity, id+"-3", map[string]any{name: "BOB"})
		for _, op := range []string{"alias", "tag"} {
			run(op, entity, id, map[string]any{"value": " \u2003"})
			run(op, entity, id, map[string]any{"value": "zeta"})
			run(op, entity, id, map[string]any{"value": "alpha"})
			run(op, entity, id, map[string]any{"value": "zeta"})
		}
		run("classify", entity, id, map[string]any{"value": "unknown"})
		for _, c := range []string{"personal", "business", "customer", "lead", "partner", "business"} {
			run("classify", entity, id, map[string]any{"value": c})
		}
		run("unclassify", entity, id, map[string]any{"value": "unknown"})
		run("unclassify", entity, id, map[string]any{"value": "personal"})
		run("point", entity, id, map[string]any{"Kind": "unknown"})
		run("point", entity, id, map[string]any{"Kind": "email"})
		for _, pair := range [][2]string{{"email", " ADA@Example.com "}, {"phone", "+49 (123)"}, {"address", " Main Street "}, {"url", "HTTPS://Example.com/"}, {"handle", " @Ada "}} {
			run("point", entity, id, map[string]any{"Kind": pair[0], "RawValue": pair[1], "Label": "work", "IsPreferred": true, "IsVerified": true})
		}
		run("point", entity, id, map[string]any{"Kind": "email", "RawValue": "ada@example.com"})
		run("point", entity, "missing", map[string]any{"Kind": "email", "RawValue": "other@example.com"})
		run("alias", entity, "missing", map[string]any{"value": "orphan"})
		run("tag", entity, "missing", map[string]any{"value": "rolled-back"})
		run("classify", entity, "missing", map[string]any{"value": "lead"})
		run("get", entity, id, nil)
		run("remove_point", entity, id+"-2", map[string]any{"value": "point-" + id})
		run("remove_point", entity, id, map[string]any{"value": "point-" + id})
		run("remove_point", entity, id, map[string]any{"value": "point-" + id})
		for _, filter := range []map[string]any{nil, {"Limit": 1}, {"Limit": 1, "Offset": 1}, {"Limit": 999, "Offset": -2}, {"Query": "_%"}, {"Query": "  Ada  "}, {"Query": "\\"}, {"Classification": "business"}, {"Classification": "missing"}, {"Query": "bob", "Limit": 1}} {
			run("list", entity, "", filter)
		}
		run("update", entity, id, map[string]any{name: " \n"})
		run("update", entity, id, map[string]any{name: "Changed", "Notes": "", "GivenName": "", "FamilyName": "Byron"})
		run("update", entity, id, nil)
		run("get", entity, "missing", nil)
		run("update", entity, "missing", map[string]any{"Notes": "x"})
		run("delete", entity, "missing", nil)
		run("delete", entity, id, nil)
		run("get", entity, id, nil)
		run("delete", entity, id, nil)
		run("list", entity, "", nil)
	}
	queries := []string{}
	snapshots := []any{}
	for _, table := range []string{"persons", "organizations", "aliases", "tags", "entity_tags", "entity_classifications", "contact_points"} {
		q := "SELECT * FROM " + table + " ORDER BY id"
		queries = append(queries, q)
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
	for _, rel := range []string{"internal/contacts/internal/service/contact/persons.go", "internal/contacts/internal/service/contact/organizations.go", "internal/contacts/internal/service/contact/childtables.go", "internal/contacts/internal/service/contact/service.go", "internal/contacts/internal/domain/contact/types.go", "internal/contacts/internal/domain/page/page.go", "internal/contacts/internal/errs/errors.go"} {
		b, e := os.ReadFile(filepath.Join(root, rel))
		if e != nil {
			t.Fatal(e)
		}
		h := sha256.Sum256(b)
		hashes[rel] = hex.EncodeToString(h[:])
	}
	fixture := map[string]any{"oracle": fixtureoracle.Current(), "source_hashes": hashes, "timestamp": stamp, "ids": entropy.ids, "actions": actions, "results": results, "queries": queries, "snapshots": snapshots}
	data, e := json.MarshalIndent(fixture, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	data = append(data, '\n')
	path := filepath.Join(root, "testdata/port/contacts/crud.json")
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
			t.Fatal("contacts CRUD fixture drift")
		}
	}
	fmt.Printf("contacts CRUD: %d actions, %d snapshots\n", len(actions), len(snapshots))
}
