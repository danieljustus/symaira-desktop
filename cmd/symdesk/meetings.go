package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/service"
)

func newMeetingCmd() *cobra.Command {
	meetingCmd := &cobra.Command{
		Use:   "meeting",
		Short: "Import and review SymMeet meeting artifacts",
	}

	importCmd := &cobra.Command{
		Use:   "import <meeting_id>",
		Short: "Import a reviewed SymMeet meeting into the vault",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close() //nolint:errcheck // matches the existing CLI command pattern in this package
			svc := service.New(vRoot, db)

			path, err := svc.MeetingImport(args[0])
			if err != nil {
				return err
			}
			return outputResult(map[string]string{"path": path, "status": "imported"})
		},
	}
	meetingCmd.AddCommand(importCmd)

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List imported meeting notes",
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close() //nolint:errcheck // matches the existing CLI command pattern in this package
			svc := service.New(vRoot, db)

			results, err := svc.MeetingList()
			if err != nil {
				return err
			}
			return outputResult(results)
		},
	}
	meetingCmd.AddCommand(listCmd)

	availableCmd := &cobra.Command{
		Use:   "available",
		Short: "List SymMeet meetings that have not yet been imported",
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close() //nolint:errcheck // matches the existing CLI command pattern in this package
			svc := service.New(vRoot, db)

			results, err := svc.AvailableMeetings()
			if err != nil {
				return err
			}
			return outputResult(results)
		},
	}
	meetingCmd.AddCommand(availableCmd)

	showCmd := &cobra.Command{
		Use:   "show <vault-note>",
		Short: "Show one imported meeting note",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close() //nolint:errcheck // matches the existing CLI command pattern in this package
			svc := service.New(vRoot, db)

			doc, err := svc.MeetingShow(args[0])
			if err != nil {
				return err
			}
			return outputResult(doc)
		},
	}
	meetingCmd.AddCommand(showCmd)

	refreshCmd := &cobra.Command{
		Use:   "refresh <vault-note>",
		Short: "Preview (or apply) a re-export of a meeting note's transcript",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			apply, _ := cmd.Flags().GetBool("apply")

			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close() //nolint:errcheck // matches the existing CLI command pattern in this package
			svc := service.New(vRoot, db)

			result, err := svc.MeetingRefresh(args[0], apply)
			if err != nil {
				return err
			}
			if jsonFlag {
				return outputResult(result)
			}
			if !result.Changed {
				fmt.Println("No changes.")
				return nil
			}
			for _, line := range result.DiffLines {
				fmt.Println(line)
			}
			if result.Applied {
				fmt.Println("Applied.")
			} else {
				fmt.Println("Preview only; re-run with --apply to write these changes.")
			}
			return nil
		},
	}
	refreshCmd.Flags().Bool("apply", false, "write the refreshed transcript instead of only previewing it")
	meetingCmd.AddCommand(refreshCmd)

	segmentsCmd := &cobra.Command{
		Use:   "segments <vault-note>",
		Short: "List the time-coded transcript segments of a meeting note's source artifact",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close() //nolint:errcheck // matches the existing CLI command pattern in this package
			svc := service.New(vRoot, db)

			segments, err := svc.MeetingSegments(args[0])
			if err != nil {
				return err
			}
			return outputResult(segments)
		},
	}
	meetingCmd.AddCommand(segmentsCmd)

	speakersCmd := &cobra.Command{
		Use:   "speakers <vault-note>",
		Short: "List the speakers of a meeting note's source artifact with their labels",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close() //nolint:errcheck // matches the existing CLI command pattern in this package
			svc := service.New(vRoot, db)

			speakers, err := svc.MeetingSpeakers(args[0])
			if err != nil {
				return err
			}
			return outputResult(speakers)
		},
	}
	meetingCmd.AddCommand(speakersCmd)
	meetingCmd.AddCommand(newMeetingSpeakerCmd())

	reviewCmd := &cobra.Command{
		Use:   "review <vault-note>",
		Short: "Mark a meeting note as reviewed (snapshots the previous state to history first)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close() //nolint:errcheck // matches the existing CLI command pattern in this package
			svc := service.New(vRoot, db)

			if err := svc.MeetingMarkReviewed(args[0]); err != nil {
				return err
			}
			return outputResult(map[string]string{"path": args[0], "review_state": "reviewed"})
		},
	}
	meetingCmd.AddCommand(reviewCmd)
	meetingCmd.AddCommand(newMeetingParticipantCmd())

	publishCmd := &cobra.Command{
		Use:   "publish <vault-note>",
		Short: "Publish reviewed meeting knowledge to Symaira Memory",
		Long: "Publishes a reviewed proposal for one meeting to Symaira Memory: an\n" +
			"'attended' relation for every confirmed participant plus each --fact,\n" +
			"linked to the meeting's Memory entity. Repeat applies are idempotent:\n" +
			"already-published facts are skipped, relations are naturally idempotent.\n" +
			"Nothing is written without this explicit command.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			facts, _ := cmd.Flags().GetStringArray("fact")

			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close() //nolint:errcheck // matches the existing CLI command pattern in this package
			svc := service.New(vRoot, db)

			proposal := service.MeetingPublishProposal{}
			for _, fact := range facts {
				proposal.Facts = append(proposal.Facts, service.MeetingFact{Value: fact})
			}
			result, err := svc.PublishMeetingProposal(args[0], proposal)
			if err != nil {
				return err
			}
			return outputResult(result)
		},
	}
	publishCmd.Flags().StringArray("fact", nil, "a reviewed fact/decision/action item to publish (repeatable)")
	meetingCmd.AddCommand(publishCmd)

	return meetingCmd
}

