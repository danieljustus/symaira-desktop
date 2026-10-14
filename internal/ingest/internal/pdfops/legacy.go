package pdfops

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/danieljustus/symaira-desktop/internal/pdfprocess"
)

// SplitLegacy retains the barcode caller's qpdf-only contract and part-N names,
// but shares the bounded stage/validate/publish operation with Split.
func (t Tools) SplitLegacy(ctx context.Context, input string, boundaries []int, outputDir string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, pdfprocess.Timeout)
	defer cancel()
	if err := requireFile(input); err != nil {
		return nil, err
	}
	if len(boundaries) >= maxPages {
		return nil, fmt.Errorf("PDF split exceeds 10000 parts")
	}
	path, err := toolPath(t.QPDF, "qpdf")
	if err != nil {
		return nil, err
	}
	stdout, err := pdfprocess.Run(ctx, path, "--show-npages", input)
	if err != nil {
		return nil, err
	}
	pages, err := strconv.Atoi(strings.TrimSpace(string(stdout)))
	if err != nil || pages <= 0 {
		return nil, fmt.Errorf("qpdf did not report a positive page count for %s", input)
	}
	if pages > maxPages {
		return nil, fmt.Errorf("PDF page count exceeds 10000 pages")
	}
	previous := 0
	for _, boundary := range boundaries {
		if boundary <= previous || boundary >= pages {
			return nil, fmt.Errorf("split boundaries must increase within the document; got %d after %d for %d pages", boundary, previous, pages)
		}
		previous = boundary
	}
	ends := append(append([]int(nil), boundaries...), pages)
	outputs := make([]string, len(ends))
	for i := range ends {
		outputs[i] = filepath.Join(outputDir, fmt.Sprintf("part-%d.pdf", i+1))
	}
	return t.splitParts(ctx, input, ends, outputs, outputDir, true)
}

// Copy preserves the legacy no-split/no-tool behavior without overwriting a
// destination or allocating the entire input in memory.
func Copy(input, output string) error {
	if err := requireFile(input); err != nil {
		return err
	}
	if err := newOutputs([]string{input}, []string{output}); err != nil {
		return err
	}
	in, err := os.Open(input)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	tmp, err := os.MkdirTemp(filepath.Dir(output), ".symdesk-pdf-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	staged := filepath.Join(tmp, "copy.pdf")
	out, err := os.OpenFile(staged, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(out, io.LimitReader(in, 512*1024*1024+1))
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if n > 512*1024*1024 {
		return fmt.Errorf("PDF copy exceeds 512 MiB")
	}
	return publish(context.Background(), staged, output)
}
