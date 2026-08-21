package contact

import (
	"context"
	"database/sql"
	"testing"

	"github.com/danieljustus/symaira-relate/internal/domain/contact"
	"github.com/danieljustus/symaira-relate/internal/errs"
	"github.com/danieljustus/symaira-relate/internal/storage/sqlite"
)

func TestCreatePersonWithContactPoints_Success(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	p, err := s.CreatePersonWithContactPoints(ctx, contact.PersonInput{DisplayName: "Ada Lovelace"}, "Ada@Example.com", "+1 (555) 123-4567")
	if err != nil {
		t.Fatalf("CreatePersonWithContactPoints() error = %v", err)
	}
	if p.DisplayName != "Ada Lovelace" {
		t.Errorf("DisplayName = %q, want Ada Lovelace", p.DisplayName)
	}
	if len(p.ContactPoints) != 2 {
		t.Fatalf("len(ContactPoints) = %d, want 2", len(p.ContactPoints))
	}
	got := map[contact.ContactPointKind]string{}
	for _, cp := range p.ContactPoints {
		got[cp.Kind] = cp.NormalizedValue
	}
	if got[contact.ContactPointEmail] != "ada@example.com" {
		t.Errorf("email normalized = %q, want ada@example.com", got[contact.ContactPointEmail])
	}
	if got[contact.ContactPointPhone] == "" {
		t.Errorf("phone contact point missing")
	}

	// The person must be durable and reloadable through the normal read path.
	reloaded, err := s.GetPerson(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetPerson() error = %v", err)
	}
	if len(reloaded.ContactPoints) != 2 {
		t.Errorf("reloaded len(ContactPoints) = %d, want 2", len(reloaded.ContactPoints))
	}
}

func TestCreatePersonWithContactPoints_OptionalPoints(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	// Email only.
	p1, err := s.CreatePersonWithContactPoints(ctx, contact.PersonInput{DisplayName: "Email Only"}, "e@example.com", "")
	if err != nil {
		t.Fatalf("email-only error = %v", err)
	}
	if len(p1.ContactPoints) != 1 || p1.ContactPoints[0].Kind != contact.ContactPointEmail {
		t.Errorf("email-only ContactPoints = %+v, want 1 email", p1.ContactPoints)
	}

	// Phone only.
	p2, err := s.CreatePersonWithContactPoints(ctx, contact.PersonInput{DisplayName: "Phone Only"}, "", "+49 30 123456")
	if err != nil {
		t.Fatalf("phone-only error = %v", err)
	}
	if len(p2.ContactPoints) != 1 || p2.ContactPoints[0].Kind != contact.ContactPointPhone {
		t.Errorf("phone-only ContactPoints = %+v, want 1 phone", p2.ContactPoints)
	}

	// Neither — plain person create.
	p3, err := s.CreatePersonWithContactPoints(ctx, contact.PersonInput{DisplayName: "No Points"}, "", "")
	if err != nil {
		t.Fatalf("no-points error = %v", err)
	}
	if len(p3.ContactPoints) != 0 {
		t.Errorf("no-points ContactPoints = %+v, want none", p3.ContactPoints)
	}
}

func TestCreatePersonWithContactPoints_RequiresDisplayName(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	_, err := s.CreatePersonWithContactPoints(ctx, contact.PersonInput{DisplayName: "  "}, "e@example.com", "")
	if errs.KindOf(err) != errs.KindInvalid {
		t.Fatalf("error kind = %v, want invalid (err=%v)", errs.KindOf(err), err)
	}

	// No phantom person may exist after the failed call.
	res, err := s.ListPersons(ctx, ListPersonsOptions{})
	if err != nil {
		t.Fatalf("ListPersons() error = %v", err)
	}
	if len(res.Items) != 0 {
		t.Errorf("ListPersons() = %d persons after failed create, want 0", len(res.Items))
	}
}

// TestCreatePersonWithContactPoints_TransactionRollsBackOnPointFailure
// verifies the atomicity contract directly: when the contact-point insert
// inside the transaction fails, the person insert is rolled back with it.
// A same-entity duplicate contact point (the per-entity unique index on
// person_id+kind+normalized_value) is the failure the quick-add can hit;
// it is exercised here through the same execer-shaped helpers the service
// method uses, because a brand-new person can never collide with its own
// unique index through the public API.
func TestCreatePersonWithContactPoints_TransactionRollsBackOnPointFailure(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	var personID string
	err := sqlite.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		var err error
		personID, err = insertPerson(ctx, tx, contact.PersonInput{DisplayName: "Rollback Target"})
		if err != nil {
			return err
		}
		ref := personRef(personID)
		if _, err := addContactPoint(ctx, tx, ref, contact.ContactPointInput{Kind: contact.ContactPointEmail, RawValue: "dup@example.com"}); err != nil {
			return err
		}
		// Same entity, same kind, same normalized value -> unique-index
		// conflict; this must abort the transaction.
		if _, err := addContactPoint(ctx, tx, ref, contact.ContactPointInput{Kind: contact.ContactPointEmail, RawValue: "DUP@example.com"}); err != nil {
			return err
		}
		return nil
	})
	if errs.KindOf(err) != errs.KindConflict {
		t.Fatalf("duplicate point error kind = %v, want conflict (err=%v)", errs.KindOf(err), err)
	}

	// The person inserted earlier in the same transaction must be gone.
	if _, err := s.GetPerson(ctx, personID); errs.KindOf(err) != errs.KindNotFound {
		t.Fatalf("GetPerson() after rollback = %v, want not-found", err)
	}
	res, err := s.ListPersons(ctx, ListPersonsOptions{})
	if err != nil {
		t.Fatalf("ListPersons() error = %v", err)
	}
	if len(res.Items) != 0 {
		t.Errorf("ListPersons() = %d persons after rollback, want 0", len(res.Items))
	}
}

func TestCreatePersonWithContactPoints_RetryAfterFailureCreatesOnePerson(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	// First attempt fails validation before any insert.
	_, err := s.CreatePersonWithContactPoints(ctx, contact.PersonInput{DisplayName: "  "}, "retry@example.com", "")
	if errs.KindOf(err) != errs.KindInvalid {
		t.Fatalf("first attempt error kind = %v, want invalid", errs.KindOf(err))
	}

	// Retry with valid input must not create a second person from the
	// failed attempt.
	retried, err := s.CreatePersonWithContactPoints(ctx, contact.PersonInput{DisplayName: "Retryer"}, "retry@example.com", "")
	if err != nil {
		t.Fatalf("retry error = %v", err)
	}
	if retried.DisplayName != "Retryer" {
		t.Errorf("retried DisplayName = %q, want Retryer", retried.DisplayName)
	}

	res, err := s.ListPersons(ctx, ListPersonsOptions{})
	if err != nil {
		t.Fatalf("ListPersons() error = %v", err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("ListPersons() = %d persons after retry, want exactly 1", len(res.Items))
	}
}
