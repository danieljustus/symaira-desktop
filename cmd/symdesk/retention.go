package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

			svc := service.New(vRoot, db)
			docs, err := svc.DocsList(sidecar.DocsFilter{})
			if err != nil {
				return fmt.Errorf("list documents: %w", err)
			}

			now := time.Now()
			var allItems []retention.ProposalItem
			var stateFailures []string

			for _, rule := range rules {
				for _, d := range docs {
					state, stateErr := svc.RetentionState(d.Path)
					if stateErr != nil {
						stateFailures = append(stateFailures, fmt.Sprintf("%s: %v", d.Path, stateErr))
						continue
					}
					// Dataset handles are eligible only for the rule they declare.
					// This check uses the authoritative handle, not sidecar metadata.
					if state.Dataset {
						if rule.Name != state.RuleName {
							continue
						}
					}
					items := retention.Evaluate(rule, []retention.DocMeta{state.Meta}, now)
					for i := range items {
						items[i].RuleName = rule.Name
						items[i].Fingerprint = state.Fingerprint
					}
					allItems = append(allItems, items...)
				}
			}

			if len(stateFailures) > 0 {
				return fmt.Errorf("retention evaluation failed closed: %s", strings.Join(stateFailures, "; "))
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
				if p.Status != retention.ProposalStatusPending && p.Status != retention.ProposalStatusFailed && p.Status != retention.ProposalStatusPartial {
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
			if p.Status != retention.ProposalStatusPending && p.Status != retention.ProposalStatusFailed && p.Status != retention.ProposalStatusPartial && p.Status != retention.ProposalStatusAccepted {
				return fmt.Errorf("proposal %s is %s", args[0], p.Status)
			}

			svc := service.New(vRoot, db)
			now := time.Now()
			acted := 0
			var failures []string

			for i := range p.Items {
				item := &p.Items[i]
				if item.Status == retention.ProposalItemStatusAccepted {
					continue
				}
				if item.Status == retention.ProposalItemStatusActionCompleted {
					if err := retention.AppendHistory(vRoot, retention.HistoryEntry{
						Timestamp: now.UTC(),
						ActionID:  retention.StableActionID(p.RunID, i),
						RuleName:  item.RuleName,
						Action:    item.Action,
						Path:      item.Path,
						Title:     item.Title,
					}); err != nil {
						item.Failure = fmt.Sprintf("history append failed after action: %v", err)
						failures = append(failures, item.Path+": "+item.Failure)
						continue
					}
					item.Status = retention.ProposalItemStatusAccepted
					item.Failure = ""
					p.Status = retentionProposalStatus(p.Items)
					if err := retention.WriteProposal(vRoot, p); err != nil {
						return fmt.Errorf("save retention progress for %s: %w", item.Path, err)
					}
					continue
				}

				state, stateErr := svc.RetentionState(item.Path)
				if stateErr != nil {
					failures = appendRetentionFailure(item, fmt.Sprintf("cannot re-read authoritative state: %v", stateErr), failures)
					continue
				}
				if expectedSlug, isDatasetPath := datasetRetentionSlug(item.Path); isDatasetPath {
					if !state.Dataset || state.Meta.Path != item.Path || state.Meta.Title == "" || expectedSlug == "" {
						failures = appendRetentionFailure(item, "dataset handle is missing or changed", failures)
						continue
					}
					if item.RuleName == "" || state.RuleName != item.RuleName {
						failures = appendRetentionFailure(item, fmt.Sprintf("dataset declares retention rule %q, proposal requires %q", state.RuleName, item.RuleName), failures)
						continue
					}
				}
				if item.Fingerprint == "" {
					failures = appendRetentionFailure(item, "proposal has no fingerprint; re-run retention eval", failures)
					continue
				}
				if state.Fingerprint != item.Fingerprint {
					failures = appendRetentionFailure(item, "proposal is stale: authoritative fingerprint changed", failures)
					continue
				}

				var actionErr error
				if slug, isDatasetPath := datasetRetentionSlug(item.Path); isDatasetPath {
					if item.Action != retention.ActionTrash {
						actionErr = fmt.Errorf("dataset retention action must be trash, got %q", item.Action)
					} else {
						actionErr = svc.DatasetPurge(slug, item.RuleName)
					}
				} else {
					switch item.Action {
					case retention.ActionTrash:
						_, actionErr = svc.NoteDelete(item.Path)
					case retention.ActionFlagReview:
						actionErr = svc.DocStatus(item.Path, "needs_review")
					default:
						actionErr = fmt.Errorf("unsupported retention action %q", item.Action)
					}
				}
				if actionErr != nil {
					failures = appendRetentionFailure(item, fmt.Sprintf("action failed: %v", actionErr), failures)
					continue
				}

				item.Status = retention.ProposalItemStatusActionCompleted
				item.Failure = ""
				p.Status = retentionProposalStatus(p.Items)
				if err := retention.WriteProposal(vRoot, p); err != nil {
					return fmt.Errorf("save action progress for %s: %w", item.Path, err)
				}

				if err := retention.AppendHistory(vRoot, retention.HistoryEntry{
					Timestamp: now.UTC(),
					ActionID:  retention.StableActionID(p.RunID, i),
					RuleName:  item.RuleName,
					Action:    item.Action,
					Path:      item.Path,
					Title:     item.Title,
				}); err != nil {
					item.Failure = fmt.Sprintf("history append failed after action: %v", err)
					failures = append(failures, item.Path+": "+item.Failure)
					continue
				}
				item.Status = retention.ProposalItemStatusAccepted
				item.Failure = ""
				acted++
				p.Status = retentionProposalStatus(p.Items)
				if err := retention.WriteProposal(vRoot, p); err != nil {
					return fmt.Errorf("save retention progress for %s: %w", item.Path, err)
				}
			}

			p.Status = retentionProposalStatus(p.Items)
			if err := retention.WriteProposal(vRoot, p); err != nil {
				return fmt.Errorf("save retention proposal: %w", err)
			}
			result := map[string]interface{}{
				"status":   p.Status,
				"run_id":   args[0],
				"acted":    acted,
				"failures": failures,
				"items":    p.Items,
			}
			if err := outputResult(result); err != nil {
				return err
			}
			if len(failures) > 0 {
				return fmt.Errorf("retention acceptance %s: %s", p.Status, strings.Join(failures, "; "))
			}
			return nil
		},
	}
}

func appendRetentionFailure(item *retention.ProposalItem, message string, failures []string) []string {
	item.Status = ""
	item.Failure = message
	return append(failures, item.Path+": "+message)
}

func retentionProposalStatus(items []retention.ProposalItem) string {
	accepted, failed, pending := 0, 0, 0
	for _, item := range items {
		switch {
		case item.Status == retention.ProposalItemStatusAccepted:
			accepted++
		case item.Failure != "":
			failed++
		default:
			pending++
		}
	}
	if failed == 0 && pending == 0 {
		return retention.ProposalStatusAccepted
	}
	if failed == 0 {
		return retention.ProposalStatusPending
	}
	if accepted == 0 {
		return retention.ProposalStatusFailed
	}
	return retention.ProposalStatusPartial
}

func datasetRetentionSlug(path string) (string, bool) {
	path = filepath.ToSlash(strings.TrimSpace(path))
	if !strings.HasPrefix(path, "datasets/") || !strings.HasSuffix(path, ".md") {
		return "", false
	}
	slug := strings.TrimSuffix(strings.TrimPrefix(path, "datasets/"), ".md")
	if slug == "" || strings.Contains(slug, "/") {
		return "", false
	}
	return slug, true
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
