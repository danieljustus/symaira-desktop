package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/documentformat"
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
				indexed, err := indexOneFile(db, p, indexReembed)
				if err != nil {
					return err
				}
				if indexed {
					count++
				} else {
					skipped++
				}
				return nil
			}

			if info.IsDir() {
				err = vault.WalkAll(target, func(path string, entry fs.DirEntry) error {
					markUnsupportedFile(db, path)
					return nil
				})
				if err == nil {
					err = vault.Walk(target, processFile)
				}
			} else if filepath.Ext(target) == ".md" {
				err = processFile(target)
			} else {
				markUnsupportedFile(db, target)
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
	cmd.AddCommand(newIndexRetryCmd())
	cmd.AddCommand(newIndexMaintenanceCmd())
	return cmd
}

// newIndexStatusCmd reports the hybrid index snapshot and whether the
// embedding backend answers. Without it a degraded retrieval path is
// invisible: queries still return, just worse (issue #515).
var indexStatusDocuments bool
var indexStatusState string

func newIndexStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Report the hybrid search index and embedding backend state",
		RunE: func(cmd *cobra.Command, args []string) error {
			if indexStatusDocuments {
				vRoot, err := vault.ResolveVaultRoot("", cfg)
				if err != nil {
					return err
				}
				db, err := sidecar.OpenForVault(vRoot)
				if err != nil {
					return err
				}
				defer func() { _ = db.Close() }()
				rows, err := db.ListIndexStatuses(sidecar.IndexState(indexStatusState))
				if err != nil {
					return err
				}
				return outputResult(rows)
			}
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
	cmd.Flags().BoolVar(&indexStatusDocuments, "documents", false, "List per-document index lifecycle states")
	cmd.Flags().StringVar(&indexStatusState, "state", "", "Filter document states (queued, indexing, indexed, failed, encrypted, unsupported)")
	return cmd
}

func markUnsupportedFile(db *sidecar.DB, path string) {
	if reason, ok := documentformat.UnsupportedReason(filepath.Ext(path)); ok {
		recordIndexStatus(db, path, sidecar.IndexStateUnsupported, reason)
	}
}

func recordIndexStatus(db *sidecar.DB, path string, state sidecar.IndexState, reason string) {
	if err := db.SetIndexStatus(path, state, reason); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to record index status for %s: %v\n", path, err)
	}
}

func indexOneFile(db *sidecar.DB, path string, force bool) (bool, error) {
	recordIndexStatus(db, path, sidecar.IndexStateIndexing, "")
	doc, err := vault.ParseFile(path)
	if err != nil {
		state := sidecar.IndexStateFailed
		if errors.Is(err, documentformat.ErrDRMProtected) {
			state = sidecar.IndexStateEncrypted
		}
		if documentformat.IsUnsupported(filepath.Ext(path)) {
			state = sidecar.IndexStateUnsupported
		}
		recordIndexStatus(db, path, state, err.Error())
		return false, fmt.Errorf("failed to parse %s: %w", path, err)
	}
	indexed, err := db.IsIndexed(doc.Path, doc.SHA256)
	if err != nil {
		recordIndexStatus(db, path, sidecar.IndexStateFailed, err.Error())
		return false, err
	}
	if indexed && !force {
		recordIndexStatus(db, path, sidecar.IndexStateIndexed, "")
		return false, nil
	}
	if err := db.IndexDocument(doc); err != nil {
		recordIndexStatus(db, path, sidecar.IndexStateFailed, err.Error())
		return false, fmt.Errorf("failed to index %s: %w", path, err)
	}
	if err := retrieval.IndexWithMetadata(doc.Path, doc.Body, retrieval.SearchMetadataFromVault(doc)); err != nil {
		recordIndexStatus(db, path, sidecar.IndexStateFailed, err.Error())
		fmt.Fprintf(os.Stderr, "Warning: failed to update hybrid index for %s: %v\n", doc.Path, err)
		return true, nil
	}
	recordIndexStatus(db, path, sidecar.IndexStateIndexed, "")
	return true, nil
}
