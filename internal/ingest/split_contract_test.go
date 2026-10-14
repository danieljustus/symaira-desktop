package ingest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacySplitToolContract(t *testing.T) {
	for _, qpdf := range []bool{false, true} {
		name := "poppler-only"
		if qpdf {
			name = "qpdf-only"
		}
		t.Run(name, func(t *testing.T) {
			dir, out := t.TempDir(), t.TempDir()
			if qpdf {
				writeMockScript(t, dir, "qpdf", `
if [ "$1" = --show-npages ]; then printf '2\n'; exit 0; fi
if [ "$1" = --check ]; then exit 0; fi
printf 'controlled output' > "$6"
`)
			} else {
				for _, tool := range []string{"pdfinfo", "pdfseparate", "pdfunite"} {
					writeMockScript(t, dir, tool, "exit 0\n")
				}
			}
			t.Setenv("PATH", dir)
			if hasSplitTools() != qpdf || HasQPDF() != qpdf || HasPDFSplit() {
				t.Fatal("availability disagrees with required tools")
			}
			input := filepath.Join(dir, "input.pdf")
			if err := os.WriteFile(input, []byte("controlled input"), 0o600); err != nil {
				t.Fatal(err)
			}
			parts, err := SplitPDF(input, []int{1}, out)
			if qpdf {
				if err != nil || len(parts) != 2 {
					t.Fatalf("qpdf-only split: %v %v", parts, err)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), "qpdf not found") || len(parts) != 0 {
					t.Fatalf("Poppler-only split: %v %v", parts, err)
				}
				entries, err := os.ReadDir(out)
				if err != nil || len(entries) != 0 {
					t.Fatalf("missing qpdf published files: %v %v", entries, err)
				}
				// Copy-only behavior does not acquire a new tool dependency.
				parts, err := SplitPDF(input, nil, out)
				if err != nil || len(parts) != 1 {
					t.Fatalf("no-tool copy: %v %v", parts, err)
				}
			}
		})
	}
}
