package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

func registerCommands(rootCmd *cobra.Command) {
	rootCmd.AddCommand(newMailCmd())
	rootCmd.AddCommand(newRecipeCmd())
	rootCmd.AddCommand(newHistoryCmd())
	rootCmd.AddCommand(newRestoreCmd())
	rootCmd.AddCommand(newTrashCmd())
	rootCmd.AddCommand(newMeetingCmd())
	rootCmd.AddCommand(newRetentionCmd())
	rootCmd.AddCommand(newDoctorCmd())
	rootCmd.AddCommand(newIndexCmd())
	rootCmd.AddCommand(newLsCmd())
	rootCmd.AddCommand(newSearchCmd())
	rootCmd.AddCommand(newPropsCmd())
	rootCmd.AddCommand(newBacklinksCmd())
	rootCmd.AddCommand(newRelationsCmd())
	rootCmd.AddCommand(newAskCmd())
	rootCmd.AddCommand(newTransformCmd())
	rootCmd.AddCommand(newIngestCmd())
	rootCmd.AddCommand(newNoteCmd())
	rootCmd.AddCommand(newGraphCmd())
	rootCmd.AddCommand(newRelatedCmd())
	rootCmd.AddCommand(newViewsCmd())
	rootCmd.AddCommand(newEventsCmd())
	rootCmd.AddCommand(newPaperlessCmd())
	rootCmd.AddCommand(newDocsCmd())
	rootCmd.AddCommand(newDocCmd())
	rootCmd.AddCommand(newSimilarCmd())
	rootCmd.AddCommand(newDemoCmd())
	rootCmd.AddCommand(newConflictCmd())
	rootCmd.AddCommand(newClipCmd())
	rootCmd.AddCommand(newExportCmd())
	rootCmd.AddCommand(newAICmd())
	rootCmd.AddCommand(newAIConfigCmd())
	rootCmd.AddCommand(newConsumeCmd())
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
