package main

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-room/internal/journal"
)

// runLog implements the "log" subcommand.
func runLog(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("log", flag.ExitOnError)
	sinceFlag := fs.String("since", "", "Filter events since RFC3339 timestamp")
	untilFlag := fs.String("until", "", "Filter events until RFC3339 timestamp")
	kindFlag := fs.String("kind", "", "Filter events by kind")
	authorFlag := fs.String("author", "", "Filter events by author member ID")
	runFlag := fs.String("run", "", "Filter events by run ID")
	limitFlag := fs.Int("limit", 0, "Limit number of events returned")
	jsonFlag := fs.Bool("json", false, "Output events as NDJSON")
	if err := fs.Parse(args[2:]); err != nil {
		return int(exitcodes.ExitNoInput)
	}

	j := journal.New(filepath.Join(roomDir(), "journal"))
	res, err := j.QueryLog(journal.LogFilter{
		Since:  *sinceFlag,
		Until:  *untilFlag,
		Kind:   *kindFlag,
		Author: *authorFlag,
		Run:    *runFlag,
		Limit:  *limitFlag,
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "Error querying log: %v\n", err)
		return int(exitcodes.ExitGeneric)
	}

	journal.PrintLogWarnings(res.InvalidCount)

	if *jsonFlag {
		for _, ev := range res.Events {
			line, _ := ev.MarshalJSONLine()
			_, _ = fmt.Fprint(stdout, string(line))
		}
	} else {
		for _, ev := range res.Events {
			_, _ = fmt.Fprintln(stdout, journal.FormatEventHuman(ev))
		}
	}
	return int(exitcodes.ExitOK)
}
