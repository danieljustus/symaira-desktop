package main

import (
	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/service"
)

func newMeetingCmd() *cobra.Command {
	var includeErrors bool
	meetingCmd := &cobra.Command{
		Use:   "meeting",
		Short: "Import and review SymMeet meeting artifacts",
	}
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

			if includeErrors {
				result, err := svc.MeetingListDetailed()
				if err != nil {
					return err
				}
				return outputResult(result)
			}
			results, err := svc.MeetingList()
			if err != nil {
				return err
			}
			return outputResult(results)
		},
	}
	listCmd.Flags().BoolVar(&includeErrors, "include-errors", false, "include per-file decode failures in the response")
	meetingCmd.AddCommand(listCmd)
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

	contactCmd := &cobra.Command{
		Use:   "contact <relate_contact_id>",
		Short: "Resolve a symrelate contact reference for review before linking",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close() //nolint:errcheck // matches the existing CLI command pattern in this package
			svc := service.New(vRoot, db)

			ref, err := svc.ResolveMeetingContactRef(args[0])
			if err != nil {
				return err
			}
			return outputResult(ref)
		},
	}
	participantCmd.AddCommand(contactCmd)

	linkContactCmd := &cobra.Command{
		Use:   "link-contact <vault-note> <speaker_id> <relate_contact_id>",
		Short: "Link a speaker to a reviewed symrelate contact reference",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close() //nolint:errcheck // matches the existing CLI command pattern in this package
			svc := service.New(vRoot, db)

			ref, err := svc.LinkParticipantContact(args[0], args[1], args[2])
			if err != nil {
				return err
			}
			return outputResult(map[string]string{"speaker_id": args[1], "contact_id": ref.ID, "status": "linked"})
		},
	}
	participantCmd.AddCommand(linkContactCmd)

	unlinkContactCmd := &cobra.Command{
		Use:   "unlink-contact <vault-note> <speaker_id>",
		Short: "Remove a speaker's symrelate contact reference",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close() //nolint:errcheck // matches the existing CLI command pattern in this package
			svc := service.New(vRoot, db)

			if err := svc.UnlinkParticipantContact(args[0], args[1]); err != nil {
				return err
			}
			return outputResult(map[string]string{"speaker_id": args[1], "status": "unlinked"})
		},
	}
	participantCmd.AddCommand(unlinkContactCmd)

	return participantCmd
}
