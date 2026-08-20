// Package ocr shells out to external OCR tools (tesseract, pdftoppm).
package ocr

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/danieljustus/symaira-ingest/internal/extract"
	"golang.org/x/sync/errgroup"
)

// Runner executes external OCR tools.
type Runner struct {
	Tesseract string // path to tesseract binary
	PDFToPPM  string // path to pdftoppm binary
	SIPS      string // optional macOS image conversion tool for HEIC/HEIF
	OCRLang   string // tesseract language, e.g. "eng" or "deu+eng"

	langMu              sync.Mutex
	langCacheLoaded     bool
	langCache           map[string]bool
	validatedLangKey    string
	validatedLang       string
	validatedLangErr    error
	validatedLangCached bool
}

// DefaultRunner returns a runner that looks up tools on PATH.
func DefaultRunner(ocrLang string) *Runner {
	return &Runner{
		Tesseract: filepath.Clean("tesseract"),
		PDFToPPM:  filepath.Clean("pdftoppm"),
		SIPS:      filepath.Clean("sips"),
		OCRLang:   ocrLang,
	}
}

// cleanToolPath sanitises a tool path and returns an error if it is unusable.
func cleanToolPath(name, path string) (string, error) {
	cleaned := filepath.Clean(path)
	if cleaned == "" || cleaned == "." {
		return "", fmt.Errorf("%s command is not configured", name)
	}
	return cleaned, nil
}

// Available returns an error if required tools are missing.
// For image-only pipelines pdftoppm is not required.
func (r *Runner) Available() error {
	path, err := cleanToolPath("tesseract", r.Tesseract)
	if err != nil {
		return err
	}
	if _, err := exec.LookPath(path); err != nil {
		return fmt.Errorf("tesseract not found on PATH: %w", err)
	}
	return nil
}

// AvailableForPDF returns an error if tools for PDF OCR are missing.
func (r *Runner) AvailableForPDF() error {
	if err := r.Available(); err != nil {
		return err
	}
	path, err := cleanToolPath("pdftoppm", r.PDFToPPM)
	if err != nil {
		return err
	}
	if _, err := exec.LookPath(path); err != nil {
		return fmt.Errorf("pdftoppm not found on PATH: %w", err)
	}
	return nil
}

// Extract implements extract.Engine.
func (r *Runner) Extract(ctx context.Context, path string, kind extract.Kind) (*extract.Result, error) {
	switch kind {
	case extract.KindPNG, extract.KindJPEG, extract.KindTIFF, extract.KindWebP:
		return r.extractImage(ctx, path)
	case extract.KindHEIC:
		return r.extractHEIC(ctx, path)
	case extract.KindPDF:
		return r.extractPDF(ctx, path)
	default:
		return nil, fmt.Errorf("ocr: unsupported source kind %q", kind)
	}
}

func (r *Runner) validateLanguages(ctx context.Context) (string, error) {
	lang := r.OCRLang
	if lang == "" {
		lang = "eng"
	}
	r.langMu.Lock()
	if r.validatedLangCached && r.validatedLangKey == lang {
		cachedLang, cachedErr := r.validatedLang, r.validatedLangErr
		r.langMu.Unlock()
		return cachedLang, cachedErr
	}
	r.langMu.Unlock()

	validated, err := r.validateLanguagesUncached(ctx, lang)
	if ctx.Err() != nil {
		return validated, err
	}
	r.langMu.Lock()
	r.validatedLangKey = lang
	r.validatedLang = validated
	r.validatedLangErr = err
	r.validatedLangCached = true
	r.langMu.Unlock()
	return validated, err
}

func (r *Runner) validateLanguagesUncached(ctx context.Context, lang string) (string, error) {
	available, err := r.availableLanguages(ctx)
	if err != nil {
		return "", err
	}
	if available == nil {
		// If tesseract --list-langs fails, we cannot validate, so just return
		// the original lang. The failed validation state is cached so we do not
		// respawn --list-langs for every document in the same run.
		return lang, nil
	}

	parts := strings.Split(lang, "+")
	var installed []string
	var missing []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if available[p] {
			installed = append(installed, p)
		} else {
			missing = append(missing, p)
		}
	}

	if len(missing) > 0 {
		if len(installed) > 0 {
			fallback := strings.Join(installed, "+")
			fmt.Fprintf(os.Stderr, "Warning: tesseract language(s) %v not installed; falling back to %q\n", missing, fallback)
			return fallback, nil
		}
		var availList []string
		for a := range available {
			availList = append(availList, a)
		}
		sort.Strings(availList)
		return "", fmt.Errorf("none of the configured OCR languages %q are installed (available: %v)", lang, availList)
	}

	return lang, nil
}

