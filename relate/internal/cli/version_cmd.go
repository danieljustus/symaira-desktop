package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"

	"github.com/danieljustus/symaira-corekit/updatecheck"
	"github.com/danieljustus/symaira-relate/internal/version"
)

func init() {
	Register(&Command{
		Name:  "version",
		Short: "Print version information",
		Run:   runVersion,
	})
}

func runVersion(ctx context.Context, iostreams IO, args []string) error {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(iostreams.Stderr)
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON")
	check := fs.Bool("check", false, "check for updates on GitHub")
	if err := fs.Parse(args); err != nil {
		return err
	}

	info := version.Get()
	if *jsonOut {
		// Emits the versionkit handshake payload {tool, version,
		// schema_version} plus api_version (docs/CLI_CONTRACT.md).
		return json.NewEncoder(iostreams.Stdout).Encode(info)
	}
	fmt.Fprintf(iostreams.Stdout, "%s %s (schema %d)\n", info.Tool, info.Version, info.SchemaVersion)

	if *check {
		checker := updatecheck.NewChecker("danieljustus", "symaira-relate")
		release, err := checker.Check(ctx, version.Version)
		if err != nil {
			fmt.Fprintf(iostreams.Stderr, "update check failed: %v\n", err)
			return nil
		}
		if release != nil {
			fmt.Fprintf(iostreams.Stderr, "Update available: %s\n", release.TagName)
			fmt.Fprintf(iostreams.Stderr, "Download: %s\n", release.HTMLURL)
		} else {
			fmt.Fprintf(iostreams.Stderr, "Already up to date.\n")
		}
	}
	return nil
}
