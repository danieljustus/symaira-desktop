// Package contacts resolves opaque contact references for meeting notes.
//
// The contact store is symrelate, which lives in this repository as the
// nested relate/ module since the repo consolidation. It is linked in-process
// through relate/api — there is no symrelate binary to find and no
// subprocess.
//
// Only the reference-only contract is reachable from here: a reference
// carries provider, schema version, ID, kind and a display-name cache, and
// never contact points, notes, tags, or filesystem paths. That boundary is
// now structural — relate/api's return type has no field that could hold a
// private value — where it used to be a deny-list applied to whatever JSON a
// separately versioned symrelate binary printed.
//
// For the same reason there is no schema-compatibility check any more: the
// store is compiled from this repository, so producer and consumer cannot
// disagree about the reference schema.
package contacts

import (
	"context"
	"time"

	relateapi "github.com/danieljustus/symaira-relate/api"
)

// Provider is the provider marker every reference carries.
const Provider = relateapi.Provider

// SchemaVersion is the contact-reference schema version this consumer speaks.
const SchemaVersion = relateapi.SchemaVersion

// ErrContactNotFound marks an unknown or erased contact ID.
var ErrContactNotFound = relateapi.ErrContactNotFound

// Ref is the opaque contact reference as it is stored in a meeting note's
// frontmatter. Identity is ID + Kind; DisplayName is a refreshable rendering
// cache only.
type Ref struct {
	Provider      string `json:"provider" yaml:"provider"`
	SchemaVersion int    `json:"schema_version" yaml:"schema_version"`
	ID            string `json:"id" yaml:"id"`
	Kind          string `json:"kind" yaml:"kind"`
	DisplayName   string `json:"display_name" yaml:"display_name,omitempty"`

	// Extras carries additive fields found in notes written by an earlier
	// SymDesk, so re-saving such a note does not silently drop them. It is
	// never populated by ResolveRef — the in-process store cannot produce
	// fields this type does not know.
	Extras map[string]interface{} `json:"-" yaml:",inline"`
}

// callTimeout bounds a single contact-store call. It is a package variable so
// tests can shrink it instead of sleeping.
var callTimeout = 5 * time.Second

// The store seam. These are the single injectable entry points into the
// contact store, so a test can substitute a double without seeding the
// user's real store (which lives under $HOME, not in a temp dir).
//
// Production code never reassigns them; a test that does must restore the
// original in t.Cleanup.
var (
	AvailableFunc  = relateapi.Available
	ResolveFunc    = resolve
	FindByNameFunc = findByName
)

func resolve(ctx context.Context, contactID string) (*Ref, error) {
	ref, err := relateapi.ResolveContactRef(ctx, contactID)
	if err != nil {
		return nil, err
	}
	return &Ref{
		Provider:      ref.Provider,
		SchemaVersion: ref.SchemaVersion,
		ID:            ref.ID,
		Kind:          ref.Kind,
		DisplayName:   ref.DisplayName,
	}, nil
}

func findByName(ctx context.Context, name string) ([]Ref, error) {
	found, err := relateapi.FindContactRefs(ctx, name)
	if err != nil {
		return nil, err
	}
	refs := make([]Ref, 0, len(found))
	for _, ref := range found {
		refs = append(refs, Ref{
			Provider:      ref.Provider,
			SchemaVersion: ref.SchemaVersion,
			ID:            ref.ID,
			Kind:          ref.Kind,
			DisplayName:   ref.DisplayName,
		})
	}
	return refs, nil
}

// Available reports whether the local contact store can be opened.
func Available() bool {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return AvailableFunc(ctx)
}

// ResolveRef resolves a contact ID to its reference-only shape. It returns
// ErrContactNotFound for an unknown or erased ID.
func ResolveRef(contactID string) (*Ref, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return ResolveFunc(ctx, contactID)
}

// FindRefsByName resolves a display name to the contact references carrying
// it. A name nobody carries yields an empty slice — that is an answer, not a
// failure, and callers must render it as "unresolved" rather than an error.
func FindRefsByName(name string) ([]Ref, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return FindByNameFunc(ctx, name)
}
