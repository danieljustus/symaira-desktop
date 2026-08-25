// Package contacts resolves opaque contact references for meeting notes.
//
// The contact store is the absorbed SymRelate store, which lives directly in
// internal/contacts/ (with internal subpackages app, domain, service, storage,
// etc.). It is linked in-process — there is no symrelate binary to find, no
// PATH probe, and no subprocess.
//
// Only the reference-only contract is reachable from here: a reference
// carries provider, schema version, ID, kind and a display-name cache, and
// never contact points, notes, tags, or filesystem paths. That boundary is
// structural — Ref has no field that could hold a private value.
package contacts

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/contacts/internal/app"
	"github.com/danieljustus/symaira-desktop/internal/contacts/internal/domain/contact"
	"github.com/danieljustus/symaira-desktop/internal/contacts/internal/errs"
)

// Provider is the provider marker every reference carries.
const Provider = contact.RefProvider

// SchemaVersion is the contact-reference schema version this consumer speaks.
const SchemaVersion = contact.RefSchemaVersion

// ErrContactNotFound marks an unknown or erased contact ID.
var ErrContactNotFound = errors.New("symrelate contact not found")

// Ref is the opaque contact reference as it is stored in a meeting note's
// frontmatter. Identity is ID + Kind; DisplayName is a refreshable rendering
// cache only.
type Ref struct {
	Provider      string `json:"provider" yaml:"provider"`
	SchemaVersion int    `json:"schema_version" yaml:"schema_version"`
	ID            string `json:"id" yaml:"id"`
	Kind          string `json:"kind" yaml:"kind"`
	DisplayName   string `json:"display_name,omitempty" yaml:"display_name,omitempty"`

	// Extras carries additive fields found in notes written by an earlier
	// SymDesk, so re-saving such a note does not silently drop them. It is
	// never populated by ResolveRef — the in-process store cannot produce
	// fields this type does not know.
	Extras map[string]interface{} `json:"-" yaml:",inline"`
}

// ReferenceTargetPrefix is the sidecar link-graph namespace for contact
// references. It is deliberately not a filesystem path: a contact is owned by
// the contact store, not by the vault.
const ReferenceTargetPrefix = "contact:"

// ReferenceTarget returns the stable derived link target for a contact
// reference. Provider, kind and ID are all included so IDs from another
// provider or contact kind cannot collide in the sidecar graph.
func ReferenceTarget(ref Ref) string {
	return ReferenceTargetPrefix + ref.Provider + ":" + ref.Kind + ":" + ref.ID
}

// ParseReferenceTarget recognizes a target returned by ReferenceTarget. It is
// intentionally strict so a malformed user-supplied backlinks query cannot
// accidentally be treated as a contact.
func ParseReferenceTarget(target string) (Ref, bool) {
	if !strings.HasPrefix(target, ReferenceTargetPrefix) {
		return Ref{}, false
	}
	parts := strings.Split(strings.TrimPrefix(target, ReferenceTargetPrefix), ":")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return Ref{}, false
	}
	return Ref{Provider: parts[0], Kind: parts[1], ID: parts[2]}, true
}

// ReferencesInFrontmatter extracts only structurally valid opaque contact
// references from arbitrary decoded YAML. Meeting participants nest these
// under participants[].contact_ref, while future note types may place them at
// another depth. No other contact-store data is copied into the index.
func ReferencesInFrontmatter(frontmatter map[string]interface{}) []Ref {
	var refs []Ref
	var walk func(interface{})
	walk = func(value interface{}) {
		switch v := value.(type) {
		case map[string]interface{}:
			if ref, ok := refFromMap(v); ok {
				refs = append(refs, ref)
				return
			}
			for _, child := range v {
				walk(child)
			}
		case []interface{}:
			for _, child := range v {
				walk(child)
			}
		}
	}
	walk(frontmatter)
	return refs
}

func refFromMap(value map[string]interface{}) (Ref, bool) {
	provider, _ := value["provider"].(string)
	id, _ := value["id"].(string)
	kind, _ := value["kind"].(string)
	if provider == "" || id == "" || kind == "" {
		return Ref{}, false
	}
	version := 0
	switch v := value["schema_version"].(type) {
	case int:
		version = v
	case int64:
		version = int(v)
	case uint64:
		if v > uint64(^uint(0)>>1) {
			return Ref{}, false
		}
		version = int(v)
	case float64:
		version = int(v)
	}
	return Ref{
		Provider:      provider,
		SchemaVersion: version,
		ID:            id,
		Kind:          kind,
		DisplayName:   stringValue(value["display_name"]),
	}, true
}

func stringValue(value interface{}) string {
	result, _ := value.(string)
	return result
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
	DefaultAvailableFunc  = defaultAvailable
	DefaultResolveFunc    = defaultResolve
	DefaultFindByNameFunc = defaultFindByName
	AvailableFunc         = DefaultAvailableFunc
	ResolveFunc           = DefaultResolveFunc
	FindByNameFunc        = DefaultFindByNameFunc
)

func defaultResolve(ctx context.Context, contactID string) (*Ref, error) {
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

func defaultAvailable(ctx context.Context) bool {
	a, err := app.Open(ctx)
	if err != nil {
		return false
	}
	defer func() { _ = a.Close() }()
	return a.Ping(ctx) == nil
}

func defaultFindByName(ctx context.Context, name string) ([]Ref, error) {
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
