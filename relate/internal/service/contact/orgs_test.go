package contact

import (
	"context"
	"testing"

	"github.com/danieljustus/symaira-relate/internal/domain/contact"
	"github.com/danieljustus/symaira-relate/internal/domain/page"
	"github.com/danieljustus/symaira-relate/internal/errs"
)

func TestListOrganizations_QueryAndClassificationFilters(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	acme, _ := s.CreateOrganization(ctx, contact.OrganizationInput{Name: "Acme Corp", Notes: "widgets"})
	globex, _ := s.CreateOrganization(ctx, contact.OrganizationInput{Name: "Globex"})
	_, _ = s.CreateOrganization(ctx, contact.OrganizationInput{Name: "Initech"})

	if err := s.SetOrganizationClassification(ctx, acme.ID, contact.ClassificationCustomer); err != nil {
		t.Fatalf("SetOrganizationClassification() error = %v", err)
	}
	if err := s.SetOrganizationClassification(ctx, globex.ID, contact.ClassificationPartner); err != nil {
		t.Fatalf("SetOrganizationClassification(globex) error = %v", err)
	}

	// Free-text query matches name substring, case-insensitive.
	res, err := s.ListOrganizations(ctx, ListOrganizationsOptions{Query: "glob"})
	if err != nil {
		t.Fatalf("ListOrganizations(query) error = %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].ID != globex.ID {
		t.Fatalf("ListOrganizations(query) = %+v, want only Globex", res.Items)
	}

	// Classification filter.
	res, err = s.ListOrganizations(ctx, ListOrganizationsOptions{Classification: contact.ClassificationCustomer})
	if err != nil {
		t.Fatalf("ListOrganizations(classification) error = %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].ID != acme.ID {
		t.Fatalf("ListOrganizations(classification) = %+v, want only Acme", res.Items)
	}

	// Combined query + classification.
	res, err = s.ListOrganizations(ctx, ListOrganizationsOptions{Query: "acme", Classification: contact.ClassificationCustomer})
	if err != nil {
		t.Fatalf("ListOrganizations(combined) error = %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].ID != acme.ID {
		t.Fatalf("ListOrganizations(combined) = %+v, want only Acme", res.Items)
	}
}

func TestListOrganizations_Pagination(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	for i := 0; i < 5; i++ {
		if _, err := s.CreateOrganization(ctx, contact.OrganizationInput{Name: "Org " + string(rune('A'+i))}); err != nil {
			t.Fatalf("CreateOrganization() error = %v", err)
		}
	}

	res, err := s.ListOrganizations(ctx, ListOrganizationsOptions{Page: page.Request{Limit: 2, Offset: 0}})
	if err != nil {
		t.Fatalf("ListOrganizations(page1) error = %v", err)
	}
	if len(res.Items) != 2 || !res.HasMore {
		t.Fatalf("page1 = %d items, HasMore=%v; want 2 items and HasMore", len(res.Items), res.HasMore)
	}

	res2, err := s.ListOrganizations(ctx, ListOrganizationsOptions{Page: page.Request{Limit: 2, Offset: 2}})
	if err != nil {
		t.Fatalf("ListOrganizations(page2) error = %v", err)
	}
	if len(res2.Items) != 2 || !res2.HasMore {
		t.Fatalf("page2 = %d items, HasMore=%v; want 2 items and HasMore", len(res2.Items), res2.HasMore)
	}
	if res.Items[0].ID == res2.Items[0].ID {
		t.Fatalf("pages overlap: page1[0]=%s == page2[0]=%s", res.Items[0].ID, res2.Items[0].ID)
	}

	res3, err := s.ListOrganizations(ctx, ListOrganizationsOptions{Page: page.Request{Limit: 2, Offset: 4}})
	if err != nil {
		t.Fatalf("ListOrganizations(page3) error = %v", err)
	}
	if len(res3.Items) != 1 || res3.HasMore {
		t.Fatalf("page3 = %d items, HasMore=%v; want 1 item and no HasMore", len(res3.Items), res3.HasMore)
	}
}

func TestOrganization_AliasTagClassificationContactPointRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	o, err := s.CreateOrganization(ctx, contact.OrganizationInput{Name: "Acme Corp"})
	if err != nil {
		t.Fatalf("CreateOrganization() error = %v", err)
	}

	if err := s.AddOrganizationAlias(ctx, o.ID, "ACME"); err != nil {
		t.Fatalf("AddOrganizationAlias() error = %v", err)
	}
	if err := s.AddOrganizationTag(ctx, o.ID, "vendor"); err != nil {
		t.Fatalf("AddOrganizationTag() error = %v", err)
	}
	if err := s.SetOrganizationClassification(ctx, o.ID, contact.ClassificationBusiness); err != nil {
		t.Fatalf("SetOrganizationClassification() error = %v", err)
	}
	cp, err := s.AddOrganizationContactPoint(ctx, o.ID, contact.ContactPointInput{Kind: contact.ContactPointEmail, RawValue: "Info@Acme.example"})
	if err != nil {
		t.Fatalf("AddOrganizationContactPoint() error = %v", err)
	}

	got, err := s.GetOrganization(ctx, o.ID)
	if err != nil {
		t.Fatalf("GetOrganization() error = %v", err)
	}
	if len(got.Aliases) != 1 || got.Aliases[0] != "ACME" {
		t.Errorf("Aliases = %v, want [ACME]", got.Aliases)
	}
	if len(got.Tags) != 1 || got.Tags[0] != "vendor" {
		t.Errorf("Tags = %v, want [vendor]", got.Tags)
	}
	if len(got.Classifications) != 1 || got.Classifications[0] != contact.ClassificationBusiness {
		t.Errorf("Classifications = %v, want [business]", got.Classifications)
	}
	if len(got.ContactPoints) != 1 || got.ContactPoints[0].NormalizedValue != "info@acme.example" {
		t.Errorf("ContactPoints = %+v, want 1 normalized info@acme.example", got.ContactPoints)
	}

	// Removal paths: classification, then contact point, alias and tag
	// round-trip to empty state.
	if err := s.RemoveOrganizationClassification(ctx, o.ID, contact.ClassificationBusiness); err != nil {
		t.Fatalf("RemoveOrganizationClassification() error = %v", err)
	}
	if err := s.RemoveOrganizationContactPoint(ctx, o.ID, cp.ID); err != nil {
		t.Fatalf("RemoveOrganizationContactPoint() error = %v", err)
	}
	got, err = s.GetOrganization(ctx, o.ID)
	if err != nil {
		t.Fatalf("GetOrganization() after removal error = %v", err)
	}
	if len(got.Classifications) != 0 || len(got.ContactPoints) != 0 {
		t.Errorf("after removal: Classifications=%v ContactPoints=%+v, want empty", got.Classifications, got.ContactPoints)
	}
}

func TestOrganizationAlias_RequiresValue(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	o, _ := s.CreateOrganization(ctx, contact.OrganizationInput{Name: "Acme"})
	err := s.AddOrganizationAlias(ctx, o.ID, "  ")
	if errs.KindOf(err) != errs.KindInvalid {
		t.Fatalf("AddOrganizationAlias(blank) error kind = %v, want invalid (err=%v)", errs.KindOf(err), err)
	}
}

func TestOrganizationTag_RequiresValue(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	o, _ := s.CreateOrganization(ctx, contact.OrganizationInput{Name: "Acme"})
	err := s.AddOrganizationTag(ctx, o.ID, "")
	if errs.KindOf(err) != errs.KindInvalid {
		t.Fatalf("AddOrganizationTag(blank) error kind = %v, want invalid (err=%v)", errs.KindOf(err), err)
	}
}

func TestOrganizationClassification_RejectsUnknownValue(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	o, _ := s.CreateOrganization(ctx, contact.OrganizationInput{Name: "Acme"})
	err := s.SetOrganizationClassification(ctx, o.ID, contact.Classification("bogus"))
	if errs.KindOf(err) != errs.KindInvalid {
		t.Fatalf("SetOrganizationClassification(bogus) error kind = %v, want invalid (err=%v)", errs.KindOf(err), err)
	}
}

func TestOrganizationContactPoint_DuplicateOnSameOrganizationRejected(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	o, _ := s.CreateOrganization(ctx, contact.OrganizationInput{Name: "Acme"})
	in := contact.ContactPointInput{Kind: contact.ContactPointEmail, RawValue: "acme@example.com"}
	if _, err := s.AddOrganizationContactPoint(ctx, o.ID, in); err != nil {
		t.Fatalf("first AddOrganizationContactPoint() error = %v", err)
	}
	dup := contact.ContactPointInput{Kind: contact.ContactPointEmail, RawValue: " ACME@Example.com "}
	_, err := s.AddOrganizationContactPoint(ctx, o.ID, dup)
	if errs.KindOf(err) != errs.KindConflict {
		t.Fatalf("duplicate AddOrganizationContactPoint() error kind = %v, want conflict (err=%v)", errs.KindOf(err), err)
	}
}

func TestOrganizationContactPoint_SameValueAllowedAcrossDifferentOrganizations(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	o1, _ := s.CreateOrganization(ctx, contact.OrganizationInput{Name: "Acme"})
	o2, _ := s.CreateOrganization(ctx, contact.OrganizationInput{Name: "Globex"})

	in := contact.ContactPointInput{Kind: contact.ContactPointEmail, RawValue: "shared@example.com"}
	if _, err := s.AddOrganizationContactPoint(ctx, o1.ID, in); err != nil {
		t.Fatalf("AddOrganizationContactPoint(o1) error = %v", err)
	}
	if _, err := s.AddOrganizationContactPoint(ctx, o2.ID, in); err != nil {
		t.Fatalf("AddOrganizationContactPoint(o2) error = %v (cross-entity duplicates must be allowed)", err)
	}
}

func TestOrganizationContactPoint_UnknownKindRejected(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	o, _ := s.CreateOrganization(ctx, contact.OrganizationInput{Name: "Acme"})
	_, err := s.AddOrganizationContactPoint(ctx, o.ID, contact.ContactPointInput{Kind: contact.ContactPointKind("bogus"), RawValue: "x"})
	if errs.KindOf(err) != errs.KindInvalid {
		t.Fatalf("AddOrganizationContactPoint(bogus kind) error kind = %v, want invalid (err=%v)", errs.KindOf(err), err)
	}
}

func TestListMembershipsByOrganization_ReturnsMembers(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	p1, _ := s.CreatePerson(ctx, contact.PersonInput{DisplayName: "Alice"})
	p2, _ := s.CreatePerson(ctx, contact.PersonInput{DisplayName: "Bob"})
	org, _ := s.CreateOrganization(ctx, contact.OrganizationInput{Name: "Acme"})

	if _, err := s.AddMembership(ctx, p1.ID, org.ID, contact.MembershipInput{Role: "employee", Title: "Engineer"}); err != nil {
		t.Fatalf("AddMembership(alice) error = %v", err)
	}
	if _, err := s.AddMembership(ctx, p2.ID, org.ID, contact.MembershipInput{Role: "advisor", Title: "Board Member"}); err != nil {
		t.Fatalf("AddMembership(bob) error = %v", err)
	}
	// A membership in another organization must not leak into this list.
	other, _ := s.CreateOrganization(ctx, contact.OrganizationInput{Name: "Globex"})
	if _, err := s.AddMembership(ctx, p1.ID, other.ID, contact.MembershipInput{Role: "consultant"}); err != nil {
		t.Fatalf("AddMembership(globex) error = %v", err)
	}

	memberships, err := s.ListMembershipsByOrganization(ctx, org.ID)
	if err != nil {
		t.Fatalf("ListMembershipsByOrganization() error = %v", err)
	}
	if len(memberships) != 2 {
		t.Fatalf("len(memberships) = %d, want 2", len(memberships))
	}
	seen := map[string]bool{}
	for _, m := range memberships {
		seen[m.PersonID] = true
		if m.OrganizationID != org.ID {
			t.Errorf("membership %+v points at wrong organization", m)
		}
	}
	if !seen[p1.ID] || !seen[p2.ID] {
		t.Errorf("memberships missing person: got %v, want both Alice and Bob", seen)
	}
}

func TestListMembershipsByOrganization_Empty(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	org, _ := s.CreateOrganization(ctx, contact.OrganizationInput{Name: "Acme"})
	memberships, err := s.ListMembershipsByOrganization(ctx, org.ID)
	if err != nil {
		t.Fatalf("ListMembershipsByOrganization() error = %v", err)
	}
	if len(memberships) != 0 {
		t.Fatalf("len(memberships) = %d, want 0", len(memberships))
	}
}

func TestDeleteOrganization_CascadesChildRows(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	o, _ := s.CreateOrganization(ctx, contact.OrganizationInput{Name: "Acme"})
	if _, err := s.AddOrganizationContactPoint(ctx, o.ID, contact.ContactPointInput{Kind: contact.ContactPointEmail, RawValue: "x@example.com"}); err != nil {
		t.Fatalf("AddOrganizationContactPoint() error = %v", err)
	}
	if err := s.AddOrganizationAlias(ctx, o.ID, "ACME"); err != nil {
		t.Fatalf("AddOrganizationAlias() error = %v", err)
	}

	if err := s.DeleteOrganization(ctx, o.ID); err != nil {
		t.Fatalf("DeleteOrganization() error = %v", err)
	}
	if _, err := s.GetOrganization(ctx, o.ID); errs.KindOf(err) != errs.KindNotFound {
		t.Fatalf("GetOrganization() after delete error kind = %v, want not_found", errs.KindOf(err))
	}

	for _, table := range []string{"contact_points", "aliases"} {
		var count int
		if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE organization_id = ?", o.ID).Scan(&count); err != nil {
			t.Fatalf("query %s error = %v", table, err)
		}
		if count != 0 {
			t.Errorf("table %s not cascaded, count = %d", table, count)
		}
	}
}

func TestOrganizationUpdate_PatchesOnlyGivenFields(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	o, err := s.CreateOrganization(ctx, contact.OrganizationInput{Name: "Original Name", Notes: "notes"})
	if err != nil {
		t.Fatalf("CreateOrganization() error = %v", err)
	}
	createdAt := o.CreatedAt

	newName := "Updated Name"
	updated, err := s.UpdateOrganization(ctx, o.ID, contact.OrganizationUpdate{Name: &newName})
	if err != nil {
		t.Fatalf("UpdateOrganization() error = %v", err)
	}
	if updated.Name != "Updated Name" {
		t.Errorf("Name = %q, want Updated Name", updated.Name)
	}
	if updated.Notes != "notes" {
		t.Errorf("Notes = %q, want notes (unset field must be preserved)", updated.Notes)
	}
	if !updated.CreatedAt.Equal(createdAt) {
		t.Errorf("CreatedAt changed: got %v, want %v", updated.CreatedAt, createdAt)
	}
}
