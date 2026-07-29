package service

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/compose"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

func TestComposeSearchAndRelated(t *testing.T) {
	// 1. Setup temp paths
	tempDir, err := os.MkdirTemp("", "symdesk-compose-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	vaultPath := filepath.Join(tempDir, "vault")
	_ = os.MkdirAll(vaultPath, 0755)

	notePath := filepath.Join(vaultPath, "compose-test-note.md")

	// 2. Setup mocks
	mockSeek := filepath.Join(tempDir, "symseek")
	mockSeekContent := fmt.Sprintf(`#!/bin/bash
if [ "$1" = "version" ] && [ "$2" = "--json" ]; then
  echo '{"tool":"symseek","version":"0.1.0-mock","schema_version":1}'
  exit 0
elif [ "$1" = "search" ]; then
  echo '[{"path":"%s","chunk_id":"mock-chunk-uuid","score":0.99,"snippet":"mock content snippet containing Mock Project keyword"}]'
  exit 0
else
  exit 0
fi
`, notePath)
	if err := os.WriteFile(mockSeek, []byte(mockSeekContent), 0755); err != nil {
		t.Fatal(err)
	}

	mockMemory := filepath.Join(tempDir, "symmemory")
	mockMemoryContent := `#!/bin/bash
if [ "$1" = "version" ] && [ "$2" = "--json" ]; then
  echo '{"tool":"symmemory","version":"0.1.0-mock","schema_version":1}'
  exit 0
elif [ "$1" = "entity" ] && [ "$2" = "list" ]; then
  echo '[{"id":"entity-1-uuid","name":"Mock Project","type":"project","aliases":["MockProj","AliasProj"],"description":"A project for testing"}]'
  exit 0
elif [ "$1" = "entity" ] && [ "$2" = "neighbors" ]; then
  echo '{"nodes":[{"id":"entity-1-uuid","name":"Mock Project","type":"project","aliases":["MockProj","AliasProj"]},{"id":"entity-2-uuid","name":"Alice","type":"person","aliases":[]}],"edges":[{"from_entity_id":"entity-2-uuid","to_entity_id":"entity-1-uuid","relation_type":"tester"}]}'
  exit 0
else
  exit 0
fi
`
	if err := os.WriteFile(mockMemory, []byte(mockMemoryContent), 0755); err != nil {
		t.Fatal(err)
	}

	// Override PATH
	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", tempDir+string(os.PathListSeparator)+oldPath)
	defer os.Setenv("PATH", oldPath)

	compose.ResetCache()

	// 3. Setup DB
	dbPath := filepath.Join(vaultPath, "sidecar.db")
	db, err := sidecar.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	svc := New(vaultPath, db)

	// Write mock file in vault matching search path
	noteContent := "---\ntitle: \"Compose Test Note\"\n---\nSome text mentioning Mock Project"
	if err := os.WriteFile(notePath, []byte(noteContent), 0644); err != nil {
		t.Fatal(err)
	}

	doc, err := vault.ParseFile(notePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.IndexDocument(doc); err != nil {
		t.Fatal(err)
	}

	// 4. Verify tool probing
	hasSeek, seekVer := compose.HasSymseek()
	if !hasSeek || seekVer != "0.1.0-mock" {
		t.Fatalf("expected HasSymseek to be true, got version %s", seekVer)
	}

	hasMem, memVer := compose.HasSymmemory()
	if !hasMem || memVer != "0.1.0-mock" {
		t.Fatalf("expected HasSymmemory to be true, got version %s", memVer)
	}

	// 5. Test Search Composition
	searchResults, err := svc.Search("Mock Project")
	if err != nil {
		t.Fatal(err)
	}
	if len(searchResults) == 0 {
		t.Fatalf("expected search results from composed symseek")
	}
	if searchResults[0].Title != "Compose Test Note" {
		t.Errorf("expected title to be resolved as 'Compose Test Note', got '%v'", searchResults[0].Title)
	}

	// 6. Test Related Composition
	relatedResults, err := svc.Related("compose-test-note.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(relatedResults.Entities) == 0 {
		t.Fatalf("expected related entities, got none")
	}
	foundAlice := false
	for _, e := range relatedResults.Entities {
		if e.Name == "Alice" && e.Relation == "tester" {
			foundAlice = true
		}
	}
	if !foundAlice {
		t.Errorf("expected neighbor 'Alice' with relation 'tester' to be resolved, got %v", relatedResults.Entities)
	}

	// 7. Test Graph Composition
	graphData, err := svc.Graph()
	if err != nil {
		t.Fatal(err)
	}
	foundEntityNode := false
	for _, node := range graphData.Nodes {
		if node.ID == "entity:Mock Project" {
			foundEntityNode = true
		}
	}
	if !foundEntityNode {
		t.Errorf("expected entity node 'entity:Mock Project' in enriched graph")
	}
}

func TestRelatedEntityMatchingBranches(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "symdesk-related-branches")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	vaultPath := filepath.Join(tempDir, "vault")
	_ = os.MkdirAll(vaultPath, 0755)
	vaultPath, err = filepath.EvalSymlinks(vaultPath)
	if err != nil {
		t.Fatal(err)
	}

	mockMemory := filepath.Join(tempDir, "symmemory")
	mockMemoryContent := `#!/bin/bash
if [ "$1" = "version" ] && [ "$2" = "--json" ]; then
  echo '{"tool":"symmemory","version":"0.1.0-mock","schema_version":1}'
  exit 0
elif [ "$1" = "entity" ] && [ "$2" = "list" ]; then
  echo '[{"id":"entity-1-uuid","name":"Project Alpha","type":"project","aliases":["MockProj"],"description":"A project"}]'
  exit 0
elif [ "$1" = "entity" ] && [ "$2" = "neighbors" ]; then
  echo '{"nodes":[{"id":"entity-2-uuid","name":"Alice","type":"person","aliases":[]}],"edges":[{"from_entity_id":"entity-2-uuid","to_entity_id":"entity-1-uuid","relation_type":"tester"}]}'
  exit 0
else
  exit 0
fi
`
	if err := os.WriteFile(mockMemory, []byte(mockMemoryContent), 0755); err != nil {
		t.Fatal(err)
	}

	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", tempDir+string(os.PathListSeparator)+oldPath)
	defer os.Setenv("PATH", oldPath)
	compose.ResetCache()

	dbPath := filepath.Join(vaultPath, "sidecar.db")
	db, err := sidecar.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	svc := New(vaultPath, db)

	notes := map[string]string{
		"main-note.md":      "---\ntitle: \"Project Alpha\"\n---\nMain note about Project Alpha",
		"Project Alpha.md":  "---\ntitle: \"FilenameMatch\"\n---\nFilename matches Project Alpha",
		"alias-note.md":     "---\ntitle: \"AliasNote\"\n---\nThis note mentions MockProj in the body",
		"body-note.md":      "---\ntitle: \"BodyNote\"\n---\nThis note mentions Project Alpha in the body",
		"neighbor-note.md":  "---\ntitle: \"NeighborNote\"\n---\nThis note mentions Alice in the body",
		"unrelated-note.md": "---\ntitle: \"UnrelatedNote\"\n---\nNothing relevant here",
	}

	for name, content := range notes {
		path := filepath.Join(vaultPath, name)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		doc, err := vault.ParseFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.IndexDocument(doc); err != nil {
			t.Fatal(err)
		}
	}

	related, err := svc.Related("main-note.md")
	if err != nil {
		t.Fatal(err)
	}

	entityNames := map[string]bool{}
	for _, e := range related.Entities {
		entityNames[e.Name] = true
	}
	if !entityNames["Project Alpha"] {
		t.Errorf("expected Project Alpha entity, got %v", related.Entities)
	}
	if !entityNames["Alice"] {
		t.Errorf("expected Alice neighbor entity, got %v", related.Entities)
	}

	foundRelation := false
	for _, e := range related.Entities {
		if e.Name == "Alice" && e.Relation == "tester" {
			foundRelation = true
		}
	}
	if !foundRelation {
		t.Errorf("expected Alice entity with relation 'tester', got %v", related.Entities)
	}

	noteNames := map[string]bool{}
	for _, n := range related.Notes {
		noteNames[filepath.Base(n)] = true
	}
	if !noteNames["Project Alpha.md"] {
		t.Errorf("expected filename-match note, got %v", related.Notes)
	}
	if !noteNames["alias-note.md"] {
		t.Errorf("expected alias-note, got %v", related.Notes)
	}
	if !noteNames["body-note.md"] {
		t.Errorf("expected body-note, got %v", related.Notes)
	}
	if !noteNames["neighbor-note.md"] {
		t.Errorf("expected neighbor-note, got %v", related.Notes)
	}
	if noteNames["main-note.md"] {
		t.Errorf("did not expect main-note (self-skip), got %v", related.Notes)
	}
	if noteNames["unrelated-note.md"] {
		t.Errorf("did not expect unrelated-note, got %v", related.Notes)
	}
}

func TestRelatedMainDocMatchBranches(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "symdesk-related-main-match")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	vaultPath := filepath.Join(tempDir, "vault")
	_ = os.MkdirAll(vaultPath, 0755)
	vaultPath, err = filepath.EvalSymlinks(vaultPath)
	if err != nil {
		t.Fatal(err)
	}

	mockMemory := filepath.Join(tempDir, "symmemory")
	mockMemoryContent := `#!/bin/bash
if [ "$1" = "version" ] && [ "$2" = "--json" ]; then
  echo '{"tool":"symmemory","version":"0.1.0-mock","schema_version":1}'
  exit 0
elif [ "$1" = "entity" ] && [ "$2" = "list" ]; then
  echo '[{"id":"entity-1-uuid","name":"Project Alpha","type":"project","aliases":["MockProj"],"description":"A project"}]'
  exit 0
elif [ "$1" = "entity" ] && [ "$2" = "neighbors" ]; then
  echo '{"nodes":[],"edges":[]}'
  exit 0
else
  exit 0
fi
`
	if err := os.WriteFile(mockMemory, []byte(mockMemoryContent), 0755); err != nil {
		t.Fatal(err)
	}

	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", tempDir+string(os.PathListSeparator)+oldPath)
	defer os.Setenv("PATH", oldPath)
	compose.ResetCache()

	dbPath := filepath.Join(vaultPath, "sidecar.db")
	db, err := sidecar.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	svc := New(vaultPath, db)

	writeNote := func(name, content string) {
		t.Helper()
		path := filepath.Join(vaultPath, name)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		doc, err := vault.ParseFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.IndexDocument(doc); err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		name     string
		file     string
		content  string
		expected []string
	}{
		{
			name:     "alias title match",
			file:     "alias-title.md",
			content:  "---\ntitle: \"MockProj\"\n---\nBody",
			expected: []string{"Project Alpha"},
		},
		{
			name:     "filename match",
			file:     "Project Alpha.md",
			content:  "---\ntitle: \"FilenameMatch\"\n---\nBody",
			expected: []string{"Project Alpha"},
		},
		{
			name:     "body name match",
			file:     "body-name.md",
			content:  "---\ntitle: \"BodyName\"\n---\nText about Project Alpha",
			expected: []string{"Project Alpha"},
		},
		{
			name:     "body alias match",
			file:     "body-alias.md",
			content:  "---\ntitle: \"BodyAlias\"\n---\nText about MockProj",
			expected: []string{"Project Alpha"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeNote(tc.file, tc.content)
			related, err := svc.Related(tc.file)
			if err != nil {
				t.Fatal(err)
			}
			entityNames := map[string]bool{}
			for _, e := range related.Entities {
				entityNames[e.Name] = true
			}
			for _, exp := range tc.expected {
				if !entityNames[exp] {
					t.Errorf("expected entity %q, got %v", exp, related.Entities)
				}
			}
		})
	}
}

