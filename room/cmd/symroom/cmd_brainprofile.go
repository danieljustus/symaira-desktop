package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-room/internal/brainprofile"
)

// runBrainProfile implements the "brain-profile" subcommand.
func runBrainProfile(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("brain-profile", flag.ExitOnError)
	memberFlag := fs.String("member", "", "Member ID for the agent")
	installFlag := fs.Bool("install", false, "Install profile to symbrain config path")
	if err := fs.Parse(args[2:]); err != nil {
		return int(exitcodes.ExitNoInput)
	}

	if *memberFlag == "" {
		_, _ = fmt.Fprintln(stderr, "Usage: symroom brain-profile --member <id> [--install]")
		return int(exitcodes.ExitNoInput)
	}

	content, prof, err := brainprofile.Generate(roomDir(), *memberFlag)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "Error generating brain profile: %v\n", err)
		return int(exitcodes.ExitGeneric)
	}

	if *installFlag {
		msg, err := brainprofile.Install(prof.Name, content)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "Error installing profile: %v\n", err)
			return int(exitcodes.ExitGeneric)
		}
		_, _ = fmt.Fprintln(stdout, msg)
	} else {
		_, _ = fmt.Fprintln(stdout, content)
		_, _ = fmt.Fprintf(stdout, "# To install run:\n# symbrain install --harness <harness> --profile %s\n", prof.Name)
	}
	return int(exitcodes.ExitOK)
}
