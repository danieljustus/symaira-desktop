package contact

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-relate/internal/domain/contact"
	"github.com/danieljustus/symaira-relate/internal/errs"
)

// The tests in this file are the contract guard for the contact-reference
// shape documented in docs/integrations/CONTACT_REF.md: a Ref must never
// carry contact points, notes, aliases, or filesystem paths, no matter how
// fully populated the underlying contact is. They pin the exact top-level
// key set so any additive change is a deliberate, review-visible decision
// (additive-only within api_version v1, see docs/CLI_CONTRACT.md).

// seedFullyPopulatedPerson creates a person with notes and one contact
// point of every kind, returning the person ID plus every private string a
// Ref must never leak.
func seedFullyPopulatedPerson(t *testing.T, ctx context.Context, s *Service) (id string, private []string) {
	t.Helper()
	p, err := s.CreatePerson(ctx, contact.PersonInput{
		DisplayName: "Ada Lovelace",
		Notes:       "met at the private-countess-salon",
	})
	if err != nil {
		t.Fatalf("CreatePerson() error = %v", err)
	}
	points := []contact.ContactPointInput{
		{Kind: contact.ContactPointEmail, RawValue: "Ada@Private-Example.com"},
		{Kind: contact.ContactPointPhone, RawValue: "+1 (555) 987-6543"},
		{Kind: contact.ContactPointAddress, RawValue: "12 Secret Lane, Ockham Park"},
		{Kind: contact.ContactPointURL, RawValue: "https://ada.private-example.net/blog"},
		{Kind: contact.ContactPointHandle, RawValue: "@countess-lovelace"},
	}
	for _, in := range points {
		if _, err := s.AddPersonContactPoint(ctx, p.ID, in); err != nil {
			t.Fatalf("AddPersonContactPoint(%s) error = %v", in.Kind, err)
		}
	}
	if err := s.AddPersonAlias(ctx, p.ID, "alias-countess-of-lovelace"); err != nil {
		t.Fatalf("AddPersonAlias() error = %v", err)
	}
	private = []string{
		"private-countess-salon",
		"Ada@Private-Example.com", "ada@private-example.com",
		"987-6543", "9876543",
		"Secret Lane", "Ockham",
		"ada.private-example.net",
		"countess-lovelace",
	}
	return p.ID, private
}

func seedFullyPopulatedOrganization(t *testing.T, ctx context.Context, s *Service) (id string, private []string) {
	t.Helper()
	o, err := s.CreateOrganization(ctx, contact.OrganizationInput{
		Name:  "Analytical Engines Ltd",
		Notes: "confidential-board-notes",
	})
	if err != nil {
		t.Fatalf("CreateOrganization() error = %v", err)
	}
	points := []contact.ContactPointInput{
		{Kind: contact.ContactPointEmail, RawValue: "Office@Corp-Example.com"},
		{Kind: contact.ContactPointPhone, RawValue: "+44 20 7946 0018"},
		{Kind: contact.ContactPointAddress, RawValue: "1 Hidden Works, London"},
		{Kind: contact.ContactPointURL, RawValue: "https://corp.private-example.net"},
		{Kind: contact.ContactPointHandle, RawValue: "@analytical-engines"},
	}
	for _, in := range points {
		if _, err := s.AddOrganizationContactPoint(ctx, o.ID, in); err != nil {
			t.Fatalf("AddOrganizationContactPoint(%s) error = %v", in.Kind, err)
		}
	}
	private = []string{
		"confidential-board-notes",
		"Office@Corp-Example.com", "office@corp-example.com",
		"7946", "Hidden Works",
		"corp.private-example.net",
		"analytical-engines",
	}
	return o.ID, private
}

