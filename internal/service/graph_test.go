package service

import (
	"path/filepath"
	"sort"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/compose"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

const mockSymmemoryGraphScript = `#!/bin/bash
if [ "$1" = "entity" ] && [ "$2" = "list" ]; then
  echo '[{"id":"e-alice","name":"Alice","type":"person","aliases":[],"description":""},{"id":"e-bob","name":"Bob","type":"person","aliases":[],"description":""}]'
elif [ "$1" = "entity" ] && [ "$2" = "neighbors" ]; then
  if [ "$3" = "Alice" ]; then
    echo '{"nodes":[{"id":"e-bob","name":"Bob","type":"person","aliases":[],"description":""}],"edges":[{"from_entity_id":"e-alice","to_entity_id":"e-bob","relation_type":"knows"}]}'
  elif [ "$3" = "Bob" ]; then
    echo '{"nodes":[{"id":"e-alice","name":"Alice","type":"person","aliases":[],"description":""}],"edges":[{"from_entity_id":"e-alice","to_entity_id":"e-bob","relation_type":"knows"}]}'
  fi
fi
`

func graphNodeIDs(t *testing.T, data *GraphData) []string {
	t.Helper()
	ids := make([]string, 0, len(data.Nodes))
	for _, n := range data.Nodes {
		ids = append(ids, n.ID)
	}
	sort.Strings(ids)
	return ids
}

func countEdge(data *GraphData, source, target string) int {
	count := 0
	for _, e := range data.Edges {
		if e.Source == source && e.Target == target {
			count++
		}
	}
	return count
}

// Both notes mention Alice and Bob, so each entity matches two documents and
// each entity's neighbor lookup surfaces the same Alice<->Bob relation from
// the opposite direction. The graph must still contain each entity node and
// the relation edge exactly once.
func TestGraphDeduplicatesEntityNodesAndRelationEdges(t *testing.T) {
	dir := t.TempDir()
	writeMockSymmemory(t, dir, mockSymmemoryGraphScript)
	withMockSymmemoryPath(t, dir)

	svc := newTestService(t)
	if _, err := svc.NoteNew("Standup", "Alice gave an update. Bob was absent.", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.NoteNew("Team Update", "Alice and Bob discussed the project.", ""); err != nil {
		t.Fatal(err)
	}

	data, err := svc.Graph()
	if err != nil {
		t.Fatal(err)
	}

	ids := graphNodeIDs(t, data)
	aliceCount, bobCount := 0, 0
	for _, id := range ids {
		if id == "entity:Alice" {
			aliceCount++
		}
		if id == "entity:Bob" {
			bobCount++
		}
	}
	if aliceCount != 1 {
		t.Errorf("expected exactly one entity:Alice node, got %d (nodes: %v)", aliceCount, ids)
	}
	if bobCount != 1 {
		t.Errorf("expected exactly one entity:Bob node, got %d (nodes: %v)", bobCount, ids)
	}

	forward := countEdge(data, "entity:Alice", "entity:Bob")
	backward := countEdge(data, "entity:Bob", "entity:Alice")
	if forward+backward != 1 {
		t.Errorf("expected the Alice<->Bob relation exactly once, got forward=%d backward=%d", forward, backward)
	}

	// Both source documents matched Alice, so both must have an edge into
	// the entity node.
	if countEdge(data, "Standup.md", "entity:Alice") != 1 {
		t.Error("expected Standup.md to link to entity:Alice")
	}
	if countEdge(data, "Team_Update.md", "entity:Alice") != 1 {
		t.Error("expected Team_Update.md to link to entity:Alice")
	}
}

func TestGraphWithoutSymmemoryReturnsDocumentNodesOnly(t *testing.T) {
	// A bare-bones PATH, not a prepended one: the real symmemory installed
	// on the dev machine must not leak into this "unavailable" scenario.
	t.Setenv("PATH", "/usr/bin:/bin")
	compose.ResetCache()
	t.Cleanup(compose.ResetCache)

	svc := newTestService(t)
	if _, err := svc.NoteNew("Solo Note", "Nothing special here.", ""); err != nil {
		t.Fatal(err)
	}

	data, err := svc.Graph()
	if err != nil {
		t.Fatal(err)
	}

	if len(data.Nodes) != 1 || data.Nodes[0].ID != "Solo_Note.md" {
		t.Errorf("expected a single document node, got %+v", data.Nodes)
	}
	if len(data.Edges) != 0 {
		t.Errorf("expected no edges, got %+v", data.Edges)
	}
}

func TestGraphResolvesWikilinksThroughAliases(t *testing.T) {
	svc := newTestService(t)

	// Note A with aliases
	agencyDoc := &vault.Document{
		Path:    filepath.Join(svc.VaultRoot, "Bundesagentur.md"),
		Title:   "Bundesagentur für Arbeit",
		Aliases: []string{"BA", "Federal Agency"},
		Created: "2026-01-01T00:00:00Z",
		SHA256:  "h-ba",
		Body:    "Agency details",
	}
	if err := svc.DB.IndexDocument(agencyDoc); err != nil {
		t.Fatal(err)
	}

	// Note B linking to [[BA]]
	sourceDoc := &vault.Document{
		Path:    filepath.Join(svc.VaultRoot, "Source.md"),
		Title:   "Source",
		Created: "2026-01-01T00:00:00Z",
		SHA256:  "h-src",
		Links:   []string{"BA"},
	}
	if err := svc.DB.IndexDocument(sourceDoc); err != nil {
		t.Fatal(err)
	}

	// Note C linking to multi-word alias [[Federal Agency]]
	otherDoc := &vault.Document{
		Path:    filepath.Join(svc.VaultRoot, "Other.md"),
		Title:   "Other",
		Created: "2026-01-01T00:00:00Z",
		SHA256:  "h-oth",
		Links:   []string{"Federal Agency"},
	}
	if err := svc.DB.IndexDocument(otherDoc); err != nil {
		t.Fatal(err)
	}

	data, err := svc.Graph()
	if err != nil {
		t.Fatalf("Graph failed: %v", err)
	}

	if countEdge(data, "Source.md", "Bundesagentur.md") != 1 {
		t.Errorf("expected edge Source.md -> Bundesagentur.md, got edges: %+v", data.Edges)
	}
	if countEdge(data, "Other.md", "Bundesagentur.md") != 1 {
		t.Errorf("expected edge Other.md -> Bundesagentur.md, got edges: %+v", data.Edges)
	}
}
