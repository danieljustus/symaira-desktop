package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-room/internal/doctor"
)

// runDoctor implements the "doctor" subcommand.
func runDoctor(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	jsonFlag := fs.Bool("json", false, "Emit stable machine-readable JSON")
	if err := fs.Parse(args[2:]); err != nil {
		return int(exitcodes.ExitNoInput)
	}
	report, err := doctor.Run(roomDir())
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "Error running doctor: %v\n", err)
		return int(exitcodes.ExitGeneric)
	}
	if *jsonFlag {
		data, _ := json.MarshalIndent(report, "", "  ")
		_, _ = fmt.Fprintln(stdout, string(data))
	} else {
		for _, c := range report.Checks {
			_, _ = fmt.Fprintf(stdout, "[%s] %s: %s\n  remediation: %s\n", strings.ToUpper(string(c.Status)), c.Name, c.Message, c.Remediation)
		}
		for _, t := range report.Tools {
			_, _ = fmt.Fprintf(stdout, "[%s] %s: %s", strings.ToUpper(string(t.Status)), t.Name, t.Path)
			if t.Version != "" {
				_, _ = fmt.Fprintf(stdout, " (%s)", t.Version)
			}
			_, _ = fmt.Fprintf(stdout, "\n  remediation: %s\n", t.Remediation)
		}
	}
	if report.Failed {
		return int(exitcodes.ExitGeneric)
	}
	return int(exitcodes.ExitOK)
}
