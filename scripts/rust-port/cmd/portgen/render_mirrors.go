package main

import (
	"bytes"
	"fmt"
	"os"
)

// These versioned consumer inputs are exact copies of the live Go captures,
// not historical observations. Generate from fresh captures, never relabel JSON.
var renderFixtureMirrors = []struct{ source, mirror string }{
	{"testdata/port/render/html-note.json", "testdata/port/render/v2/html-note.json"},
	{"testdata/port/render/json-ir.json", "testdata/port/render/v2/json-ir.json"},
	{"testdata/port/cli/symdesk-command-tree.json", "testdata/port/render/v2/symdesk-command-tree.json"},
	{"testdata/port/mcp/symdesk-tools.json", "testdata/port/render/v2/symdesk-tools.json"},
}

func syncRenderFixtureMirrors(repoRoot string, generate bool) error {
	var paths []string
	for _, pair := range renderFixtureMirrors {
		paths = append(paths, pair.source, pair.mirror)
	}
	if err := validateDestinations(repoRoot, paths); err != nil {
		return err
	}
	root, err := os.OpenRoot(repoRoot)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	for _, pair := range renderFixtureMirrors {
		observed, err := root.ReadFile(pair.source)
		if err != nil {
			return err
		}
		if generate {
			if err := root.WriteFile(pair.mirror, observed, 0o600); err != nil {
				return err
			}
			continue
		}
		mirrored, err := root.ReadFile(pair.mirror)
		if err != nil {
			return err
		}
		if !bytes.Equal(observed, mirrored) {
			return fmt.Errorf("versioned fixture %s differs from live Go capture %s", pair.mirror, pair.source)
		}
	}
	return nil
}