func (r *Runner) availableLanguages(ctx context.Context) (map[string]bool, error) {
	r.langMu.Lock()
	defer r.langMu.Unlock()
	if r.langCacheLoaded {
		return r.langCache, nil
	}

	path, err := cleanToolPath("tesseract", r.Tesseract)
	if err != nil {
		return nil, err
	}
	execPath, err := exec.LookPath(path)
	if err != nil {
		return nil, fmt.Errorf("tesseract not found on PATH: %w", err)
	}

	// We capture stdout and stderr separately
	cmd := exec.CommandContext(ctx, execPath, "--list-langs")
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		r.langCacheLoaded = true
		return nil, nil
	}

	available := make(map[string]bool)
	lines := strings.Split(stdoutBuf.String(), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, ":") || strings.Contains(line, "available") {
			continue
		}
		available[line] = true
	}
	r.langCache = available
	r.langCacheLoaded = true
	return available, nil
}

func (r *Runner) extractImage(ctx context.Context, path string) (*extract.Result, error) {
	if err := r.Available(); err != nil {
		return nil, err
	}
	lang, err := r.validateLanguages(ctx)
	if err != nil {
		return nil, err
	}
	// Run tesseract with its working directory set to the image's directory
	// and pass a relative file name. Leptonica (tesseract's image library) can
	// fail to open an absolute path when invoked from an unrelated cwd; running
	// from the image's own directory avoids that failure mode.
	out, err := r.runToolInDir(ctx, filepath.Dir(path), r.Tesseract, "-l", lang, filepath.Base(path), "stdout")
	if err != nil {
		return nil, fmt.Errorf("tesseract failed: %w", err)
	}
	return &extract.Result{
		Text:   strings.TrimSpace(string(out)),
		MIME:   "image/ocr",
		Engine: "tesseract",
	}, nil
}

func (r *Runner) extractHEIC(ctx context.Context, path string) (*extract.Result, error) {
	sipsPath, err := cleanToolPath("sips", r.SIPS)
	if err != nil {
		return nil, err
	}
	if _, err := exec.LookPath(sipsPath); err != nil {
		return nil, fmt.Errorf("sips not found on PATH; HEIC/HEIF OCR requires macOS sips conversion before tesseract: %w", err)
	}
	dir, err := os.MkdirTemp("", "symingest-heic-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(dir)
	converted := filepath.Join(dir, "converted.png")
	if _, err := r.runTool(ctx, sipsPath, "-s", "format", "png", path, "--out", converted); err != nil {
		return nil, fmt.Errorf("sips HEIC conversion failed: %w", err)
	}
	res, err := r.extractImage(ctx, converted)
	if err != nil {
		return nil, err
	}
	res.MIME = "image/heic"
	res.Engine = "sips+tesseract"
	return res, nil
}

func (r *Runner) extractPDF(ctx context.Context, path string) (*extract.Result, error) {
	if err := r.AvailableForPDF(); err != nil {
		return nil, err
	}
	lang, err := r.validateLanguages(ctx)
	if err != nil {
		return nil, err
	}

	dir, err := os.MkdirTemp("", "symingest-pdf-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	prefix := filepath.Join(dir, "page")
	if _, err := r.runTool(ctx, r.PDFToPPM, "-png", "-r", "150", path, prefix); err != nil {
		return nil, fmt.Errorf("pdftoppm failed: %w", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read rendered pages: %w", err)
	}

	var pages []string
	for _, e := range entries {
		if strings.HasSuffix(strings.ToLower(e.Name()), ".png") {
			pages = append(pages, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(pages)

	// Process pages concurrently with errgroup.
	// Results are collected in order by page index.
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(4) // reasonable default concurrency

	var (
		mu      sync.Mutex
		results = make([]string, len(pages))
	)
	for i, p := range pages {
		i, p := i, p
		g.Go(func() error {
			out, err := r.runTool(ctx, r.Tesseract, "-l", lang, p, "stdout")
			if err != nil {
				return fmt.Errorf("tesseract failed for page %s: %w", filepath.Base(p), err)
			}
			mu.Lock()
			results[i] = string(out)
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	var sb strings.Builder
	for _, text := range results {
		sb.WriteString(text)
		sb.WriteByte('\n')
	}

	return &extract.Result{
		Text:   strings.TrimSpace(sb.String()),
		MIME:   "application/pdf",
		Engine: "pdftoppm+tesseract",
	}, nil
}

// runTool runs a command with stdout/stderr captured away from the process stdout.
// On error the captured stderr is included.
func (r *Runner) runTool(ctx context.Context, name string, args ...string) ([]byte, error) {
	return r.runToolInDir(ctx, "", name, args...)
}

// runToolInDir runs a command with its working directory set to dir (the
// process's current directory when dir is empty), capturing stdout/stderr
// away from the process stdout. On error the captured stderr is included.
func (r *Runner) runToolInDir(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("%s: %s", filepath.Base(name), msg)
	}
	return out.Bytes(), nil
}

// Ensure Runner implements extract.Engine.
var _ extract.Engine = (*Runner)(nil)
