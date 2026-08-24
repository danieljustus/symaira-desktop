// Package api is the stable in-process entry point into symrelate.
//
// symrelate's logic lives in internal/ packages, which Go's import rules make
// unreachable from other modules. Consumers that link this module rather than
// executing the symrelate binary — symdesk since the repo consolidation — go
// through this package instead.
//
// The surface is deliberately limited to the reference-only contact contract
// (docs/integrations/CONTACT_REF.md). Everything that can surface contact
// points, notes, tags, or classifications stays behind the CLI/MCP, so an
// embedding consumer cannot reach private fields even by mistake.
package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/danieljustus/symaira-relate/internal/app"
	"github.com/danieljustus/symaira-relate/internal/domain/contact"
	"github.com/danieljustus/symaira-relate/internal/errs"
)

// Provider is the provider marker every Ref carries.
const Provider = contact.RefProvider

// SchemaVersion is the contact-reference schema version this package emits.
const SchemaVersion = contact.RefSchemaVersion

// ErrContactNotFound marks an unknown or erased contact ID. It does not
// disclose which store was probed, matching symrelate's privacy contract.
var ErrContactNotFound = errors.New("symrelate contact not found")

// Ref is the minimal, privacy-safe reference to a contact. Identity is
// ID + Kind; DisplayName is a refreshable rendering cache only.
type Ref struct {
	Provider      string `json:"provider"`
	SchemaVersion int    `json:"schema_version"`
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	DisplayName   string `json:"display_name,omitempty"`
}

// ResolveContactRef resolves a contact ID to its reference-only shape.
//
// It returns ErrContactNotFound for an unknown or erased ID. Any other error
// is a genuine failure of the local store, which the caller should surface
// rather than treat as "no such contact".
func ResolveContactRef(ctx context.Context, contactID string) (*Ref, error) {
	if contactID == "" {
		return nil, fmt.Errorf("a symrelate contact id is required")
	}

	a, err := app.Open(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = a.Close() }()

	ref, err := a.Contacts.GetRef(ctx, contactID)
	if err != nil {
		if errs.KindOf(err) == errs.KindNotFound {
			return nil, ErrContactNotFound
		}
		return nil, err
	}

	return &Ref{
		Provider:      ref.Provider,
		SchemaVersion: ref.SchemaVersion,
		ID:            ref.ID,
		Kind:          string(ref.Kind),
		DisplayName:   ref.DisplayName,
	}, nil
}

// Available reports whether the local symrelate store can be opened. A
// consumer uses it to decide whether to offer contact linking at all, the
// same way it previously probed for the symrelate binary.
func Available(ctx context.Context) bool {
	a, err := app.Open(ctx)
	if err != nil {
		return false
	}
	defer func() { _ = a.Close() }()
	return a.Ping(ctx) == nil
}

// FindContactRefs resolves a display name to the reference-only shapes that
// carry it. It is the name-side counterpart to ResolveContactRef, for
// consumers that hold a name rather than an ID — a document's correspondent,
// for instance.
//
// Matching is exact but case-insensitive, and the result is reference-only
// like everything else on this surface: no contact points, notes, tags or
// paths. An unmatched name returns an empty slice, not an error: "nobody by
// that name" is a normal answer, unlike a failing store.
func FindContactRefs(ctx context.Context, name string) ([]Ref, error) {
	if strings.TrimSpace(name) == "" {
		return nil, nil
	}

	a, err := app.Open(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = a.Close() }()

	found, err := a.Contacts.FindRefsByName(ctx, name)
	if err != nil {
		return nil, err
	}

	refs := make([]Ref, 0, len(found))
	for _, ref := range found {
		refs = append(refs, Ref{
			Provider:      ref.Provider,
			SchemaVersion: ref.SchemaVersion,
			ID:            ref.ID,
			Kind:          string(ref.Kind),
			DisplayName:   ref.DisplayName,
		})
	}
	return refs, nil
}
