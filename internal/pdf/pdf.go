// Package pdf renders vault documents to PDF.
//
// The renderer is the absorbed SymPrint PDF pipeline, which lives directly in
// internal/pdf/ (with internal subpackages config, press, assets). It is linked
// in-process — there is no symprint binary to find, no PATH probe, and no
// subprocess. The only external requirement is the typesetting engine
// (typst), which is a third-party tool, not a Symaira sibling.
package pdf

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/pdf/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/pdf/internal/press"
)

// renderTimeout bounds a single render. It matches the bound the previous
// out-of-process renderer applied to the symprint subprocess; symprint's own
// engine timeout applies inside it.
const renderTimeout = 60 * time.Second

// ErrEngineUnavailable marks a render that failed because the typesetting
// engine is missing, as opposed to a document that violates its profile
// contract. Consumers use it to tell "install typst" apart from "fix your
// frontmatter"; the wrapped error carries the actionable install hint.
var ErrEngineUnavailable = errors.New("typesetting engine unavailable")

// Profile is a built-in output profile, reduced to the fields a consumer can
// present in a picker. The full profile record stays internal.
type Profile struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Stability   string `json:"stability"`
}

// Result describes a completed render.
type Result struct {
	OutputPath    string `json:"output_path"`
	Profile       string `json:"profile"`
	EngineVersion string `json:"engine_version,omitempty"`
	Bytes         int64  `json:"bytes"`
	DurationMS    int64  `json:"duration_ms"`
}

// Options adjusts a single render. The zero value renders with the profile
// from the document frontmatter (or the configured default) and refuses to
// overwrite an existing non-PDF file at the output path.
type Options struct {
	// Profile overrides both the frontmatter and the configured default.
	Profile string
	// SourceDir is the directory local Markdown image references resolve
	// against. Empty means the document has no filesystem base, and local
	// image references then fail with a clear contract error.
	SourceDir string
	// Overwrite permits replacing an existing non-PDF file at OutputPath.
	// Existing PDFs are always replaced.
	Overwrite bool
}

// The renderer seam. These are the single injectable entry points into
// symprint, so a test can substitute a double without requiring a real typst
// installation on the machine running the suite.
//
// Production code never reassigns them; a test that does must restore the
// original in t.Cleanup.
var (
	DefaultRenderFunc          = defaultRender
	DefaultEngineAvailableFunc = defaultEngineAvailable
	RenderFunc                 = DefaultRenderFunc
	EngineAvailableFunc        = DefaultEngineAvailableFunc
)

func defaultRender(ctx context.Context, source []byte, outputPath string, opts Options) (*Result, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}

	if err := press.CheckOverwrite(outputPath, opts.Overwrite); err != nil {
		return nil, err
	}

	res, err := press.Render(ctx, press.Request{
		Source:          source,
		SourceName:      outputPath,
		SourceDir:       opts.SourceDir,
		OutputPath:      outputPath,
		ProfileOverride: opts.Profile,
		DefaultProfile:  cfg.Defaults.Profile,
		Engine: press.EngineConfig{
			TypstBin:          cfg.Engine.Typst,
			FontPaths:         cfg.Engine.FontPaths,
			IgnoreSystemFonts: cfg.Engine.IgnoreSystemFonts,
			Timeout:           cfg.Engine.Timeout(),
		},
	})
	if err != nil {
		var re *press.RenderError
		if errors.As(err, &re) && re.Stage == "engine" {
			return nil, fmt.Errorf("%w: %s", ErrEngineUnavailable, re.Hint)
		}
		return nil, err
	}

	return &Result{
		OutputPath:    res.OutputPath,
		Profile:       res.Profile,
		EngineVersion: res.EngineVersion,
		Bytes:         res.Bytes,
		DurationMS:    res.DurationMS,
	}, nil
}

func defaultEngineAvailable(ctx context.Context) (bool, string) {
	cfg, err := config.Load()
	typstBin := ""
	if err == nil {
		typstBin = cfg.Engine.Typst
	}
	info := press.DetectTypst(ctx, typstBin)
	if !info.Available {
		return false, info.Hint
	}
	return true, info.Version
}

// EngineAvailable reports whether the typesetting engine is reachable, plus
// its version when it is, or an actionable install hint when it is not.
func EngineAvailable() (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return EngineAvailableFunc(ctx)
}

// Render writes a rendered Markdown document to a PDF at outputPath using the
// named profile (empty means the document's frontmatter or the configured
// default decides) and sourceDir to resolve local Markdown image references.
// It returns the render result.
func Render(markdown []byte, outputPath, profile, sourceDir string) (*Result, error) {
	ctx, cancel := context.WithTimeout(context.Background(), renderTimeout)
	defer cancel()
	return RenderFunc(ctx, markdown, outputPath, Options{
		Profile:   profile,
		SourceDir: sourceDir,
	})
}

// Profiles returns the built-in profiles in presentation order.
func Profiles() []Profile {
	all := press.All()
	out := make([]Profile, 0, len(all))
	for _, p := range all {
		out = append(out, Profile{
			Name:        p.Name,
			Title:       p.Title,
			Description: p.Description,
			Stability:   p.Stability,
		})
	}
	return out
}
