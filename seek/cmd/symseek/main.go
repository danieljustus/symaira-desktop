package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-corekit/logkit"
	"github.com/danieljustus/symaira-corekit/updatecheck"
	"github.com/danieljustus/symaira-corekit/versionkit"
	"github.com/danieljustus/symaira-seek/internal/bench"
	"github.com/danieljustus/symaira-seek/internal/config"
	"github.com/danieljustus/symaira-seek/internal/db"
	"github.com/danieljustus/symaira-seek/internal/engine"
	"github.com/danieljustus/symaira-seek/internal/mcp"
	"github.com/danieljustus/symaira-seek/internal/server"
	"github.com/danieljustus/symaira-seek/internal/tui"
)

var version = "0.1.0-dev"

var (
	cfgFile        string
	cfg            config.Config
	limitFlag      int
	jsonFlag       bool
	tuiFlag        bool
	plainFlag      bool
	pathFilterFlag string
	watchFlag      bool
	portFlag       int
	noAuthFlag     bool
	urlFlag        string
	stdinFlag      bool
	sourceFlag     string
	verboseFlag    bool
	quietFlag      bool
)

func main() {
	cobra.OnInitialize(initConfig)

	rootCmd := newRootCmd()

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, exitcodes.FormatCLIError(err))
		os.Exit(int(exitcodes.ExitCodeFromError(err)))
	}
}

func newRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:           "symseek",
		Short:         "Symaira-Seek: A local hybrid document retrieval CLI and MCP tool",
		SilenceErrors: true,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			level := slog.LevelInfo
			if verboseFlag {
				level = slog.LevelDebug
			} else if quietFlag {
				level = slog.LevelError
			}
			slog.SetDefault(logkit.New(os.Stderr, level, "text"))
		},
	}
	rootCmd.Version = version

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.config/symseek/config.toml)")
	rootCmd.PersistentFlags().BoolVar(&verboseFlag, "verbose", false, "enable debug-level logging")
	rootCmd.PersistentFlags().BoolVar(&quietFlag, "quiet", false, "suppress all output except errors")

	// 1. Search Command
	searchCmd := &cobra.Command{
		Use:   "search [query]",
		Short: "Perform keyword and vector hybrid search over indexed documents",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := args[0]
			dbClient, err := db.Open()
			if err != nil {
				return err
			}
			defer dbClient.Close()

			embedder := engine.NewEmbeddingsGeneratorWithOllamaConfig(cfg.OllamaConfig())
			searchOpts := engine.SearchOptions{ExpandCfg: cfg.ExpandConfig(), PathFilter: pathFilterFlag}

			results, err := engine.SearchHybridWithOptions(dbClient, dbClient, embedder, query, limitFlag, searchOpts)
			if err != nil {
				return err
			}

			// Extract query terms for snippet builder
			queryTerms := strings.Fields(query)

			// JSON output — never launch TUI in JSON mode.
			if jsonFlag {
				structured := make([]*db.StructuredSearchResult, 0, len(results))
				for _, r := range results {
					if s := r.Structured(); s != nil {
						s.Snippet = engine.BuildSnippet(s.Snippet, queryTerms, engine.DefaultSnippetBound)
						structured = append(structured, s)
					}
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(structured)
			}

			useTUI := tuiFlag || (!plainFlag && !jsonFlag && isatty.IsTerminal(os.Stdout.Fd()))
			if useTUI {
				return tui.Run(query, results)
			}

			writeSearchHuman(os.Stdout, results, queryTerms)
			return nil
		},
	}
	searchCmd.Flags().IntVarP(&limitFlag, "limit", "l", 5, "Number of search results to return")
	searchCmd.Flags().BoolVar(&jsonFlag, "json", false, "Output results in JSON format")
	searchCmd.Flags().BoolVar(&tuiFlag, "tui", false, "Launch interactive TUI browser for results")
	searchCmd.Flags().BoolVar(&plainFlag, "plain", false, "Output plain human-readable results instead of launching the TUI")
	searchCmd.Flags().StringVar(&pathFilterFlag, "path", "", "Limit search to documents whose path starts with this prefix")
	rootCmd.AddCommand(searchCmd)

	// 2. Index Command
	indexCmd := &cobra.Command{
		Use:          "index [folder_path]",
		Short:        "Crawl and index a local directory, URL, or stdin",
		Args:         cobra.ArbitraryArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			dbClient, err := db.Open()
			if err != nil {
				return err
			}
			defer dbClient.Close()

			embedder := engine.NewEmbeddingsGeneratorWithOllamaConfig(cfg.OllamaConfig())

			if urlFlag != "" {
				fmt.Fprintf(os.Stderr, "Indexing URL: %s...\n", urlFlag)
				return engine.IndexURL(dbClient, embedder, urlFlag)
			}

			if stdinFlag {
				source := sourceFlag
				if source == "" {
					source = "stdin"
				}
				fmt.Fprintf(os.Stderr, "Indexing from stdin (source: %s)...\n", source)
				return engine.IndexStdin(dbClient, embedder, os.Stdin, source)
			}

			if len(args) == 0 {
				_ = cmd.Help()
				return fmt.Errorf("folder path, --url, or --stdin required")
			}
			dirPath := args[0]

			if watchFlag {
				ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
				defer cancel()

				fmt.Fprintf(os.Stderr, "Starting watch daemon on: %s (fsnotify event-based)\n", dirPath)
				return engine.WatchDirectory(ctx, dbClient, embedder, dirPath)
			}
			fmt.Fprintf(os.Stderr, "Indexing folder: %s...\n", dirPath)
			return engine.IndexDirectory(dbClient, embedder, dirPath)
		},
	}
	indexCmd.Flags().BoolVarP(&watchFlag, "watch", "w", false, "Run in background and monitor folder for changes")
	indexCmd.Flags().StringVar(&urlFlag, "url", "", "Index content from a URL")
	indexCmd.Flags().BoolVar(&stdinFlag, "stdin", false, "Index content from stdin")
	indexCmd.Flags().StringVar(&sourceFlag, "source", "", "Source label for stdin content (used with --stdin)")
	rootCmd.AddCommand(indexCmd)

	// 3. Delete Command
	deleteCmd := &cobra.Command{
		Use:   "delete [document_path]",
		Short: "Remove a document and its chunks from the index",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			docPath := args[0]
			dbClient, err := db.Open()
			if err != nil {
				return err
			}
			defer dbClient.Close()

			existing, err := dbClient.GetDocument(docPath)
			if err != nil {
				return err
			}
			if existing == nil {
				fmt.Fprintf(os.Stderr, "Document not found in index: %s\n", docPath)
				return nil
			}

			if err := dbClient.DeleteDocument(docPath); err != nil {
				return err
			}

			fmt.Fprintf(os.Stderr, "Removed from index: %s\n", docPath)
			return nil
		},
	}
	rootCmd.AddCommand(deleteCmd)

	rootCmd.AddCommand(newExtractCmd())

	// 4. Status Command
	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Display statistics about the local search index",
		RunE: func(cmd *cobra.Command, args []string) error {
			dbClient, err := db.Open()
			if err != nil {
				return err
			}
			defer dbClient.Close()

			stats, err := dbClient.GetStats()
			if err != nil {
				return err
			}

			if jsonFlag {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(stats)
			}

			fmt.Printf("Indexed Documents: %d\n", stats.DocumentCount)
			fmt.Printf("Indexed Chunks:    %d\n", stats.ChunkCount)
			fmt.Printf("Database Size:     %s\n", humanize.Bytes(uint64(stats.DatabaseSize)))
			return nil
		},
	}
	statusCmd.Flags().BoolVar(&jsonFlag, "json", false, "Output stats in JSON format")
	rootCmd.AddCommand(statusCmd)

	// 5. Config Command
	var configSetKey string
	var configSetValue string
	var configJSONFlag bool
	configCmd := &cobra.Command{
		Use:   "config",
		Short: "View and edit settings",
		RunE: func(cmd *cobra.Command, args []string) error {
			if configSetKey != "" {
				if err := config.SetValue(cfgFile, configSetKey, configSetValue, &cfg); err != nil {
					return err
				}
				fmt.Fprintf(os.Stderr, "Set %s = %s in %s\n", configSetKey, configSetValue, cfgFile)
				return nil
			}
			if !configJSONFlag {
				fmt.Fprintf(os.Stderr, "Config file: %s\n", cfgFile)
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(cfg)
		},
	}
	configCmd.Flags().StringVar(&configSetKey, "set-key", "", "Set a config key (e.g. ollama_url, model)")
	configCmd.Flags().StringVar(&configSetValue, "set-value", "", "Value for the config key set via --set-key")
	configCmd.Flags().BoolVar(&configJSONFlag, "json", false, "Output config in JSON format only (no file path)")
	rootCmd.AddCommand(configCmd)

	// 5b. Migrate Command
	migrateCmd := &cobra.Command{
		Use:   "migrate",
		Short: "Migrate configuration from JSON to TOML format",
		RunE: func(cmd *cobra.Command, args []string) error {
			config.MigrateJSONToTOML()
			fmt.Fprintf(os.Stderr, "Migration complete. Config file: %s\n", config.GlobalPath())
			return nil
		},
	}
	rootCmd.AddCommand(migrateCmd)

	// 5c. Quantize Command
	var quantBitsFlag int
	var quantSeedFlag int
	quantizeCmd := &cobra.Command{
		Use:   "quantize",
		Short: "Backfill quantized vector sidecars for approximate search",
		RunE: func(cmd *cobra.Command, args []string) error {
			dbClient, err := db.Open()
			if err != nil {
				return err
			}
			defer dbClient.Close()

			count, err := engine.BackfillQuantSidecars(dbClient, quantBitsFlag, quantSeedFlag, func(processed, total int) {
				fmt.Fprintf(os.Stderr, "\rBackfilling sidecars: %d/%d", processed, total)
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "\nDone: %d chunks backfilled\n", count)
			return nil
		},
	}
	quantizeCmd.Flags().IntVar(&quantBitsFlag, "bits", 4, "Quantization bit width (2, 3, or 4)")
	quantizeCmd.Flags().IntVar(&quantSeedFlag, "seed", 42, "Random rotation seed")
	rootCmd.AddCommand(quantizeCmd)

	// 5d. Bench (Evaluation Harness)
	var benchJSON bool
	var benchCorpus string
	var benchJudgments string
	var benchAllConfigs bool
	var benchLimit int
	benchCmd := &cobra.Command{
		Use:   "bench",
		Short: "Run retrieval-quality evaluation harness (Hit Rate / MRR / NDCG)",
		Long: `Run the pinned-corpus retrieval-quality evaluation.

Indexes the fixture corpus into a temporary database, runs all queries from
the judgment set through SearchHybrid, and reports Hit Rate@k / MRR / NDCG@k
metrics.

By default runs in hybrid mode (BM25 + vector). With --all-configs, runs
vector-only, hybrid, and hybrid+rerank configurations, each bounded by the
targets from docs/research/README.md (§4.3.1):
  - Hit Rate@10  > 95%
  - MRR          > 0.70
  - NDCG@10      > 0.75
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			embedder := engine.NewEmbeddingsGeneratorWithOllamaConfig(cfg.OllamaConfig())

			if !benchAllConfigs {
				// Single run: hybrid mode (default).
				res, err := bench.RunAll(embedder, benchCorpus, benchJudgments, benchLimit,
					engine.RerankConfig{Enabled: false},
					engine.ExpandConfig{Enabled: false})
				if err != nil {
					return err
				}

				if benchJSON {
					enc := json.NewEncoder(os.Stdout)
					enc.SetIndent("", "  ")
					return enc.Encode(res)
				}

				printBenchResults(os.Stdout, "Hybrid", res)
				return nil
			}

			// Run all three configurations.
			configRuns := []struct {
				name      string
				mode      bench.SearchMode
				rerankCfg engine.RerankConfig
				expandCfg engine.ExpandConfig
			}{
				{"Vector Only", bench.SearchVectorOnlyMode, engine.RerankConfig{Enabled: false}, engine.ExpandConfig{Enabled: false}},
				{"Hybrid", bench.SearchHybridMode, engine.RerankConfig{Enabled: false}, engine.ExpandConfig{Enabled: false}},
				{"Hybrid+Rerank", bench.SearchHybridMode, engine.RerankConfig{Enabled: true, URL: cfg.OllamaURL, Model: cfg.RerankModel, Timeout: time.Duration(cfg.RerankTimeoutSeconds) * time.Second}, engine.ExpandConfig{Enabled: false}},
			}

			type configResult struct {
				name    string
				results []*bench.Result
			}

			allResults := make([]configResult, 0, len(configRuns))
			for _, cr := range configRuns {
				// Load judgments and run each query through the harness with the
				// configured mode (vector-only, hybrid, or hybrid+rerank).
				judgments, err := bench.LoadJudgments(benchJudgments)
				if err != nil {
					return fmt.Errorf("loading judgments for %s: %w", cr.name, err)
				}
				var modeResults []*bench.Result
				for _, j := range judgments {
					cfg2 := bench.Config{
						Query:       j.Query,
						RelevantIDs: j.RelevantIDs,
						CorpusDir:   benchCorpus,
						SearchLimit: benchLimit,
						Mode:        cr.mode,
						SearchOpts: engine.SearchOptions{
							RerankCfg: cr.rerankCfg,
							ExpandCfg: cr.expandCfg,
						},
					}
					r2, err2 := bench.Run(embedder, cfg2)
					if err2 != nil {
						return err2
					}
					modeResults = append(modeResults, r2)
				}
				allResults = append(allResults, configResult{name: cr.name, results: modeResults})
			}

			if benchJSON {
				out := make(map[string][]*bench.Result)
				for _, ar := range allResults {
					out[ar.name] = ar.results
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}

			for _, ar := range allResults {
				printBenchResults(os.Stdout, ar.name, ar.results)
			}

			// Print aggregate comparison table.
			fmt.Fprintln(os.Stdout, "\n=== Aggregate Comparison ===")
			fmt.Fprintf(os.Stdout, "%-20s | %10s %10s %10s %10s %10s\n",
				"Config", "HR@1", "HR@3", "HR@10", "MRR", "NDCG@10")
			fmt.Fprintln(os.Stdout, strings.Repeat("-", 85))
			for _, ar := range allResults {
				agg := bench.ComputeAggregate(ar.results)
				fmt.Fprintf(os.Stdout, "%-20s | %10.4f %10.4f %10.4f %10.4f %10.4f\n",
					ar.name, agg.HitRate1, agg.HitRate3, agg.HitRate10, agg.MRR, agg.NDCG10)
			}
			return nil
		},
	}
	benchCmd.Flags().BoolVar(&benchJSON, "json", false, "Emit machine-readable JSON output")
	benchCmd.Flags().StringVar(&benchCorpus, "corpus", "testdata/bench/corpus", "Path to corpus directory")
	benchCmd.Flags().StringVar(&benchJudgments, "judgments", "testdata/bench/judgments.yaml", "Path to judgments YAML file")
	benchCmd.Flags().BoolVar(&benchAllConfigs, "all-configs", false, "Run vector-only, hybrid, and hybrid+rerank configurations")
	benchCmd.Flags().IntVar(&benchLimit, "limit", 10, "Search result limit")
	rootCmd.AddCommand(benchCmd)

	// 6. Version Command
	var checkUpdate bool
	var versionJSON bool
	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Print the version number of Symaira-Seek",
		RunE: func(cmd *cobra.Command, args []string) error {
			info := versionkit.New("symseek", version, 1)
			if versionJSON {
				return info.Write(os.Stdout)
			}
			fmt.Println(info.String())
			if checkUpdate {
				ctx := context.Background()
				checker := updatecheck.NewChecker("danieljustus", "symaira-seek")
				release, err := checker.Check(ctx, version)
				if err != nil {
					return fmt.Errorf("update check failed: %w", err)
				}
				if release != nil {
					fmt.Printf("New version available: %s\n", release.TagName)
					fmt.Printf("Download: %s\n", release.HTMLURL)
				} else {
					fmt.Println("You are running the latest version.")
				}
			}
			return nil
		},
	}
	versionCmd.Flags().BoolVar(&checkUpdate, "check", false, "Check for updates on GitHub")
	versionCmd.Flags().BoolVar(&versionJSON, "json", false, "Emit version as machine-readable JSON")
	rootCmd.AddCommand(versionCmd)

	// 7. Serve Command
	serveCmd := &cobra.Command{
		Use:   "serve",
		Short: "Launch the MCP server or HTTP REST daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			if portFlag > 0 {
				fmt.Fprintf(os.Stderr, "HTTP REST Server implementation starting on port %d...\n", portFlag)
				authToken, err := resolveAPIToken(noAuthFlag)
				if err != nil {
					return err
				}
				return startHTTPServer(portFlag, authToken)
			}
			fmt.Fprintln(os.Stderr, "MCP server starting over stdio...")
			return startMCPServer()
		},
	}
	serveCmd.Flags().IntVarP(&portFlag, "port", "p", 0, "Launch HTTP REST server on this port instead of stdio MCP")
	serveCmd.Flags().BoolVar(&noAuthFlag, "no-auth", false, "Run the HTTP daemon without authentication (token file is neither read nor created; all endpoints except /health stay open)")
	rootCmd.AddCommand(serveCmd)

	return rootCmd
}

func initConfig() {
	if cfgFile == "" {
		cfgFile = config.GlobalPath()
	}

	loaded, err := config.LoadFromPath(cfgFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "symseek: could not load config: %v; using built-in defaults\n", err)
		cfg = *config.DefaultConfig()
		return
	}
	cfg = *loaded
}

// resolveAPIToken determines the HTTP daemon's auth token: SEEK_API_TOKEN
// wins when set; otherwise the token file at the XDG config path is loaded,
// creating it on first start. With --no-auth the daemon runs unauthenticated.
// All diagnostics are written to stderr only (the repo contract forbids
// stdout pollution while the daemon runs).
func resolveAPIToken(noAuth bool) (string, error) {
	if noAuth {
		return "", nil
	}
	if token := os.Getenv("SEEK_API_TOKEN"); token != "" {
		return token, nil
	}
	tokenPath := config.APITokenPath()
	token, created, err := config.LoadOrCreateAPIToken(tokenPath)
	if err != nil {
		return "", err
	}
	if created {
		fmt.Fprintf(os.Stderr, "Generated API token and saved it to %s (permissions 0600)\n", tokenPath)
	}
	return token, nil
}

func startHTTPServer(port int, authToken string) error {
	cooldown := time.Duration(cfg.IndexCooldownSeconds) * time.Second
	if cooldown <= 0 {
		cooldown = 5 * time.Second
	}
	return server.StartHTTPServer(port, authToken, cfg.OllamaConfig(), cooldown, cfg.QuantDBConfig(), cfg.RerankConfig(), cfg.ExpandConfig())
}

func startMCPServer() error {
	mcp.ServerVersion = version
	return mcp.StartServer(cfg.OllamaConfig(), cfg.QuantDBConfig(), cfg.RerankConfig(), cfg.ExpandConfig())
}

func writeSearchHuman(w io.Writer, results []*db.SearchResult, queryTerms []string) {
	if len(results) == 0 {
		fmt.Fprintln(w, "No matching documents found.")
		return
	}
	for idx, r := range results {
		snippet := engine.BuildSnippet(r.Chunk.Content, queryTerms, engine.DefaultSnippetBound)
		fmt.Fprintf(w, "[%d] Path: %s (Chunk Index: %d)\n", idx+1, r.Chunk.DocumentPath, r.Chunk.ChunkIndex)
		fmt.Fprintf(w, "    Score: RRF=%.4f Cosine=%.4f (Ranks: BM25=%d Vector=%d)\n", r.RRFScore, r.CosineScore, r.BM25Rank, r.VectorRank)
		fmt.Fprintln(w, "    --- Snippet ---")
		fmt.Fprintf(w, "    %s\n", strings.ReplaceAll(snippet, "\n", "\n    "))
		fmt.Fprintln(w, "    ----------------")
		fmt.Fprintln(w)
	}
}

func printBenchResults(w io.Writer, configName string, results []*bench.Result) {
	agg := bench.ComputeAggregate(results)

	fmt.Fprintf(w, "\n=== Bench Results: %s ===\n", configName)
	fmt.Fprintf(w, "Queries: %d\n", agg.Count)
	fmt.Fprintf(w, "\n%-40s | %8s %8s %8s %8s %8s %8s\n",
		"Query", "HR@1", "HR@3", "HR@5", "HR@10", "MRR", "NDCG@10")
	fmt.Fprintln(w, strings.Repeat("-", 110))
	for _, r := range results {
		errMarker := ""
		if r.Error != "" {
			errMarker = " [ERR]"
		}
		display := r.Query
		if len(display) > 38 {
			display = display[:35] + "..."
		}
		fmt.Fprintf(w, "%-40s | %8.4f %8.4f %8.4f %8.4f %8.4f %8.4f%s\n",
			display, r.HitRate1, r.HitRate3, r.HitRate5, r.HitRate10, r.MRR, r.NDCG10, errMarker)
	}
	fmt.Fprintln(w, strings.Repeat("-", 110))
	fmt.Fprintf(w, "%-40s | %8.4f %8.4f %8.4f %8.4f %8.4f %8.4f\n",
		"Mean", agg.HitRate1, agg.HitRate3, agg.HitRate5, agg.HitRate10, agg.MRR, agg.NDCG10)
}
