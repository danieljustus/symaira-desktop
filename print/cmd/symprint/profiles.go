package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-print/internal/press"
)

func newProfilesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profiles [name]",
		Short: "List the built-in profiles or show one in detail",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				p, ok := press.Lookup(args[0])
				if !ok {
					return exitcodes.Wrapf(nil, exitcodes.ExitNoInput, exitcodes.KindNotFound,
						"unknown profile %q", args[0])
				}
				if jsonOut {
					return printJSON(struct {
						press.Profile
						Capability press.Capability `json:"capability"`
					}{p, p.Capability()})
				}
				showProfile(p)
				return nil
			}

			all := press.All()
			if jsonOut {
				return printJSON(all)
			}
			fmt.Printf("%-10s  %-7s  %s\n", "PROFILE", "STAB.", "DESCRIPTION")
			for _, p := range all {
				fmt.Printf("%-10s  %-7s  %s\n", p.Name, p.Stability, truncate(p.Title, 60))
			}
			fmt.Println("\nRun 'symprint profiles <name>' for capabilities and an example.")
			return nil
		},
	}
	return cmd
}

func showProfile(p press.Profile) {
	cap := p.Capability()
	fmt.Printf("%s — %s\n\n", p.Name, p.Title)
	fmt.Printf("  %s\n\n", p.Description)
	fmt.Printf("  stability     %s\n", p.Stability)
	fmt.Printf("  engine        %s\n", p.Engine)
	fmt.Printf("  template      %s\n", p.Template)
	if p.Form != "" {
		fmt.Printf("  din 5008 form %s (default)\n", p.Form)
	}
	if len(p.PDFStandard) > 0 {
		fmt.Printf("  pdf standard  %s\n", strings.Join(p.PDFStandard, ", "))
	}
	if len(p.RequiredFields) > 0 {
		fmt.Printf("  required      %s\n", strings.Join(p.RequiredFields, ", "))
	}
	fmt.Printf("  guarantees    %s\n", capLabel(cap))
}

func capLabel(c press.Capability) string {
	var on []string
	if c.PDFA {
		on = append(on, "PDF/A")
	}
	if c.PDFUA {
		on = append(on, "PDF/UA")
	}
	if c.DINWindow {
		on = append(on, "DIN-window")
	}
	if c.Reproducible {
		on = append(on, "reproducible")
	}
	if len(on) == 0 {
		return "tagged PDF"
	}
	return strings.Join(on, ", ")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}
