package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-desktop/internal/compose"
	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/secrets"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check system health, vault, and sidecar configuration",
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

			results["overall"] = "ok"
			if !allOk {
				results["overall"] = "error"
			}

			// 4. Sibling-tool composition status.
			tools := map[string]string{}
			versions := map[string]string{}
			for _, name := range []string{"symseek", "symmemory", "symingest", "symfetch", "symvault", "symmeet"} {
				if ok, version := compose.HasTool(name); ok {
					tools[name] = "ok"
					versions[name] = version
				} else {
					tools[name] = "not_found"
					versions[name] = ""
				}
			}
			results["tools"] = tools
			results["versions"] = versions

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
			} else {
				aiMap["ollama_url"] = cfg.OllamaURL
				model := cfg.OllamaModel
				if model == "" {
					model = "llama3.2"
				}
				aiMap["model"] = model
			}
			results["ai"] = aiMap

			if jsonFlag {
				b, _ := json.Marshal(results)
				fmt.Println(string(b))
			} else {
				for k, v := range results {
					if k != "overall" && k != "tools" && k != "versions" && k != "conflicts" && k != "asn" {
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
				for _, name := range []string{"symseek", "symmemory", "symingest", "symfetch", "symvault", "symmeet"} {
					status := tools[name]
					version := versions[name]
					if status == "ok" {
						fmt.Printf("  %s: ok (version %s)\n", name, version)
					} else {
						fmt.Printf("  %s: not found\n", name)
					}
				}

				fmt.Printf("ai: provider=%s", aiMap["provider"])
				if src, ok := aiMap["secret_source"]; ok {
					fmt.Printf(", secret_source=%s", src)
				}
				if model, ok := aiMap["model"]; ok {
					fmt.Printf(", model=%s", model)
				}
				if url, ok := aiMap["ollama_url"]; ok && url != "" {
					fmt.Printf(", ollama_url=%s", url)
				}
				fmt.Println()

				fmt.Printf("Overall status: %s\n", results["overall"])
			}

			if !allOk {
				return exitcodes.Wrap(nil, exitcodes.ExitGeneric, exitcodes.KindInternal, "doctor: one or more health checks failed")
			}
			return nil
		},
	}
}

// checkArchiveInVault tries to discover the symingest archive path and reports
// whether it is contained inside the vault directory. It prefers the
// symingest JSON doctor output, falls back to the SYMINGEST_ARCHIVE_PATH env
// variable, and finally uses the XDG default.
func checkArchiveInVault(vaultPath string) (string, bool, error) {
	archivePath := ""

	// 1. Try symingest doctor JSON.
	if ok, _ := compose.HasTool("symingest"); ok {
		out, err := exec.Command("symingest", "doctor", "--json").Output()
		if err == nil {
			var report struct {
				Checks []struct {
					Name    string `json:"name"`
					Status  string `json:"status"`
					Message string `json:"message"`
				} `json:"checks"`
			}
			if json.Unmarshal(out, &report) == nil {
				for _, check := range report.Checks {
					if check.Name == "path.archive" && check.Message != "" {
						archivePath = check.Message
						break
					}
				}
			}
		}
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
