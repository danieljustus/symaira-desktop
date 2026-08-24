// Package pdf renders vault documents to PDF.
//
// The renderer is symprint, which lives in this repository as the nested
// print/ module since the repo consolidation. It is linked in-process through
// print/api — there is no symprint binary to find, no PATH probe, and no
// subprocess. The only external requirement is the typesetting engine
// (typst), which is a third-party tool, not a Symaira sibling.
package pdf

import (
	"context"
	"time"

	printapi "github.com/danieljustus/symaira-print/api"
)

// renderTimeout bounds a single render. It matches the bound the previous
// out-of-process renderer applied to the symprint subprocess; symprint's own
// engine timeout applies inside it.
const renderTimeout = 60 * time.Second

// ErrEngineUnavailable marks a render that failed because the typesetting
// engine is missing.
var ErrEngineUnavailable = printapi.ErrEngineUnavailable

// The renderer seam. These are the single injectable entry points into
// symprint, so a test can substitute a double without requiring a real typst
// installation on the machine running the suite.
//
// Production code never reassigns them; a test that does must restore the
// original in t.Cleanup.
var (
	DefaultRenderFunc          = printapi.Render
	DefaultEngineAvailableFunc = printapi.EngineAvailable
	RenderFunc                 = DefaultRenderFunc
	EngineAvailableFunc        = DefaultEngineAvailableFunc
)

// Profile is a built-in output profile.
type Profile = printapi.Profile

// Result describes a completed render.
type Result = printapi.Result

// Options adjusts a single render.
type Options = printapi.Options

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
	return RenderFunc(ctx, markdown, outputPath, printapi.Options{
		Profile:   profile,
		SourceDir: sourceDir,
	})
}

// Profiles returns the built-in profiles in presentation order.
func Profiles() []Profile {
	return printapi.Profiles()
}
