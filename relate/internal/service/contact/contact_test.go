package contact

import (
	"context"
	"testing"

	"github.com/danieljustus/symaira-relate/internal/domain/contact"
	"github.com/danieljustus/symaira-relate/internal/domain/page"
	"github.com/danieljustus/symaira-relate/internal/errs"
	"github.com/danieljustus/symaira-relate/internal/storage/sqlite"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	db, err := sqlite.OpenMemory(context.Background())
	if err != nil {
		t.Fatalf("OpenMemory() error = %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return New(db)
}

func TestCreatePerson_UnicodeNameRoundTrips(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	p, err := s.CreatePerson(ctx, contact.PersonInput{DisplayName: "山田 太郎", Notes: "café ☕"})
	if err != nil {
		t.Fatalf("CreatePerson() error = %v", err)
	}

	got, err := s.GetPerson(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetPerson() error = %v", err)
	}
	if got.DisplayName != "山田 太郎" {
		t.Errorf("DisplayName = %q, want 山田 太郎", got.DisplayName)
	}
	if got.Notes != "café ☕" {
		t.Errorf("Notes = %q, want café ☕", got.Notes)
	}
}

func TestCreatePerson_RequiresDisplayName(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	_, err := s.CreatePerson(ctx, contact.PersonInput{DisplayName: "  "})
	if errs.KindOf(err) != errs.KindInvalid {
		t.Fatalf("CreatePerson() error kind = %v, want invalid (err=%v)", errs.KindOf(err), err)
	}
}

func TestPerson_ManyTypedContactPointsAndAliases(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	p, err := s.CreatePerson(ctx, contact.PersonInput{DisplayName: "Ada Lovelace"})
	if err != nil {
		t.Fatalf("CreatePerson() error = %v", err)
	}

	if _, err := s.AddPersonContactPoint(ctx, p.ID, contact.ContactPointInput{Kind: contact.ContactPointEmail, RawValue: "Ada@Example.com", IsPreferred: true}); err != nil {
		t.Fatalf("AddPersonContactPoint(email) error = %v", err)
	}
	if _, err := s.AddPersonContactPoint(ctx, p.ID, contact.ContactPointInput{Kind: contact.ContactPointPhone, RawValue: "+1 (555) 123-4567"}); err != nil {
		t.Fatalf("AddPersonContactPoint(phone) error = %v", err)
	}
	if err := s.AddPersonAlias(ctx, p.ID, "Countess of Lovelace"); err != nil {
		t.Fatalf("AddPersonAlias() error = %v", err)
	}
	if err := s.AddPersonAlias(ctx, p.ID, "AAL"); err != nil {
		t.Fatalf("AddPersonAlias() error = %v", err)
	}

	got, err := s.GetPerson(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetPerson() error = %v", err)
	}
	if len(got.ContactPoints) != 2 {
		t.Fatalf("len(ContactPoints) = %d, want 2", len(got.ContactPoints))
	}
	if len(got.Aliases) != 2 {
		t.Fatalf("len(Aliases) = %d, want 2", len(got.Aliases))
	}
	if got.ContactPoints[0].NormalizedValue != "ada@example.com" {
		t.Errorf("normalized email = %q, want ada@example.com", got.ContactPoints[0].NormalizedValue)
	}
}

func TestContactPoint_DuplicateOnSameEntityRejected(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	p, err := s.CreatePerson(ctx, contact.PersonInput{DisplayName: "Grace Hopper"})
	if err != nil {
		t.Fatalf("CreatePerson() error = %v", err)
	}

	in := contact.ContactPointInput{Kind: contact.ContactPointEmail, RawValue: "grace@example.com"}
	if _, err := s.AddPersonContactPoint(ctx, p.ID, in); err != nil {
		t.Fatalf("first AddPersonContactPoint() error = %v", err)
	}
	// Same normalized value, different casing/whitespace in raw_value —
	// must still be rejected as a duplicate on this entity.
	dup := contact.ContactPointInput{Kind: contact.ContactPointEmail, RawValue: " Grace@Example.com "}
	_, err = s.AddPersonContactPoint(ctx, p.ID, dup)
	if errs.KindOf(err) != errs.KindConflict {
		t.Fatalf("duplicate AddPersonContactPoint() error kind = %v, want conflict (err=%v)", errs.KindOf(err), err)
	}
}

func TestContactPoint_SameValueAllowedAcrossDifferentPersons(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	p1, _ := s.CreatePerson(ctx, contact.PersonInput{DisplayName: "Person One"})
	p2, _ := s.CreatePerson(ctx, contact.PersonInput{DisplayName: "Person Two"})

	in := contact.ContactPointInput{Kind: contact.ContactPointEmail, RawValue: "shared@example.com"}
	if _, err := s.AddPersonContactPoint(ctx, p1.ID, in); err != nil {
		t.Fatalf("AddPersonContactPoint(p1) error = %v", err)
	}
	if _, err := s.AddPersonContactPoint(ctx, p2.ID, in); err != nil {
		t.Fatalf("AddPersonContactPoint(p2) error = %v (cross-entity duplicates must be allowed)", err)
	}
}

func TestPerson_MultipleOrganizationsOverTimeWithRolesAndDates(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	p, _ := s.CreatePerson(ctx, contact.PersonInput{DisplayName: "Multi Org Person"})
	org1, _ := s.CreateOrganization(ctx, contact.OrganizationInput{Name: "Acme"})
	org2, _ := s.CreateOrganization(ctx, contact.OrganizationInput{Name: "Globex"})

	if _, err := s.AddMembership(ctx, p.ID, org1.ID, contact.MembershipInput{Role: "employee", Title: "Engineer"}); err != nil {
		t.Fatalf("AddMembership(org1) error = %v", err)
	}
	if _, err := s.AddMembership(ctx, p.ID, org2.ID, contact.MembershipInput{Role: "advisor", Title: "Board Member"}); err != nil {
		t.Fatalf("AddMembership(org2) error = %v", err)
	}

	memberships, err := s.ListMembershipsByPerson(ctx, p.ID)
	if err != nil {
		t.Fatalf("ListMembershipsByPerson() error = %v", err)
	}
	if len(memberships) != 2 {
		t.Fatalf("len(memberships) = %d, want 2", len(memberships))
	}
}

func TestMembership_ForeignEntityRejected(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	_, err := s.AddMembership(ctx, "does-not-exist", "also-missing", contact.MembershipInput{})
	if errs.KindOf(err) != errs.KindInvalid {
		t.Fatalf("AddMembership() with missing refs error kind = %v, want invalid (err=%v)", errs.KindOf(err), err)
	}
}

func TestClassification_SupportsPersonalAndBusinessOnSameContact(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	p, _ := s.CreatePerson(ctx, contact.PersonInput{DisplayName: "Dual Role"})
	if err := s.SetPersonClassification(ctx, p.ID, contact.ClassificationPersonal); err != nil {
		t.Fatalf("SetPersonClassification(personal) error = %v", err)
	}
	if err := s.SetPersonClassification(ctx, p.ID, contact.ClassificationCustomer); err != nil {
		t.Fatalf("SetPersonClassification(customer) error = %v", err)
	}

	got, err := s.GetPerson(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetPerson() error = %v", err)
	}
	if len(got.Classifications) != 2 {
		t.Fatalf("len(Classifications) = %d, want 2 (%v)", len(got.Classifications), got.Classifications)
	}
}

func TestClassification_RejectsUnknownValue(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	p, _ := s.CreatePerson(ctx, contact.PersonInput{DisplayName: "Bad Classification"})
	err := s.SetPersonClassification(ctx, p.ID, contact.Classification("bogus"))
	if errs.KindOf(err) != errs.KindInvalid {
		t.Fatalf("SetPersonClassification() error kind = %v, want invalid (err=%v)", errs.KindOf(err), err)
	}
}

func TestUpdatePerson_PreservesTimestampsAndPatchesOnlyGivenFields(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	p, err := s.CreatePerson(ctx, contact.PersonInput{DisplayName: "Original Name", GivenName: "Orig"})
	if err != nil {
		t.Fatalf("CreatePerson() error = %v", err)
	}
	createdAt := p.CreatedAt

	newName := "Updated Name"
	updated, err := s.UpdatePerson(ctx, p.ID, contact.PersonUpdate{DisplayName: &newName})
	if err != nil {
		t.Fatalf("UpdatePerson() error = %v", err)
	}
	if updated.DisplayName != "Updated Name" {
		t.Errorf("DisplayName = %q, want Updated Name", updated.DisplayName)
	}
	if updated.GivenName != "Orig" {
		t.Errorf("GivenName = %q, want Orig (unset field must be preserved)", updated.GivenName)
	}
	if !updated.CreatedAt.Equal(createdAt) {
		t.Errorf("CreatedAt changed: got %v, want %v", updated.CreatedAt, createdAt)
	}
	if !updated.UpdatedAt.After(createdAt) && !updated.UpdatedAt.Equal(createdAt) {
		t.Errorf("UpdatedAt = %v, want >= CreatedAt %v", updated.UpdatedAt, createdAt)
	}
}

func TestDeletePerson_CascadesChildRows(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	p, _ := s.CreatePerson(ctx, contact.PersonInput{DisplayName: "To Delete"})
	if _, err := s.AddPersonContactPoint(ctx, p.ID, contact.ContactPointInput{Kind: contact.ContactPointEmail, RawValue: "x@example.com"}); err != nil {
		t.Fatalf("AddPersonContactPoint() error = %v", err)
	}
	if err := s.AddPersonAlias(ctx, p.ID, "alias"); err != nil {
		t.Fatalf("AddPersonAlias() error = %v", err)
	}

	if err := s.DeletePerson(ctx, p.ID); err != nil {
		t.Fatalf("DeletePerson() error = %v", err)
	}

	if _, err := s.GetPerson(ctx, p.ID); errs.KindOf(err) != errs.KindNotFound {
		t.Fatalf("GetPerson() after delete error kind = %v, want not_found", errs.KindOf(err))
	}

	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM contact_points WHERE person_id = ?", p.ID).Scan(&count); err != nil {
		t.Fatalf("query contact_points error = %v", err)
	}
	if count != 0 {
		t.Errorf("contact_points not cascaded, count = %d", count)
	}
}

func TestDeletePerson_NotFound(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	err := s.DeletePerson(ctx, "missing")
	if errs.KindOf(err) != errs.KindNotFound {
		t.Fatalf("DeletePerson() error kind = %v, want not_found", errs.KindOf(err))
	}
}

func TestListPersons_PaginationIsDeterministicAndHasMore(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	names := []string{"Alice", "Bob", "Carol", "Dave", "Erin"}
	for _, n := range names {
		if _, err := s.CreatePerson(ctx, contact.PersonInput{DisplayName: n}); err != nil {
			t.Fatalf("CreatePerson(%s) error = %v", n, err)
		}
	}

	page1, err := s.ListPersons(ctx, ListPersonsOptions{Page: page.Request{Limit: 2, Offset: 0}})
	if err != nil {
		t.Fatalf("ListPersons() page1 error = %v", err)
	}
	if len(page1.Items) != 2 || !page1.HasMore {
		t.Fatalf("page1 = %+v, want 2 items with HasMore=true", page1)
	}
	if page1.Items[0].DisplayName != "Alice" || page1.Items[1].DisplayName != "Bob" {
		t.Errorf("page1 order = [%s, %s], want [Alice, Bob]", page1.Items[0].DisplayName, page1.Items[1].DisplayName)
	}

	page3, err := s.ListPersons(ctx, ListPersonsOptions{Page: page.Request{Limit: 2, Offset: 4}})
	if err != nil {
		t.Fatalf("ListPersons() page3 error = %v", err)
	}
	if len(page3.Items) != 1 || page3.HasMore {
		t.Fatalf("page3 = %+v, want 1 item with HasMore=false", page3)
	}
}

func TestListPersons_FiltersByClassification(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	p1, _ := s.CreatePerson(ctx, contact.PersonInput{DisplayName: "Business Contact"})
	_, _ = s.CreatePerson(ctx, contact.PersonInput{DisplayName: "Personal Contact"})
	if err := s.SetPersonClassification(ctx, p1.ID, contact.ClassificationBusiness); err != nil {
		t.Fatalf("SetPersonClassification() error = %v", err)
	}

	result, err := s.ListPersons(ctx, ListPersonsOptions{Classification: contact.ClassificationBusiness})
	if err != nil {
		t.Fatalf("ListPersons() error = %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].ID != p1.ID {
		t.Fatalf("filtered result = %+v, want exactly %s", result.Items, p1.ID)
	}
}

func TestOrganization_CRUDAndCascadeDelete(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	o, err := s.CreateOrganization(ctx, contact.OrganizationInput{Name: "Test Org"})
	if err != nil {
		t.Fatalf("CreateOrganization() error = %v", err)
	}
	if _, err := s.AddOrganizationContactPoint(ctx, o.ID, contact.ContactPointInput{Kind: contact.ContactPointURL, RawValue: "https://example.com/"}); err != nil {
		t.Fatalf("AddOrganizationContactPoint() error = %v", err)
	}

	newName := "Renamed Org"
	updated, err := s.UpdateOrganization(ctx, o.ID, contact.OrganizationUpdate{Name: &newName})
	if err != nil {
		t.Fatalf("UpdateOrganization() error = %v", err)
	}
	if updated.Name != "Renamed Org" {
		t.Errorf("Name = %q, want Renamed Org", updated.Name)
	}

	if err := s.DeleteOrganization(ctx, o.ID); err != nil {
		t.Fatalf("DeleteOrganization() error = %v", err)
	}
	if _, err := s.GetOrganization(ctx, o.ID); errs.KindOf(err) != errs.KindNotFound {
		t.Fatalf("GetOrganization() after delete error kind = %v, want not_found", errs.KindOf(err))
	}
}

func TestEndMembership_SetsValidTo(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	p, _ := s.CreatePerson(ctx, contact.PersonInput{DisplayName: "Ends Membership"})
	o, _ := s.CreateOrganization(ctx, contact.OrganizationInput{Name: "Ending Org"})
	m, err := s.AddMembership(ctx, p.ID, o.ID, contact.MembershipInput{Role: "member"})
	if err != nil {
		t.Fatalf("AddMembership() error = %v", err)
	}

	ended, err := s.EndMembership(ctx, m.ID, now())
	if err != nil {
		t.Fatalf("EndMembership() error = %v", err)
	}
	if ended.ValidTo == nil {
		t.Fatal("ValidTo is nil after EndMembership()")
	}
}
