package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

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
			defer closeWithWarning("sidecar database", db.Close)

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

			if info.IsDir() {
				// A directory is the full/initial index pass, so route it
				// through the batched path: indexDirectory commits up to
				// indexBatchSize documents per transaction instead of one
				// commit per file (#760).
				err = vault.WalkAll(target, func(path string, entry fs.DirEntry) error {
					markUnsupportedFile(db, path)
					return nil
				})
				if err == nil {
					count, skipped, err = indexDirectory(db, target, indexReembed)
				}
			} else if filepath.Ext(target) == ".md" {
				// A single explicit file stays on the unbatched, one-file
				// path: batching only pays off for the full/initial index
				// (#760).
				var indexed bool
				indexed, err = indexOneFile(db, target, indexReembed)
				if err == nil {
					if indexed {
						count++
					} else {
						skipped++
					}
				}
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
const defaultIndexStatusTimeout = 10 * time.Second

var (
	indexStatusDocuments bool
	indexStatusState     string
	indexStatusTimeout   time.Duration

	indexStatusRetrievalFunc = defaultIndexStatusRetrieval
	indexStatusVaultWalkFunc = defaultIndexStatusVaultWalk
)

func defaultIndexStatusRetrieval(ctx context.Context) (*retrieval.Status, error) {
	return retrieval.CurrentStatusContext(ctx)
}

func defaultIndexStatusVaultWalk(ctx context.Context, vaultRoot string, fn func(path string) error) error {
	return vault.WalkContext(ctx, vaultRoot, fn)
}

// IndexStatusTimeoutError reports which phase of index status exceeded the deadline (#806).
type IndexStatusTimeoutError struct {
	Phase   string
	Timeout time.Duration
	Err     error
}

func (e *IndexStatusTimeoutError) Error() string {
	return fmt.Sprintf("index status timed out during %s after %v: %v", e.Phase, e.Timeout, e.Err)
}

func (e *IndexStatusTimeoutError) Unwrap() error {
	return e.Err
}

func newIndexStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Report the hybrid search index and embedding backend state",
		Long: `Report the hybrid search index and embedding backend state.

Status retrieval and active vault counting are bounded by an internal deadline
(--timeout, default 10s) so cloud-backed or slow filesystems do not block
callers indefinitely.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			timeout := defaultIndexStatusTimeout
			if cmd.Flags().Changed("timeout") {
				timeout = indexStatusTimeout
			} else if indexStatusTimeout > 0 {
				timeout = indexStatusTimeout
			}

			parentCtx := cmd.Context()
			if parentCtx == nil {
				parentCtx = context.Background()
			}

			var ctx context.Context
			var cancel context.CancelFunc
			if timeout > 0 {
				ctx, cancel = context.WithTimeout(parentCtx, timeout)
				defer cancel()
			} else {
				ctx = parentCtx
			}

			if indexStatusDocuments {
				if err := ctx.Err(); err != nil {
					return &IndexStatusTimeoutError{
						Phase:   "document status listing",
						Timeout: timeout,
						Err:     err,
					}
				}
				vRoot, err := vault.ResolveVaultRoot("", cfg)
				if err != nil {
					return err
				}
				db, err := sidecar.OpenForVault(vRoot)
				if err != nil {
					return err
				}
				defer closeWithWarning("sidecar database", db.Close)
				rows, err := db.ListIndexStatuses(sidecar.IndexState(indexStatusState))
				if err != nil {
					return err
				}
				return outputResult(rows)
			}

			status, err := indexStatusRetrievalFunc(ctx)
			if err != nil {
				if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || ctx.Err() != nil {
					errToReport := err
					if errToReport == nil || (!errors.Is(errToReport, context.DeadlineExceeded) && !errors.Is(errToReport, context.Canceled)) {
						errToReport = ctx.Err()
					}
					return &IndexStatusTimeoutError{
						Phase:   "retrieval status",
						Timeout: timeout,
						Err:     errToReport,
					}
				}
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
				if walkErr := indexStatusVaultWalkFunc(ctx, vRoot, func(path string) error {
					vaultCount++
					return nil
				}); walkErr != nil {
					if errors.Is(walkErr, context.DeadlineExceeded) || errors.Is(walkErr, context.Canceled) || ctx.Err() != nil {
						errToReport := walkErr
						if errToReport == nil || (!errors.Is(errToReport, context.DeadlineExceeded) && !errors.Is(errToReport, context.Canceled)) {
							errToReport = ctx.Err()
						}
						return &IndexStatusTimeoutError{
							Phase:   "vault counting",
							Timeout: timeout,
							Err:     errToReport,
						}
					}
					return walkErr
				}
				status.VaultDocumentCount = &vaultCount
			}
			return outputResult(status)
		},
	}
	cmd.Flags().BoolVar(&indexStatusDocuments, "documents", false, "List per-document index lifecycle states")
	cmd.Flags().StringVar(&indexStatusState, "state", "", "Filter document states (queued, indexing, indexed, failed, encrypted, unsupported)")
	cmd.Flags().DurationVar(&indexStatusTimeout, "timeout", defaultIndexStatusTimeout, "Maximum duration to wait for index status and vault counting (0 to disable)")
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

// indexBatchSize matches the sidecar's per-transaction batch size (#760) so
// a full/initial vault index commits in the same-sized chunks whether it
// runs through RefreshIndex or through this CLI command.
const indexBatchSize = 200

// indexDirectory walks root (the full/initial index case for `symdesk
// index`) and indexes every Markdown file that needs it, committing to the
// sidecar in batches of up to indexBatchSize documents via
// db.IndexDocuments instead of once per file (#760). Per-file bookkeeping
// that indexOneFile does around a single sidecar commit — hybrid retrieval
// indexing and index-status recording — still happens per file, just after
// its batch has committed rather than after its own individual commit.
//
// A file that fails to parse or whose IsIndexed check errors aborts the
// walk, same as the previous one-file-at-a-time path; any batch already
// accumulated at that point is still flushed first so its documents are not
// lost.
func indexDirectory(db *sidecar.DB, root string, force bool) (indexed int, skipped int, err error) {
	batch := make([]*vault.Document, 0, indexBatchSize)

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		// Detach and reset the batch before indexing so a failed flush is
		// never retried with the same documents by the deferred flush that
		// always runs after the walk.
		docs := batch
		batch = make([]*vault.Document, 0, indexBatchSize)

		if ferr := db.IndexDocuments(docs); ferr != nil {
			return fmt.Errorf("failed to index batch: %w", ferr)
		}
		for _, doc := range docs {
			if rerr := retrieval.IndexWithMetadata(doc.Path, doc.Body, retrieval.SearchMetadataFromVault(doc)); rerr != nil {
				recordIndexStatus(db, doc.Path, sidecar.IndexStateFailed, rerr.Error())
				fmt.Fprintf(os.Stderr, "Warning: failed to update hybrid index for %s: %v\n", doc.Path, rerr)
				continue
			}
			recordIndexStatus(db, doc.Path, sidecar.IndexStateIndexed, "")
		}
		indexed += len(docs)
		return nil
	}

	walkErr := vault.Walk(root, func(path string) error {
		recordIndexStatus(db, path, sidecar.IndexStateIndexing, "")
		doc, perr := vault.ParseFile(path)
		if perr != nil {
			state := sidecar.IndexStateFailed
			if errors.Is(perr, documentformat.ErrDRMProtected) {
				state = sidecar.IndexStateEncrypted
			}
			if documentformat.IsUnsupported(filepath.Ext(path)) {
				state = sidecar.IndexStateUnsupported
			}
			recordIndexStatus(db, path, state, perr.Error())
			return fmt.Errorf("failed to parse %s: %w", path, perr)
		}
		alreadyIndexed, ierr := db.IsIndexed(doc.Path, doc.SHA256)
		if ierr != nil {
			recordIndexStatus(db, path, sidecar.IndexStateFailed, ierr.Error())
			return ierr
		}
		if alreadyIndexed && !force {
			recordIndexStatus(db, path, sidecar.IndexStateIndexed, "")
			skipped++
			return nil
		}

		batch = append(batch, doc)
		if len(batch) >= indexBatchSize {
			return flush()
		}
		return nil
	})

	// Flush whatever accumulated even if the walk stopped early on an
	// error, so a mid-vault failure does not discard documents already
	// parsed and queued in this run (#760). A flush already triggered from
	// within the walk callback above leaves the batch empty, so this is a
	// no-op in that case.
	if ferr := flush(); ferr != nil {
		if walkErr == nil {
			return indexed, skipped, ferr
		}
	}
	return indexed, skipped, walkErr
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
