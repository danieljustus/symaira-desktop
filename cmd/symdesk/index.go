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
var indexReembed bool

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

			// --re-embed forces re-embedding of every document that still
			// holds pending (unembeddable) chunks, then runs the normal
			// index pass so any newly-available backend fills the gaps
			// (#663/#679).
			if indexReembed {
				n, rerr := retrieval.ReembedPending()
				if rerr != nil {
					return fmt.Errorf("re-embed failed: %w", rerr)
				}
				if !jsonFlag {
					fmt.Printf("Re-embedded %d document(s) with pending chunks.\n", n)
				}
			}

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
				if indexed && !indexReembed {
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
				if err := retrieval.IndexWithMetadata(doc.Path, doc.Body, retrieval.SearchMetadataFromVault(doc)); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to update hybrid index for %s: %v\n", doc.Path, err)
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

			// Prune stale entries if --prune was passed. The result is
			// folded into the single report below: emitting one JSON object
			// per phase would hand a `--prune --json` caller two documents on
			// one stream, which no JSON decoder accepts.
			pruned := 0
			if indexPrune {
				pruned, err = db.Prune(vRoot)
				if err != nil {
					return fmt.Errorf("prune failed: %w", err)
				}
			}

			if jsonFlag {
				out := map[string]interface{}{
					"status":  "ok",
					"indexed": count,
					"skipped": skipped,
				}
				if indexPrune {
					out["pruned"] = pruned
				}
				b, _ := json.Marshal(out)
				fmt.Println(string(b))
			} else {
				fmt.Printf("Index complete. %d new/updated files, %d skipped.\n", count, skipped)
				if indexPrune {
					fmt.Printf("Prune complete. %d stale entries removed.\n", pruned)
				}
			}

			return nil
		},
	}
	cmd.Flags().BoolVar(&indexPrune, "prune", false, "Remove stale entries for deleted or newly-ignored files")
	cmd.Flags().BoolVar(&indexReembed, "re-embed", false, "Re-embed documents that are still pending because the embedding backend was unavailable")
	cmd.AddCommand(newIndexStatusCmd())
	return cmd
}

// newIndexStatusCmd reports the hybrid index snapshot and whether the
// embedding backend answers. Without it a degraded retrieval path is
// invisible: queries still return, just worse (issue #515).
func newIndexStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Report the hybrid search index and embedding backend state",
		RunE: func(cmd *cobra.Command, args []string) error {
			status, err := retrieval.CurrentStatus()
			if err != nil {
				return err
			}
			// Retrieval currently stores one shared database, while the
			// sidecar and vault are selected by --vault. Include the active
			// vault's Markdown count as a comparison figure so consumers can
			// reconcile the global index count without implying scoping.
			if cfg != nil && cfg.Vault != "" {
				vRoot, resolveErr := vault.ResolveVaultRoot("", cfg)
				if resolveErr != nil {
					return resolveErr
				}
				vaultCount := 0
				if walkErr := vault.Walk(vRoot, func(path string) error {
					vaultCount++
					return nil
				}); walkErr != nil {
					return walkErr
				}
				status.VaultDocumentCount = &vaultCount
			}
			return outputResult(status)
		},
	}
}
