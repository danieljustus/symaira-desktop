package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-print/internal/press"
)

func newRenderCmd() *cobra.Command {
	var (
		output       string
		profile      string
		standard     string
		fontPath     string
		reproducible bool
		reproSet     bool
		overwrite    bool
	)

	cmd := &cobra.Command{
		Use:   "render <input.md>",
		Short: "Render a Markdown document to PDF using a profile",
		Long: `Render a Markdown document (with YAML frontmatter) to PDF.

The profile is chosen from the frontmatter (profile:) or --profile. Output
defaults to the input name with a .pdf extension. Precedence (low → high):
config defaults < profile < frontmatter < CLI flags.

An existing PDF output is replaced normally; an existing non-PDF file at the
output path is only overwritten with --overwrite.

Examples:
  symprint render brief.md
  symprint render anhoerung.md --profile behoerde -o out.pdf
  symprint render report.md --pdf-standard a-2b --reproducible`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfigOrWarn(cmd)

			src, err := os.ReadFile(args[0])
			if err != nil {
				return exitcodes.Wrap(err, exitcodes.ExitNoInput, exitcodes.KindNotFound, "read input")
			}
			srcAbs, err := filepath.Abs(args[0])
			if err != nil {
				return exitcodes.Wrap(err, exitcodes.ExitNoInput, exitcodes.KindNotFound, "resolve input path")
			}

			out := output
			if out == "" {
				out = strings.TrimSuffix(args[0], filepath.Ext(args[0])) + ".pdf"
			}

			// Fail closed on existing non-PDF output and directories, matching the
			// MCP overwrite guard: only explicit --overwrite bypasses the refusal.
			// Existing PDF outputs keep their normal replace-without-flag behavior.
			if err := press.CheckOverwrite(out, overwrite); err != nil {
				return exitcodes.Wrap(err, exitcodes.ExitConflict, exitcodes.KindConflict, "output")
			}

			req := press.Request{
				Source:          src,
				SourceName:      args[0],
				SourceDir:       filepath.Dir(srcAbs),
				OutputPath:      out,
				ProfileOverride: profile, // empty unless --profile was passed
				DefaultProfile:  cfg.Defaults.Profile,
				Engine:          engineFromConfig(cfg),
			}

			if fontPath != "" {
				req.Engine.FontPaths = append(req.Engine.FontPaths, fontPath)
			}
			if standard != "" {
				req.StandardOverride = splitCSV(standard)
			}
			if reproSet {
				req.Reproducible = &reproducible
			} else if cfg.Defaults.Reproducible {
				v := true
				req.Reproducible = &v
			}

			res, err := press.Render(cmd.Context(), req)
			if err != nil {
				return mapPressError(err)
			}

			if jsonOut {
				return printJSON(res)
			}
			fmt.Printf("✓ %s\n", res.OutputPath)
			fmt.Printf("  profile %s · engine typst %s · %s · %.1f kB · %d ms\n",
				res.Profile, res.EngineVersion, standardLabel(res.PDFStandard), float64(res.Bytes)/1024, res.DurationMS)
			return nil
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", "", "output PDF path (default: input with .pdf)")
	cmd.Flags().StringVarP(&profile, "profile", "p", "", "profile to use (overrides frontmatter)")
	cmd.Flags().StringVar(&standard, "pdf-standard", "", "comma-separated typst --pdf-standard (e.g. a-2a,ua-1)")
	cmd.Flags().StringVar(&fontPath, "font-path", "", "extra font directory (typst --font-path); embedded fonts always included")
	cmd.Flags().BoolVar(&reproducible, "reproducible", false, "export SOURCE_DATE_EPOCH for byte-stable output")
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "allow overwriting an existing non-PDF file at the output path")
	cmd.PreRun = func(cmd *cobra.Command, _ []string) {
		reproSet = cmd.Flags().Changed("reproducible")
	}
	return cmd
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func standardLabel(std []string) string {
	if len(std) == 0 {
		return "PDF (tagged)"
	}
	return "PDF/" + strings.ToUpper(strings.Join(std, "+"))
}