func assertRefJSONContract(t *testing.T, ref *contact.Ref, wantID string, wantKind contact.RefKind, wantName string, private []string) {
	t.Helper()

	if ref.Provider != contact.RefProvider {
		t.Errorf("Provider = %q, want %q", ref.Provider, contact.RefProvider)
	}
	if ref.SchemaVersion != contact.RefSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", ref.SchemaVersion, contact.RefSchemaVersion)
	}
	if ref.ID != wantID {
		t.Errorf("ID = %q, want %q", ref.ID, wantID)
	}
	if ref.Kind != wantKind {
		t.Errorf("Kind = %q, want %q", ref.Kind, wantKind)
	}
	if ref.DisplayName != wantName {
		t.Errorf("DisplayName = %q, want %q", ref.DisplayName, wantName)
	}

	raw, err := json.Marshal(ref)
	if err != nil {
		t.Fatalf("json.Marshal(Ref) error = %v", err)
	}

	// Exact top-level key set, all snake_case: the additive-only contract
	// guard. Adding a field must fail here first.
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("Ref JSON invalid: %v (%s)", err, raw)
	}
	wantKeys := map[string]bool{
		"provider": true, "schema_version": true, "id": true, "kind": true, "display_name": true,
	}
	if len(obj) != len(wantKeys) {
		t.Errorf("Ref JSON keys = %v, want exactly %v", keysOf(obj), keysOf(wantKeys))
	}
	for k := range obj {
		if !wantKeys[k] {
			t.Errorf("Ref JSON has unexpected key %q (contract is additive-only; extend deliberately)", k)
		}
		if k != strings.ToLower(k) || strings.Contains(k, " ") {
			t.Errorf("Ref JSON key %q is not snake_case", k)
		}
	}

	// The privacy guard: nothing private may appear anywhere in the payload.
	out := string(raw)
	for _, leak := range private {
		if strings.Contains(out, leak) {
			t.Errorf("Ref JSON leaks private data %q: %s", leak, out)
		}
	}
	for _, forbidden := range []string{"notes", "contact_point", "email", "phone", "address", "alias", t.TempDir()} {
		if strings.Contains(out, forbidden) {
			t.Errorf("Ref JSON contains forbidden fragment %q: %s", forbidden, out)
		}
	}
}

func keysOf[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func TestGetRef_PersonShapeContainsNoPrivateData(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	id, private := seedFullyPopulatedPerson(t, ctx, s)

	ref, err := s.GetRef(ctx, id)
	if err != nil {
		t.Fatalf("GetRef() error = %v", err)
	}
	assertRefJSONContract(t, ref, id, contact.RefKindPerson, "Ada Lovelace", private)
}

func TestGetRef_OrganizationShapeContainsNoPrivateData(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	id, private := seedFullyPopulatedOrganization(t, ctx, s)

	ref, err := s.GetRef(ctx, id)
	if err != nil {
		t.Fatalf("GetRef() error = %v", err)
	}
	assertRefJSONContract(t, ref, id, contact.RefKindOrganization, "Analytical Engines Ltd", private)
}

func TestGetRef_UnknownID_NotFound(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	_, err := s.GetRef(ctx, "does-not-exist")
	if errs.KindOf(err) != errs.KindNotFound {
		t.Fatalf("GetRef() error kind = %v, want not_found (err=%v)", errs.KindOf(err), err)
	}
	// The message must not disclose which tables were probed or echo input.
	if strings.Contains(err.Error(), "person") || strings.Contains(err.Error(), "organization") {
		t.Errorf("not-found message leaks probe detail: %v", err)
	}
}

func TestGetRef_DeletedContact_NotFound(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	id, _ := seedFullyPopulatedPerson(t, ctx, s)
	if err := s.DeletePerson(ctx, id); err != nil {
		t.Fatalf("DeletePerson() error = %v", err)
	}

	_, err := s.GetRef(ctx, id)
	if errs.KindOf(err) != errs.KindNotFound {
		t.Fatalf("GetRef() after delete error kind = %v, want not_found (err=%v)", errs.KindOf(err), err)
	}
}

func TestGetRef_PersonAndOrganizationIDsAreDistinct(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	personID, _ := seedFullyPopulatedPerson(t, ctx, s)
	orgID, _ := seedFullyPopulatedOrganization(t, ctx, s)

	pref, err := s.GetRef(ctx, personID)
	if err != nil {
		t.Fatalf("GetRef(person) error = %v", err)
	}
	oref, err := s.GetRef(ctx, orgID)
	if err != nil {
		t.Fatalf("GetRef(organization) error = %v", err)
	}
	if pref.Kind != contact.RefKindPerson || oref.Kind != contact.RefKindOrganization {
		t.Errorf("kinds = %q/%q, want person/organization", pref.Kind, oref.Kind)
	}
	if pref.ID == oref.ID {
		t.Errorf("person and organization share ID %q — references must be unambiguous", pref.ID)
	}
}
