package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-corekit/logkit"
	"github.com/danieljustus/symaira-corekit/updatecheck"
	"github.com/danieljustus/symaira-corekit/versionkit"

	"github.com/danieljustus/symaira-ingest/internal/config"
	"github.com/danieljustus/symaira-ingest/internal/version"
)

var stdout io.Writer = os.Stdout

// checkForUpdateFn is the function used to check for updates.
// It is a package-level variable to allow injection in tests.
var checkForUpdateFn = func(ctx context.Context, currentVersion string) (*updatecheck.Release, error) {
	checker := updatecheck.NewChecker("danieljustus", "symaira-ingest")
	return checker.Check(ctx, currentVersion)
}

func main() {
	logkit.InitDefault("symingest")
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, exitcodes.FormatCLIError(err))
		os.Exit(int(exitcodes.ExitCodeFromError(err)))
	}
}

func run(args []string) (retErr error) {
	if len(args) == 0 {
		return printUsage()
	}

	// Fire-and-forget update check after every successful non-trivial command.
	// Skipped for version, help, and MCP server since these are either
	// self-referential, trivial, or long-lived.
	defer func() {
		if retErr == nil && !isSilentCommand(args[0]) {
			checkForUpdateAsync(version.Version)
		}
	}()

	switch args[0] {
	case "--version", "-v", "version":
		checkFlag := false
		jsonFlag := false
		for _, arg := range args[1:] {
			if arg == "--json" {
				jsonFlag = true
			}
			if arg == "--check" {
				checkFlag = true
			}
		}
		info := versionkit.New("symingest", version.Version, 1)
		if jsonFlag {
			return info.Write(stdout)
		}
		fmt.Fprintln(stdout, info.String())
		if checkFlag {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			release, err := checkForUpdateFn(ctx, version.Version)
			if err != nil {
				fmt.Fprintf(os.Stderr, "update check failed: %v\n", err)
				return nil
			}
			if release != nil {
				fmt.Fprintf(os.Stderr, "\nUpdate available: %s\nDownload: %s\n", release.TagName, release.HTMLURL)
			} else {
				fmt.Fprintln(os.Stderr, "Already up to date.")
			}
		}
		return nil
	case "--help", "-h", "help":
		return printUsage()
	case "ingest":
		return runIngest(args[1:])
	case "reocr":
		return runReocr(args[1:])
	case "split":
		return runPDFSplit(args[1:])
	case "merge":
		return runPDFMerge(args[1:])
	case "rotate":
		return runPDFRotate(args[1:])
	case "watch":
		return runWatch(args[1:])
	case "service":
		return runService(args[1:])
	case "search":
		return runSearch(args[1:])
	case "jobs":
		return runJobs(args[1:])
	case "retry":
		return runRetry(args[1:])
	case "rules":
		return runRules(args[1:])
	case "mail":
		return runMail(args[1:])
	case "import":
		return runImport(args[1:])
	case "doctor":
		return runDoctor(args[1:])
	case "setup":
		return runSetup(args[1:])
	case "validate-vault":
		return runValidateVault(args[1:])
	case "update":
		return runUpdate(args[1:])
	case "bulk-update":
		return runBulkUpdate(args[1:])
	case "apply-corrections":
		return runApplyCorrections(args[1:])
	case "review-report":
		return runReviewReport(args[1:])
	case "report":
		return runReport(args[1:])
	case "cutover-check":
		return runCutoverCheck(args[1:])
	case "extract":
		return runExtract(args[1:])
	case "mcp":
		return runMCP(args[1:])
	default:
		return exitcodes.Wrapf(nil, exitcodes.ExitNoInput, exitcodes.KindValidation,
			"unknown command %q", args[0])
	}
}

func printUsage() error {
	fmt.Fprintln(stdout, `symingest — document ingestion + OCR for the Symaira ecosystem
 
Usage:
  symingest [command]

Commands:
  ingest <file>       Ingest a file into the vault (one-shot)
  reocr <file|id>     Reprocess an archived original for an existing document
  split <file>        Split a PDF after selected pages
  merge <files...>    Merge two or more PDFs into one output
  rotate <file>       Rotate selected PDF pages
  extract <file>      Extract structured data from a file (one-shot preview)
  watch <dir>         Watch a directory for new/modified files and ingest in the background
  service             Manage the macOS LaunchAgent for the watcher
  search              Index the vault with symseek and validate search fixtures
  mail                Read/write IMAP mail-ingest configuration
  import paperless    Import documents from a Paperless-ngx instance
  import notion       Import a Notion Markdown + CSV export into the vault
  doctor              Validate production readiness
  setup               Generate a production config file
  validate-vault      Validate generated Markdown notes and archived originals
  update              Safely update one note by Paperless ID
  bulk-update         Safely update multiple notes selected by frontmatter
  apply-corrections   Apply YAML corrections keyed by paperless_id
  review-report       Generate a human-reviewable migration report
  report              Validate machine-readable migration reports
  cutover-check       Gate whether Paperless can stop being source of truth
  jobs                List ingestion jobs in the queue
  retry <id>          Retry a failed job by ID
  rules               Manage classification rules (list, add, update, test, dry-run, delete)
  mcp                 Start the MCP server
  version             Print version
  help                Show this help`)
	return nil
}

