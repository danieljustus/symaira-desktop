package archive

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/api"
)

func TestGenerateImageAndExistingPDF(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))

	var imageBytes bytes.Buffer
	if err := png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(dir, "scan.png")
	if err := os.WriteFile(input, imageBytes.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"image", "existing-pdf"} {
		output := filepath.Join(dir, name+".pdf")
		if err := Generate(input, output, "Archive regression text"); err != nil {
			t.Fatalf("%s: generate archive: %v", name, err)
		}
		if err := api.ValidateFile(context.Background(), output, nil, nil); err != nil {
			t.Fatalf("%s: validate archive: %v", name, err)
		}
		pages, err := api.PageCountFile(context.Background(), output)
		if err != nil || pages != 1 {
			t.Fatalf("%s: page count = %d, error = %v; want one page", name, pages, err)
		}
		input = output
	}
}
