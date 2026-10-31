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
	"time"

	domain "github.com/danieljustus/symaira-desktop/internal/contacts/internal/domain/contact"
	"github.com/danieljustus/symaira-desktop/internal/contacts/internal/errs"
	"github.com/danieljustus/symaira-desktop/internal/contacts/internal/storage/sqlite"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
	"github.com/google/uuid"
)

// CONTACT-001: memberships, quick-add and the reference-only GetRef /
// FindRefsByName facade against the real Go service.
//
// Regenerate with:
// PORT_GENERATE=1 go test ./internal/contacts/internal/service/contact -run '^TestPortContactsMembershipRefContract$'

type portMembershipRand struct {
	n   byte
	ids []string
}

func (r *portMembershipRand) Read(p []byte) (int, error) {
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

type portMembershipAction struct {
	Op     string         `json:"op"`
	ID     string         `json:"id"`
	Second string         `json:"second,omitempty"`
	Input  map[string]any `json:"input"`
}

func TestPortContactsMembershipRefContract(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	entropy := &portMembershipRand{}
	uuid.SetRand(entropy)
	t.Cleanup(func() { uuid.SetRand(nil) })
	svc := New(db)
	const stamp = "2026-01-02T03:04:05Z"
	actions := []portMembershipAction{}
	results := []any{}
	refs := map[string]string{}

	var normalize func(any) any
	normalize = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			for k, item := range x {
				if k == "CreatedAt" || k == "UpdatedAt" {
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
	parseTimeField := func(input map[string]any, key string) *time.Time {
		s, ok := input[key].(string)
		if !ok || s == "" {
			return nil
		}
		tm, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			t.Fatal(err)
		}
		return &tm
	}
	resolve := func(key string) string {
		if v, ok := refs[key]; ok {
			return v
		}
		return key
	}
	run := func(op, id, second string, input map[string]any) {
		actions = append(actions, portMembershipAction{op, id, second, input})
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		var out any
		var callErr error
		switch op {
		case "create_person":
			var in domain.PersonInput
			_ = json.Unmarshal(raw, &in)
			out, callErr = svc.CreatePerson(ctx, in)
		case "create_org":
			var in domain.OrganizationInput
			_ = json.Unmarshal(raw, &in)
			out, callErr = svc.CreateOrganization(ctx, in)
		case "quickadd":
			var in domain.PersonInput
			_ = json.Unmarshal(raw, &in)
			email, _ := input["email"].(string)
			phone, _ := input["phone"].(string)
			out, callErr = svc.CreatePersonWithContactPoints(ctx, in, email, phone)
		case "get_person":
			out, callErr = svc.GetPerson(ctx, resolve(id))
		case "persons":
			out, callErr = svc.ListPersons(ctx, ListPersonsOptions{})
		case "membership":
			in := domain.MembershipInput{Role: str(input, "Role"), Title: str(input, "Title"), ValidFrom: parseTimeField(input, "ValidFrom"), ValidTo: parseTimeField(input, "ValidTo")}
			out, callErr = svc.AddMembership(ctx, resolve(id), resolve(second), in)
		case "end_membership":
			end := parseTimeField(input, "ValidTo")
			if end == nil {
				t.Fatal("end_membership needs ValidTo")
			}
			out, callErr = svc.EndMembership(ctx, resolve(id), *end)
		case "memberships_person":
			out, callErr = svc.ListMembershipsByPerson(ctx, resolve(id))
		case "memberships_org":
			out, callErr = svc.ListMembershipsByOrganization(ctx, resolve(id))
		case "get_ref":
			out, callErr = svc.GetRef(ctx, resolve(id))
		case "find_refs":
			out, callErr = svc.FindRefsByName(ctx, id)
		case "delete_person":
			callErr = svc.DeletePerson(ctx, resolve(id))
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
				if key, ok := obj["ID"].(string); ok {
					if ref, ok := input["ref"].(string); ok {
						refs[ref] = key
					}
				}
			}
		}
		msg := ""
		if callErr != nil {
			msg = callErr.Error()
		}
		results = append(results, map[string]any{"value": value, "error": msg, "kind": errs.KindOf(callErr)})
		for _, q := range []string{
			"UPDATE persons SET created_at=?, updated_at=?",
			"UPDATE organizations SET created_at=?, updated_at=?",
			"UPDATE organization_memberships SET created_at=?, updated_at=?",
			"UPDATE contact_points SET created_at=?, updated_at=?",
		} {
			if _, err := db.Exec(q, stamp, stamp); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.Exec("UPDATE aliases SET created_at=?", stamp); err != nil {
			t.Fatal(err)
		}
	}

	run("create_person", "", "", map[string]any{"DisplayName": "Ada Lovelace", "ref": "ada"})
	run("create_person", "", "", map[string]any{"DisplayName": "Bob Builder", "ref": "bob"})
	run("create_person", "", "", map[string]any{"DisplayName": "ada lovelace", "ref": "ada2"})
	run("create_person", "", "", map[string]any{"DisplayName": "Zoë Ünal", "ref": "zoe"})
	run("create_org", "", "", map[string]any{"Name": "Example Corp", "ref": "corp"})
	run("create_org", "", "", map[string]any{"Name": "ADA LOVELACE", "ref": "ada-org"})
	run("create_org", "", "", map[string]any{"Name": "Other Inc", "ref": "other"})

	for _, in := range []map[string]any{
		{},
		{"Role": "engineer"},
		{"Role": "engineer", "Title": "Staff", "ValidFrom": "2020-01-01T00:00:00Z"},
		{"Role": "advisor", "ValidFrom": "2021-06-01T12:30:00.123456789Z", "ValidTo": "2022-06-01T00:00:00Z"},
		{"Role": "bad", "ValidFrom": "2023-01-01T00:00:00Z", "ValidTo": "2022-01-01T00:00:00Z"},
		{"Role": "rejoin", "ValidFrom": "2020-01-01T00:00:00Z", "ref": "m-rejoin"},
		{"Role": "open", "ValidFrom": "2019-01-01T00:00:00Z", "ref": "m-open"},
		{"Role": "past", "ValidTo": "2018-01-01T00:00:00Z", "ref": "m-past"},
	} {
		run("membership", "ada", "corp", in)
	}
	run("membership", "missing", "corp", map[string]any{"Role": "x"})
	run("membership", "ada", "missing", map[string]any{"Role": "x"})
	run("membership", "bob", "corp", map[string]any{"Role": "intern", "ref": "m-bob"})
	run("membership", "ada", "other", map[string]any{"Role": "board", "ref": "m-other"})
	run("memberships_person", "ada", "", nil)
	run("memberships_person", "bob", "", nil)
	run("memberships_person", "zoe", "", nil)
	run("memberships_person", "missing", "", nil)
	run("memberships_org", "corp", "", nil)
	run("memberships_org", "other", "", nil)
	run("memberships_org", "ada-org", "", nil)
	run("end_membership", "m-open", "", map[string]any{"ValidTo": "2020-06-30T00:00:00Z"})
	run("end_membership", "m-open", "", map[string]any{"ValidTo": "2018-12-31T23:59:59Z"})
	run("end_membership", "m-past", "", map[string]any{"ValidTo": "2019-01-01T00:00:00Z"})
	run("end_membership", "m-bob", "", map[string]any{"ValidTo": "2030-01-01T00:00:00.5Z"})
	run("end_membership", "missing", "", map[string]any{"ValidTo": "2030-01-01T00:00:00Z"})
	run("memberships_person", "ada", "", nil)
	run("memberships_org", "corp", "", nil)

	run("quickadd", "", "", map[string]any{"DisplayName": "Quick Person", "ref": "quick"})
	run("quickadd", "", "", map[string]any{"DisplayName": "Quick Email", "email": "Quick@Example.COM", "ref": "quick-email"})
	run("quickadd", "", "", map[string]any{"DisplayName": "Quick Phone", "phone": "+49 (170) 123-4567", "ref": "quick-phone"})
	run("quickadd", "", "", map[string]any{"DisplayName": "Quick Both", "GivenName": "Quick", "FamilyName": "Both", "Notes": "n", "Source": "import:test", "SourceRef": "uid-1", "email": "both@example.com", "phone": "555 0100", "ref": "quick-both"})
	run("quickadd", "", "", map[string]any{"DisplayName": " \t ", "email": "x@example.com"})
	run("quickadd", "", "", map[string]any{"DisplayName": "Quick Same", "email": "same@example.com", "phone": "same@example.com", "ref": "quick-same"})
	run("persons", "", "", nil)
	run("get_person", "quick-both", "", nil)

	run("get_ref", "ada", "", nil)
	run("get_ref", "corp", "", nil)
	run("get_ref", "ada-org", "", nil)
	run("get_ref", "missing", "", nil)
	run("get_ref", "", "", nil)
	run("find_refs", "Ada Lovelace", "", nil)
	run("find_refs", "  ada lovelace  ", "", nil)
	run("find_refs", "ADA LOVELACE", "", nil)
	run("find_refs", "Zoë Ünal", "", nil)
	run("find_refs", "ZOË ÜNAL", "", nil)
	run("find_refs", "Ada", "", nil)
	run("find_refs", "", "", nil)
	run("find_refs", "   ", "", nil)
	run("find_refs", "Other Inc", "", nil)
	run("find_refs", "nobody", "", nil)
	run("delete_person", "ada", "", nil)
	run("get_ref", "ada", "", nil)
	run("memberships_org", "corp", "", nil)
	run("find_refs", "Ada Lovelace", "", nil)

	queries := []string{
		"SELECT * FROM persons ORDER BY id",
		"SELECT * FROM organizations ORDER BY id",
		"SELECT * FROM organization_memberships ORDER BY id",
		"SELECT * FROM contact_points ORDER BY id",
	}
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
	for _, rel := range []string{
		"internal/contacts/internal/service/contact/memberships.go", "internal/contacts/internal/service/contact/quickadd.go",
		"internal/contacts/internal/service/contact/reference.go", "internal/contacts/internal/domain/contact/reference.go",
		"internal/contacts/internal/service/contact/persons.go", "internal/contacts/internal/service/contact/childtables.go",
	} {
		b, err := os.ReadFile(filepath.Join(root, rel)) //nolint:gosec // fixed repository paths
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(b)
		hashes[rel] = hex.EncodeToString(sum[:])
	}
	fixture := map[string]any{"oracle": fixtureoracle.Current(), "source_hashes": hashes, "timestamp": stamp, "ids": entropy.ids, "actions": actions, "results": results, "queries": queries, "snapshots": snapshots}
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join(root, "testdata/port/contacts/membership-ref.json")
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
			t.Fatal("contacts membership/ref fixture drift")
		}
	}
	fmt.Printf("contacts membership/ref: %d actions, %d snapshots\n", len(actions), len(snapshots))
}

func str(input map[string]any, key string) string {
	s, _ := input[key].(string)
	return s
}
