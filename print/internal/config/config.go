// Package config loads symprint configuration from ~/.config/symprint/config.toml
// and the SYMPRINT_* environment, via the shared corekit configkit loader.
package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/danieljustus/symaira-corekit/configkit"
	"github.com/danieljustus/symaira-print/internal/press"
)

// Config is the full symprint configuration.
type Config struct {
	Engine   Engine   `json:"engine" toml:"engine"`
	Defaults Defaults `json:"defaults" toml:"defaults"`
	MCP      MCP      `json:"mcp" toml:"mcp"`
}

// MCP controls Model Context Protocol server options.
type MCP struct {
	// OutputRoot bounds the allowed output paths for rendered PDFs.
	OutputRoot string `json:"output_root" toml:"output_root"`
}

// Engine controls how symprint reaches and drives the typesetting engine.
type Engine struct {
	// Typst is the path or name of the typst binary. Empty = resolve "typst"
	// from PATH (standalone-first: the engine is never linked, only exec'd).
	Typst string `json:"typst" toml:"typst"`

	// FontPaths are extra directories scanned for fonts, passed to typst via
	// --font-path. Bundled brand fonts (Phase 2) are added automatically.
	FontPaths []string `json:"font_paths" toml:"font_paths"`

	// IgnoreSystemFonts forces deterministic font resolution by ignoring fonts
	// installed on the host (typst --ignore-system-fonts). Recommended for
	// reproducible, machine-independent output.
	IgnoreSystemFonts bool `json:"ignore_system_fonts" toml:"ignore_system_fonts"`

	// TimeoutSeconds bounds a single compile. 0 → 60s default.
	TimeoutSeconds int `json:"timeout_seconds" toml:"timeout_seconds"`
}

// Defaults applies when a document's frontmatter does not specify a value.
type Defaults struct {
	// Profile selected when a document does not set `profile:`.
	Profile string `json:"profile" toml:"profile"`

	// Reproducible exports SOURCE_DATE_EPOCH so the same input yields a
	// byte-identical PDF. Profiles and frontmatter can override this.
	Reproducible bool `json:"reproducible" toml:"reproducible"`
}

// Default returns the built-in configuration.
func Default() *Config {
	return &Config{
		Engine: Engine{
			Typst:             "",
			FontPaths:         nil,
			IgnoreSystemFonts: false,
			TimeoutSeconds:    60,
		},
		Defaults: Defaults{
			Profile:      "report",
			Reproducible: false,
		},
		MCP: MCP{
			OutputRoot: "",
		},
	}
}

// Timeout returns the configured compile timeout as a duration.
func (e Engine) Timeout() time.Duration {
	if e.TimeoutSeconds <= 0 {
		return 60 * time.Second
	}
	return time.Duration(e.TimeoutSeconds) * time.Second
}

var loader = configkit.NewLoader[Config](configkit.Options{
	AppName:   "symprint",
	EnvPrefix: "SYMPRINT",
}, Default)

// Load reads config from disk and environment, falling back to defaults.
func Load() (*Config, error) {
	return loader.Load()
}

// ResetForTest clears the cached configuration in the loader.
func ResetForTest() {
	loader.ResetCache()
}

// Path returns the global config file path (~/.config/symprint/config.toml).
func Path() string {
	return configkit.DefaultPath("symprint")
}

// DefaultConfigTOML is the template written by `symprint config init`.
func DefaultConfigTOML() string {
	profs := press.All()
	names := make([]string, 0, len(profs))
	for _, p := range profs {
		names = append(names, p.Name)
	}
	return fmt.Sprintf(`# symprint configuration
# Turns Markdown (+ a frontmatter contract) into beautiful PDFs via named
# use-case profiles. The typesetting engine (Typst) is reached over PATH and is
# never linked — symprint stays a single CGO-free Go binary.

[engine]
# Path or name of the typst binary. Empty = resolve "typst" from PATH.
# Install: 'brew install typst', 'winget install --id Typst.Typst',
# 'cargo install typst-cli', or https://github.com/typst/typst/releases
typst = ""

# Extra font directories (typst --font-path). Bundled fonts are added too.
font_paths = []

# Ignore host-installed fonts for deterministic, machine-independent output.
ignore_system_fonts = false

# Per-compile timeout in seconds.
timeout_seconds = 60

[defaults]
# Profile used when a document does not set 'profile:' in its frontmatter.
# Built-ins: %s  (see 'symprint profiles').
profile = "report"

# Export SOURCE_DATE_EPOCH for byte-identical output. Profiles/frontmatter win.
reproducible = false

[mcp]
# Optional root directory to constrain PDF rendering outputs.
# When set, any output_path outside this directory is rejected.
# output_root = "/path/to/allowed/output/dir"
`, strings.Join(names, ", "))
}
