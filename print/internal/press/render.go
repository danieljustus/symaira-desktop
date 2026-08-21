package press

import (
	"context"
	"time"
)

// EngineConfig carries the host-specific engine settings (from config/env).
type EngineConfig struct {
	TypstBin          string
	FontPaths         []string
	IgnoreSystemFonts bool
	Timeout           time.Duration
}

// Request is a single render request. Source is the raw document (frontmatter +
// Markdown body). OutputPath is where the PDF is written.
type Request struct {
	Source     []byte
	SourceName string // for diagnostics only
	// SourceDir is the directory of the input document; locally referenced
	// Markdown images are resolved against it and copied into the render work
	// dir. Empty for inputs without a filesystem base (e.g. MCP) — local image
	// references then fail with a clear error instead of an engine error.
	SourceDir  string
	OutputPath string

	// Overrides (CLI/MCP flags) win over frontmatter and profile defaults.
	ProfileOverride  string
	StandardOverride []string
	Reproducible     *bool

	// DefaultProfile is the lowest-precedence fallback (config default), used
	// only when neither an override nor the frontmatter selects a profile.
	DefaultProfile string

	// ShortDiagnostics requests the engine's short diagnostic format
	// (one line per diagnostic: file:line:col: error: message) for
	// machine-facing surfaces such as the MCP server and --json output.
	// Leave false for interactive terminal renders, which keep the rich
	// human format.
	ShortDiagnostics bool

	Engine EngineConfig
}

// Result describes a successful render.
type Result struct {
	OutputPath    string   `json:"output_path"`
	Profile       string   `json:"profile"`
	Engine        string   `json:"engine"`
	EngineVersion string   `json:"engine_version,omitempty"`
	PDFStandard   []string `json:"pdf_standard,omitempty"`
	Reproducible  bool     `json:"reproducible"`
	Bytes         int64    `json:"bytes"`
	DurationMS    int64    `json:"duration_ms"`
}

// Render runs the full pipeline: parse + validate the contract, select the
// profile, resolve output options, then hand off to the engine. Every failure
// is a typed error (*ContractError or *RenderError) the caller maps to an exit
// code; the engine's raw log is never leaked unfiltered.
func Render(ctx context.Context, req Request) (*Result, error) {
	doc, err := Parse(req.Source)
	if err != nil {
		return nil, err // *ContractError
	}

	name := req.ProfileOverride
	if name == "" {
		name = doc.Front.Profile
	}
	if name == "" {
		name = req.DefaultProfile
	}
	if name == "" {
		return nil, &RenderError{
			Stage:   "contract",
			Message: "no profile selected",
			Hint:    "set `profile:` in the frontmatter or pass --profile (see `symprint profiles`)",
		}
	}
	prof, ok := Lookup(name)
	if !ok {
		return nil, &RenderError{
			Stage:   "contract",
			Message: "unknown profile: " + name,
			Hint:    "run `symprint profiles` to list the built-in profiles",
		}
	}

	// Resolve form before validation/render so it is part of the contract check.
	if doc.Front.Form == "" {
		doc.Front.Form = prof.Form
	}
	doc.Front.Profile = prof.Name

	if issues := doc.Validate(prof); hasErrors(issues) {
		return nil, &ContractError{Issues: issues}
	}

	// Resolve PDF standard: override > frontmatter > profile default.
	std := req.StandardOverride
	if len(std) == 0 {
		std = doc.Front.PDF.Standard
	}
	if len(std) == 0 {
		std = prof.PDFStandard
	}

	// Resolve reproducibility: override > frontmatter > profile default.
	repro := prof.Reproducible
	if doc.Front.PDF.Reproducible != nil {
		repro = *doc.Front.PDF.Reproducible
	}
	if req.Reproducible != nil {
		repro = *req.Reproducible
	}

	eng := DetectTypst(ctx, req.Engine.TypstBin)
	if !eng.Available {
		return nil, &RenderError{
			Stage:   "engine",
			Message: "rendering engine not available",
			Hint:    eng.Hint,
		}
	}

	job := typstJob{
		profile:          prof,
		front:            doc.Front,
		body:             doc.Body,
		sourceDir:        req.SourceDir,
		outputPath:       req.OutputPath,
		pdfStandard:      std,
		reproducible:     repro,
		fontPaths:        req.Engine.FontPaths,
		ignoreFonts:      req.Engine.IgnoreSystemFonts,
		shortDiagnostics: req.ShortDiagnostics,
		timeout:          req.Engine.Timeout,
	}
	return renderTypst(ctx, eng, job)
}
