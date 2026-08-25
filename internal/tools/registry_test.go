package tools

import (
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/config"
)

func TestRegistryCatalogOrderAndCapabilities(t *testing.T) {
	registry := NewRegistry(RegistryOptions{
		Config:        &config.Config{Vault: "/test/vault"},
		ServerVersion: "test-version",
		AllowWrite:    true,
	})

	wantNames := []string{
		"desk_status",
		"desk_ls",
		"desk_search",
		"vault_timeline",
		"desk_props",
		"desk_backlinks",
		"desk_ask",
		"desk_transform",
		"desk_docs",
		"docs_review",
		"docs_similar",
		"vault_health",
		"desk_related",
		"desk_diagram",
		"desk_ingest_jobs",
		"meeting_list",
		"meeting_get",
		"desk_read_result",
		"notebook_list",
		"notebook_get",
		"notebook_ask",
		"desk_undo_task",
		"desk_note_new",
		"desk_ingest",
		"doc_set_status",
		"desk_ingest_retry",
		"desk_clip",
		"desk_export",
		"notebook_create",
		"notebook_add_source",
		"notebook_remove_source",
		"desk_autofill",
		"desk_asset_store",
		// Legacy compatibility aliases (issue #598) — appended after the
		// canonical catalog, in NewRegistry order.
		"ingest_file",
		"list_jobs",
		"retry_job",
		"list_documents",
		"search_documents",
	}
	wantReadOnly := map[string]bool{
		"desk_status":      true,
		"desk_ls":          true,
		"desk_search":      true,
		"vault_timeline":   true,
		"desk_props":       true,
		"desk_backlinks":   true,
		"desk_ask":         true,
		"desk_transform":   true,
		"desk_docs":        true,
		"docs_review":      true,
		"docs_similar":     true,
		"vault_health":     true,
		"desk_related":     true,
		"desk_diagram":     true,
		"desk_ingest_jobs": true,
		"meeting_list":     true,
		"meeting_get":      true,
		"desk_read_result": true,
		"notebook_list":    true,
		"notebook_get":     true,
		"notebook_ask":     true,
		"list_jobs":        true,
		"list_documents":   true,
		"search_documents": true,
	}

	entries := registry.All()
	if len(entries) != len(wantNames) {
		t.Fatalf("expected %d canonical tools, got %d", len(wantNames), len(entries))
	}
	for i, entry := range entries {
		if entry.Name != wantNames[i] {
			t.Errorf("tool %d: expected %q, got %q", i, wantNames[i], entry.Name)
		}
		if entry.ReadOnly != wantReadOnly[entry.Name] {
			t.Errorf("tool %q: read-only=%v, want %v", entry.Name, entry.ReadOnly, wantReadOnly[entry.Name])
		}
		if entry.Description == "" || len(entry.InputSchema) == 0 || entry.Handler == nil {
			t.Errorf("tool %q is missing description, schema, or handler", entry.Name)
		}
		if got, ok := registry.Lookup(entry.Name); !ok || got.Name != entry.Name {
			t.Errorf("lookup failed for canonical tool %q", entry.Name)
		}
	}

	if got := len(registry.Enabled(false)); got != len(wantReadOnly) {
		t.Errorf("read-only registry has %d tools, want %d", got, len(wantReadOnly))
	}
	if got := len(registry.Enabled(true)); got != len(wantNames) {
		t.Errorf("read-write registry has %d tools, want %d", got, len(wantNames))
	}
}

func TestCapabilitiesDeriveFromRegistry(t *testing.T) {
	capabilities := Capabilities()
	registry := NewRegistry(RegistryOptions{})

	if len(capabilities) != len(registry.All()) {
		t.Fatalf("capability count=%d, registry count=%d", len(capabilities), len(registry.All()))
	}
	for _, entry := range registry.All() {
		want := Mutating
		if entry.ReadOnly {
			want = ReadOnly
		}
		if got := capabilities[entry.Name]; got != want {
			t.Errorf("tool %q: capability=%q, want %q", entry.Name, got, want)
		}
	}
}
