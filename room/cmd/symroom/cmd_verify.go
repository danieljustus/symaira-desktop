package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-room/internal/journal"
)

// runVerify implements the "verify" subcommand.
func runVerify(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	jsonFlag := fs.Bool("json", false, "Output verification findings as JSON")
	if err := fs.Parse(args[2:]); err != nil {
		return int(exitcodes.ExitNoInput)
	}

	j := journal.New(filepath.Join(roomDir(), "journal"))
	report, err := j.Verify()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "Error verifying journal: %v\n", err)
		return int(exitcodes.ExitGeneric)
	}

	if *jsonFlag {
		data, _ := json.MarshalIndent(report, "", "  ")
		_, _ = fmt.Fprintln(stdout, string(data))
	} else {
		if report.Valid {
			_, _ = fmt.Fprintln(stdout, "Journal verification PASSED: zero findings")
		} else {
			_, _ = fmt.Fprintf(stdout, "Journal verification FAILED: %d finding(s):\n", len(report.Findings))
			for _, f := range report.Findings {
				_, _ = fmt.Fprintf(stdout, "  - [%s] %s (event: %s, author: %s)\n", f.Code, f.Message, f.EventID, f.Author)
			}
		}
	}

	if !report.Valid {
		return int(exitcodes.ExitGeneric)
	}
	return int(exitcodes.ExitOK)
}
