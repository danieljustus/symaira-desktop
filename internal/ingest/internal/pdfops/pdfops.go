// Package pdfops provides page-level PDF operations through external, well-known
// command-line tools. It never modifies an input PDF in place.
package pdfops

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/danieljustus/symaira-desktop/internal/pdfprocess"
)

const maxPages = 10_000

// Tools contains the external PDF utilities used by the operations.
type Tools struct {
	PDFInfo     string
	PDFSeparate string
	PDFUnite    string
	QPDF        string
}

// toolsFactory returns the Tools value each operation uses. Tests can
// override it with WithToolsFactory to inject fake tool paths without
// touching PATH (issue #644).
var toolsFactory = DefaultTools

// WithToolsFactory replaces the package-level tools factory for the duration
// of fn and restores it afterwards. It is safe for sequential test use; tests
// calling it must not run in parallel with other pdfops tests.
func WithToolsFactory(factory func() Tools, fn func()) {
	prev := toolsFactory
	toolsFactory = factory
	defer func() { toolsFactory = prev }()
	fn()
}

// DefaultTools resolves the standard Poppler/qpdf command names through PATH
// when each operation is executed.
func DefaultTools() Tools {
	return Tools{PDFInfo: "pdfinfo", PDFSeparate: "pdfseparate", PDFUnite: "pdfunite", QPDF: "qpdf"}
}

// NewTools returns the currently configured Tools value, consulting the
// (overridable) package tools factory so tests can inject fake tool paths.
func NewTools() Tools { return toolsFactory() }

// ParsePages parses a comma-separated page selector such as "1-3,5" into
// sorted, unique, one-based page numbers.
func ParsePages(spec string) ([]int, error) {
	if strings.TrimSpace(spec) == "" {
		return nil, fmt.Errorf("page selector is empty")
	}
	seen := make(map[int]bool)
	var pages []int
	for _, raw := range strings.Split(spec, ",") {
		part := strings.TrimSpace(raw)
		if part == "" {
			return nil, fmt.Errorf("invalid empty page selector in %q", spec)
		}
		bounds := strings.Split(part, "-")
		if len(bounds) > 2 {
			return nil, fmt.Errorf("invalid page range %q", part)
		}
		start, err := strconv.Atoi(strings.TrimSpace(bounds[0]))
		if err != nil || start <= 0 {
			return nil, fmt.Errorf("invalid page number %q", part)
		}
		end := start
		if len(bounds) == 2 {
			end, err = strconv.Atoi(strings.TrimSpace(bounds[1]))
			if err != nil || end <= 0 || end < start {
				return nil, fmt.Errorf("invalid page range %q", part)
			}
		}
		if end-start >= 1_000_000 || len(pages) > 1_000_000-(end-start+1) {
			return nil, fmt.Errorf("page selector exceeds 1000000 pages")
		}
		for page := start; ; page++ {
			if seen[page] {
				return nil, fmt.Errorf("page %d appears more than once", page)
			}
			seen[page] = true
			pages = append(pages, page)
			if page == end {
				break
			}
		}
	}
	sort.Ints(pages)
	return pages, nil
}

// Split splits input after each page in atSpec and returns the generated part
// paths in document order.
func (t Tools) Split(ctx context.Context, input, atSpec, outputDir string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, pdfprocess.Timeout)
	defer cancel()
	pages, err := t.pageCount(ctx, input)
	if err != nil {
		return nil, err
	}
	boundaries, err := ParsePages(atSpec)
	if err != nil {
		return nil, err
	}
	for _, boundary := range boundaries {
		if boundary >= pages {
			return nil, fmt.Errorf("split page %d is outside the document; document has %d pages", boundary, pages)
		}
	}
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		return nil, fmt.Errorf("create split output directory: %w", err)
	}

	ends := append(append([]int(nil), boundaries...), pages)
	outputs := make([]string, len(ends))
	for index := range ends {
		outputs[index] = filepath.Join(outputDir, fmt.Sprintf("part-%03d.pdf", index+1))
	}
	return t.splitParts(ctx, input, ends, outputs, outputDir, false)
}

