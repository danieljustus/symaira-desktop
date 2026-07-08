package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/service"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

func registerCommands(rootCmd *cobra.Command) {
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

			// 2. Contract (Check VAULT.md if exists, or frontmatter parseable)
			// A basic check: can we initialize the walker and parse one file?
			if vRoot != "" {
				var fileCount int
				err = vault.Walk(vRoot, func(p string) error {
					fileCount++
					if fileCount == 1 {
						_, err := vault.ParseFile(p)
						return err // return error if first file fails to parse frontmatter
					}
					return nil
				})
				if err != nil {
					results["contract"] = map[string]string{"status": "error", "message": err.Error()}
					allOk = false
				} else {
					results["contract"] = map[string]interface{}{"status": "ok", "files_found": fileCount}
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

			if jsonFlag {
				b, _ := json.Marshal(results)
				fmt.Println(string(b))
			} else {
				for k, v := range results {
					if k != "overall" {
						fmt.Printf("%s: %v\n", k, v)
					}
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

			results, err := svc.Search(args[0])
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

	ingestCmd := &cobra.Command{
		Use:   "ingest [file]",
		Short: "Ingest a file into the vault",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
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

	noteNewCmd := &cobra.Command{
		Use:   "new",
		Short: "Create a new note",
		RunE: func(cmd *cobra.Command, args []string) error {
			title, _ := cmd.Flags().GetString("title")
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

			path, err := svc.NoteNew(title, content)
			if err != nil {
				return err
			}
			return outputResult(map[string]string{"status": "created", "path": path})
		},
	}
	noteNewCmd.Flags().String("title", "", "title of the new note")
	noteNewCmd.MarkFlagRequired("title")
	noteCmd.AddCommand(noteNewCmd)

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
		Short: "Mutate document metadata (status, due date)",
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
