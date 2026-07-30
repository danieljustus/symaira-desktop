package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/service"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

func newDocsCmd() *cobra.Command {
	docsCmd := &cobra.Command{
		Use:   "docs",
		Short: "Manage document metadata (contract v2)",
	}

	docsListCmd := &cobra.Command{
		Use:   "list",
		Short: "List indexed documents with filters",
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)

			f := sidecar.DocsFilter{}
			f.Type, _ = cmd.Flags().GetString("type")
			f.FileType, _ = cmd.Flags().GetString("file-type")
			f.Status, _ = cmd.Flags().GetString("status")
			f.Person, _ = cmd.Flags().GetString("person")
			f.Correspondent, _ = cmd.Flags().GetString("correspondent")
			f.Year, _ = cmd.Flags().GetString("year")
			f.DueBefore, _ = cmd.Flags().GetString("due-before")
			if minC, _ := cmd.Flags().GetInt("min-confidence"); minC > 0 {
				f.MinConfidence = &minC
			}
			if maxC, _ := cmd.Flags().GetInt("max-confidence"); maxC > 0 {
				f.MaxConfidence = &maxC
			}
			if cmd.Flags().Changed("asn") {
				asn, _ := cmd.Flags().GetInt("asn")
				if err := vault.ValidateASN(asn); err != nil {
					return fmt.Errorf("invalid --asn: %w", err)
				}
				f.ASN = &asn
			}

			results, err := svc.DocsList(f)
			if err != nil {
				return err
			}
			return outputResult(results)
		},
	}
	docsListCmd.Flags().String("type", "", "filter by document_type")
	docsListCmd.Flags().String("file-type", "", "filter by file type (note|document|meeting)")
	docsListCmd.Flags().String("status", "", "filter by status (open|paid|submitted|done|needs_review|waiting_for_reply)")
	docsListCmd.Flags().String("person", "", "filter by person (household member)")
	docsListCmd.Flags().String("correspondent", "", "filter by correspondent")
	docsListCmd.Flags().String("year", "", "filter by document year (e.g. 2026)")
	docsListCmd.Flags().String("due-before", "", "filter by due_date <= date (ISO-8601)")
	docsListCmd.Flags().Int("min-confidence", 0, "minimum confidence (0-100)")
	docsListCmd.Flags().Int("max-confidence", 0, "maximum confidence (0-100)")
	docsListCmd.Flags().Int("asn", 0, "filter by archive serial number")
	docsCmd.AddCommand(docsListCmd)

	docsReviewCmd := &cobra.Command{
		Use:   "review",
		Short: "List documents needing review (low confidence or missing metadata)",
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)

			threshold, _ := cmd.Flags().GetInt("threshold")
			if threshold <= 0 {
				threshold = cfg.ReviewThreshold
			}

			results, err := svc.DocsReview(threshold)
			if err != nil {
				return err
			}
			return outputResult(results)
		},
	}
	docsReviewCmd.Flags().Int("threshold", 0, "confidence threshold (default from config)")
	docsCmd.AddCommand(docsReviewCmd)

	return docsCmd
}
