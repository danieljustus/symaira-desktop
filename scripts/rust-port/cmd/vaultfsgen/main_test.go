package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

// The fixture is POSIX-complete: it records the symlink entries the Windows
// runner cannot create. The Windows leg therefore compares its corpus against the
// fixture without those entries, and that comparison has to hold — otherwise the
// push-only Windows job fails on every merge. This test builds the corpus with
// symlink support disabled, which is exactly what Windows does.
func TestCorpusWithoutSymlinksMatchesTheFixtureMinusSymlinkCases(t *testing.T) {
	restore := platformSupportsSymlinks
	platformSupportsSymlinks = func() bool { return false }
	defer func() { platformSupportsSymlinks = restore }()

	fixturePath := filepath.Join("..", "..", "..", "..", "testdata", "port", "vault", "filesystem.json")
	//nolint:gosec // the fixture path is fixed and repository-local
	fixture, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var recorded document
	if err := json.Unmarshal(fixture, &recorded); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	value, err := build(recorded.Oracle)
	if err != nil {
		t.Fatalf("build corpus without symlink support: %v", err)
	}
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("marshal corpus: %v", err)
	}
	content = append(content, '\n')
	expected, err := withoutSymlinkCases(fixture)
	if err != nil {
		t.Fatalf("drop symlink cases: %v", err)
	}
	if !bytes.Equal(expected, content) {
		t.Fatalf("the Windows comparison would drift: %s", inventory.FirstDifference(expected, content))
	}
	if len(value.Tree) != 14 || len(value.WalkAll) != 6 || len(value.WalkMarkdown) != 3 || len(value.SecurePaths) != 5 || len(value.ConfinedReads) != 0 {
		t.Fatalf("corpus without symlinks = %d tree, %d walk, %d markdown, %d secure, %d reads; want 14/6/3/5/0",
			len(value.Tree), len(value.WalkAll), len(value.WalkMarkdown), len(value.SecurePaths), len(value.ConfinedReads))
	}
}