func TestMatchesOtherBranches(t *testing.T) {
	entity := compose.MemoryEntity{
		ID:      "entity-1-uuid",
		Name:    "Project Alpha",
		Type:    "project",
		Aliases: []string{"MockProj"},
	}

	cases := []struct {
		name    string
		doc     *vault.Document
		matches bool
	}{
		{
			name: "title matches name",
			doc: &vault.Document{
				Title: "Project Alpha",
				Path:  filepath.Join("vault", "note.md"),
				Body:  "",
			},
			matches: true,
		},
		{
			name: "filename matches name",
			doc: &vault.Document{
				Title: "Other",
				Path:  filepath.Join("vault", "Project Alpha.md"),
				Body:  "",
			},
			matches: true,
		},
		{
			name: "title matches alias",
			doc: &vault.Document{
				Title: "MockProj",
				Path:  filepath.Join("vault", "note.md"),
				Body:  "",
			},
			matches: true,
		},
		{
			name: "body contains name",
			doc: &vault.Document{
				Title: "Other",
				Path:  filepath.Join("vault", "note.md"),
				Body:  "Text about Project Alpha",
			},
			matches: true,
		},
		{
			name: "body contains alias",
			doc: &vault.Document{
				Title: "Other",
				Path:  filepath.Join("vault", "note.md"),
				Body:  "Text about MockProj",
			},
			matches: true,
		},
		{
			name: "no match",
			doc: &vault.Document{
				Title: "Other",
				Path:  filepath.Join("vault", "note.md"),
				Body:  "Nothing relevant",
			},
			matches: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := matchesOther(tc.doc, entity)
			if got != tc.matches {
				t.Errorf("matchesOther() = %v, want %v", got, tc.matches)
			}
		})
	}
}

