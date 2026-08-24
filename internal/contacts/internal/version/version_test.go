package version

import (
	"encoding/json"
	"testing"
)

// The version --json payload carries the versionkit handshake contract
// {tool, version, schema_version} (corekit/AGENTS.md — field names must not
// be renamed) plus the repo's own api_version wire-contract field
// (docs/CLI_CONTRACT.md). Additional fields do not break the handshake:
// SymairaToolKit reads the known field names and ignores everything else.
func TestInfo_JSONMatchesVersionkitHandshake(t *testing.T) {
	b, err := json.Marshal(Get())
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	for _, key := range []string{"tool", "version", "schema_version", "api_version"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("missing expected snake_case field %q in %s", key, b)
		}
	}
}
