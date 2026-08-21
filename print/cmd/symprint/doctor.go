package main

import (
	"fmt"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-print/internal/press"
)

func newDoctorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check the rendering engine and optional tools are available",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := loadConfigOrWarn(cmd)
			typst := press.DetectTypst(cmd.Context(), cfg.Engine.Typst)
			pandoc := lookOptional("pandoc")
			verapdf := lookOptional("verapdf")

			if jsonOut {
				return printJSON(map[string]any{
					"typst":   typst,
					"pandoc":  pandoc,
					"verapdf": verapdf,
				})
			}

			fmt.Println("symprint doctor")
			fmt.Println()
			fmt.Printf("  %s  typst    %s\n", okGlyph(typst.Available), engineLine(typst))
			fmt.Printf("  %s  pandoc   %s  (optional: high-fidelity Markdown path)\n", okGlyph(pandoc.Available), pathOrDash(pandoc))
			fmt.Printf("  %s  verapdf  %s  (optional: PDF/A + PDF/UA validation)\n", okGlyph(verapdf.Available), pathOrDash(verapdf))

			if !typst.Available {
				fmt.Println()
				fmt.Println(typst.Hint)
			}
			return nil
		},
	}
	return cmd
}

func lookOptional(name string) press.EngineInfo {
	info := press.EngineInfo{Name: name}
	if path, err := exec.LookPath(name); err == nil {
		info.Path = path
		info.Available = true
	}
	return info
}

func engineLine(info press.EngineInfo) string {
	if !info.Available {
		return "not found"
	}
	v := info.Version
	if v == "" {
		v = "?"
	}
	return fmt.Sprintf("%s  (%s)", v, info.Path)
}

func pathOrDash(info press.EngineInfo) string {
	if info.Available {
		return info.Path
	}
	return "—"
}

func okGlyph(ok bool) string {
	if ok {
		return "✓"
	}
	return "✗"
}
