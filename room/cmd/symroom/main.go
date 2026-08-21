package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-room/internal/config"
	"github.com/danieljustus/symaira-room/internal/identity"
	"github.com/danieljustus/symaira-room/internal/members"
)

const usageText = `symroom - room management and coordination tool

Usage:
  symroom <subcommand> [flags] [args]

Available Subcommands:
  init           Initialize a room
  identity       Manage Ed25519 identities
  member         Manage room members
  note           Post a journal note
  decide         Record a room decision
  artifact       Manage room artifacts
  log            Display room journal log
  verify         Verify journal chains and signatures
  index          Rebuild or manage derived SQLite index
  run            Manage room runs
  checkpoint     Manage run checkpoints
  watch          Watch symdesk events stream
  brain-profile  Emit a symbrain profile
  doctor         Run system and environment checks
  version        Print version information
  mcp            Run MCP server mode

Use "symroom <subcommand> --help" for more information about a subcommand.
`

// roomDir returns the room directory for commands that operate on the room.
// Defaults to the current working directory (the CLI contract: run symroom
// inside the room). Clients that need to target a room without changing their
// own working directory (e.g. the macOS hub module) can set SYMROOM_ROOM_DIR.
func roomDir() string {
	if d := os.Getenv("SYMROOM_ROOM_DIR"); d != "" {
		return d
	}
	return "."
}

// main is a thin dispatcher: each subcommand is implemented in its own
// cmd_*.go file as a runXxx(args, stdout, stderr) int function that returns
// the process exit code. main() only maps the result to os.Exit.
func main() {
	if len(os.Args) < 2 {
		_, _ = fmt.Fprint(os.Stderr, usageText)
		os.Exit(int(exitcodes.ExitNoInput))
	}
	os.Exit(dispatch(context.Background(), os.Args))
}

// dispatch routes the first argument to the per-command implementation and
// returns the exit code the process should terminate with.
func dispatch(ctx context.Context, args []string) int {
	switch args[1] {
	case "version":
		return runVersion(args, os.Stdout, os.Stderr)
	case "identity":
		return runIdentity(args, os.Stdout, os.Stderr)
	case "init":
		return runInit(args, os.Stdout, os.Stderr)
	case "member":
		return runMember(args, os.Stdout, os.Stderr)
	case "note":
		return runNote(args, os.Stdout, os.Stderr)
	case "decide":
		return runDecide(args, os.Stdout, os.Stderr)
	case "verify":
		return runVerify(args, os.Stdout, os.Stderr)
	case "log":
		return runLog(args, os.Stdout, os.Stderr)
	case "index":
		return runIndex(args, os.Stdout, os.Stderr)
	case "artifact":
		return runArtifact(args, os.Stdout, os.Stderr)
	case "watch":
		return runWatch(ctx, args, os.Stdout, os.Stderr)
	case "run":
		return runRun(ctx, args, os.Stdout, os.Stderr)
	case "checkpoint":
		return runCheckpoint(ctx, args, os.Stdout, os.Stderr)
	case "brain-profile":
		return runBrainProfile(args, os.Stdout, os.Stderr)
	case "doctor":
		return runDoctor(args, os.Stdout, os.Stderr)
	case "mcp":
		return runMcp(args, os.Stdout, os.Stderr)
	case "-h", "--help", "help":
		_, _ = fmt.Fprint(os.Stdout, usageText)
		return int(exitcodes.ExitOK)
	default:
		_, _ = fmt.Fprintf(os.Stderr, "Unknown subcommand: %s\n\n%s", args[1], usageText)
		return int(exitcodes.ExitNoInput)
	}
}

// resolveIdentity resolves the identity used to sign journal events,
// falling back to the configured default identity.
func resolveIdentity(idName string) *identity.Identity {
	if idName == "" {
		cfg := config.LoadOrExit()
		idName = cfg.DefaultIdentity
	}
	if idName == "" {
		_, _ = fmt.Fprintln(os.Stderr, "Error: --identity is required when default_identity is not configured")
		os.Exit(int(exitcodes.ExitNoInput))
	}
	id, err := identity.Load(idName)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Error loading identity %s: %v\n", idName, err)
		os.Exit(int(exitcodes.ExitNotFound))
	}
	return id
}

// exitMemberError maps member-management errors to a clear message and exit
// code, then terminates the process.
func exitMemberError(err error) {
	switch {
	case errors.Is(err, members.ErrUnauthorizedOwnerAction):
		_, _ = fmt.Fprintln(os.Stderr, "Error: only room owners can perform member management")
		os.Exit(int(exitcodes.ExitForbidden))
	case errors.Is(err, members.ErrMemberNotFound):
		_, _ = fmt.Fprintln(os.Stderr, "Error: member not found")
		os.Exit(int(exitcodes.ExitNotFound))
	case errors.Is(err, members.ErrInvalidRole):
		_, _ = fmt.Fprintln(os.Stderr, "Error: invalid member role (valid: owner|member|agent|observer)")
		os.Exit(int(exitcodes.ExitNoInput))
	case errors.Is(err, members.ErrInvalidKind):
		_, _ = fmt.Fprintln(os.Stderr, "Error: invalid member kind (valid: human|agent)")
		os.Exit(int(exitcodes.ExitNoInput))
	default:
		_, _ = fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(int(exitcodes.ExitGeneric))
	}
}
