package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderFixtureMirrors(t *testing.T) {
	root := t.TempDir()
	for _, pair := range renderFixtureMirrors {
		writePortgenTestFile(t, root, pair.source, "fresh Go capture\n")
		writePortgenTestFile(t, root, pair.mirror, "stale copy\n")
	}
	if err := validateFixtureCheckCoverage(); err != nil {
		t.Fatal(err)
	}
	if err := syncRenderFixtureMirrors(root, false); err == nil {
		t.Fatal("check accepted stale copies")
	}
	if err := syncRenderFixtureMirrors(root, true); err != nil {
		t.Fatal(err)
	}
	if err := syncRenderFixtureMirrors(root, false); err != nil {
		t.Fatal(err)
	}
	for _, pair := range renderFixtureMirrors {
		path := filepath.Join(root, pair.mirror)
		if !isPortDerivedOutput(pair.mirror) {
			t.Fatalf("copy omitted from artifact allowlist: %s", pair.mirror)
		}
		// #nosec G304 -- fixed mirror registry destination inside t.TempDir().
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "fresh Go capture\n" {
			t.Fatalf("generation did not copy actual source bytes: %s: %v", pair.mirror, err)
		}
		writePortgenTestFile(t, root, pair.mirror, "mutated copy\n")
		if err := syncRenderFixtureMirrors(root, false); err == nil || !strings.Contains(err.Error(), pair.mirror) {
			t.Fatalf("mutated copy was not rejected: %s: %v", pair.mirror, err)
		}
		// #nosec G304 -- fixed mirror registry destination inside t.TempDir().
		data, err = os.ReadFile(path)
		if err != nil || string(data) != "mutated copy\n" {
			t.Fatal("read-only check rewrote the mutated input")
		}
		writePortgenTestFile(t, root, pair.mirror, "fresh Go capture\n")
	}
	pair := renderFixtureMirrors[0]
	path := filepath.Join(root, pair.mirror)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, pair.source), path); err != nil {
		t.Fatal(err)
	}
	if err := syncRenderFixtureMirrors(root, true); err == nil {
		t.Fatal("generation followed a symlink destination")
	}
}
