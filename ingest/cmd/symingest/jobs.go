package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"strconv"
	"text/tabwriter"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-ingest/internal/store"
)

func runJobs(args []string) error {
	fs := flag.NewFlagSet("jobs", flag.ContinueOnError)
	jsonFlag := fs.Bool("json", false, "Output jobs in JSON format")
	limitFlag := fs.Int("limit", 100, "Maximum number of jobs to return")
	ocrLang, vault, archive, db := registerSharedFlags(fs)
	configureUsage(fs, "jobs [flags]", "List ingestion jobs in the queue.")
	help, err := parseFlags(fs, args, "invalid jobs flags")
	if help || err != nil {
		return err
	}
	cfg, err := resolveConfig(fs, ocrLang, vault, archive, db)
	if err != nil {
		return err
	}

	st, err := store.Open(cfg.db)
	if err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitConfig, exitcodes.KindConfig,
			"failed to open document store")
	}
	defer st.Close()

	ctx := context.Background()
	jobs, err := st.ListJobs(ctx, *limitFlag)
	if err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal,
			"failed to list jobs")
	}

	if *jsonFlag {
		if jobs == nil {
			// Ensure we output empty array instead of null
			fmt.Fprintln(stdout, "[]")
			return nil
		}
		data, err := json.MarshalIndent(jobs, "", "  ")
		if err != nil {
			return exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal,
				"failed to marshal jobs to JSON")
		}
		fmt.Fprintln(stdout, string(data))
		return nil
	}

	if len(jobs) == 0 {
		fmt.Fprintln(stdout, "No jobs in queue.")
		return nil
	}

	w := tabwriter.NewWriter(stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "ID\tDOCUMENT ID\tSTATUS\tATTEMPTS\tKIND\tSOURCE PATH")
	for _, j := range jobs {
		fmt.Fprintf(w, "%d\t%d\t%s\t%d\t%s\t%s\n",
			j.ID, j.DocumentID, j.Status, j.Attempts, j.Kind, j.SourcePath)
	}
	w.Flush()
	return nil
}

func runRetry(args []string) error {
	fs := flag.NewFlagSet("retry", flag.ContinueOnError)
	ocrLang, vault, archive, db := registerSharedFlags(fs)
	configureUsage(fs, "retry [flags] <job-id>", "Retry a failed job by resetting its status to pending.")
	help, err := parseFlags(fs, args, "invalid retry flags")
	if help || err != nil {
		return err
	}
	cfg, err := resolveConfig(fs, ocrLang, vault, archive, db)
	if err != nil {
		return err
	}
	remaining := fs.Args()
	if len(remaining) == 0 {
		fs.Usage()
		return nil
	}

	jobIDStr := remaining[0]
	jobID, err := strconv.ParseInt(jobIDStr, 10, 64)
	if err != nil {
		return exitcodes.Wrapf(err, exitcodes.ExitData, exitcodes.KindValidation,
			"invalid job ID %q; must be an integer", jobIDStr)
	}

	st, err := store.Open(cfg.db)
	if err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitConfig, exitcodes.KindConfig,
			"failed to open document store")
	}
	defer st.Close()

	ctx := context.Background()
	if err := st.RetryJob(ctx, jobID); err != nil {
		return exitcodes.Wrapf(err, exitcodes.ExitGeneric, exitcodes.KindInternal,
			"failed to retry job %d", jobID)
	}

	fmt.Fprintf(stdout, "Job %d status set to pending. Background workers will process it shortly.\n", jobID)
	return nil
}
