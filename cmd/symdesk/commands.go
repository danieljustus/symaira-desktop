package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/ai"
	"github.com/danieljustus/symaira-desktop/internal/compose"
	"github.com/danieljustus/symaira-desktop/internal/demo"
	"github.com/danieljustus/symaira-desktop/internal/secrets"
	"github.com/danieljustus/symaira-desktop/internal/service"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

func registerCommands(rootCmd *cobra.Command) {
	rootCmd.AddCommand(newRecipeCmd())
	rootCmd.AddCommand(newHistoryCmd())
	rootCmd.AddCommand(newRestoreCmd())
	rootCmd.AddCommand(newTrashCmd())
	doctorCmd := &cobra.Command{
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

			// 3. Sidecar
			db, err := sidecar.Open("")
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
			for _, name := range []string{"symseek", "symmemory", "symingest", "symfetch", "symvault"} {
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
				for _, name := range []string{"symseek", "symmemory", "symingest", "symfetch", "symvault"} {
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
					fmt.Printf(", secret_source=%s\n", src)
				} else {
					fmt.Println()
				}

				fmt.Printf("Overall status: %s\n", results["overall"])
			}

			if !allOk {
				os.Exit(1)
			}
			return nil
		},
	}
	rootCmd.AddCommand(doctorCmd)

	indexCmd := &cobra.Command{
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

			db, err := sidecar.Open("")
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
	rootCmd.AddCommand(indexCmd)

	lsCmd := &cobra.Command{
		Use:   "ls",
		Short: "List files in the vault",
		RunE: func(cmd *cobra.Command, args []string) error {
			dirPrefix, _ := cmd.Flags().GetString("dir")
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)

			results, err := svc.Ls(dirPrefix)
			if err != nil {
				return err
			}
			return outputResult(results)
		},
	}
	lsCmd.Flags().String("dir", "", "directory prefix")
	rootCmd.AddCommand(lsCmd)

	searchCmd := &cobra.Command{
		Use:   "search [query]",
		Short: "Search files in the vault",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)

			results, err := svc.SearchWithMeta(args[0])
			if err != nil {
				return err
			}
			return outputResult(results)
		},
	}
	rootCmd.AddCommand(searchCmd)

	propsCmd := &cobra.Command{
		Use:   "props",
		Short: "Manage file properties",
	}
	rootCmd.AddCommand(propsCmd)

	propsGetCmd := &cobra.Command{
		Use:   "get [file]",
		Short: "Get properties for a file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)
			res, err := svc.Props(args[0])
			if err != nil {
				return err
			}
			return outputResult(res)
		},
	}
	propsCmd.AddCommand(propsGetCmd)

	propsEditCmd := &cobra.Command{
		Use:   "edit [file] [key] [value]",
		Short: "Edit a property",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)
			err = svc.PropsEdit(args[0], args[1], args[2])
			if err != nil {
				return err
			}
			return outputResult(map[string]string{"status": "updated"})
		},
	}
	propsCmd.AddCommand(propsEditCmd)

	backlinksCmd := &cobra.Command{
		Use:   "backlinks [file]",
		Short: "Get backlinks for a file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)

			results, err := svc.Backlinks(args[0])
			if err != nil {
				return err
			}
			return outputResult(results)
		},
	}
	rootCmd.AddCommand(backlinksCmd)

	relationsCmd := &cobra.Command{
		Use:   "relations",
		Short: "Inspect typed relations between notes",
	}
	rootCmd.AddCommand(relationsCmd)

	relationsInverseCmd := &cobra.Command{
		Use:   "inverse [file]",
		Short: "List notes that reference a file via frontmatter properties or wikilinks",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)

			results, err := svc.RelationsInverse(args[0])
			if err != nil {
				return err
			}
			return outputResult(results)
		},
	}
	relationsCmd.AddCommand(relationsInverseCmd)

	askCmd := &cobra.Command{
		Use:   "ask [query]",
		Short: "Ask the AI a question about the vault",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)

			out := make(chan interface{})
			go svc.Ask(args[0], out)

			return outputStream(out)
		},
	}
	rootCmd.AddCommand(askCmd)

	transformCmd := &cobra.Command{
		Use:   "transform <intent>",
		Short: "Transform text with AI (summarize|rewrite|continue); reads text from --text or stdin",
		Long: "Transform a piece of text with a local AI action. The intent is one of " +
			"summarize, rewrite or continue. The text is taken from --text, or read from " +
			"stdin when the flag is empty. Operates purely on the given text and never " +
			"touches the vault; degrades honestly when Ollama is not configured.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			text, _ := cmd.Flags().GetString("text")
			if text == "" {
				data, err := io.ReadAll(cmd.InOrStdin())
				if err != nil {
					return err
				}
				text = string(data)
			}

			out := make(chan interface{})
			go func() {
				chunks := make(chan ai.AskChunk)
				go ai.Transform(text, args[0], chunks)
				for c := range chunks {
					out <- c
				}
				close(out)
			}()

			return outputStream(out)
		},
	}
	transformCmd.Flags().String("text", "", "Text to transform (otherwise read from stdin)")
	rootCmd.AddCommand(transformCmd)

	ingestCmd := &cobra.Command{
		Use:   "ingest",
		Short: "Ingest a file or manage ingestion jobs",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)

			res, err := svc.Ingest(args[0])
			if err != nil {
				return err
			}
			return outputResult(res)
		},
	}

	ingestJobsCmd := &cobra.Command{
		Use:   "jobs",
		Short: "List ingestion jobs in the queue",
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)

			res, err := svc.IngestJobs()
			if err != nil {
				return err
			}
			if jsonFlag {
				fmt.Println(res)
				return nil
			}
			var rawJobs []map[string]interface{}
			if err := json.Unmarshal([]byte(res), &rawJobs); err == nil {
				for _, rj := range rawJobs {
					fmt.Printf("Job ID: %v | Status: %v | Source: %v\n", rj["id"], rj["status"], rj["source_path"])
				}
			} else {
				fmt.Println(res)
			}
			return nil
		},
	}

	ingestRetryCmd := &cobra.Command{
		Use:   "retry [job-id]",
		Short: "Retry a failed ingestion job",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)

			err = svc.IngestRetry(args[0])
			if err != nil {
				return err
			}

			res := map[string]interface{}{
				"status": "retried",
				"job_id": args[0],
			}
			return outputResult(res)
		},
	}
	ingestCmd.AddCommand(ingestJobsCmd)
	ingestCmd.AddCommand(ingestRetryCmd)
	rootCmd.AddCommand(ingestCmd)

	noteCmd := &cobra.Command{
		Use:   "note",
		Short: "Manage notes",
	}
	rootCmd.AddCommand(noteCmd)

	graphCmd := &cobra.Command{
		Use:   "graph",
		Short: "Get graph data",
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)

			results, err := svc.Graph()
			if err != nil {
				return err
			}
			return outputResult(results)
		},
	}
	rootCmd.AddCommand(graphCmd)

	relatedCmd := &cobra.Command{
		Use:   "related [file]",
		Short: "Get related entities and notes for a file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)

			results, err := svc.Related(args[0])
			if err != nil {
				return err
			}
			return outputResult(results)
		},
	}
	rootCmd.AddCommand(relatedCmd)

	noteNewCmd := &cobra.Command{
		Use:   "new",
		Short: "Create a new note",
		RunE: func(cmd *cobra.Command, args []string) error {
			title, _ := cmd.Flags().GetString("title")
			templateName, _ := cmd.Flags().GetString("template")
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)

			// Simple content logic
			content := ""
			if len(args) > 0 {
				content = args[0]
			}

			path, err := svc.NoteNew(title, content, templateName)
			if err != nil {
				return err
			}
			return outputResult(map[string]string{"status": "created", "path": path})
		},
	}
	noteNewCmd.Flags().String("title", "", "title of the new note")
	noteNewCmd.Flags().String("template", "", "template name to use (optional)")
	noteNewCmd.MarkFlagRequired("title")
	noteCmd.AddCommand(noteNewCmd)

	noteDailyCmd := &cobra.Command{
		Use:   "daily",
		Short: "Create or open today's daily note",
		RunE: func(cmd *cobra.Command, args []string) error {
			dateStr, _ := cmd.Flags().GetString("date")
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)

			path, err := svc.NoteDaily(dateStr)
			if err != nil {
				return err
			}
			return outputResult(map[string]string{"status": "ok", "path": path})
		},
	}
	noteDailyCmd.Flags().String("date", "", "optional date (YYYY-MM-DD)")
	noteCmd.AddCommand(noteDailyCmd)

	noteMoveCmd := &cobra.Command{
		Use:   "move [oldPath] [newPath]",
		Short: "Move/Rename a note",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)

			err = svc.NoteMove(args[0], args[1])
			if err != nil {
				return err
			}
			return outputResult(map[string]string{"status": "moved", "from": args[0], "to": args[1]})
		},
	}
	noteCmd.AddCommand(noteMoveCmd)
	noteCmd.AddCommand(newNoteDeleteCmd())

	viewsCmd := &cobra.Command{
		Use:   "views",
		Short: "Manage saved views",
	}
	rootCmd.AddCommand(viewsCmd)

	viewsListCmd := &cobra.Command{
		Use:   "list",
		Short: "List saved views",
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)
			res, err := svc.ViewsList()
			if err != nil {
				return err
			}
			return outputResult(res)
		},
	}
	viewsCmd.AddCommand(viewsListCmd)

	viewsGetCmd := &cobra.Command{
		Use:   "get [id]",
		Short: "Get a specific view",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)
			res, err := svc.ViewsGet(args[0])
			if err != nil {
				return err
			}
			return outputResult(res)
		},
	}
	viewsCmd.AddCommand(viewsGetCmd)

	viewsSaveCmd := &cobra.Command{
		Use:   "save [json]",
		Short: "Save a view",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)
			err = svc.ViewsSave([]byte(args[0]))
			if err != nil {
				return err
			}
			return outputResult(map[string]string{"status": "saved"})
		},
	}
	viewsCmd.AddCommand(viewsSaveCmd)

	viewsDeleteCmd := &cobra.Command{
		Use:   "delete [id]",
		Short: "Delete a saved view",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)
			if err := svc.ViewsDelete(args[0]); err != nil {
				return err
			}
			return outputResult(map[string]string{"status": "deleted"})
		},
	}
	viewsCmd.AddCommand(viewsDeleteCmd)

	viewsNewEntryCmd := &cobra.Command{
		Use:   "new-entry [id] [title]",
		Short: "Create a pre-filled note from a saved view",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			path, err := service.New(vRoot, db).ViewsNewEntry(args[0], args[1])
			if err != nil {
				return err
			}
			return outputResult(map[string]string{"path": path})
		},
	}
	viewsCmd.AddCommand(viewsNewEntryCmd)

	viewsSiblingsCmd := &cobra.Command{
		Use:   "siblings [id]",
		Short: "List saved views that share a source",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			result, err := service.New(vRoot, db).ViewsSiblings(args[0])
			if err != nil {
				return err
			}
			return outputResult(result)
		},
	}
	viewsCmd.AddCommand(viewsSiblingsCmd)

	viewsExecCmd := &cobra.Command{
		Use:   "exec [id]",
		Short: "Execute a view and get results",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)
			res, err := svc.ViewsExec(args[0])
			if err != nil {
				return err
			}
			return outputResult(res)
		},
	}
	viewsCmd.AddCommand(viewsExecCmd)

	rootCmd.AddCommand(newEventsCmd())

	docsCmd := &cobra.Command{
		Use:   "docs",
		Short: "Manage document metadata (contract v2)",
	}
	rootCmd.AddCommand(docsCmd)

	docsListCmd := &cobra.Command{
		Use:   "list",
		Short: "List indexed documents with filters",
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)

			f := sidecar.DocsFilter{}
			f.Type, _ = cmd.Flags().GetString("type")
			f.Status, _ = cmd.Flags().GetString("status")
			f.Person, _ = cmd.Flags().GetString("person")
			f.Correspondent, _ = cmd.Flags().GetString("correspondent")
			f.Year, _ = cmd.Flags().GetString("year")
			f.DueBefore, _ = cmd.Flags().GetString("due-before")
			if minC, _ := cmd.Flags().GetInt("min-confidence"); minC > 0 {
				f.MinConfidence = &minC
			}
			if maxC, _ := cmd.Flags().GetInt("max-confidence"); maxC > 0 {
				f.MaxConfidence = &maxC
			}
			if cmd.Flags().Changed("asn") {
				asn, _ := cmd.Flags().GetInt("asn")
				if err := vault.ValidateASN(asn); err != nil {
					return fmt.Errorf("invalid --asn: %w", err)
				}
				f.ASN = &asn
			}

			results, err := svc.DocsList(f)
			if err != nil {
				return err
			}
			return outputResult(results)
		},
	}
	docsListCmd.Flags().String("type", "", "filter by document_type")
	docsListCmd.Flags().String("status", "", "filter by status (open|paid|submitted|done|needs_review|waiting_for_reply)")
	docsListCmd.Flags().String("person", "", "filter by person (household member)")
	docsListCmd.Flags().String("correspondent", "", "filter by correspondent")
	docsListCmd.Flags().String("year", "", "filter by document year (e.g. 2026)")
	docsListCmd.Flags().String("due-before", "", "filter by due_date <= date (ISO-8601)")
	docsListCmd.Flags().Int("min-confidence", 0, "minimum confidence (0-100)")
	docsListCmd.Flags().Int("max-confidence", 0, "maximum confidence (0-100)")
	docsListCmd.Flags().Int("asn", 0, "filter by archive serial number")
	docsCmd.AddCommand(docsListCmd)

	docsReviewCmd := &cobra.Command{
		Use:   "review",
		Short: "List documents needing review (low confidence or missing metadata)",
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)

			threshold, _ := cmd.Flags().GetInt("threshold")
			if threshold <= 0 {
				threshold = cfg.ReviewThreshold
			}

			results, err := svc.DocsReview(threshold)
			if err != nil {
				return err
			}
			return outputResult(results)
		},
	}
	docsReviewCmd.Flags().Int("threshold", 0, "confidence threshold (default from config)")
	docsCmd.AddCommand(docsReviewCmd)

	docCmd := &cobra.Command{
		Use:   "doc",
		Short: "Mutate document metadata (status, due date, archive serial number)",
	}
	rootCmd.AddCommand(docCmd)

	docStatusCmd := &cobra.Command{
		Use:   "status [file] [status]",
		Short: "Set document status (open|paid|submitted|done|needs_review|waiting_for_reply)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)
			if err := svc.DocStatus(args[0], args[1]); err != nil {
				return err
			}
			return outputResult(map[string]string{"status": "updated", "file": args[0], "new_status": args[1]})
		},
	}
	docCmd.AddCommand(docStatusCmd)

	docDueCmd := &cobra.Command{
		Use:   "due [file] [date]",
		Short: "Set document due date (ISO-8601)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)
			if err := svc.DocDue(args[0], args[1]); err != nil {
				return err
			}
			return outputResult(map[string]string{"status": "updated", "file": args[0], "due_date": args[1]})
		},
	}
	docCmd.AddCommand(docDueCmd)

	docASNCmd := &cobra.Command{
		Use:   "asn [file] [next|N]",
		Short: "Assign a unique archive serial number",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			asn, err := service.New(vRoot, db).DocASN(args[0], args[1])
			if err != nil {
				return err
			}
			return outputResult(map[string]interface{}{"status": "updated", "file": args[0], "asn": asn})
		},
	}
	docCmd.AddCommand(docASNCmd)

	similarCmd := &cobra.Command{
		Use:   "similar [file]",
		Short: "Find near-duplicate documents by SimHash similarity",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)

			threshold, _ := cmd.Flags().GetInt("threshold")
			if threshold <= 0 {
				threshold = 50
			}

			results, err := svc.SimilarDocs(args[0], threshold)
			if err != nil {
				return err
			}
			return outputResult(results)
		},
	}
	similarCmd.Flags().Int("threshold", 50, "minimum similarity percentage (0-100)")
	rootCmd.AddCommand(similarCmd)

	demoCmd := &cobra.Command{
		Use:   "demo",
		Short: "Demo vault management",
	}
	rootCmd.AddCommand(demoCmd)

	demoInitCmd := &cobra.Command{
		Use:   "init [dir]",
		Short: "Materialise the built-in demo vault into a directory",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "symdesk-demo"
			if len(args) > 0 {
				dir = args[0]
			}

			size, _ := cmd.Flags().GetString("size")
			if err := demo.InitSize(dir, size); err != nil {
				return err
			}

			absDir, _ := filepath.Abs(dir)
			if jsonFlag {
				out := map[string]string{
					"status": "ok",
					"path":   absDir,
				}
				b, _ := json.Marshal(out)
				fmt.Println(string(b))
			} else {
				fmt.Printf("Demo vault created at %s\n", absDir)
				fmt.Println("Next steps:")
				fmt.Printf("  SYMDESK_VAULT=%s symdesk index\n", absDir)
				fmt.Printf("  SYMDESK_VAULT=%s symdesk docs list\n", absDir)
			}
			return nil
		},
	}
	demoInitCmd.Flags().String("size", "small", "demo size: small or large")
	demoCmd.AddCommand(demoInitCmd)

	conflictCmd := &cobra.Command{
		Use:   "conflict",
		Short: "Manage iCloud/Obsidian sync conflicts",
	}

	conflictResolveCmd := &cobra.Command{
		Use:   "resolve [path]",
		Short: "Resolve a sync conflict file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			conflictPath := args[0]
			action, _ := cmd.Flags().GetString("action")
			if action != "keep-mine" && action != "keep-theirs" {
				return fmt.Errorf("invalid action: must be keep-mine or keep-theirs")
			}

			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()

			var absConflict string
			if filepath.IsAbs(conflictPath) {
				absConflict = conflictPath
			} else {
				absConflict = filepath.Join(vRoot, conflictPath)
			}

			if _, err := os.Stat(absConflict); err != nil {
				return fmt.Errorf("conflict file not found: %w", err)
			}

			originalPath := deriveOriginalPath(absConflict)

			if action == "keep-mine" {
				if err := os.Remove(absConflict); err != nil {
					return fmt.Errorf("failed to remove conflict file: %w", err)
				}
			} else if action == "keep-theirs" {
				content, err := os.ReadFile(absConflict)
				if err != nil {
					return fmt.Errorf("failed to read conflict file: %w", err)
				}
				if err := os.WriteFile(originalPath, content, 0644); err != nil {
					return fmt.Errorf("failed to write original file: %w", err)
				}
				if err := os.Remove(absConflict); err != nil {
					return fmt.Errorf("failed to remove conflict file: %w", err)
				}
			}

			svc := service.New(vRoot, db)
			_ = svc.DeleteDocument(absConflict)

			if action == "keep-theirs" {
				if doc, err := vault.ParseFile(originalPath); err == nil {
					_ = svc.IndexDocument(doc)
				}
			}

			res := map[string]interface{}{
				"status":        "resolved",
				"action":        action,
				"original_path": originalPath,
				"conflict_path": absConflict,
			}
			return outputResult(res)
		},
	}
	conflictResolveCmd.Flags().String("action", "", "resolution action (keep-mine|keep-theirs)")
	_ = conflictResolveCmd.MarkFlagRequired("action")
	conflictCmd.AddCommand(conflictResolveCmd)
	rootCmd.AddCommand(conflictCmd)

	clipCmd := &cobra.Command{
		Use:   "clip <url>",
		Short: "Fetch a URL via symfetch and save it as a note",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			urlStr := args[0]
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)

			path, err := svc.NoteClip(urlStr)
			if err != nil {
				return err
			}
			return outputResult(map[string]string{"status": "ok", "path": path})
		},
	}
	rootCmd.AddCommand(clipCmd)
}

func initServiceDeps() (string, *sidecar.DB, error) {
	vRoot, err := vault.ResolveVaultRoot("", cfg)
	if err != nil {
		return "", nil, err
	}
	db, err := sidecar.Open("")
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

func deriveOriginalPath(conflictPath string) string {
	dir := filepath.Dir(conflictPath)
	base := filepath.Base(conflictPath)
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)

	if strings.HasSuffix(name, " conflicted copy") {
		name = strings.TrimSuffix(name, " conflicted copy")
	} else if strings.HasSuffix(name, " 2") {
		name = strings.TrimSuffix(name, " 2")
	}

	return filepath.Join(dir, name+ext)
}
