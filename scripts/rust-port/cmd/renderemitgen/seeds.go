package main

import (
	"bytes"
	"fmt"
	parsepkg "github.com/danieljustus/symaira-desktop/internal/draw/parse"
	"os"
	"path/filepath"
)

// Seeds are parser inputs accepted by the production Go parser, not invented
// expected outputs. Check mode verifies every tracked seed byte-for-byte.
func parserSeeds(root string, check bool) error {
	seeds := []struct{ name, input string }{
		{"graph", "graph TD\n A[Start] --> B{Ready}\n B --> C[Done]"},
		{"sequence", "sequenceDiagram\n participant A as Alice\n participant B as Bob\n A->>B: Hello"},
		{"json", `{"kind":"graph","nodes":[{"id":"a","label":"Unicode 日本語 <&"}],"custom":{"nested":[null,true,{"x":1}]}}`},
	}
	for _, s := range seeds {
		if _, err := parsepkg.Source(s.input); err != nil {
			return err
		}
		path := filepath.Join(root, "fuzz/seeds/draw_parser", s.name)
		data := []byte(s.input)
		if check {
			old, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if !bytes.Equal(old, data) {
				return fmt.Errorf("parser seed %s is stale", s.name)
			}
		} else {
			if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
				return err
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				return err
			}
		}
	}
	return nil
}
