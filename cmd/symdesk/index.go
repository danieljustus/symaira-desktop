package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

func newIndexCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "index [path]",
		Short: "Index the full vault or a specific file",
		RunE: func(cmd *cobra.Command, args []string) error {
			flagPath := ""
			if len(args) > 0 {
				flagPath = args[0]
			}
			vRoot, err := vault.ResolveVaultRoot(flagPath, cfg)
			if err != nil {
				return err
			}

			db, err := sidecar.OpenForVault(vRoot)
			if err != nil {
				return err
			}
			defer db.Close()

			target := vRoot
			if len(args) > 0 {
				abs, err := filepath.Abs(args[0])
				if err != nil {
					return err
				}
				target = abs
			}

			info, err := os.Stat(target)
			if err != nil {
				return err
			}

			count := 0
			skipped := 0

			processFile := func(p string) error {
				doc, err := vault.ParseFile(p)
				if err != nil {
					return fmt.Errorf("failed to parse %s: %w", p, err)
				}
				indexed, err := db.IsIndexed(doc.Path, doc.SHA256)
				if err != nil {
					return err
				}
				if indexed {
					skipped++
					return nil
				}
				if err := db.IndexDocument(doc); err != nil {
					return fmt.Errorf("failed to index %s: %w", p, err)
				}
				count++
				return nil
			}

			if info.IsDir() {
				err = vault.Walk(target, processFile)
			} else {
				if filepath.Ext(target) == ".md" {
					err = processFile(target)
				}
			}

			if err != nil {
				return err
			}

			if jsonFlag {
				out := map[string]interface{}{
					"status":  "ok",
					"indexed": count,
					"skipped": skipped,
				}
				b, _ := json.Marshal(out)
				fmt.Println(string(b))
			} else {
				fmt.Printf("Index complete. %d new/updated files, %d skipped.\n", count, skipped)
			}

			return nil
		},
	}
}
