package contact

import (
	"context"
	"testing"
	"time"

	"github.com/danieljustus/symaira-relate/internal/domain/contact"
	"github.com/danieljustus/symaira-relate/internal/errs"
)

// TestPersonTag_AddIdempotentAndRemove covers AddPersonTag: adding the
// same tag twice must stay one row (INSERT OR IGNORE), and removing the
// tag link must leave the person tag-free. The service exposes no
// remove-tag method yet (only classification and contact-point removal
// exist), so the link row is deleted directly; a second delete is an
// idempotent no-op affecting zero rows.
func TestPersonTag_AddIdempotentAndRemove(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	p, err := s.CreatePerson(ctx, contact.PersonInput{DisplayName: "Tagged Person"})
	if err != nil {
		t.Fatalf("CreatePerson() error = %v", err)
	}

	if err := s.AddPersonTag(ctx, p.ID, "vip"); err != nil {
		t.Fatalf("AddPersonTag() error = %v", err)
	}
	// Re-adding the same tag must not create a duplicate link row.
	if err := s.AddPersonTag(ctx, p.ID, "vip"); err != nil {
		t.Fatalf("AddPersonTag(second time) error = %v", err)
	}

	got, err := s.GetPerson(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetPerson() error = %v", err)
	}
	if len(got.Tags) != 1 || got.Tags[0] != "vip" {
		t.Fatalf("Tags = %v, want exactly [vip]", got.Tags)
	}

	res, err := s.db.ExecContext(ctx, "DELETE FROM entity_tags WHERE person_id = ?", p.ID)
	if err != nil {
		t.Fatalf("delete entity_tags error = %v", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("RowsAffected = %d, want 1", n)
	}
	got, err = s.GetPerson(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetPerson() after remove error = %v", err)
	}
	if len(got.Tags) != 0 {
		t.Errorf("Tags after remove = %v, want empty", got.Tags)
	}

	res, err = s.db.ExecContext(ctx, "DELETE FROM entity_tags WHERE person_id = ?", p.ID)
	if err != nil {
		t.Fatalf("second delete entity_tags error = %v", err)
	}
	if n, _ := res.RowsAffected(); n != 0 {
		t.Errorf("second delete RowsAffected = %d, want 0 (idempotent)", n)
	}
}

// TestPersonClassification_RemoveAndSecondRemoveNoop covers
// RemovePersonClassification: after removal the classification is gone
// from the read path, and removing it again (or one never set) is an
// idempotent no-op — the DELETE simply matches no row.
func TestPersonClassification_RemoveAndSecondRemoveNoop(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	p, err := s.CreatePerson(ctx, contact.PersonInput{DisplayName: "Classified Person"})
	if err != nil {
		t.Fatalf("CreatePerson() error = %v", err)
	}
	if err := s.SetPersonClassification(ctx, p.ID, contact.ClassificationBusiness); err != nil {
		t.Fatalf("SetPersonClassification() error = %v", err)
	}

	got, err := s.GetPerson(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetPerson() error = %v", err)
	}
	if len(got.Classifications) != 1 || got.Classifications[0] != contact.ClassificationBusiness {
		t.Fatalf("Classifications = %v, want exactly [business]", got.Classifications)
	}

	// First remove clears the row.
	if err := s.RemovePersonClassification(ctx, p.ID, contact.ClassificationBusiness); err != nil {
		t.Fatalf("RemovePersonClassification() error = %v", err)
	}
	got, err = s.GetPerson(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetPerson() after remove error = %v", err)
	}
	if len(got.Classifications) != 0 {
		t.Fatalf("Classifications after remove = %v, want empty", got.Classifications)
	}

	// Second remove and removal of a classification never set are both
	// no-ops matching no row.
	if err := s.RemovePersonClassification(ctx, p.ID, contact.ClassificationBusiness); err != nil {
		t.Fatalf("second RemovePersonClassification() error = %v", err)
	}
	if err := s.RemovePersonClassification(ctx, p.ID, contact.ClassificationPersonal); err != nil {
		t.Fatalf("RemovePersonClassification(unset) error = %v", err)
	}

	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM entity_classifications WHERE person_id = ?", p.ID).Scan(&count); err != nil {
		t.Fatalf("query entity_classifications error = %v", err)
	}
	if count != 0 {
		t.Errorf("entity_classifications count = %d, want 0", count)
	}
}

// TestPersonContactPoint_RemoveAndSecondRemoveNotFound covers
// RemovePersonContactPoint: the first remove clears the row, and a
// second remove — or a remove of a contact point owned by another
// person — matches no row and returns the documented not-found error.
func TestPersonContactPoint_RemoveAndSecondRemoveNotFound(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	p, err := s.CreatePerson(ctx, contact.PersonInput{DisplayName: "Point Person"})
	if err != nil {
		t.Fatalf("CreatePerson() error = %v", err)
	}
	other, err := s.CreatePerson(ctx, contact.PersonInput{DisplayName: "Other Person"})
	if err != nil {
		t.Fatalf("CreatePerson(other) error = %v", err)
	}
	cp, err := s.AddPersonContactPoint(ctx, p.ID, contact.ContactPointInput{Kind: contact.ContactPointEmail, RawValue: "remove@example.com"})
	if err != nil {
		t.Fatalf("AddPersonContactPoint() error = %v", err)
	}

	// Removing through the wrong owner must not remove the row and
	// must report not-found (the DELETE is scoped to person_id).
	if err := s.RemovePersonContactPoint(ctx, other.ID, cp.ID); errs.KindOf(err) != errs.KindNotFound {
		t.Fatalf("RemovePersonContactPoint(wrong owner) error kind = %v, want not_found (err=%v)", errs.KindOf(err), err)
	}

	// First remove clears the row.
	if err := s.RemovePersonContactPoint(ctx, p.ID, cp.ID); err != nil {
		t.Fatalf("RemovePersonContactPoint() error = %v", err)
	}
	got, err := s.GetPerson(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetPerson() after remove error = %v", err)
	}
	if len(got.ContactPoints) != 0 {
		t.Fatalf("ContactPoints after remove = %+v, want empty", got.ContactPoints)
	}

	// Second remove matches no row: documented not-found behavior.
	if err := s.RemovePersonContactPoint(ctx, p.ID, cp.ID); errs.KindOf(err) != errs.KindNotFound {
		t.Fatalf("second RemovePersonContactPoint() error kind = %v, want not_found (err=%v)", errs.KindOf(err), err)
	}
}

// TestMembership_CheckConstraintViolationRejected exercises the
// isCheckConstraintErr classifier: a membership whose valid_to precedes
// valid_from violates the schema CHECK constraint and must surface as an
// invalid-argument error, not an internal one.
func TestMembership_CheckConstraintViolationRejected(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	p, err := s.CreatePerson(ctx, contact.PersonInput{DisplayName: "Bad Range Person"})
	if err != nil {
		t.Fatalf("CreatePerson() error = %v", err)
	}
	o, err := s.CreateOrganization(ctx, contact.OrganizationInput{Name: "Bad Range Org"})
	if err != nil {
		t.Fatalf("CreateOrganization() error = %v", err)
	}

	from := now().Add(-time.Hour)
	to := now().Add(-2 * time.Hour) // before valid_from -> violates CHECK
	_, err = s.AddMembership(ctx, p.ID, o.ID, contact.MembershipInput{Role: "member", ValidFrom: &from, ValidTo: &to})
	if errs.KindOf(err) != errs.KindInvalid {
		t.Fatalf("AddMembership(valid_to < valid_from) error kind = %v, want invalid (err=%v)", errs.KindOf(err), err)
	}
}