func TestComposeFallback(t *testing.T) {
	// Ensure tools are NOT on PATH
	tempDir, err := os.MkdirTemp("", "symdesk-compose-fallback-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", tempDir) // empty path
	defer os.Setenv("PATH", oldPath)

	compose.ResetCache()

	vaultPath := filepath.Join(tempDir, "vault")
	_ = os.MkdirAll(vaultPath, 0755)

	dbPath := filepath.Join(vaultPath, "sidecar.db")
	db, err := sidecar.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	svc := New(vaultPath, db)

	// Write mock file in vault
	notePath := filepath.Join(vaultPath, "compose-test-note.md")
	noteContent := "---\ntitle: \"Compose Test Note\"\n---\nSome text mentioning Mock Project"
	if err := os.WriteFile(notePath, []byte(noteContent), 0644); err != nil {
		t.Fatal(err)
	}

	doc, err := vault.ParseFile(notePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.IndexDocument(doc); err != nil {
		t.Fatal(err)
	}

	// Test Search Fallback to FTS5
	searchResults, err := svc.Search("mock")
	if err != nil {
		t.Fatal(err)
	}
	if len(searchResults) != 1 || searchResults[0].Title != "Compose Test Note" {
		t.Errorf("expected fallback FTS search to find the note, got %v", searchResults)
	}

	// Test Related Fallback (should return empty result cleanly)
	relatedResults, err := svc.Related("compose-test-note.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(relatedResults.Entities) != 0 || len(relatedResults.Notes) != 0 {
		t.Errorf("expected empty related data on fallback, got %+v", relatedResults)
	}
}
