package pdfops

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

// All final paths must be new. File identity gives actionable alias errors;
// exclusive link publication, not this preflight, closes replacement races.
func newOutputs(inputs, outputs []string) error {
	for _, output := range outputs {
		if info, err := os.Stat(output); err == nil {
			for _, input := range inputs {
				original, err := os.Stat(input)
				if err != nil {
					return err
				}
				if os.SameFile(info, original) {
					return fmt.Errorf("PDF output aliases input: %s", output)
				}
			}
		}
		if _, err := os.Lstat(output); err == nil {
			return fmt.Errorf("PDF output already exists: %s", output)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (t Tools) writeOutput(ctx context.Context, inputs []string, output string, write func(string) error) error {
	if err := newOutputs(inputs, []string{output}); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(output), ".symdesk-pdf-*")
	if err != nil {
		return fmt.Errorf("create PDF output workspace: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(tmp); err != nil {
			log.Printf("PDF output workspace cleanup failed: %v", err)
		}
	}()
	staged := filepath.Join(tmp, "result.pdf")
	if err := write(staged); err != nil {
		return err
	}
	if err := t.validate(ctx, staged); err != nil {
		return err
	}
	return publish(ctx, staged, output)
}

func (t Tools) validate(ctx context.Context, staged string) error {
	info, err := os.Lstat(staged)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("PDF tool output is not a regular file")
	}
	if err := t.run(ctx, t.QPDF, "--check", staged); err != nil {
		return fmt.Errorf("validate PDF output: %w", err)
	}
	return os.Chmod(staged, 0o600)
}

func publish(ctx context.Context, staged, output string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Link is an atomic create-if-absent operation on the same filesystem.
	// Never remove/replace an existing path, even if it appeared after preflight.
	// A multi-part publish failure may retain already-published new parts.
	if err := os.Link(staged, output); err != nil {
		return fmt.Errorf("publish PDF output: %w", err)
	}
	return nil
}
