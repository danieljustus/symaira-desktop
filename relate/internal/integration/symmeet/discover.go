// Package symmeet implements the optional, runtime-detected SymMeet
// integration: a discovery/capability handshake used only to give the
// user an accurate status and an actionable install/upgrade message. It
// never imports a symaira-meet package (see ARCHITECTURE.md's
// standalone-first rule) — every call shells out to a `symmeet` binary
// found on PATH, exactly like internal/integration/symmemory does for
// SymMemory.
//
// The `symmeet capabilities --json` command name and the tool/version/
// schema_version fields this package reads match symaira-meet's now-
// published v1 ecosystem integration contract (symaira-meet#19,
// CapabilitiesCommand.swift) — confirmed against the published contract
// rather than only the issue's own evidence section. See
// docs/integrations/SYMMEET.md for the full status.
package symmeet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"time"
)

// ErrUnavailable is returned when symmeet cannot be reached right now:
// not installed, timed out, or returned something this package cannot
// parse. Never fatal — see internal/domain/security.SymVaultKeyProvider
// for the same pattern applied to SymVault.
var ErrUnavailable = errors.New("symmeet: unavailable (not installed, not responding, or incompatible)")

// DefaultTimeout bounds every symmeet subprocess call. A var (not const)
// so tests can shrink it instead of sleeping through the production
// timeout.
var DefaultTimeout = 3 * time.Second

const binaryName = "symmeet"

// Capabilities is the subset of `symmeet capabilities --json` this
// package relies on. The real payload is documented (symaira-meet#19) to
// carry substantially more — installed engine/model capabilities,
// supported import/export formats, artifact contract versions — which
// this package intentionally leaves unparsed (Go's encoding/json ignores
// unknown fields by default) rather than hard-coding a shape it cannot
// verify.
type Capabilities struct {
	Tool          string `json:"tool"`
	Version       string `json:"version"`
	SchemaVersion int    `json:"schema_version"`
}

// Discover runs `symmeet capabilities --json` with a bounded timeout.
// Missing binary, timeout, non-zero exit, malformed JSON, or a "tool"
// field that isn't "symmeet" all collapse to ErrUnavailable — callers
// react by degrading gracefully with an actionable message, never by
// failing the calling command (see the parent issue's acceptance
// criteria: "Absence or incompatibility of SymMeet leaves Relate
// operational and gives an actionable install/upgrade message").
func Discover(ctx context.Context) (Capabilities, error) {
	binPath, err := exec.LookPath(binaryName)
	if err != nil {
		return Capabilities{}, ErrUnavailable
	}

	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, binPath, "capabilities", "--json").Output()
	if err != nil {
		return Capabilities{}, ErrUnavailable
	}

	var caps Capabilities
	if err := json.Unmarshal(bytes.TrimSpace(out), &caps); err != nil {
		return Capabilities{}, ErrUnavailable
	}
	if caps.Tool != "symmeet" {
		return Capabilities{}, ErrUnavailable
	}
	return caps, nil
}
