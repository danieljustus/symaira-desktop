package memorylink

import (
	"context"
	"testing"

	"github.com/danieljustus/symaira-relate/internal/domain/memorylink"
	"github.com/danieljustus/symaira-relate/internal/errs"
)

func TestLinkOrganization_GetUnlink_RoundTrip(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	orgID := f.org(t, "Acme")

	if link, err := f.links.GetOrganizationLink(ctx, orgID); err != nil || link != nil {
		t.Fatalf("GetOrganizationLink() before linking = (%v, %v), want (nil, nil)", link, err)
	}

	link, err := f.links.LinkOrganization(ctx, orgID, memorylink.LinkInput{MemoryEntityID: "entity-1", MemoryEntityType: "organization", LinkedBy: "tester"})
	if err != nil {
		t.Fatalf("LinkOrganization() error = %v", err)
	}
	if link.OrganizationID != orgID || link.MemoryEntityID != "entity-1" {
		t.Errorf("LinkOrganization() = %+v, want OrganizationID=%s MemoryEntityID=entity-1", link, orgID)
	}
	if link.PersonID != "" {
		t.Errorf("LinkOrganization() PersonID = %q, want empty", link.PersonID)
	}

	got, err := f.links.GetOrganizationLink(ctx, orgID)
	if err != nil || got == nil {
		t.Fatalf("GetOrganizationLink() after linking = (%v, %v)", got, err)
	}
	if got.MemoryEntityID != "entity-1" {
		t.Errorf("GetOrganizationLink().MemoryEntityID = %q, want entity-1", got.MemoryEntityID)
	}

	if err := f.links.UnlinkOrganization(ctx, orgID); err != nil {
		t.Fatalf("UnlinkOrganization() error = %v", err)
	}
	if got, err := f.links.GetOrganizationLink(ctx, orgID); err != nil || got != nil {
		t.Fatalf("GetOrganizationLink() after unlink = (%v, %v), want (nil, nil)", got, err)
	}
}

func TestLinkOrganization_SecondLink_IsConflict(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	orgID := f.org(t, "Acme")

	if _, err := f.links.LinkOrganization(ctx, orgID, memorylink.LinkInput{MemoryEntityID: "entity-1", LinkedBy: "tester"}); err != nil {
		t.Fatalf("first LinkOrganization() error = %v", err)
	}
	_, err := f.links.LinkOrganization(ctx, orgID, memorylink.LinkInput{MemoryEntityID: "entity-2", LinkedBy: "tester"})
	if errs.KindOf(err) != errs.KindConflict {
		t.Errorf("second LinkOrganization() kind = %v, want conflict", errs.KindOf(err))
	}
}

func TestLinkOrganization_UnknownOrganization_IsInvalid(t *testing.T) {
	f := newFixture(t)
	_, err := f.links.LinkOrganization(context.Background(), "does-not-exist", memorylink.LinkInput{MemoryEntityID: "entity-1", LinkedBy: "tester"})
	if errs.KindOf(err) != errs.KindInvalid {
		t.Errorf("LinkOrganization(unknown org) kind = %v, want invalid", errs.KindOf(err))
	}
}

func TestLinkOrganization_EmptyEntityID_IsInvalid(t *testing.T) {
	f := newFixture(t)
	orgID := f.org(t, "Acme")
	_, err := f.links.LinkOrganization(context.Background(), orgID, memorylink.LinkInput{MemoryEntityID: "", LinkedBy: "tester"})
	if errs.KindOf(err) != errs.KindInvalid {
		t.Errorf("LinkOrganization(empty entity id) kind = %v, want invalid", errs.KindOf(err))
	}
}

func TestUnlinkOrganization_NoExistingLink_IsNoop(t *testing.T) {
	f := newFixture(t)
	orgID := f.org(t, "Acme")
	if err := f.links.UnlinkOrganization(context.Background(), orgID); err != nil {
		t.Errorf("UnlinkOrganization() with no link error = %v, want nil", err)
	}
}
