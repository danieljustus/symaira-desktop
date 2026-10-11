package pdfops

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Extract is the legacy barcode split's range writer. It shares publication,
// validation and process ownership with the page-selector operations.
func (t Tools) Extract(ctx context.Context, input, output, pages string) error {
	if err := requireFile(input); err != nil {
		return err
	}
	return t.writeOutput(ctx, []string{input}, output, func(staged string) error {
		return t.run(ctx, t.QPDF, "--empty", "--pages", input, pages, "--", staged)
	})
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
