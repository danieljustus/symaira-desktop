// Package barcode detects configured separator barcodes on rasterized PDF pages
// and creates document PDFs with separator sheets removed.
package barcode

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Options controls opt-in barcode separation.
type Options struct {
	SeparatorPrefix string
	SeparatorValue  string
	PDFToPPM        string
	ZBarImg         string
	PDFInfo         string
	PDFSeparate     string
	PDFUnite        string
}

// Result describes barcode separation. A nil Paths slice means no separator
// was found and the caller should ingest the original as one document.
type Result struct {
	Paths         []string
	SeparatorPages []int
	Warning       string
}

// DefaultOptions uses the standard command names from Homebrew/Poppler/ZBar.
func DefaultOptions(prefix, value string) Options {
	return Options{
		SeparatorPrefix: prefix,
		SeparatorValue:  value,
		PDFToPPM:        "pdftoppm",
		ZBarImg:         "zbarimg",
		PDFInfo:         "pdfinfo",
		PDFSeparate:     "pdfseparate",
		PDFUnite:        "pdfunite",
	}
}

// Enabled reports whether the caller configured a separator value or prefix.
func (o Options) Enabled() bool {
	return strings.TrimSpace(o.SeparatorPrefix) != "" || strings.TrimSpace(o.SeparatorValue) != ""
}

// Separate detects separator pages and creates one PDF per document. Missing
// or unreadable decoder tools return a warning and no paths so callers can
// safely fall back to normal single-document ingestion.
func (o Options) Separate(ctx context.Context, input, outputDir string) (Result, error) {
	if !o.Enabled() {
		return Result{}, nil
	}
	if _, err := os.Stat(input); err != nil {
		return Result{}, err
	}
	if _, err := lookup(o.PDFToPPM, "pdftoppm"); err != nil {
		return Result{Warning: err.Error()}, nil
	}
	if _, err := lookup(o.ZBarImg, "zbarimg"); err != nil {
		return Result{Warning: err.Error()}, nil
	}
	if _, err := lookup(o.PDFSeparate, "pdfseparate"); err != nil {
		return Result{Warning: err.Error()}, nil
	}
	if _, err := lookup(o.PDFUnite, "pdfunite"); err != nil {
		return Result{Warning: err.Error()}, nil
	}

	workspace, err := os.MkdirTemp("", "symingest-barcode-*")
	if err != nil {
		return Result{}, fmt.Errorf("create barcode workspace: %w", err)
	}
	defer os.RemoveAll(workspace)
	prefix := filepath.Join(workspace, "page")
	if err := o.run(ctx, o.PDFToPPM, "-png", "-r", "150", input, prefix); err != nil {
		return Result{Warning: fmt.Sprintf("barcode rasterization unavailable: %v", err)}, nil
	}
	entries, err := os.ReadDir(workspace)
	if err != nil {
		return Result{}, fmt.Errorf("read barcode rasterized pages: %w", err)
	}
	var images []string
	for _, entry := range entries {
		if strings.HasSuffix(strings.ToLower(entry.Name()), ".png") {
			images = append(images, filepath.Join(workspace, entry.Name()))
		}
	}
	sort.Strings(images)
	if len(images) == 0 {
		return Result{Warning: "barcode rasterization produced no pages"}, nil
	}

	var separators []int
	for page, image := range images {
		data, err := o.decode(ctx, image)
		if err != nil {
			continue
		}
		for _, value := range data {
			if o.matches(value) {
				separators = append(separators, page+1)
				break
			}
		}
	}
	if len(separators) == 0 {
		return Result{Warning: "no configured separator barcode detected; ingesting as one document"}, nil
	}

	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		return Result{}, fmt.Errorf("create barcode output directory: %w", err)
	}
	pagePDFs := make([]string, len(images))
	for page := range images {
		pattern := filepath.Join(workspace, "page-%d.pdf")
		if err := o.run(ctx, o.PDFSeparate, "-f", fmt.Sprint(page+1), "-l", fmt.Sprint(page+1), input, pattern); err != nil {
			return Result{Warning: fmt.Sprintf("barcode split unavailable: %v", err)}, nil
		}
		pagePDFs[page] = filepath.Join(workspace, fmt.Sprintf("page-%d.pdf", page+1))
	}

	separators = uniqueSorted(separators)
	paths := make([]string, 0, len(separators)+1)
	start := 1
	part := 1
	for _, separator := range append(separators, len(images)+1) {
		if separator > start {
			output := filepath.Join(outputDir, fmt.Sprintf("document-%03d.pdf", part))
			args := append(append([]string(nil), pagePDFs[start-1:separator-1]...), output)
			if err := o.run(ctx, o.PDFUnite, args...); err != nil {
				return Result{Warning: fmt.Sprintf("barcode document merge unavailable: %v", err)}, nil
			}
			paths = append(paths, output)
			part++
		}
		start = separator + 1
	}
	if len(paths) == 0 {
		return Result{Warning: "separator barcode pages contained no document pages; ingesting as one document"}, nil
	}
	return Result{Paths: paths, SeparatorPages: separators}, nil
}

func (o Options) decode(ctx context.Context, image string) ([]string, error) {
	path, err := lookup(o.ZBarImg, "zbarimg")
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, path, "--quiet", "--raw", image)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	var values []string
	for _, line := range strings.Split(stdout.String(), "\n") {
		if value := strings.TrimSpace(line); value != "" {
			values = append(values, value)
		}
	}
	return values, nil
}

func (o Options) matches(value string) bool {
	if wanted := strings.TrimSpace(o.SeparatorValue); wanted != "" && value == wanted {
		return true
	}
	return strings.TrimSpace(o.SeparatorPrefix) != "" && strings.HasPrefix(value, strings.TrimSpace(o.SeparatorPrefix))
}

func (o Options) run(ctx context.Context, name string, args ...string) error {
	path, err := lookup(name, filepath.Base(name))
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, path, args...)
	var stderr bytes.Buffer
	cmd.Stdout = &bytes.Buffer{}
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("%s: %s", filepath.Base(path), message)
	}
	return nil
}

func lookup(path, name string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("%s is not configured; barcode separation is disabled", name)
	}
	resolved, err := exec.LookPath(path)
	if err != nil {
		return "", fmt.Errorf("%s not found in PATH; barcode separation fell back to normal ingest", name)
	}
	return resolved, nil
}

func uniqueSorted(values []int) []int {
	seen := make(map[int]bool)
	result := make([]int, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Ints(result)
	return result
}