// newMeetingParticipantCmd groups the reviewed participant-confirmation
// commands: candidate lookup, confirming an existing Memory entity,
// creating a confirmed new person, and unlinking. Nothing here matches or
// creates identities automatically — every write takes an explicit,
// reviewer-chosen argument.
func newMeetingParticipantCmd() *cobra.Command {
	participantCmd := &cobra.Command{
		Use:   "participant",
		Short: "Resolve and confirm meeting participants against Symaira Memory",
	}

	candidatesCmd := &cobra.Command{
		Use:   "candidates <label>",
		Short: "List deterministic Memory person candidates for a participant label",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close() //nolint:errcheck // matches the existing CLI command pattern in this package
			svc := service.New(vRoot, db)

			candidates, err := svc.ResolveParticipantCandidates(args[0])
			if err != nil {
				return err
			}
			return outputResult(candidates)
		},
	}
	participantCmd.AddCommand(candidatesCmd)

	confirmCmd := &cobra.Command{
		Use:   "confirm <vault-note> <speaker_id> <entity_id>",
		Short: "Link a speaker to a confirmed Memory entity (empty entity_id unlinks)",
		Args:  cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			entityID := ""
			if len(args) == 3 {
				entityID = args[2]
			}

			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close() //nolint:errcheck // matches the existing CLI command pattern in this package
			svc := service.New(vRoot, db)

			if err := svc.ConfirmParticipant(args[0], args[1], entityID); err != nil {
				return err
			}
			status := "confirmed"
			if entityID == "" {
				status = "unlinked"
			}
			return outputResult(map[string]string{"speaker_id": args[1], "entity_id": entityID, "status": status})
		},
	}
	participantCmd.AddCommand(confirmCmd)

	createCmd := &cobra.Command{
		Use:   "create <vault-note> <speaker_id> <name>",
		Short: "Create a confirmed new Memory person and link the speaker to it",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close() //nolint:errcheck // matches the existing CLI command pattern in this package
			svc := service.New(vRoot, db)

			entityID, err := svc.ConfirmParticipantNewPerson(args[0], args[1], args[2])
			if err != nil {
				return err
			}
			return outputResult(map[string]string{"speaker_id": args[1], "entity_id": entityID, "name": args[2], "status": "created"})
		},
	}
	participantCmd.AddCommand(createCmd)

	return participantCmd
}

// newMeetingSpeakerCmd groups the speaker-correction passthroughs. Each
// edits the symmeet artifact's edit layer for the note's source meeting;
// the raw engine output is never mutated, and a transcript refresh
// afterwards picks the corrections up.
func newMeetingSpeakerCmd() *cobra.Command {
	speakerCmd := &cobra.Command{
		Use:   "speaker",
		Short: "Correct speakers of a meeting note's source artifact",
	}

	runSpeakerOp := func(op func(svc *service.Service) error) error {
		vRoot, db, err := initServiceDeps()
		if err != nil {
			return err
		}
		defer db.Close() //nolint:errcheck // matches the existing CLI command pattern in this package
		return op(service.New(vRoot, db))
	}

	labelCmd := &cobra.Command{
		Use:   "label <vault-note> <speaker_id> <label>",
		Short: "Assign a display label to an anonymous speaker",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSpeakerOp(func(svc *service.Service) error {
				if err := svc.MeetingSpeakerLabel(args[0], args[1], args[2]); err != nil {
					return err
				}
				return outputResult(map[string]string{"speaker_id": args[1], "label": args[2], "status": "labeled"})
			})
		},
	}
	speakerCmd.AddCommand(labelCmd)

	mergeCmd := &cobra.Command{
		Use:   "merge <vault-note> <from_speaker_id> <to_speaker_id>",
		Short: "Merge one speaker into another",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSpeakerOp(func(svc *service.Service) error {
				if err := svc.MeetingSpeakerMerge(args[0], args[1], args[2]); err != nil {
					return err
				}
				return outputResult(map[string]string{"from": args[1], "to": args[2], "status": "merged"})
			})
		},
	}
	speakerCmd.AddCommand(mergeCmd)

	splitCmd := &cobra.Command{
		Use:   "split <vault-note> <speaker_id>",
		Short: "Split a segment away from its current speaker",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			segmentID, _ := cmd.Flags().GetString("segment")
			if segmentID == "" {
				return fmt.Errorf("--segment is required")
			}
			return runSpeakerOp(func(svc *service.Service) error {
				if err := svc.MeetingSpeakerSplit(args[0], args[1], segmentID); err != nil {
					return err
				}
				return outputResult(map[string]string{"speaker_id": args[1], "segment_id": segmentID, "status": "split"})
			})
		},
	}
	splitCmd.Flags().String("segment", "", "the segment UUID to split out")
	speakerCmd.AddCommand(splitCmd)

	resetCmd := &cobra.Command{
		Use:   "reset <vault-note>",
		Short: "Reset all speaker edits for the source meeting",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSpeakerOp(func(svc *service.Service) error {
				if err := svc.MeetingSpeakerReset(args[0]); err != nil {
					return err
				}
				return outputResult(map[string]string{"status": "reset"})
			})
		},
	}
	speakerCmd.AddCommand(resetCmd)

	return speakerCmd
}
