package tools

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

const symdeskToolsFixtureRel = "../../testdata/port/mcp/symdesk-tools.json"

func TestSymdeskMCPInventory(t *testing.T) {
	oracle := inventory.Oracle{
		Commit:  "ae86331930fdfa2b128b68ae5af7437091b9949a",
		Release: "v0.12.2",
	}

	doc := buildSymDeskMCPDocument(oracle)
	if len(doc.Tools) != 57 {
		t.Fatalf("expected 57 tools in canonical registry, got %d", len(doc.Tools))
	}

	content, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("marshal mcp document: %v", err)
	}
	content = append(content, '\n')

	fixturePath := filepath.Clean(symdeskToolsFixtureRel)
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(fixturePath), 0o750); err != nil {
			t.Fatalf("mkdir fixture dir: %v", err)
		}
		if err := os.WriteFile(fixturePath, content, 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		t.Logf("Wrote %s with %d tools", fixturePath, len(doc.Tools))
		return
	}

	existing, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read fixture %s: %v (run make port-fixtures-generate to generate)", fixturePath, err)
	}
	if !bytes.Equal(existing, content) {
		t.Fatalf("SymDesk MCP inventory has drifted from %s; run make port-fixtures-generate", fixturePath)
	}
}

func buildSymDeskMCPDocument(oracle inventory.Oracle) inventory.MCPToolDocument {
	registry := NewRegistry(RegistryOptions{
		Config:        &config.Config{Vault: "/test/vault"},
		ServerVersion: "0.12.2",
		AllowWrite:    true,
	})

	enabled := registry.Enabled(true)
	tools := make([]inventory.MCPToolSpec, len(enabled))
	for i, entry := range enabled {
		isAlias := i >= 45 // The 12 legacy aliases are entries 46..57
		tools[i] = inventory.MCPToolSpec{
			Name:             entry.Name,
			Order:            i + 1,
			Description:      entry.Description,
			InputSchema:      entry.InputSchema,
			ReadOnly:         entry.ReadOnly,
			Destructive:      entry.Destructive,
			IsAlias:          isAlias,
			ApprovalGranting: false,
		}
	}

	return inventory.MCPToolDocument{
		SchemaVersion: 1,
		Oracle:        oracle,
		ServerName:    "symdesk",
		ServerVersion: "0.12.2",
		Tools:         tools,
	}
}

func TestBuildSymDeskMCPDocumentIsDeterministic(t *testing.T) {
	oracle := inventory.Oracle{
		Commit:  "test-commit",
		Release: "v0.0.0",
	}
	first, err := json.Marshal(buildSymDeskMCPDocument(oracle))
	if err != nil {
		t.Fatal(err)
	}
	second, err := json.Marshal(buildSymDeskMCPDocument(oracle))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("SymDesk MCP document generation is not deterministic")
	}
}
