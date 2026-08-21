// Package pdfops provides page-level PDF operations through external, well-known
// command-line tools. It never modifies an input PDF in place.
package pdfops

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Tools contains the external PDF utilities used by the operations.
type Tools struct {
	PDFInfo     string
	PDFSeparate string
	PDFUnite    string
	QPDF        string
}

// DefaultTools resolves the standard Poppler/qpdf command names through PATH
// when each operation is executed.
func DefaultTools() Tools {
	return Tools{PDFInfo: "pdfinfo", PDFSeparate: "pdfseparate", PDFUnite: "pdfunite", QPDF: "qpdf"}
}

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
		for page := start; page <= end; page++ {
			if seen[page] {
				return nil, fmt.Errorf("page %d appears more than once", page)
			}
			seen[page] = true
			pages = append(pages, page)
		}
	}
	sort.Ints(pages)
	return pages, nil
}

// Split splits input after each page in atSpec and returns the generated part
// paths in document order.
func (t Tools) Split(ctx context.Context, input, atSpec, outputDir string) ([]string, error) {
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

	tmp, err := os.MkdirTemp("", "symingest-pdf-split-*")
	if err != nil {
		return nil, fmt.Errorf("create PDF split workspace: %w", err)
	}
	defer os.RemoveAll(tmp)

	pagePaths := make([]string, pages)
	for page := 1; page <= pages; page++ {
		pattern := filepath.Join(tmp, "page-%d.pdf")
		if err := t.run(ctx, t.PDFSeparate, "-f", strconv.Itoa(page), "-l", strconv.Itoa(page), input, pattern); err != nil {
			return nil, fmt.Errorf("split page %d: %w", page, err)
		}
		pagePaths[page-1] = filepath.Join(tmp, fmt.Sprintf("page-%d.pdf", page))
		if _, err := os.Stat(pagePaths[page-1]); err != nil {
			return nil, fmt.Errorf("split page %d did not produce a PDF: %w", page, err)
		}
	}

	ends := append(append([]int(nil), boundaries...), pages)
	start := 1
	outputs := make([]string, 0, len(ends))
	for index, end := range ends {
		output := filepath.Join(outputDir, fmt.Sprintf("part-%03d.pdf", index+1))
		inputs := pagePaths[start-1 : end]
		args := append(append([]string(nil), inputs...), output)
		if err := t.run(ctx, t.PDFUnite, args...); err != nil {
			return nil, fmt.Errorf("merge split part %d: %w", index+1, err)
		}
		outputs = append(outputs, output)
		start = end + 1
	}
	return outputs, nil
}

// Merge combines two or more PDFs into output without modifying the inputs.
func (t Tools) Merge(ctx context.Context, inputs []string, output string) error {
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
	if err := t.run(ctx, t.PDFUnite, append(inputs, output)...); err != nil {
		return fmt.Errorf("merge PDFs: %w", err)
	}
	return requireFile(output)
}

// Rotate rotates selected pages by degrees. An empty page selector rotates all
// pages. qpdf is intentionally required only for this operation.
func (t Tools) Rotate(ctx context.Context, input, output string, degrees int, pageSpec string) error {
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
	if err := t.run(ctx, t.QPDF, "--warning-exit-0", input, output, "--rotate="+selector); err != nil {
		return fmt.Errorf("rotate PDF (qpdf is required): %w", err)
	}
	return requireFile(output)
}

func (t Tools) pageCount(ctx context.Context, input string) (int, error) {
	if err := requireFile(input); err != nil {
		return 0, fmt.Errorf("split input: %w", err)
	}
	path, err := toolPath(t.PDFInfo, "pdfinfo")
	if err != nil {
		return 0, err
	}
	cmd := exec.CommandContext(ctx, path, input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return 0, commandError(path, stderr.String(), err)
	}
	for _, line := range strings.Split(stdout.String(), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok && strings.TrimSpace(key) == "Pages" {
			pages, err := strconv.Atoi(strings.TrimSpace(value))
			if err == nil && pages > 0 {
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
	cmd := exec.CommandContext(ctx, path, args...)
	var stderr bytes.Buffer
	cmd.Stdout = &bytes.Buffer{}
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return commandError(path, stderr.String(), err)
	}
	return nil
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
	return nil
}
