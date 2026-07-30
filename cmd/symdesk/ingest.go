package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/service"
)

func newIngestCmd() *cobra.Command {
	ingestCmd := &cobra.Command{
		Use:   "ingest",
		Short: "Ingest a file or manage ingestion jobs",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)

			res, err := svc.Ingest(args[0])
			if err != nil {
				return err
			}
			return outputResult(res)
		},
	}

	ingestJobsCmd := &cobra.Command{
		Use:   "jobs",
		Short: "List ingestion jobs in the queue",
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)

			res, err := svc.IngestJobs()
			if err != nil {
				return err
			}
			if jsonFlag {
				fmt.Println(res)
				return nil
			}
			var rawJobs []map[string]interface{}
			if err := json.Unmarshal([]byte(res), &rawJobs); err == nil {
				for _, rj := range rawJobs {
					fmt.Printf("Job ID: %v | Status: %v | Source: %v\n", rj["id"], rj["status"], rj["source_path"])
				}
			} else {
				fmt.Println(res)
			}
			return nil
		},
	}

	ingestRetryCmd := &cobra.Command{
		Use:   "retry [job-id]",
		Short: "Retry a failed ingestion job",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)

			err = svc.IngestRetry(args[0])
			if err != nil {
				return err
			}

			res := map[string]interface{}{
				"status": "retried",
				"job_id": args[0],
			}
			return outputResult(res)
		},
	}
	ingestCmd.AddCommand(ingestJobsCmd)
	ingestCmd.AddCommand(ingestRetryCmd)

	return ingestCmd
}
