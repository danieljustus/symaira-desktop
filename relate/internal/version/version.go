// Package version carries the build-time version metadata surfaced by
// `symrelate version --json` and the doctor command.
package version

import "github.com/danieljustus/symaira-corekit/versionkit"

// SchemaVersion is the highest embedded migration version this build knows
// about. It is independent of the tool release version.
const SchemaVersion = 7

// APIVersion is the declared version of the CLI `--json` output and MCP
// tool contract (see docs/CLI_CONTRACT.md). It is independent of both
// Version (the release) and SchemaVersion (the on-disk database): the JSON
// contract may stay stable across schema migrations, and changes to it are
// additive within a major version — a field is never removed or repurposed
// without bumping APIVersion.
const APIVersion = "v1"

// Version is overridden at build time via
// -ldflags "-X github.com/danieljustus/symaira-relate/internal/version.Version=v0.1.0".
var Version = "dev"

// Tool is the stable machine-readable tool identifier.
const Tool = "symrelate"

// Info is the `version --json` payload: the versionkit handshake fields
// ({tool, version, schema_version} — the exact shape symaira-appkit's
// SymairaToolKit validates against, see corekit/AGENTS.md) plus the repo's
// own wire-contract version api_version (docs/CLI_CONTRACT.md). The embedded
// versionkit.Info keeps the handshake field names untouched; api_version is
// an additional contract field whose removal would require a v2 bump.
type Info struct {
	versionkit.Info
	APIVersion string `json:"api_version"`
}

// Get returns the `version --json` payload. The plain `version` output is
// derived from the same payload.
func Get() Info {
	return Info{Info: versionkit.New(Tool, Version, SchemaVersion), APIVersion: APIVersion}
}
