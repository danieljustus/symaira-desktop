package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/config"
)

// legacyAliasContract pins the legacy-name → canonical-name mapping for the
// absorbed SymIngest and SymSeek MCP contracts (issue #598). Each alias must
// delegate to the exact handler of its canonical tool so existing harnesses
// keep the same behavior under the historical name.
var legacyAliasContract = []struct {
	alias     string
	canonical string
	readOnly  bool
}{
	{alias: "ingest_file", canonical: "desk_ingest", readOnly: false},
	{alias: "list_jobs", canonical: "desk_ingest_jobs", readOnly: true},
	{alias: "retry_job", canonical: "desk_ingest_retry", readOnly: false},
	{alias: "list_documents", canonical: "desk_docs", readOnly: true},
	{alias: "search_documents", canonical: "desk_search", readOnly: true},
}

// TestLegacyAliasesRegistered verifies every legacy alias is present in the
// registry with the same input schema and capability class as its canonical
// tool. Enabled() is what the MCP transport serves in tools/list, so this
// doubles as the tools/list contract test for the compatibility names.
func TestLegacyAliasesRegistered(t *testing.T) {
	registry := NewRegistry(testRegistryOptions())
	all := registry.All()

	for _, want := range legacyAliasContract {
		alias, ok := registry.Lookup(want.alias)
		if !ok {
			t.Fatalf("legacy alias %q missing from registry", want.alias)
		}
		canonical, ok := registry.Lookup(want.canonical)
		if !ok {
			t.Fatalf("canonical tool %q missing from registry", want.canonical)
		}
		if alias.Name != want.alias {
			t.Errorf("alias name = %q, want %q", alias.Name, want.alias)
		}
		if alias.ReadOnly != want.readOnly {
			t.Errorf("alias %q readOnly = %v, want %v", want.alias, alias.ReadOnly, want.readOnly)
		}
		if string(alias.InputSchema) != string(canonical.InputSchema) {
			t.Errorf("alias %q input schema differs from %q", want.alias, want.canonical)
		}

		// The alias must appear in the served tool lists with the same
		// capability class as the canonical tool.
		enabled := registry.Enabled(!want.readOnly)
		found := false
		for _, e := range enabled {
			if e.Name == want.alias {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("alias %q not served in Enabled(allowWrite=%v)", want.alias, !want.readOnly)
		}
		// Sanity: the full catalog carries the alias exactly once.
		count := 0
		for _, e := range all {
			if e.Name == want.alias {
				count++
			}
		}
		if count != 1 {
			t.Errorf("alias %q appears %d times in catalog, want 1", want.alias, count)
		}
	}
}

// TestLegacyAliasHandlerDelegation proves the alias handler behaves exactly
// like the canonical handler. Both are invoked with a failing service
// factory, so the returned error must be byte-identical — this is the
// tools/call contract check for the compatibility names.
func TestLegacyAliasHandlerDelegation(t *testing.T) {
	ctx := context.Background()
	registry := NewRegistry(RegistryOptions{
		Config:        &config.Config{Vault: "/test/vault"},
		ServerVersion: "test-version",
		AllowWrite:    true,
		GetService:    errorServiceFactory(),
	})

	for _, want := range legacyAliasContract {
		t.Run(want.alias, func(t *testing.T) {
			alias, ok := registry.Lookup(want.alias)
			if !ok {
				t.Fatalf("alias %q missing", want.alias)
			}
			canonical, ok := registry.Lookup(want.canonical)
			if !ok {
				t.Fatalf("canonical %q missing", want.canonical)
			}

			// `{}` is valid for every alias schema; the shared service
			// factory fails, so both handlers must return the identical
			// service error.
			_, aliasErr := alias.Handler(ctx, json.RawMessage(`{}`))
			_, canonicalErr := canonical.Handler(ctx, json.RawMessage(`{}`))

			if (aliasErr == nil) != (canonicalErr == nil) {
				t.Fatalf("alias err=%v canonical err=%v — delegation diverged", aliasErr, canonicalErr)
			}
			if aliasErr == nil {
				t.Fatal("expected service error from failing factory")
			}
			if aliasErr.Error() != canonicalErr.Error() {
				t.Errorf("alias error %q != canonical error %q", aliasErr, canonicalErr)
			}
		})
	}
}

// TestLegacyAliasCapabilities verifies the Capabilities() view (used by
// compatibility callers) carries the legacy names with the inherited class.
func TestLegacyAliasCapabilities(t *testing.T) {
	caps := Capabilities()
	for _, want := range legacyAliasContract {
		got, ok := caps[want.alias]
		if !ok {
			t.Errorf("Capabilities() missing alias %q", want.alias)
			continue
		}
		wantClass := ReadOnly
		if !want.readOnly {
			wantClass = Mutating
		}
		if got != wantClass {
			t.Errorf("Capabilities()[%q] = %q, want %q", want.alias, got, wantClass)
		}
	}
}
