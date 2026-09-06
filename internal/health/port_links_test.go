package health

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

type portHealthLinkInventory struct {
	Paths       []string `json:"paths"`
	Titles      []string `json:"titles"`
	Aliases     []string `json:"aliases"`
	Attachments []string `json:"attachments"`
}

type portHealthLinkCase struct {
	Name       string                  `json:"name"`
	Raw        string                  `json:"raw"`
	Inventory  portHealthLinkInventory `json:"inventory"`
	Normalized string                  `json:"normalized"`
	Checked    bool                    `json:"checked"`
	Exists     bool                    `json:"exists"`
}

type portHealthLinkFixture struct {
	SchemaVersion int                  `json:"schema_version"`
	Cases         []portHealthLinkCase `json:"cases"`
}

func TestHealthLinkResolutionInventory(t *testing.T) {
	base := portHealthLinkInventory{
		Paths:       []string{"folder/note.md", "folder/note", "note.md", "note"},
		Titles:      []string{"human title"},
		Aliases:     []string{"alias name", "alias.pdf", "deep/alias.pdf", "alias"},
		Attachments: []string{"assets/photo.png", "photo.png", "docs/manual.pdf", "manual.pdf", "assets/überblick_日本.png", "überblick_日本.png"},
	}
	rawCases := []struct {
		name string
		raw  string
	}{
		{"document-path", "folder/note.md"},
		{"document-without-extension", "folder/note"},
		{"document-title", "Human Title.md"},
		{"attachment-path", "assets/PHOTO.PNG"},
		{"attachment-basename", "other/photo.png"},
		{"attachment-pdf", "docs/manual.pdf|Manual"},
		{"attachment-unicode", "other/ÜBERBLICK_日本.PNG"},
		{"alias-exact", "Alias Name.md"},
		{"alias-with-extension", "deep/alias.pdf"},
		{"alias-basename", "elsewhere/alias.pdf"},
		{"fragment-and-alias", "folder/note.md#Heading|Label"},
		{"relative-clean", "./folder/../folder/note.md"},
		{"missing-document", "folder/missing.md"},
		{"missing-attachment", "assets/missing.png"},
		{"ignored-bare-name", "Person Name"},
		{"ignored-http", "https://example.com/a.pdf"},
		{"ignored-mailto", "mailto:user@example.com"},
		{"ignored-empty-fragment", "#Heading"},
	}
	fixture := portHealthLinkFixture{SchemaVersion: 1, Cases: make([]portHealthLinkCase, 0, len(rawCases))}
	for _, item := range rawCases {
		inventory := cloneHealthInventory(base)
		normalized := normalizeLinkTarget(item.raw)
		checked := normalized != ""
		exists := false
		if checked {
			exists = linkExists(normalized, toSet(inventory.Paths), toSet(inventory.Titles), toSet(inventory.Aliases), toSet(inventory.Attachments))
		}
		fixture.Cases = append(fixture.Cases, portHealthLinkCase{
			Name: item.name, Raw: item.raw, Inventory: inventory,
			Normalized: normalized, Checked: checked, Exists: exists,
		})
	}

	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	fixturePath := filepath.Join("..", "..", "testdata", "port", "vault", "health-links.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.WriteFile(fixturePath, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	//nolint:gosec // fixturePath is a fixed repo-relative path
	current, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, encoded) {
		t.Fatal("health link fixture is stale; run make port-fixtures-generate")
	}
}

func cloneHealthInventory(input portHealthLinkInventory) portHealthLinkInventory {
	output := input
	output.Paths = append([]string(nil), input.Paths...)
	output.Titles = append([]string(nil), input.Titles...)
	output.Aliases = append([]string(nil), input.Aliases...)
	output.Attachments = append([]string(nil), input.Attachments...)
	sort.Strings(output.Paths)
	sort.Strings(output.Titles)
	sort.Strings(output.Aliases)
	sort.Strings(output.Attachments)
	return output
}

func toSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}
