package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-room/internal/version"
)

// runVersion implements the "version" subcommand.
func runVersion(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("version", flag.ExitOnError)
	jsonFlag := fs.Bool("json", false, "Emit version info in JSON format")
	if err := fs.Parse(args[2:]); err != nil {
		return int(exitcodes.ExitNoInput)
	}
	info := version.GetInfo()
	if *jsonFlag {
		if err := info.Write(stdout); err != nil {
			return int(exitcodes.ExitGeneric)
		}
	} else {
		_, _ = fmt.Fprintln(stdout, info.String())
	}
	return int(exitcodes.ExitOK)
}
