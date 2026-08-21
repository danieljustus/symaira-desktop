package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-print/internal/press"
)

func newValidateCmd() *cobra.Command {
	var profile string
	cmd := &cobra.Command{
		Use:   "validate <input.md>",
		Short: "Check a document against its profile's frontmatter contract (no render)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfigOrWarn(cmd)
			src, err := os.ReadFile(args[0])
			if err != nil {
				return exitcodes.Wrap(err, exitcodes.ExitNoInput, exitcodes.KindNotFound, "read input")
			}

			doc, err := press.Parse(src)
			if err != nil {
				return mapPressError(err)
			}

			name := profile
			if name == "" {
				name = doc.Front.Profile
			}
			if name == "" {
				name = cfg.Defaults.Profile
			}
			p, ok := press.Lookup(name)
			if !ok {
				return exitcodes.Wrapf(nil, exitcodes.ExitNoInput, exitcodes.KindNotFound,
					"unknown profile %q", name)
			}

			issues := doc.Validate(p)
			if jsonOut {
				return printJSON(map[string]any{"profile": p.Name, "issues": issues, "ok": !anyError(issues)})
			}

			if len(issues) == 0 {
				fmt.Printf("✓ valid for profile %q\n", p.Name)
				return nil
			}
			for _, is := range issues {
				fmt.Printf("  %s %s — %s\n", sevGlyph(is.Severity), is.Field, is.Message)
			}
			if anyError(issues) {
				return exitcodes.Wrap(nil, exitcodes.ExitData, exitcodes.KindValidation,
					"document does not satisfy the contract")
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&profile, "profile", "p", "", "profile to validate against (overrides frontmatter)")
	return cmd
}

func anyError(issues []press.Issue) bool {
	for _, is := range issues {
		if is.Severity == "error" {
			return true
		}
	}
	return false
}

func sevGlyph(sev string) string {
	if sev == "error" {
		return "✗"
	}
	return "!"
}
