package contacts

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/contacts/internal/app"
	"github.com/danieljustus/symaira-desktop/internal/contacts/internal/domain/contact"
)

// TestPortCutoverContactsSeed creates the Go-owned contact store the RUST-018
// cutover-state recorder (cmd/symdesk TestPortCutoverStateContract) freezes.
// The domain services live behind internal/contacts/internal, so the recorder
// reaches them through this test instead of importing them. It is inert unless
// PORT_CUTOVER_CONTACTS_OUT names the file receiving the created ids; the
// store location comes from the recorder's XDG_DATA_HOME like any Go run.
func TestPortCutoverContactsSeed(t *testing.T) {
	out := os.Getenv("PORT_CUTOVER_CONTACTS_OUT")
	if out == "" {
		t.Skip("PORT_CUTOVER_CONTACTS_OUT not set: seed helper for the cutover-state recorder")
	}
	ctx := context.Background()
	a, err := app.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()

	ada, err := a.CreatePersonWithContactPoints(ctx, contact.PersonInput{
		DisplayName: "Ada Lovelace", GivenName: "Ada", FamilyName: "Lovelace",
		Notes: "Analytical engine correspondent", Source: "cutover-recorder", SourceRef: "ada",
	}, "ada@example.org", "+49 30 1234567")
	if err != nil {
		t.Fatal(err)
	}
	grace, err := a.CreatePersonWithContactPoints(ctx, contact.PersonInput{
		DisplayName: "Grace Hopper", GivenName: "Grace", FamilyName: "Hopper",
		Source: "cutover-recorder", SourceRef: "grace",
	}, "grace@example.org", "")
	if err != nil {
		t.Fatal(err)
	}
	acme, err := a.Contacts.CreateOrganization(ctx, contact.OrganizationInput{
		Name: "Acme GmbH", Notes: "Invoice sender", Source: "cutover-recorder", SourceRef: "acme",
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(map[string]string{"ada": ada.ID, "grace": grace.ID, "acme": acme.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, append(encoded, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}
