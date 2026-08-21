package memorylink

import (
	"context"
	"testing"

	contactdomain "github.com/danieljustus/symaira-relate/internal/domain/contact"
	"github.com/danieljustus/symaira-relate/internal/domain/memorylink"
	"github.com/danieljustus/symaira-relate/internal/errs"
	contactsvc "github.com/danieljustus/symaira-relate/internal/service/contact"
	"github.com/danieljustus/symaira-relate/internal/storage/sqlite"
)

type testFixture struct {
	contacts *contactsvc.Service
	links    *Service
}

func newFixture(t *testing.T) *testFixture {
	t.Helper()
	db, err := sqlite.OpenMemory(context.Background())
	if err != nil {
		t.Fatalf("OpenMemory() error = %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return &testFixture{contacts: contactsvc.New(db), links: New(db)}
}

func (f *testFixture) person(t *testing.T, name string) string {
	t.Helper()
	p, err := f.contacts.CreatePerson(context.Background(), contactdomain.PersonInput{DisplayName: name})
	if err != nil {
		t.Fatalf("CreatePerson(%s) error = %v", name, err)
	}
	return p.ID
}

func (f *testFixture) org(t *testing.T, name string) string {
	t.Helper()
	o, err := f.contacts.CreateOrganization(context.Background(), contactdomain.OrganizationInput{Name: name})
	if err != nil {
		t.Fatalf("CreateOrganization(%s) error = %v", name, err)
	}
	return o.ID
}

func TestLinkPerson_GetUnlink_RoundTrip(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	personID := f.person(t, "Ada Lovelace")

	if link, err := f.links.GetPersonLink(ctx, personID); err != nil || link != nil {
		t.Fatalf("GetPersonLink() before linking = (%v, %v), want (nil, nil)", link, err)
	}

	link, err := f.links.LinkPerson(ctx, personID, memorylink.LinkInput{MemoryEntityID: "entity-1", MemoryEntityType: "person", LinkedBy: "tester"})
	if err != nil {
		t.Fatalf("LinkPerson() error = %v", err)
	}
	if link.PersonID != personID || link.MemoryEntityID != "entity-1" {
		t.Errorf("LinkPerson() = %+v, want PersonID=%s MemoryEntityID=entity-1", link, personID)
	}
	if link.OrganizationID != "" {
		t.Errorf("LinkPerson() OrganizationID = %q, want empty", link.OrganizationID)
	}

	got, err := f.links.GetPersonLink(ctx, personID)
	if err != nil || got == nil {
		t.Fatalf("GetPersonLink() after linking = (%v, %v)", got, err)
	}
	if got.MemoryEntityID != "entity-1" {
		t.Errorf("GetPersonLink().MemoryEntityID = %q, want entity-1", got.MemoryEntityID)
	}

	if err := f.links.UnlinkPerson(ctx, personID); err != nil {
		t.Fatalf("UnlinkPerson() error = %v", err)
	}
	if got, err := f.links.GetPersonLink(ctx, personID); err != nil || got != nil {
		t.Fatalf("GetPersonLink() after unlink = (%v, %v), want (nil, nil)", got, err)
	}
}

func TestLinkPerson_SecondLink_IsConflict(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	personID := f.person(t, "Ada Lovelace")

	if _, err := f.links.LinkPerson(ctx, personID, memorylink.LinkInput{MemoryEntityID: "entity-1", LinkedBy: "tester"}); err != nil {
		t.Fatalf("first LinkPerson() error = %v", err)
	}
	_, err := f.links.LinkPerson(ctx, personID, memorylink.LinkInput{MemoryEntityID: "entity-2", LinkedBy: "tester"})
	if errs.KindOf(err) != errs.KindConflict {
		t.Errorf("second LinkPerson() kind = %v, want conflict", errs.KindOf(err))
	}
}

func TestLinkPerson_UnknownPerson_IsInvalid(t *testing.T) {
	f := newFixture(t)
	_, err := f.links.LinkPerson(context.Background(), "does-not-exist", memorylink.LinkInput{MemoryEntityID: "entity-1", LinkedBy: "tester"})
	if errs.KindOf(err) != errs.KindInvalid {
		t.Errorf("LinkPerson(unknown person) kind = %v, want invalid", errs.KindOf(err))
	}
}

func TestLinkPerson_EmptyEntityID_IsInvalid(t *testing.T) {
	f := newFixture(t)
	personID := f.person(t, "Ada Lovelace")
	_, err := f.links.LinkPerson(context.Background(), personID, memorylink.LinkInput{MemoryEntityID: "", LinkedBy: "tester"})
	if errs.KindOf(err) != errs.KindInvalid {
		t.Errorf("LinkPerson(empty entity id) kind = %v, want invalid", errs.KindOf(err))
	}
}

func TestUnlinkPerson_NoExistingLink_IsNoop(t *testing.T) {
	f := newFixture(t)
	personID := f.person(t, "Ada Lovelace")
	if err := f.links.UnlinkPerson(context.Background(), personID); err != nil {
		t.Errorf("UnlinkPerson() with no link error = %v, want nil", err)
	}
}
