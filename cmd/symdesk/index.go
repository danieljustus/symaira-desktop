package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/retrieval"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

var indexPrune bool

func newIndexCmd() *cobra.Command {
	cmd := &cobra.Command{
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
				// Indexing a document means both indexes: the sidecar above
				// and the hybrid index. Service.IndexDocument pairs them for
				// single-document writes; this bulk path bypasses the service
				// and used to update only the sidecar, so a full `symdesk
				// index` left hybrid search empty. That was invisible while
				// retrieval was an optional sibling tool and is not once it
				// ships in the binary. Failures stay best-effort.
				retrieval.Index(doc.Path, doc.Body)
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

			// Prune stale entries if --prune was passed
			if indexPrune {
				pruned, err := db.Prune(vRoot)
				if err != nil {
					return fmt.Errorf("prune failed: %w", err)
				}
				if jsonFlag {
					out := map[string]interface{}{
						"status":  "ok",
						"indexed": count,
						"skipped": skipped,
						"pruned":  pruned,
					}
					b, _ := json.Marshal(out)
					fmt.Println(string(b))
				} else {
					fmt.Printf("Prune complete. %d stale entries removed.\n", pruned)
				}
			}

			return nil
		},
	}
	cmd.Flags().BoolVar(&indexPrune, "prune", false, "Remove stale entries for deleted or newly-ignored files")
	return cmd
}
