package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/retention"
	"github.com/danieljustus/symaira-desktop/internal/service"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
)

func newRetentionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "retention",
		Short: "Evaluate and apply document retention rules",
	}

	cmd.AddCommand(newRetentionEvalCmd())
	cmd.AddCommand(newRetentionListCmd())
	cmd.AddCommand(newRetentionAcceptCmd())
	cmd.AddCommand(newRetentionRejectCmd())
	cmd.AddCommand(newRetentionDiffCmd())
	cmd.AddCommand(newRetentionHistoryCmd())

	return cmd
}

func newRetentionEvalCmd() *cobra.Command {
	var rulesFile string
	evalCmd := &cobra.Command{
		Use:   "eval",
		Short: "Evaluate retention rules and stage a reviewable proposal",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer closeWithWarning("sidecar database", db.Close)

			if rulesFile == "" {
				rulesFile = filepath.Join(vRoot, ".symdesk", "retention-rules.yaml")
			}

			rules, err := retention.LoadRules(rulesFile)
			if err != nil {
				return fmt.Errorf("load rules from %s: %w", rulesFile, err)
			}

			docs, err := db.DocsList(sidecar.DocsFilter{})
			if err != nil {
				return fmt.Errorf("list documents: %w", err)
			}

			now := time.Now()
			var allItems []retention.ProposalItem

			for _, rule := range rules {
				// Filter documents by selector
				var matched []retention.DocMeta
				for _, d := range docs {
					meta := retention.DocMeta{
						Path:          d.Path,
						Title:         d.Title,
						DocumentDate:  d.DocumentDate,
						Status:        d.Status,
						Correspondent: d.Correspondent,
						DocumentType:  d.DocumentType,
						Person:        d.Person,
					}
					if rule.Selector.Matches(meta) {
						matched = append(matched, meta)
					}
				}

				items := retention.Evaluate(rule, matched, now)
				for i := range items {
					items[i].RuleName = rule.Name
				}
				allItems = append(allItems, items...)
			}

			runID := fmt.Sprintf("ret-%d", now.Unix())
			proposal := retention.Proposal{
				RunID:    runID,
				RuleName: "batch",
				Created:  now.UTC(),
				Status:   "pending",
				Items:    allItems,
			}

			if err := retention.WriteProposal(vRoot, proposal); err != nil {
				return err
			}

			return outputResult(map[string]interface{}{
				"status":     "pending",
				"run_id":     runID,
				"item_count": len(allItems),
				"items":      allItems,
			})
		},
	}
	evalCmd.Flags().StringVar(&rulesFile, "rules", "", "path to retention rules YAML file (default: <vault>/.symdesk/retention-rules.yaml)")
	return evalCmd
}

func newRetentionListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List documents due to expire",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, _, err := initServiceDeps()
			if err != nil {
				return err
			}

			dir := retention.ProposalDir(vRoot)
			entries, err := os.ReadDir(dir)
			if err != nil {
				if os.IsNotExist(err) {
					return outputResult(map[string]interface{}{"proposals": []string{}, "message": "no pending proposals"})
				}
				return err
			}

			var proposals []retention.Proposal
			for _, entry := range entries {
				if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
					continue
				}
				runID := entry.Name()[:len(entry.Name())-5] // strip .json
				p, err := retention.LoadProposal(vRoot, runID)
				if err != nil {
					continue
				}
				if p.Status != "pending" {
					continue
				}
				proposals = append(proposals, p)
			}

			if jsonFlag {
				return outputResult(proposals)
			}

			if len(proposals) == 0 {
				fmt.Println("no pending retention proposals")
				return nil
			}

			for _, p := range proposals {
				fmt.Printf("Proposal %s (%s): %d items pending review\n", p.RunID, p.Created.Local().Format("2006-01-02 15:04"), len(p.Items))
				for _, item := range p.Items {
					fmt.Printf("  %s — expires %s → %s\n", item.Path, item.ExpiresAt, item.Action)
				}
			}
			return nil
		},
	}
}

func newRetentionAcceptCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "accept <run-id>",
		Short: "Accept a pending retention proposal",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer closeWithWarning("sidecar database", db.Close)

			p, err := retention.LoadProposal(vRoot, args[0])
			if err != nil {
				return err
			}
			if p.Status != "pending" {
				return fmt.Errorf("proposal %s is %s", args[0], p.Status)
			}

			svc := service.New(vRoot, db)
			now := time.Now()
			var acted int

			for _, item := range p.Items {
				// Re-verify the document still matches before acting
				stillMatches := true
				// (simplified: trust the proposal for now)

				if !stillMatches {
					continue
				}

				switch item.Action {
				case retention.ActionTrash:
					_, err := svc.NoteDelete(item.Path)
					if err != nil {
						fmt.Fprintf(os.Stderr, "failed to trash %s: %v\n", item.Path, err)
						continue
					}
				case retention.ActionFlagReview:
					err := svc.DocStatus(item.Path, "needs_review")
					if err != nil {
						fmt.Fprintf(os.Stderr, "failed to flag %s: %v\n", item.Path, err)
						continue
					}
				}

				if err := retention.AppendHistory(vRoot, retention.HistoryEntry{
					Timestamp: now.UTC(),
					RuleName:  item.RuleName,
					Action:    item.Action,
					Path:      item.Path,
					Title:     item.Title,
				}); err != nil {
					return fmt.Errorf("append retention history for %s: %w", item.Path, err)
				}
				acted++
			}

			p.Status = "accepted"
			if err := retention.WriteProposal(vRoot, p); err != nil {
				return fmt.Errorf("save accepted retention proposal: %w", err)
			}

			return outputResult(map[string]interface{}{
				"status": "accepted",
				"run_id": args[0],
				"acted":  acted,
			})
		},
	}
}

func newRetentionRejectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reject <run-id>",
		Short: "Reject a pending retention proposal",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, _, err := initServiceDeps()
			if err != nil {
				return err
			}
			p, err := retention.LoadProposal(vRoot, args[0])
			if err != nil {
				return err
			}
			p.Status = "rejected"
			if err := retention.WriteProposal(vRoot, p); err != nil {
				return err
			}
			return outputResult(map[string]string{"status": "rejected", "run_id": args[0]})
		},
	}
}

func newRetentionDiffCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "diff <run-id>",
		Short: "Show the proposed retention actions",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, _, err := initServiceDeps()
			if err != nil {
				return err
			}
			p, err := retention.LoadProposal(vRoot, args[0])
			if err != nil {
				return err
			}
			return outputResult(p.Items)
		},
	}
}

func newRetentionHistoryCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "history",
		Short: "Show the history of executed retention actions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, _, err := initServiceDeps()
			if err != nil {
				return err
			}
			entries, err := retention.LoadHistory(vRoot)
			if err != nil {
				return err
			}
			if jsonFlag {
				return outputResult(entries)
			}
			if len(entries) == 0 {
				fmt.Println("no retention actions recorded")
				return nil
			}
			for _, e := range entries {
				fmt.Printf("%s  %s  %s → %s\n",
					e.Timestamp.Local().Format("2006-01-02 15:04:05"),
					e.RuleName,
					e.Path,
					e.Action)
			}
			return nil
		},
	}
}
