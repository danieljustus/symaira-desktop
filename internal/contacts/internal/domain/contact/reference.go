package contact

// RefProvider identifies the issuing tool in a Ref. Consumers key on this
// constant instead of guessing where a reference came from.
const RefProvider = "symrelate"

// RefSchemaVersion is the current contact-reference schema version. It is
// independent of the storage schema_version and of the CLI/MCP
// api_version (see docs/CLI_CONTRACT.md): within a major version fields
// are additive only, so consumers must ignore fields they do not know.
const RefSchemaVersion = 1

// RefKind identifies which contact kind a Ref points at.
type RefKind string

const (
	RefKindPerson       RefKind = "person"
	RefKindOrganization RefKind = "organization"
)

func (k RefKind) Valid() bool {
	return k == RefKindPerson || k == RefKindOrganization
}

// Ref is the minimal, privacy-safe, opaque reference to a contact that
// external Symaira tools (e.g. SymDesk meeting notes) may store and later
// resolve. It deliberately contains no contact points, notes, aliases,
// tags, classifications, or filesystem paths — the full record is only
// available through the owning tool, never through the reference.
//
// DisplayName is a rendering cache so a consumer can display a linked
// contact without a runtime call; consumers treat it as refreshable at
// review time and must not rely on it for identity. Identity is ID + Kind
// only.
type Ref struct {
	Provider      string  `json:"provider"`
	SchemaVersion int     `json:"schema_version"`
	ID            string  `json:"id"`
	Kind          RefKind `json:"kind"`
	DisplayName   string  `json:"display_name"`
}
