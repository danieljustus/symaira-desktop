package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-room/internal/artifact"
)

// runArtifact implements the "artifact" subcommand.
func runArtifact(args []string, stdout, stderr io.Writer) int {
	if len(args) < 3 {
		_, _ = fmt.Fprintln(stdout, "Usage: symroom artifact <link|unlink|list> [flags] [args]")
		return int(exitcodes.ExitOK)
	}
	sub := args[2]
	switch sub {
	case "link":
		fs := flag.NewFlagSet("artifact link", flag.ExitOnError)
		titleFlag := fs.String("title", "", "Artifact title")
		idFlag := fs.String("identity", "", "Author identity name")
		if err := fs.Parse(args[3:]); err != nil {
			return int(exitcodes.ExitNoInput)
		}
		if fs.NArg() < 1 {
			_, _ = fmt.Fprintln(stderr, "Usage: symroom artifact link <path> [--title ...] [--identity <name>]")
			return int(exitcodes.ExitNoInput)
		}
		filePath := fs.Arg(0)
		id := resolveIdentity(*idFlag)
		ev, err := artifact.Link(roomDir(), "", filePath, *titleFlag, id)
		if err != nil {
			if errors.Is(err, artifact.ErrOutsideRoot) {
				_, _ = fmt.Fprintln(stderr, "Error: path is outside artifact root")
				return int(exitcodes.ExitNoInput)
			}
			_, _ = fmt.Fprintf(stderr, "Error linking artifact: %v\n", err)
			return int(exitcodes.ExitGeneric)
		}
		_, _ = fmt.Fprintln(stdout, ev.ID)
		return int(exitcodes.ExitOK)

	case "unlink":
		fs := flag.NewFlagSet("artifact unlink", flag.ExitOnError)
		idFlag := fs.String("identity", "", "Author identity name")
		if err := fs.Parse(args[3:]); err != nil {
			return int(exitcodes.ExitNoInput)
		}
		if fs.NArg() < 1 {
			_, _ = fmt.Fprintln(stderr, "Usage: symroom artifact unlink <artifact_id> [--identity <name>]")
			return int(exitcodes.ExitNoInput)
		}
		artID := fs.Arg(0)
		id := resolveIdentity(*idFlag)
		ev, err := artifact.Unlink(roomDir(), artID, id)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "Error unlinking artifact: %v\n", err)
			return int(exitcodes.ExitGeneric)
		}
		_, _ = fmt.Fprintln(stdout, ev.ID)
		return int(exitcodes.ExitOK)

	case "list":
		fs := flag.NewFlagSet("artifact list", flag.ExitOnError)
		jsonFlag := fs.Bool("json", false, "Output artifacts as JSON")
		if err := fs.Parse(args[3:]); err != nil {
			return int(exitcodes.ExitNoInput)
		}
		list, err := artifact.List(roomDir(), "")
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "Error listing artifacts: %v\n", err)
			return int(exitcodes.ExitGeneric)
		}
		if *jsonFlag {
			data, _ := json.MarshalIndent(list, "", "  ")
			_, _ = fmt.Fprintln(stdout, string(data))
		} else {
			for _, ref := range list {
				_, _ = fmt.Fprintf(stdout, "%s\t%s\t[%s]\t%s\n", ref.ID, ref.Path, ref.Status, ref.Title)
			}
		}
		return int(exitcodes.ExitOK)

	default:
		_, _ = fmt.Fprintf(stderr, "Unknown artifact action: %s\n", sub)
		return int(exitcodes.ExitNoInput)
	}
}