func configureUsage(fs *flag.FlagSet, usage, description string) {
	fs.SetOutput(stdout)
	fs.Usage = func() {
		fmt.Fprintf(stdout, "Usage: symingest %s\n\n%s\n\nFlags:\n", usage, description)
		fs.PrintDefaults()
	}
}

func parseFlags(fs *flag.FlagSet, args []string, message string) (bool, error) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return true, nil
		}
		return false, exitcodes.Wrap(err, exitcodes.ExitData, exitcodes.KindValidation, message)
	}
	return false, nil
}

type resolvedConfig struct {
	vault            string
	archive          string
	db               string
	ocrLang          string
	inbox            string
	paperlessBaseURL string
	symseekEnabled   bool
	symseekBinary    string
	ollamaBaseURL    string
	ollamaModel      string
	raw              config.Config
}

// registerSharedFlags adds the shared CLI flags to fs and returns pointers to
// their values. Call fs.Parse(args) after this, then resolveConfig to merge
// flag values with config/env/defaults.
func registerSharedFlags(fs *flag.FlagSet) (ocrLang, vault, archive, db *string) {
	ocrLang = fs.String("ocr-lang", "", "Tesseract language override")
	vault = fs.String("vault", "", "Target vault directory")
	archive = fs.String("archive", "", "Target archive directory")
	db = fs.String("db", "", "SQLite database path")
	return
}

// resolveConfig merges parsed flag values with config-file / env-var / default
// values. Precedence: explicit CLI flags > env vars / config file > defaults.
// fs.Parse(args) must already have been called so that fs.Visit can tell which
// flags the user actually supplied.
func resolveConfig(fs *flag.FlagSet, ocrLang, vaultFlag, archiveFlag, dbFlag *string) (*resolvedConfig, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, exitcodes.Wrap(err, exitcodes.ExitConfig, exitcodes.KindConfig,
			"failed to load configuration")
	}

	explicitlySet := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) {
		explicitlySet[f.Name] = true
	})

	if !explicitlySet["ocr-lang"] {
		if cfg.OCRLang != "" {
			*ocrLang = cfg.OCRLang
		}
	}
	if *ocrLang == "" {
		*ocrLang = "eng"
	}

	if !explicitlySet["vault"] {
		*vaultFlag = cfg.Vault
	}

	if !explicitlySet["archive"] {
		if cfg.ArchivePath != "" {
			*archiveFlag = cfg.ArchivePath
		} else {
			path, err := defaultArchivePath()
			if err != nil {
				return nil, err
			}
			*archiveFlag = path
		}
	}

	if !explicitlySet["db"] {
		if cfg.DBPath != "" {
			*dbFlag = cfg.DBPath
		} else {
			path, err := defaultDBPath()
			if err != nil {
				return nil, err
			}
			*dbFlag = path
		}
	}

	return &resolvedConfig{
		vault:            *vaultFlag,
		archive:          *archiveFlag,
		db:               *dbFlag,
		ocrLang:          *ocrLang,
		inbox:            cfg.Inbox,
		paperlessBaseURL: cfg.PaperlessBaseURL,
		symseekEnabled:   cfg.SymseekEnabled,
		symseekBinary:    cfg.SymseekBinary,
		ollamaBaseURL:    cfg.OllamaBaseURL,
		ollamaModel:      cfg.OllamaModel,
		raw:              *cfg,
	}, nil
}

func defaultDBPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", exitcodes.Wrapf(err, exitcodes.ExitConfig, exitcodes.KindConfig,
			"cannot determine home directory; use --db to specify a database path explicitly")
	}
	return filepath.Join(home, ".local", "share", "symingest", "symingest.db"), nil
}

func defaultArchivePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", exitcodes.Wrapf(err, exitcodes.ExitConfig, exitcodes.KindConfig,
			"cannot determine home directory; use --archive to specify an archive path explicitly")
	}
	return filepath.Join(home, ".local", "share", "symingest", "archive"), nil
}

// checkForUpdateAsync checks GitHub for a newer version of symingest.
// It prints a notice on stderr if a newer stable release exists.
// It is best-effort: it never blocks the caller and never fails on network
// errors. Results are cached in-memory for 24h by the updatecheck package.
func checkForUpdateAsync(currentVersion string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		checker := updatecheck.NewChecker("danieljustus", "symaira-ingest")
		release, err := checker.Check(ctx, currentVersion)
		if err != nil || release == nil {
			return
		}
		fmt.Fprintf(os.Stderr, "\nUpdate available: %s\nDownload: %s\n", release.TagName, release.HTMLURL)
	}()
}

// isSilentCommand returns true for commands where the post-execution update
// check should be skipped: version (handled by --check explicitly), help
// (trivial), and mcp (long-lived server).
func isSilentCommand(cmd string) bool {
	switch cmd {
	case "--version", "-v", "version", "--help", "-h", "help", "mcp":
		return true
	}
	return false
}
