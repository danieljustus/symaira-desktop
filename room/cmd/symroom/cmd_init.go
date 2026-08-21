package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-room/internal/config"
	"github.com/danieljustus/symaira-room/internal/room"
)

// runInit implements the "init" subcommand.
func runInit(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	nameFlag := fs.String("name", "Default Room", "Room display name")
	idFlag := fs.String("identity", "", "Owner identity name")
	if err := fs.Parse(args[2:]); err != nil {
		return int(exitcodes.ExitNoInput)
	}

	targetDir := roomDir()
	if fs.NArg() > 0 {
		targetDir = fs.Arg(0)
	}

	idName := *idFlag
	if idName == "" {
		cfg := config.LoadOrExit()
		idName = cfg.DefaultIdentity
	}
	if idName == "" && fs.NArg() == 0 {
		_, _ = fmt.Fprintln(stdout, "Usage: symroom init <dir> --identity <name> [--name <display_name>]")
		return int(exitcodes.ExitOK)
	}

	id := resolveIdentity(*idFlag)

	roomCfg, err := room.Init(targetDir, *nameFlag, id)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "Error initializing room: %v\n", err)
		return int(exitcodes.ExitGeneric)
	}

	_, _ = fmt.Fprintf(stdout, "Initialized room %s in %s (owner: %s)\n", roomCfg.ID, targetDir, id.Name)
	return int(exitcodes.ExitOK)
}
