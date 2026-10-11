package pdfops

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExclusiveOutputPublication(t *testing.T) {
	for _, kind := range []string{"same", "symlink", "hardlink", "existing", "raced"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			input, output := filepath.Join(dir, "input.pdf"), filepath.Join(dir, "out.pdf")
			original := []byte("original input, never replaced")
			if err := os.WriteFile(input, original, 0o600); err != nil {
				t.Fatal(err)
			}
			if kind == "raced" {
				if err := newOutputs([]string{input}, []string{output}); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "same":
				output = input
			case "symlink":
				if err := os.Symlink(input, output); err != nil {
					t.Fatal(err)
				}
			case "hardlink", "raced":
				if err := os.Link(input, output); err != nil {
					t.Fatal(err)
				}
			case "existing":
				if err := os.WriteFile(output, []byte("other original"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if kind != "raced" {
				err := newOutputs([]string{input}, []string{output})
				if err == nil || !strings.Contains(err.Error(), "PDF output") {
					t.Fatalf("preflight accepted %s: %v", kind, err)
				}
			}
			staged := filepath.Join(dir, "staged.pdf")
			if err := os.WriteFile(staged, []byte("replacement"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := publish(context.Background(), staged, output); err == nil {
				t.Fatal("publication overwrote a destination")
			}
			got, err := os.ReadFile(input)
			if err != nil || string(got) != string(original) {
				t.Fatalf("input changed: %q %v", got, err)
			}
		})
	}
}
