package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

// registerCommands adds every subcommand to rootCmd, grouped for
// `symdesk --help` (#467) so it reads as scannable sections instead of one
// flat alphabetical list.
func registerCommands(rootCmd *cobra.Command) {
	// Idempotent: newRootCmd already calls this, but registerCommands is
	// also exercised directly by tests against a bare
	// &cobra.Command{Use: "test"}, which needs the groups defined before
	// AddCommand runs below (cobra panics on an unknown GroupID).
	ensureCommandGroups(rootCmd)

	addGrouped(rootCmd, groupVault,
		newIndexCmd(),
		newLsCmd(),
		newSearchCmd(),
		newPropsCmd(),
		newBacklinksCmd(),
		newRelationsCmd(),
		newGraphCmd(),
		newRelatedCmd(),
		newViewsCmd(),
		newTagsCmd(),
		newNoteCmd(),
		newNotebookCmd(),
		newVaultCmd(),
	)

	addGrouped(rootCmd, groupDocuments,
		newDocsCmd(),
		newDocCmd(),
		newDuplicatesCmd(),
		newSimilarCmd(),
		newPaperlessCmd(),
		newIngestCmd(),
		newConsumeCmd(),
		newExportCmd(),
		newClipCmd(),
		newConflictCmd(),
	)

	addGrouped(rootCmd, groupAI,
		newAskCmd(),
		newTransformCmd(),
		newAICmd(),
		newAIConfigCmd(),
	)

	addGrouped(rootCmd, groupMaintenance,
		newMailCmd(),
		newRecipeCmd(),
		newHistoryCmd(),
		newRestoreCmd(),
		newTrashCmd(),
		newMeetingCmd(),
		newRetentionCmd(),
		newDoctorCmd(),
		newEventsCmd(),
		newDemoCmd(),
	)
}

// addGrouped assigns groupID to every command and registers it on parent, so
// group assignment can't drift out of sync with the AddCommand call.
func addGrouped(parent *cobra.Command, groupID string, cmds ...*cobra.Command) {
	for _, c := range cmds {
		c.GroupID = groupID
		parent.AddCommand(c)
	}
}

func initServiceDeps() (string, *sidecar.DB, error) {
	vRoot, err := vault.ResolveVaultRoot("", cfg)
	if err != nil {
		return "", nil, err
	}
	db, err := sidecar.OpenForVault(vRoot)
	if err != nil {
		return "", nil, err
	}
	return vRoot, db, nil
}

func outputResult(data interface{}) error {
	if jsonFlag {
		b, err := json.Marshal(data)
		if err != nil {
			return err
		}
		fmt.Println(string(b))
	} else {
		// Just a simple print for non-JSON
		fmt.Printf("%+v\n", data)
	}
	return nil
}

func outputStream(out <-chan interface{}) error {
	encoder := json.NewEncoder(os.Stdout)
	for chunk := range out {
		if jsonFlag {
			if err := encoder.Encode(chunk); err != nil {
				return err
			}
		} else {
			// Basic print if not json
			fmt.Printf("%v\n", chunk)
		}
	}
	return nil
}
