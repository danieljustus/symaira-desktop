package main

import (
	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/service"
)

func newDocCmd() *cobra.Command {
	docCmd := &cobra.Command{
		Use:   "doc",
		Short: "Mutate document metadata (status, due date, archive serial number)",
	}

	docStatusCmd := &cobra.Command{
		Use:   "status [file...] [status]",
		Short: "Set document status on one or more files (open|paid|submitted|done|needs_review|waiting_for_reply)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDocBatch(cmd, args, func(svc *service.Service, file, value string) error {
				return svc.DocStatus(file, value)
			})
		},
	}
	addBatchStdinFlag(docStatusCmd)
	docCmd.AddCommand(docStatusCmd)

	docDueCmd := &cobra.Command{
		Use:   "due [file...] [date]",
		Short: "Set document due date (ISO-8601) on one or more files",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDocBatch(cmd, args, func(svc *service.Service, file, value string) error {
				return svc.DocDue(file, value)
			})
		},
	}
	addBatchStdinFlag(docDueCmd)
	docCmd.AddCommand(docDueCmd)

	docTypeCmd := &cobra.Command{
		Use:   "type [file...] [type]",
		Short: "Set document_type on one or more files",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDocBatch(cmd, args, func(svc *service.Service, file, value string) error {
				return svc.DocType(file, value)
			})
		},
	}
	addBatchStdinFlag(docTypeCmd)
	docCmd.AddCommand(docTypeCmd)

	docCorrespondentCmd := &cobra.Command{
		Use:   "correspondent [file...] [name]",
		Short: "Set correspondent on one or more files",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDocBatch(cmd, args, func(svc *service.Service, file, value string) error {
				return svc.DocCorrespondent(file, value)
			})
		},
	}
	addBatchStdinFlag(docCorrespondentCmd)
	docCmd.AddCommand(docCorrespondentCmd)

	docTagCmd := &cobra.Command{
		Use:   "tag",
		Short: "Add or remove tags on one or more files",
	}
	docCmd.AddCommand(docTagCmd)

	docTagAddCmd := &cobra.Command{
		Use:   "add [tag] [file...]",
		Short: "Add a tag to one or more files",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDocTagBatch(cmd, args, func(svc *service.Service, file, tag string) error {
				return svc.DocTagAdd(file, tag)
			})
		},
	}
	addBatchStdinFlag(docTagAddCmd)
	docTagCmd.AddCommand(docTagAddCmd)

	docTagRemoveCmd := &cobra.Command{
		Use:   "remove [tag] [file...]",
		Short: "Remove a tag from one or more files",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDocTagBatch(cmd, args, func(svc *service.Service, file, tag string) error {
				return svc.DocTagRemove(file, tag)
			})
		},
	}
	addBatchStdinFlag(docTagRemoveCmd)
	docTagCmd.AddCommand(docTagRemoveCmd)

	docASNCmd := &cobra.Command{
		Use:   "asn [file] [next|N]",
		Short: "Assign a unique archive serial number",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			asn, err := service.New(vRoot, db).DocASN(args[0], args[1])
			if err != nil {
				return err
			}
			return outputResult(map[string]interface{}{"status": "updated", "file": args[0], "asn": asn})
		},
	}
	docCmd.AddCommand(docASNCmd)

	return docCmd
}
