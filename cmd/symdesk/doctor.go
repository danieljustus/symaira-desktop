package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-desktop/internal/ai"
	"github.com/danieljustus/symaira-desktop/internal/compose"
	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/ingest"
	"github.com/danieljustus/symaira-desktop/internal/retrieval"
	"github.com/danieljustus/symaira-desktop/internal/secrets"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

// siblingTools are the binaries SymDesk still composes at runtime. Search
// (symseek), PDF rendering (symprint), contacts (symrelate), meeting capture
// (symmeet) and document ingest (symingest) are no longer on this list: the
// repo consolidation moved them into this binary, so probing for them would
// report a tool that is not supposed to exist. What remains are the genuinely
// separate products — symmemory and symvault from symbrain's side, symbrowse
// for web clipping.
var siblingTools = []string{"symmemory", "symvault", "symbrowse"}

// ArchivePathFunc resolves where the absorbed ingest pipeline preserves
// originals. It is a variable so a doctor test can pin the answer instead of
// depending on the developer's own symingest configuration.
var ArchivePathFunc = ingest.ArchivePath

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check system health, document vault, and sidecar configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			results := make(map[string]interface{})
			allOk := true

			// 1. Vault
			vRoot, err := vault.ResolveVaultRoot("", cfg)
			if err != nil {
				results["vault"] = map[string]string{"status": "error", "message": err.Error()}
				allOk = false
			} else {
				results["vault"] = map[string]string{"status": "ok", "path": vRoot}
			}

			// 2. Contract and ASN integrity. Scan every file so malformed and
			// duplicate archive serial numbers are actionable in one doctor run.
			var asnReport vault.ASNReport
			hasASNReport := false
			if vRoot != "" {
				asnReport, err = vault.ScanASNs(vRoot)
				hasASNReport = true
				if err != nil {
					results["contract"] = map[string]string{"status": "error", "message": err.Error()}
					allOk = false
				} else {
					contractStatus := "ok"
					if len(asnReport.ParseErrors) > 0 {
						contractStatus = "error"
						allOk = false
					}
					results["contract"] = map[string]interface{}{"status": contractStatus, "files_found": asnReport.FilesScanned}
					results["asn"] = asnReport
					if !asnReport.Healthy() {
						allOk = false
					}
				}
			}

			// 2b. Archive path coverage: warn when the symingest archive is not
			// inside the vault directory, because backup strategies must cover both.
			if vRoot != "" {
				if archivePath, archiveOk, archiveErr := checkArchiveInVault(vRoot); archiveErr != nil {
					results["archive_backup"] = map[string]string{"status": "error", "message": archiveErr.Error()}
					allOk = false
				} else if archiveOk {
					results["archive_backup"] = map[string]string{"status": "ok", "path": archivePath}
				} else {
					results["archive_backup"] = map[string]string{"status": "warn", "path": archivePath, "message": "archive is outside the vault; include it separately in backups"}
				}
			}

			// 3. Sidecar
			db, err := sidecar.OpenForVault(vRoot)
			if err != nil {
				results["sidecar"] = map[string]string{"status": "error", "message": err.Error()}
				allOk = false
			} else {
				defer db.Close()
				if err := db.CheckIntegrity(); err != nil {
					results["sidecar"] = map[string]string{"status": "error", "message": err.Error()}
					allOk = false
				} else {
					results["sidecar"] = map[string]string{"status": "ok"}
				}
			}

			// 3b. Hybrid search index: backend reachability and how many
			// chunks are still pending (unembeddable fallback placeholders).
			// A degraded index is otherwise invisible from the CLI (#663/#680).
			retrievalStatus, retErr := retrieval.CurrentStatus()
			if retErr != nil {
				results["retrieval"] = map[string]string{"status": "error", "message": retErr.Error()}
				allOk = false
			} else {
				pending, pendErr := retrieval.CountPendingChunks()
				if pendErr != nil {
					results["retrieval"] = map[string]string{"status": "error", "message": pendErr.Error()}
					allOk = false
				} else {
					retStatus := "ok"
					if !retrievalStatus.BackendAvailable {
						retStatus = "warn"
					}
					if pending > 0 {
						retStatus = "warn"
					}
					results["retrieval"] = map[string]interface{}{
						"status":            retStatus,
						"backend_available": retrievalStatus.BackendAvailable,
						"embedding_model":   retrievalStatus.EmbeddingModel,
						"pending_chunks":    pending,
						"document_count":    retrievalStatus.DocumentCount,
						"chunk_count":       retrievalStatus.ChunkCount,
					}
				}
			}

			results["overall"] = "ok"
			if !allOk {
				results["overall"] = "error"
			}

			// 4. Sibling-tool composition status. origins records where each
			// tool was actually resolved from ($SYMAIRA_BIN, the managed
			// runtime directory ~/.symaira/bin, or PATH) so an "installed
			// but not found" report is diagnosable (issue #463): a tool can
			// be genuinely present yet invisible to a minimal-PATH caller
			// such as a GUI-launched app.
			tools := map[string]string{}
			versions := map[string]string{}
			origins := map[string]string{}
			for _, name := range siblingTools {
				if ok, version := compose.HasTool(name); ok {
					tools[name] = "ok"
					versions[name] = version
				} else {
					tools[name] = "not_found"
					versions[name] = ""
				}
				if _, origin, err := compose.ResolveWithOrigin(name); err == nil {
					origins[name] = string(origin)
				} else {
					origins[name] = "not_found"
				}
			}
			results["tools"] = tools
			results["versions"] = versions
			results["tool_origins"] = origins

			// The managed runtime directory backs the "symaira_bin_env" and
			// "managed_runtime" origins above. Its presence (or absence) is
			// what should gate a "run `symbrain setup`" recommendation: that
			// command populates this directory, so it is only useful advice
			// when the directory doesn't exist yet — not when it exists but
			// simply lacks a particular tool.
			managedRuntimeDir := compose.ManagedRuntimeDir()
			managedRuntimeExists := compose.ManagedRuntimeDirExists()
			results["managed_runtime"] = map[string]interface{}{
				"dir":    managedRuntimeDir,
				"exists": managedRuntimeExists,
			}

			// 5. iCloud Sync Conflicts
			conflicts := []string{}
			if vRoot != "" {
				_ = vault.Walk(vRoot, func(p string) error {
					base := filepath.Base(p)
					if strings.Contains(base, " 2.md") || strings.Contains(base, "conflicted copy") {
						rel, _ := filepath.Rel(vRoot, p)
						conflicts = append(conflicts, rel)
					}
					return nil
				})
			}
			results["conflicts"] = conflicts

			// 6. AI Provider
			provider := cfg.LLMProvider
			if provider == "" {
				provider = "ollama"
			}
			aiMap := map[string]string{"provider": provider}
			if provider == "anthropic" {
				aiMap["secret_source"] = secrets.Source(cfg.LLMAPIKey)
				model := cfg.LLMModel
				if model == "" {
					model = config.DefaultAnthropicModel
				}
				aiMap["model"] = model
			}
			results["ai"] = aiMap

			// 7. Externalized agent-results area (issue #418). Informational
			// only: the results area lives outside the vault tree so vault
			// retention never sees it — surface its size so unbounded growth
			// stays observable. A size failure does not fail doctor.
			if vRoot != "" {
				bytes, files, err := ai.ResultsSize(ai.ResultsRoot(vRoot))
				if err != nil {
					results["agent_results"] = map[string]string{"status": "error", "message": err.Error()}
				} else {
					results["agent_results"] = map[string]interface{}{"status": "ok", "files": files, "bytes": bytes}
				}
			}

			if jsonFlag {
				b, _ := json.Marshal(results)
				fmt.Println(string(b))
			} else {
				for k, v := range results {
					if k != "overall" && k != "tools" && k != "versions" && k != "tool_origins" && k != "managed_runtime" && k != "conflicts" && k != "asn" {
						fmt.Printf("%s: %v\n", k, v)
					}
				}
				if hasASNReport {
					fmt.Printf("asn: %d assigned, %d malformed, %d duplicate values\n", asnReport.Assigned, len(asnReport.Malformed), len(asnReport.Duplicates))
					for _, malformed := range asnReport.Malformed {
						fmt.Printf("  malformed: %s (%s)\n", malformed.Path, malformed.Message)
					}
					for _, duplicate := range asnReport.Duplicates {
						fmt.Printf("  duplicate ASN %d: %s\n", duplicate.ASN, strings.Join(duplicate.Paths, ", "))
					}
					for _, parseError := range asnReport.ParseErrors {
						fmt.Printf("  parse error: %s (%s)\n", parseError.Path, parseError.Message)
					}
				}
				if len(conflicts) > 0 {
					fmt.Printf("conflicts: %d found\n", len(conflicts))
					for _, c := range conflicts {
						fmt.Printf("  - %s\n", c)
					}
				} else {
					fmt.Println("conflicts: none")
				}
				fmt.Println("tools:")
				missingAny := false
				for _, name := range siblingTools {
					status := tools[name]
					version := versions[name]
					if status == "ok" {
						fmt.Printf("  %s: ok (version %s, from %s)\n", name, version, origins[name])
					} else {
						fmt.Printf("  %s: not found\n", name)
						missingAny = true
					}
				}
				fmt.Printf("managed runtime: %s", managedRuntimeDir)
				if managedRuntimeExists {
					fmt.Println(" (present)")
				} else {
					fmt.Println(" (absent)")
					// Only point at `symbrain setup` when the managed
					// runtime directory doesn't exist yet: if it exists but
					// a tool is still missing, reinstalling the runtime
					// wouldn't be the fix.
					if missingAny {
						fmt.Println("  hint: run `symbrain setup` to install the managed runtime, or install the missing tools via Homebrew.")
					}
				}

				fmt.Printf("ai: provider=%s", aiMap["provider"])
				if src, ok := aiMap["secret_source"]; ok {
					fmt.Printf(", secret_source=%s", src)
				}
				if model, ok := aiMap["model"]; ok {
					fmt.Printf(", model=%s", model)
				}
				fmt.Println()

				fmt.Printf("Overall status: %s\n", results["overall"])
			}

			if !allOk {
				err := exitcodes.Wrap(nil, exitcodes.ExitGeneric, exitcodes.KindInternal, "doctor: one or more health checks failed")
				if jsonFlag {
					// The report above already carries "overall":"error", so
					// the JSON error envelope would only add a second document
					// to stdout and break strict decoders (issue #438). Keep
					// the non-zero exit, drop the envelope.
					return jsonReportedError{err}
				}
				return err
			}
			return nil
		},
	}
}

