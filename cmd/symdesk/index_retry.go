package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

// newIndexRetryCmd retries only documents recorded as failed by an index pass.
// It never deletes or rewrites a source document; a missing source remains a
// diagnostic until the user explicitly prunes derived indexes.
func newIndexRetryCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "retry",
		Short: "Retry every failed document index update",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, err := vault.ResolveVaultRoot("", cfg)
			if err != nil {
				return err
			}
			db, err := sidecar.OpenForVault(vRoot)
			if err != nil {
				return err
			}
			defer db.Close()
			failed, err := db.ListIndexStatuses(sidecar.IndexStateFailed)
			if err != nil {
				return err
			}
			result := map[string]interface{}{"status": "ok", "attempted": len(failed), "succeeded": 0, "failed": 0}
			failures := make([]map[string]string, 0)
			for _, item := range failed {
				if _, statErr := os.Stat(item.Path); statErr != nil {
					recordIndexStatus(db, item.Path, sidecar.IndexStateFailed, statErr.Error())
					failures = append(failures, map[string]string{"path": item.Path, "reason": statErr.Error()})
					continue
				}
				indexed, indexErr := indexOneFile(db, item.Path, true)
				if indexErr != nil || !indexed {
					reason := "retry did not update the index"
					if indexErr != nil {
						reason = indexErr.Error()
					}
					failures = append(failures, map[string]string{"path": item.Path, "reason": reason})
					continue
				}
				result["succeeded"] = result["succeeded"].(int) + 1
			}
			result["failed"] = len(failures)
			if len(failures) > 0 {
				result["failures"] = failures
			}
			if jsonFlag {
				data, err := json.Marshal(result)
				if err != nil {
					return err
				}
				fmt.Println(string(data))
				return nil
			}
			fmt.Printf("Retried %d failed document(s): %d succeeded, %d failed.\n", result["attempted"], result["succeeded"], result["failed"])
			return nil
		},
	}
}
