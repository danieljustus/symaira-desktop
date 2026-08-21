package symmemory

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
)

// Context fetches SymMemory's own record for an already-linked entity id
// and returns it as opaque, pretty-printed JSON for display — symrelate
// does not parse or store its contents, only passes it through, so this
// package stays correct even if SymMemory's entity schema grows fields
// this package does not know about.
//
// KNOWN LIMITATION (confirmed against a real `symmemory` v0.12.0
// installation while building this package): `entity show` resolves its
// argument by **name**, not by the stable entity id `memory_links` stores
// — a raw id returns "entity not found". Relate deliberately stores the
// id (never a mutable name) so a link survives a rename, matching the
// acceptance criteria — but that means Context calls will fail against
// today's SymMemory for any id that isn't also a valid name, until an
// id-based lookup lands upstream (the same underlying gap
// symaira-memory#343 tracks for candidate resolution). This is a safe,
// expected ErrUnavailable, not a bug — see docs/integrations/SYMMEMORY.md.
//
// The command itself — `symmemory entity show <arg> --output json` — is
// confirmed correct against that real installation. Its JSON output is
// followed by non-JSON "Linked memories" text on the same stdout stream,
// so this reads exactly one JSON value with a streaming decoder rather
// than validating the whole output as JSON.
func Context(ctx context.Context, entityID string) (string, error) {
	if entityID == "" {
		return "", ErrUnavailable
	}
	binPath, err := exec.LookPath(binaryName)
	if err != nil {
		return "", ErrUnavailable
	}

	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, binPath, "entity", "show", entityID, "--output", "json").Output()
	if err != nil {
		return "", ErrUnavailable
	}

	// Decode exactly one JSON value and discard whatever text follows it
	// (see the "Linked memories" note above) instead of requiring the
	// entire stdout stream to be valid JSON.
	dec := json.NewDecoder(bytes.NewReader(out))
	var entity json.RawMessage
	if err := dec.Decode(&entity); err != nil {
		return "", ErrUnavailable
	}

	var buf bytes.Buffer
	if err := json.Indent(&buf, entity, "", "  "); err != nil {
		return "", ErrUnavailable
	}
	return buf.String(), nil
}
