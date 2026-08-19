package compose

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ContactRefProvider is the provider marker of the reference-only contact
// shape published by symrelate (docs/integrations/CONTACT_REF.md there).
const ContactRefProvider = "symrelate"

// ContactRefSchemaVersion is the only contact-reference schema version this
// consumer understands. Anything else is incompatible and must degrade.
const ContactRefSchemaVersion = 1

var (
	// ErrContactNotFound marks an unknown or erased symrelate contact ID.
	ErrContactNotFound = errors.New("symrelate contact not found")
	// ErrContactRefIncompatible marks a reference whose provider, schema
	// version, ID, or kind this consumer cannot safely interpret.
	ErrContactRefIncompatible = errors.New("incompatible symrelate contact reference")

	// symrelateCallTimeout bounds every symrelate invocation; it is a
	// package variable so tests can shrink it instead of sleeping.
	symrelateCallTimeout = 5 * time.Second
)

// ContactRef is the minimal, opaque reference to a symrelate contact that a
// meeting note may store. It mirrors symrelate's published contract:
// DisplayName is only a rendering cache (identity is ID + Kind), and Extras
// preserves unknown additive fields for forward-compatible round-trips.
type ContactRef struct {
	Provider      string                 `json:"provider" yaml:"provider"`
	SchemaVersion int                    `json:"schema_version" yaml:"schema_version"`
	ID            string                 `json:"id" yaml:"id"`
	Kind          string                 `json:"kind" yaml:"kind"`
	DisplayName   string                 `json:"display_name" yaml:"display_name,omitempty"`
	Extras        map[string]interface{} `json:"-" yaml:",inline"`
}

// HasSymrelate is a shorthand helper for symrelate.
func HasSymrelate() (bool, string) {
	return HasTool("symrelate")
}

// contactRefDeniedKeys can never be legitimate additive fields on a
// reference-only shape: symrelate's own contract forbids contact points,
// notes and paths there. A buggy or hostile producer smuggling them into
// unknown fields must not see them land in vault metadata.
var contactRefDeniedKeys = map[string]bool{
	"email": true, "emails": true,
	"phone": true, "phones": true,
	"address": true, "addresses": true,
	"url": true, "urls": true,
	"handle": true, "handles": true,
	"contact_point": true, "contact_points": true,
	"raw_value": true, "normalized_value": true,
	"notes":  true,
	"source": true, "source_ref": true,
	"transcript": true, "transcript_path": true,
	"path": true, "paths": true, "filepath": true, "file_path": true,
}

func looksLikeAbsolutePath(v interface{}) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	return strings.HasPrefix(s, "/") || strings.HasPrefix(s, "~/") ||
		(len(s) >= 3 && s[1] == ':' && (s[2] == '\\' || s[2] == '/'))
}

func sanitizeContactRefExtras(raw map[string]interface{}) map[string]interface{} {
	extras := make(map[string]interface{}, len(raw))
	for k, v := range raw {
		if contactRefDeniedKeys[strings.ToLower(k)] || looksLikeAbsolutePath(v) {
			continue
		}
		extras[k] = v
	}
	if len(extras) == 0 {
		return nil
	}
	return extras
}

func runSymrelate(ctx context.Context, args []string) ([]byte, error) {
	bin, err := ResolveFunc("symrelate")
	if err != nil {
		return nil, fmt.Errorf("symrelate not found: %w", err)
	}
	// CommandContext's kill targets only the direct child; a grandchild
	// (e.g. a hung subprocess of symrelate) would keep the pipes open and
	// block Wait forever. WaitDelay bounds that wait so the configured
	// timeout stays a hard upper bound.
	out, stderr, err := runTool(ctx, bin, toolOpts{WaitDelay: 2 * time.Second}, args...)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("symrelate %s timed out: %w", strings.Join(args, " "), ctx.Err())
		}
		if strings.Contains(stderr.String(), "contact not found") {
			return nil, ErrContactNotFound
		}
		return nil, fmt.Errorf("symrelate %s failed: %w (stderr: %s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out.Bytes(), nil
}

// ResolveContactRef resolves a symrelate contact ID to its reference-only
// shape via `symrelate contact ref <id> --json`, with a bounded timeout and
// the contract's schema checks: provider and schema version must match what
// this consumer understands, the resolved ID must equal the requested one,
// and kind must be a known contact kind. Unknown additive fields are
// preserved in Extras after the privacy deny-list pass, so storing the ref
// never copies contact points, notes, or absolute paths.
func ResolveContactRef(contactID string) (*ContactRef, error) {
	contactID = strings.TrimSpace(contactID)
	if contactID == "" {
		return nil, fmt.Errorf("a symrelate contact id is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), symrelateCallTimeout)
	defer cancel()
	data, err := runSymrelate(ctx, []string{"contact", "ref", contactID, "--json"})
	if err != nil {
		return nil, err
	}

	var ref ContactRef
	if err := json.Unmarshal(data, &ref); err != nil {
		return nil, fmt.Errorf("failed to unmarshal symrelate contact ref output: %w", err)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("failed to unmarshal symrelate contact ref output: %w", err)
	}
	for _, known := range []string{"provider", "schema_version", "id", "kind", "display_name"} {
		delete(raw, known)
	}
	ref.Extras = sanitizeContactRefExtras(raw)

	if ref.Provider != ContactRefProvider {
		return nil, fmt.Errorf("%w: provider %q", ErrContactRefIncompatible, ref.Provider)
	}
	if ref.SchemaVersion != ContactRefSchemaVersion {
		return nil, fmt.Errorf("%w: schema_version %d", ErrContactRefIncompatible, ref.SchemaVersion)
	}
	if ref.ID == "" || ref.ID != contactID {
		return nil, fmt.Errorf("%w: resolved id %q does not match requested %q", ErrContactRefIncompatible, ref.ID, contactID)
	}
	if ref.Kind != "person" && ref.Kind != "organization" {
		return nil, fmt.Errorf("%w: kind %q", ErrContactRefIncompatible, ref.Kind)
	}
	return &ref, nil
}
