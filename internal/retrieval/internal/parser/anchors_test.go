package parser

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseFileSectionsPreservesHeadingPathAndOffsets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.md")
	content := "intro\n\n# Guide\n\nfirst\n\n## Install\n\nsecond\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	sections, err := ParseFileSections(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) != 3 {
		t.Fatalf("got %d sections, want 3", len(sections))
	}
	if got := sections[1].Anchor; got.Kind != "heading" || got.Value != "Guide" {
		t.Fatalf("got heading anchor %#v", got)
	}
	if got := sections[2].Anchor; got.Kind != "heading" || got.Value != "Guide > Install" {
		t.Fatalf("got nested heading anchor %#v", got)
	}
	if sections[2].Start != len("intro\n\n# Guide\n\nfirst\n\n") {
		t.Fatalf("got section start %d", sections[2].Start)
	}
}
