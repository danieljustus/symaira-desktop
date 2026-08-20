package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-ingest/internal/config"
)

type setupConfig struct {
	Vault            string
	ArchivePath      string
	DBPath           string
	Inbox            string
	OCRLang          string
	PaperlessBaseURL string
}

func runSetup(args []string) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	vault := fs.String("vault", "", "Target vault directory")
	archive := fs.String("archive", "", "Target archive directory")
	db := fs.String("db", "", "SQLite database path")
	inbox := fs.String("inbox", "", "Watch inbox directory")
	ocrLang := fs.String("ocr-lang", "eng", "Default OCR language")
	paperlessBaseURL := fs.String("paperless-base-url", "", "Paperless base URL; tokens are never written")
	configPath := fs.String("config", "", "Config file path override (default: XDG config path)")
	dryRun := fs.Bool("dry-run", false, "Print a diff without writing")
	force := fs.Bool("force", false, "Overwrite an existing different config")
	configureUsage(fs, "setup [flags]", "Generate an idempotent production config file without storing secrets.")
	help, err := parseFlags(fs, args, "invalid setup flags")
	if help || err != nil {
		return err
	}
	if *configPath == "" {
		path, err := defaultConfigPath()
		if err != nil {
			return err
		}
		*configPath = path
	}
	if *archive == "" {
		path, err := defaultArchivePath()
		if err != nil {
			return err
		}
		*archive = path
	}
	if *db == "" {
		path, err := defaultDBPath()
		if err != nil {
			return err
		}
		*db = path
	}
	if strings.TrimSpace(*vault) == "" {
		return exitcodes.Wrapf(nil, exitcodes.ExitData, exitcodes.KindValidation, "--vault is required")
	}
	if strings.TrimSpace(*inbox) == "" {
		return exitcodes.Wrapf(nil, exitcodes.ExitData, exitcodes.KindValidation, "--inbox is required")
	}
	if strings.TrimSpace(*paperlessBaseURL) == "" {
		return exitcodes.Wrapf(nil, exitcodes.ExitData, exitcodes.KindValidation, "--paperless-base-url is required")
	}
	cfg := setupConfig{Vault: *vault, ArchivePath: *archive, DBPath: *db, Inbox: *inbox, OCRLang: *ocrLang, PaperlessBaseURL: *paperlessBaseURL}
	content := renderSetupConfig(cfg)
	current, readErr := os.ReadFile(*configPath)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return exitcodes.Wrap(readErr, exitcodes.ExitConfig, exitcodes.KindConfig, "failed to read existing config")
	}
	if *dryRun {
		fmt.Fprintf(stdout, "Config dry-run: %s\n", *configPath)
		printConfigDiff(stdout, string(current), content)
		return nil
	}
	if readErr == nil && string(current) == content {
		fmt.Fprintf(stdout, "Config already up to date: %s\n", *configPath)
		return nil
	}
	if readErr == nil && !*force {
		fmt.Fprintf(stdout, "Existing config differs: %s\n", *configPath)
		printConfigDiff(stdout, string(current), content)
		return exitcodes.Wrapf(nil, exitcodes.ExitConflict, exitcodes.KindConflict, "config differs; rerun with --force to overwrite")
	}
	if err := os.MkdirAll(filepath.Dir(*configPath), 0o700); err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitConfig, exitcodes.KindConfig, "failed to create config directory")
	}
	if err := os.WriteFile(*configPath, []byte(content), 0o600); err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitConfig, exitcodes.KindConfig, "failed to write config")
	}
	fmt.Fprintf(stdout, "Config written: %s\n", *configPath)
	fmt.Fprintln(stdout, "Paperless token not written; use PAPERLESS_TOKEN or a secret manager.")
	config.Loader.ResetCache()
	return nil
}

func defaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", exitcodes.Wrapf(err, exitcodes.ExitConfig, exitcodes.KindConfig, "cannot determine home directory; use --config explicitly")
	}
	return filepath.Join(home, ".config", "symingest", "config.toml"), nil
}

func renderSetupConfig(cfg setupConfig) string {
	return fmt.Sprintf("vault = %q\narchive_path = %q\ndb_path = %q\ninbox = %q\nocr_lang = %q\npaperless_base_url = %q\n", cfg.Vault, cfg.ArchivePath, cfg.DBPath, cfg.Inbox, cfg.OCRLang, cfg.PaperlessBaseURL)
}

func printConfigDiff(w io.Writer, old, new string) {
	if old == new {
		fmt.Fprintln(w, "No changes.")
		return
	}
	if old != "" {
		fmt.Fprintln(w, "--- current")
		for _, line := range strings.Split(strings.TrimRight(old, "\n"), "\n") {
			fmt.Fprintf(w, "- %s\n", line)
		}
	}
	fmt.Fprintln(w, "+++ proposed")
	for _, line := range strings.Split(strings.TrimRight(new, "\n"), "\n") {
		fmt.Fprintf(w, "+ %s\n", line)
	}
}
