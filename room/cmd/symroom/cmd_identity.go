package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"io"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-room/internal/identity"
)

// runIdentity implements the "identity" subcommand.
func runIdentity(args []string, stdout, stderr io.Writer) int {
	if len(args) < 3 {
		_, _ = fmt.Fprintln(stdout, "Usage: symroom identity <create|list|show|export> [args]")
		return int(exitcodes.ExitOK)
	}
	action := args[2]
	switch action {
	case "create":
		if len(args) < 4 {
			_, _ = fmt.Fprintln(stderr, "Usage: symroom identity create <name>")
			return int(exitcodes.ExitNoInput)
		}
		name := args[3]
		id, err := identity.Generate(name)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "Error generating identity: %v\n", err)
			return int(exitcodes.ExitGeneric)
		}
		if err := identity.Save(id); err != nil {
			_, _ = fmt.Fprintf(stderr, "Error saving identity: %v\n", err)
			return int(exitcodes.ExitGeneric)
		}
		_, _ = fmt.Fprintf(stdout, "Created identity %s (%s)\n", id.Name, id.MemberID)
		return int(exitcodes.ExitOK)
	case "list":
		names, err := identity.List()
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "Error listing identities: %v\n", err)
			return int(exitcodes.ExitGeneric)
		}
		for _, n := range names {
			_, _ = fmt.Fprintln(stdout, n)
		}
		return int(exitcodes.ExitOK)
	case "show":
		if len(args) < 4 {
			_, _ = fmt.Fprintln(stderr, "Usage: symroom identity show <name>")
			return int(exitcodes.ExitNoInput)
		}
		name := args[3]
		id, err := identity.Load(name)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "Error loading identity: %v\n", err)
			return int(exitcodes.ExitNotFound)
		}
		_, _ = fmt.Fprintf(stdout, "Name: %s\nMember ID: %s\nPublic Key: %x\n", id.Name, id.MemberID, id.PublicKey)
		return int(exitcodes.ExitOK)
	case "export":
		fs := flag.NewFlagSet("identity export", flag.ExitOnError)
		pubFlag := fs.Bool("public", false, "Export public key only")
		if err := fs.Parse(args[3:]); err != nil {
			return int(exitcodes.ExitNoInput)
		}
		if fs.NArg() < 1 {
			_, _ = fmt.Fprintln(stderr, "Usage: symroom identity export <name> --public")
			return int(exitcodes.ExitNoInput)
		}
		name := fs.Arg(0)
		id, err := identity.Load(name)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "Error loading identity: %v\n", err)
			return int(exitcodes.ExitNotFound)
		}
		if *pubFlag {
			_, _ = fmt.Fprintln(stdout, hex.EncodeToString(id.PublicKey))
		} else {
			_, _ = fmt.Fprintf(stderr, "Exporting private key is forbidden for security\n")
			return int(exitcodes.ExitForbidden)
		}
		return int(exitcodes.ExitOK)
	default:
		_, _ = fmt.Fprintf(stderr, "Unknown identity action: %s\n", action)
		return int(exitcodes.ExitNoInput)
	}
}