// splitParts stages every range under the caller's single operation deadline.
// legacy preserves barcode splitting's empty-document metadata and filenames.
func (t Tools) splitParts(ctx context.Context, input string, ends []int, outputs []string, outputDir string, legacy bool) ([]string, error) {
	if err := newOutputs([]string{input}, outputs); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp(outputDir, ".symdesk-pdf-*")
	if err != nil {
		return nil, fmt.Errorf("create PDF split workspace: %w", err)
	}
	defer func() {
		if cleanupErr := os.RemoveAll(tmp); cleanupErr != nil {
			log.Printf("PDF split workspace cleanup failed: %v", cleanupErr)
		}
	}()

	start := 1
	for index, end := range ends {
		staged := filepath.Join(tmp, filepath.Base(outputs[index]))
		args := []string{input, "--pages", ".", fmt.Sprintf("%d-%d", start, end), "--", staged}
		if legacy {
			args[0], args[2] = "--empty", input
		}
		if err := t.run(ctx, t.QPDF, args...); err != nil {
			return nil, fmt.Errorf("split part %d: %w", index+1, err)
		}
		if err := t.validate(ctx, staged); err != nil {
			return nil, err
		}
		start = end + 1
	}
	for _, output := range outputs {
		if err := publish(ctx, filepath.Join(tmp, filepath.Base(output)), output); err != nil {
			return nil, err
		}
	}
	return outputs, nil
}

// Merge combines two or more PDFs into output without modifying the inputs.
func (t Tools) Merge(ctx context.Context, inputs []string, output string) error {
	ctx, cancel := context.WithTimeout(ctx, pdfprocess.Timeout)
	defer cancel()
	if len(inputs) < 2 {
		return fmt.Errorf("merge requires at least two input PDFs")
	}
	for _, input := range inputs {
		if err := requireFile(input); err != nil {
			return fmt.Errorf("merge input %s: %w", input, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
		return fmt.Errorf("create merge output directory: %w", err)
	}
	return t.writeOutput(ctx, inputs, output, func(staged string) error {
		args := []string{inputs[0], "--pages", ".", "1-z"}
		for _, input := range inputs[1:] {
			args = append(args, input, "1-z")
		}
		args = append(args, "--", staged)
		if err := t.run(ctx, t.QPDF, args...); err != nil {
			return fmt.Errorf("merge PDFs: %w", err)
		}
		return nil
	})
}

// Rotate rotates selected pages by degrees. An empty page selector rotates all
// pages. All writing operations require qpdf and a new destination.
func (t Tools) Rotate(ctx context.Context, input, output string, degrees int, pageSpec string) error {
	ctx, cancel := context.WithTimeout(ctx, pdfprocess.Timeout)
	defer cancel()
	if err := requireFile(input); err != nil {
		return fmt.Errorf("rotate input: %w", err)
	}
	if degrees == 0 || degrees%90 != 0 || degrees < -270 || degrees > 270 {
		return fmt.Errorf("rotation must be one of -270, -180, -90, 90, 180, 270")
	}
	if pageSpec != "" {
		if _, err := ParsePages(pageSpec); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
		return fmt.Errorf("create rotation output directory: %w", err)
	}
	selector := fmt.Sprintf("%+d", degrees)
	if pageSpec != "" {
		selector += ":" + pageSpec
	}
	return t.writeOutput(ctx, []string{input}, output, func(staged string) error {
		if err := t.run(ctx, t.QPDF, input, staged, "--rotate="+selector); err != nil {
			return fmt.Errorf("rotate PDF (qpdf is required): %w", err)
		}
		return nil
	})
}

func (t Tools) pageCount(ctx context.Context, input string) (int, error) {
	if err := requireFile(input); err != nil {
		return 0, fmt.Errorf("split input: %w", err)
	}
	path, err := toolPath(t.PDFInfo, "pdfinfo")
	if err != nil {
		return 0, err
	}
	stdout, err := pdfprocess.Run(ctx, path, input)
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(stdout), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok && strings.TrimSpace(key) == "Pages" {
			pages, err := strconv.Atoi(strings.TrimSpace(value))
			if err == nil && pages > 0 {
				if pages > maxPages {
					return 0, fmt.Errorf("PDF page count exceeds 10000 pages")
				}
				return pages, nil
			}
		}
	}
	return 0, fmt.Errorf("pdfinfo did not report a positive page count for %s", input)
}

func (t Tools) run(ctx context.Context, name string, args ...string) error {
	path, err := toolPath(name, filepath.Base(name))
	if err != nil {
		return err
	}
	_, err = pdfprocess.Run(ctx, path, args...)
	return err
}

func toolPath(path, label string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("%s is not configured; install the required PDF tool", label)
	}
	resolved, err := exec.LookPath(path)
	if err != nil {
		return "", fmt.Errorf("%s not found in PATH; install %s to use this PDF operation", label, label)
	}
	return resolved, nil
}

func commandError(path, stderr string, err error) error {
	message := strings.TrimSpace(stderr)
	if message == "" {
		message = err.Error()
	}
	return fmt.Errorf("%s: %s", filepath.Base(path), message)
}

func requireFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory", path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	return nil
}
