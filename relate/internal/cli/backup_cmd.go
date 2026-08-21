package cli

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/danieljustus/symaira-relate/internal/app"
	"github.com/danieljustus/symaira-relate/internal/domain/security"
)

func init() {
	Register(&Command{
		Name:  "backup",
		Short: "Create or restore an encrypted database backup",
		Long: `Create or restore an AES-256-GCM encrypted backup of the local SQLite database.

The passphrase is resolved in order: --passphrase flag, SYMRELATE_BACKUP_PASSPHRASE
environment variable, SymVault if installed, or an interactive terminal prompt.
Backup create refuses to overwrite an existing file; restore refuses to
overwrite an existing database unless --force is passed.`,
		Examples: `  symrelate backup create --out backup.enc --passphrase "correct-horse-battery-staple"
  symrelate backup restore --in backup.enc --target /tmp/restored.db --passphrase "correct-horse-battery-staple"`,
		Run: runBackup,
	})
}

func runBackup(ctx context.Context, iostreams IO, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: symrelate backup <create|restore> ...")
	}
	verb, rest := args[0], args[1:]

	switch verb {
	case "create":
		return backupCreate(ctx, iostreams, rest)
	case "restore":
		return backupRestore(ctx, iostreams, rest)
	default:
		return fmt.Errorf("symrelate backup: unknown subcommand %q", verb)
	}
}

// resolvePassphrase tries an explicit --passphrase flag, then the
// environment variable, then SymVault if installed, then an interactive
// terminal prompt — see security.DefaultKeyProviders and docs/PRIVACY.md.
func resolvePassphrase(ctx context.Context, explicit string) ([]byte, error) {
	key, _, err := security.DefaultKeyProviders([]byte(explicit)).Resolve(ctx)
	if err != nil {
		return nil, fmt.Errorf("no passphrase available: pass --passphrase, set %s, install SymVault, or enter at the prompt", security.DefaultPassphraseEnvVar)
	}
	return key, nil
}

func resolvePassphraseConfirm(ctx context.Context, explicit string) ([]byte, error) {
	key, _, err := security.DefaultKeyProvidersConfirm([]byte(explicit)).Resolve(ctx)
	if err != nil {
		return nil, fmt.Errorf("no passphrase available: pass --passphrase, set %s, install SymVault, or enter at the prompt", security.DefaultPassphraseEnvVar)
	}
	return key, nil
}

func backupCreate(ctx context.Context, iostreams IO, args []string) error {
	fs := flag.NewFlagSet("backup create", flag.ContinueOnError)
	fs.SetOutput(iostreams.Stderr)
	out := fs.String("out", "", "output backup file path (required)")
	passphrase := fs.String("passphrase", "", "encryption passphrase (falls back to env/SymVault/terminal prompt; WARNING: visible in shell history)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return fmt.Errorf("usage: symrelate backup create --out <file> [--passphrase ...]")
	}

	key, err := resolvePassphraseConfirm(ctx, *passphrase)
	if err != nil {
		return err
	}

	a, err := app.Open(ctx)
	if err != nil {
		return err
	}
	defer a.Close()

	f, err := createSecureFile(*out)
	if err != nil {
		return fmt.Errorf("failed to create backup file: %w", err)
	}
	defer f.Close()

	if err := a.Security.Backup(ctx, key, f); err != nil {
		os.Remove(*out)
		return err
	}
	fmt.Fprintf(iostreams.Stdout, "backup written to %s\n", *out)
	return nil
}

func backupRestore(ctx context.Context, iostreams IO, args []string) error {
	fs := flag.NewFlagSet("backup restore", flag.ContinueOnError)
	fs.SetOutput(iostreams.Stderr)
	in := fs.String("in", "", "backup file to restore (required)")
	target := fs.String("target", "", "path to write the restored database (required; must be a clean profile)")
	passphrase := fs.String("passphrase", "", "encryption passphrase (falls back to env/SymVault/terminal prompt; WARNING: visible in shell history)")
	force := fs.Bool("force", false, "overwrite an existing database at --target")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" || *target == "" {
		return fmt.Errorf("usage: symrelate backup restore --in <file> --target <db-path> [--passphrase ...] [--force]")
	}

	if !*force {
		if _, err := os.Stat(*target); err == nil {
			return fmt.Errorf("refusing to overwrite existing database at %s; pass --force to overwrite", *target)
		}
	}

	key, err := resolvePassphrase(ctx, *passphrase)
	if err != nil {
		return err
	}

	f, err := os.Open(*in)
	if err != nil {
		return fmt.Errorf("failed to open backup file: %w", err)
	}
	defer f.Close()

	if err := app.RestoreBackup(ctx, key, f, *target); err != nil {
		return err
	}
	fmt.Fprintf(iostreams.Stdout, "restored to %s\n", *target)
	return nil
}
