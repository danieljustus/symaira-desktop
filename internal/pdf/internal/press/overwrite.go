package press

import (
	"fmt"
	"os"
)

// CheckOverwrite is the single overwrite policy for every output surface
// (MCP render_pdf and the CLI render command). Existing PDF files are
// replaced normally; existing non-PDF files are only replaced when overwrite
// is set, and directories are always refused. Failing closed on non-PDF
// output prevents a render from silently clobbering unrelated files.
func CheckOverwrite(path string, overwrite bool) error {
	fi, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return nil
	}
	if fi.IsDir() {
		return fmt.Errorf("output path %q is a directory", path)
	}

	isPDF, err := hasPDFHeader(path)
	if err != nil {
		return fmt.Errorf("could not read existing file: %w", err)
	}

	if !isPDF && !overwrite {
		return fmt.Errorf("refusing to overwrite existing non-PDF file %q; pass overwrite to force", path)
	}
	return nil
}

func hasPDFHeader(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	buf := make([]byte, 4)
	n, err := f.Read(buf)
	if err != nil {
		return false, nil
	}
	if n < 4 {
		return false, nil
	}
	return string(buf) == "%PDF", nil
}
