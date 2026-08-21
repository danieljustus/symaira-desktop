// Package symmemory implements the optional, runtime-detected SymMemory
// integration: discovery/version-compatibility handshake and a bounded,
// best-effort context lookup for an already-linked entity. It never
// imports a symaira-memory package (see ARCHITECTURE.md's
// standalone-first rule) — every call shells out to a `symmemory` binary
// found on PATH, exactly like internal/domain/security.SymVaultKeyProvider
// does for SymVault.
//
// What this package intentionally does NOT do: automatic candidate
// matching and ID/provenance-rich relation writes were historically
// blocked on upstream symaira-memory#343/#344. Those APIs are now
// available and are implemented in this package's Candidates and Relate
// functions; see docs/integrations/SYMMEMORY.md for the full status.
package symmemory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"time"
)

// ErrUnavailable is returned when symmemory cannot be reached right now:
// not installed, timed out, or returned something this package cannot
// parse. It is never fatal to symrelate — callers degrade to "SymMemory
// features are unavailable" rather than failing the calling command.
var ErrUnavailable = errors.New("symmemory: unavailable (not installed, not responding, or incompatible)")

// DefaultTimeout bounds every symmemory subprocess call. SymMemory is a
// convenience integration; a hung or slow subprocess must never hang a
// symrelate command. A var (not const) so tests can shrink it instead of
// sleeping through the production timeout.
var DefaultTimeout = 3 * time.Second

// binaryName is the executable this package looks for on PATH.
const binaryName = "symmemory"

// Info is the parsed `symmemory version --json` payload. The field set
// mirrors symrelate's own internal/version.Info shape (tool/version/
// schema_version) — the same convention documented in symaira-memory#8's
// evidence section for the discovery handshake this package implements.
// This is symrelate's assumption about a yet-undocumented exact wire
// shape; adjust here first if the real contract differs once published.
type Info struct {
	Tool          string `json:"tool"`
	Version       string `json:"version"`
	SchemaVersion int    `json:"schema_version"`
}

// Discover runs `symmemory version --json` with a bounded timeout and
// reports whether a compatible SymMemory installation is available.
// Missing binary, timeout, non-zero exit, malformed JSON, or a
// "tool" field that isn't "symmemory" all collapse to ErrUnavailable —
// callers do not need to distinguish the reason, only react by degrading
// gracefully (see docs/integrations/SYMMEMORY.md).
func Discover(ctx context.Context) (Info, error) {
	binPath, err := exec.LookPath(binaryName)
	if err != nil {
		return Info{}, ErrUnavailable
	}

	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, binPath, "version", "--json").Output()
	if err != nil {
		return Info{}, ErrUnavailable
	}

	var info Info
	if err := json.Unmarshal(bytes.TrimSpace(out), &info); err != nil {
		return Info{}, ErrUnavailable
	}
	if info.Tool != "symmemory" {
		return Info{}, ErrUnavailable
	}
	return info, nil
}
