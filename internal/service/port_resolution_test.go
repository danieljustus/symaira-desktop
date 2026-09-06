package service

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/compose"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

const vaultResolutionFixtureRel = "../../testdata/port/vault/resolution.json"

type resolutionFixture struct {
	SchemaVersion int              `json:"schema_version"`
	Oracle        inventory.Oracle `json:"oracle"`
	Documents     []resolutionDoc  `json:"documents"`
	Nodes         []GraphNode      `json:"nodes"`
	Edges         []GraphEdge      `json:"edges"`
}

type resolutionDoc struct {
	Path    string   `json:"path"`
	Title   string   `json:"title"`
	Aliases []string `json:"aliases,omitempty"`
	Links   []string `json:"links,omitempty"`
}

func TestVaultResolutionInventory(t *testing.T) {
	documents := []resolutionDoc{
		{Path: "folder/path-target.md", Title: "Path Title", Aliases: []string{"Path Alias"}},
		{Path: "other/base-target.md", Title: "Different Title"},
		{Path: "titles/title-file.md", Title: "Human Title"},
		{Path: "unicode/日本.md", Title: "Überblick", Aliases: []string{"Résumé"}},
		{Path: "aliases/alias-file.md", Title: "Alias Owner", Aliases: []string{"Alt Name", "Alias.ext"}},
		{Path: "sources/path.md", Title: "Source Path", Links: []string{"folder/path-target"}},
		{Path: "sources/path-case-ext.md", Title: "Source Path Case", Links: []string{"FOLDER/PATH-TARGET.MD"}},
		{Path: "sources/base.md", Title: "Source Base", Links: []string{"base-target"}},
		{Path: "sources/title.md", Title: "Source Title", Links: []string{"Human Title"}},
		{Path: "sources/alias.md", Title: "Source Alias", Links: []string{"alt name"}},
		{Path: "sources/alias-extension.md", Title: "Source Alias Extension", Links: []string{"Alias"}},
		{Path: "sources/missing.md", Title: "Source Missing", Links: []string{"Missing Target"}},
		{Path: "sources/unicode.md", Title: "Source Unicode", Links: []string{"RÉSUMÉ"}},
	}
	root := t.TempDir()
	if canonical, err := filepath.EvalSymlinks(root); err == nil {
		root = canonical
	}
	for _, item := range documents {
		path := filepath.Join(root, filepath.FromSlash(item.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		content := "---\ntitle: \"" + item.Title + "\"\n"
		if len(item.Aliases) > 0 {
			encoded, err := json.Marshal(item.Aliases)
			if err != nil {
				t.Fatal(err)
			}
			content += "aliases: " + string(encoded) + "\n"
		}
		content += "---\n"
		for _, link := range item.Links {
			content += "[[" + link + "]]\n"
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	db, err := sidecar.Open(filepath.Join(t.TempDir(), "sidecar.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.RefreshIndex(root); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	compose.ResetCache()
	t.Cleanup(compose.ResetCache)
	graph, err := New(root, db).Graph()
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(graph.Nodes, func(i, j int) bool { return graph.Nodes[i].ID < graph.Nodes[j].ID })
	sort.Slice(graph.Edges, func(i, j int) bool {
		if graph.Edges[i].Source == graph.Edges[j].Source {
			return graph.Edges[i].Target < graph.Edges[j].Target
		}
		return graph.Edges[i].Source < graph.Edges[j].Source
	})
	fixture := resolutionFixture{SchemaVersion: 1, Oracle: inventory.Oracle{Commit: "ae86331930fdfa2b128b68ae5af7437091b9949a", Release: "v0.12.2"}, Documents: documents, Nodes: graph.Nodes, Edges: graph.Edges}
	content, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	content = append(content, '\n')
	path := filepath.Clean(vaultResolutionFixtureRel)
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	existing, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v (run make vault-fixtures-generate)", err)
	}
	if !bytes.Equal(existing, content) {
		t.Fatal("vault resolution fixture drift; regenerate deliberately")
	}
}
