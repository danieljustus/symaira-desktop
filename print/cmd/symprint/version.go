package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-corekit/updatecheck"
	"github.com/danieljustus/symaira-corekit/versionkit"
)

func newVersionCmd() *cobra.Command {
	var check bool
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print the symprint version",
		RunE: func(cmd *cobra.Command, _ []string) error {
			info := versionkit.New("symprint", version, 1)
			if jsonOut {
				return info.Write(os.Stdout)
			}
			fmt.Println(info.String())
			if check {
				// The update check must never disturb normal operation:
				// the hint goes to stderr and errors are swallowed silently.
				if hint := updateHint(cmd.Context(), updatecheck.NewChecker("danieljustus", "symaira-print"), version); hint != "" {
					fmt.Fprintln(os.Stderr, hint)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "Check for updates on GitHub")
	return cmd
}

// updateHint asks the update checker for a newer symprint release and returns
// a human-readable hint (version + release URL). It returns "" when the
// current version is up to date, when it is a dev build (non-stable SemVer),
// or when the check fails — all errors are swallowed silently so the check
// never disturbs normal operation (24h cache TTL, non-blocking, offline-safe).
func updateHint(ctx context.Context, checker *updatecheck.Checker, currentVersion string) string {
	release, err := checker.Check(ctx, currentVersion)
	if err != nil || release == nil {
		return ""
	}
	return fmt.Sprintf("Update available: %s\nDownload: %s", release.TagName, release.HTMLURL)
}