// checkArchiveInVault resolves the ingest archive path and reports whether it
// is contained inside the vault directory — a layout that would make the vault
// index its own archived originals. The path comes from the absorbed ingest
// pipeline, which resolves it exactly as the CLI does; the env variable and
// XDG fallbacks below remain for the case where that resolution fails.
func checkArchiveInVault(vaultPath string) (string, bool, error) {
	archivePath := ""

	// 1. Ask the absorbed ingest pipeline.
	if path, err := ArchivePathFunc(); err == nil {
		archivePath = path
	}

	// 2. Env override.
	if archivePath == "" {
		if envPath := os.Getenv("SYMINGEST_ARCHIVE_PATH"); envPath != "" {
			archivePath = envPath
		}
	}

	// 3. Default.
	if archivePath == "" {
		dataDir := os.Getenv("XDG_DATA_HOME")
		if dataDir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", false, err
			}
			dataDir = filepath.Join(home, ".local", "share")
		}
		archivePath = filepath.Join(dataDir, "symingest", "archive")
	}

	absArchive, err := filepath.Abs(archivePath)
	if err != nil {
		return archivePath, false, err
	}
	absVault, err := filepath.Abs(vaultPath)
	if err != nil {
		return archivePath, false, err
	}

	rel, err := filepath.Rel(absVault, absArchive)
	if err != nil {
		return archivePath, false, err
	}
	inVault := !strings.HasPrefix(rel, "..") && rel != ".."
	return archivePath, inVault, nil
}
