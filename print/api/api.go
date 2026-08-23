// Package api is the stable in-process entry point into symprint.
//
// symprint's logic lives in internal/ packages, which Go's import rules make
// unreachable from other modules. Consumers that link this module rather than
// executing the symprint binary — symdesk since the repo consolidation — go
// through this package instead, so the internal layout stays free to change.
//
// The surface is deliberately narrow: it covers exactly what an embedding
// consumer needs (render a document, list the profiles, ask whether the
// engine is present) and nothing else. Everything richer stays CLI/MCP-only.
package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/danieljustus/symaira-print/internal/config"
	"github.com/danieljustus/symaira-print/internal/press"
)

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

// Render writes source (YAML frontmatter + Markdown) to a PDF at outputPath.
//
// Configuration is read the same way the CLI reads it, so an embedding
// consumer honors the user's ~/.config/symprint/config.toml — engine binary,
// font paths, timeout, and default profile included. Errors are press's typed
// contract/render errors; callers that only need a message can use them as-is.
func Render(ctx context.Context, source []byte, outputPath string, opts Options) (*Result, error) {
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

// EngineAvailable reports whether the typesetting engine is reachable, plus
// its version when it is and an actionable install hint when it is not.
// Consumers use it to decide whether to offer PDF export at all.
func EngineAvailable(ctx context.Context) (bool, string) {
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
