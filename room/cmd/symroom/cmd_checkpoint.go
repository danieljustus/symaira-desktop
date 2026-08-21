package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-room/internal/run"
)

// runCheckpoint implements the "checkpoint" subcommand.
func runCheckpoint(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) < 3 {
		_, _ = fmt.Fprintln(stdout, "Usage: symroom checkpoint <request|resolve> [flags] [args]")
		return int(exitcodes.ExitOK)
	}
	sub := args[2]
	switch sub {
	case "request":
		fs := flag.NewFlagSet("checkpoint request", flag.ExitOnError)
		runFlag := fs.String("run", "", "Run ID")
		qFlag := fs.String("question", "", "Question string")
		timeoutFlag := fs.Duration("timeout", 15*time.Minute, "Wait timeout duration")
		idFlag := fs.String("identity", "", "Author identity name")
		if err := fs.Parse(args[3:]); err != nil {
			return int(exitcodes.ExitNoInput)
		}
		if *runFlag == "" || *qFlag == "" {
			_, _ = fmt.Fprintln(stderr, "Usage: symroom checkpoint request --run <id> --question \"...\" [--identity <name>]")
			return int(exitcodes.ExitNoInput)
		}
		id := resolveIdentity(*idFlag)
		ev, err := run.RequestCheckpoint(roomDir(), *runFlag, *qFlag, id)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "Error requesting checkpoint: %v\n", err)
			return int(exitcodes.ExitGeneric)
		}
		var b struct {
			CheckpointID string `json:"checkpoint_id"`
		}
		_ = json.Unmarshal(ev.Body, &b)

		ctx, cancel := context.WithTimeout(ctx, *timeoutFlag)
		defer cancel()

		chk, err := run.WaitCheckpoint(ctx, roomDir(), b.CheckpointID, 500*time.Millisecond)
		if err != nil {
			if errors.Is(err, run.ErrWaitTimeout) {
				_, _ = fmt.Fprintf(stderr, "Error: wait timed out for checkpoint %s\n", b.CheckpointID)
				return int(exitcodes.ExitInterrupted)
			}
			_, _ = fmt.Fprintf(stderr, "Error waiting for checkpoint: %v\n", err)
			return int(exitcodes.ExitGeneric)
		}
		_, _ = fmt.Fprintln(stdout, chk.Answer)
		return int(exitcodes.ExitOK)

	case "resolve":
		fs := flag.NewFlagSet("checkpoint resolve", flag.ExitOnError)
		answerFlag := fs.String("answer", "", "Answer string")
		idFlag := fs.String("identity", "", "Author identity name")
		if err := fs.Parse(args[3:]); err != nil {
			return int(exitcodes.ExitNoInput)
		}
		if fs.NArg() < 1 || *answerFlag == "" {
			_, _ = fmt.Fprintln(stderr, "Usage: symroom checkpoint resolve <checkpoint_id> --answer \"...\" [--identity <name>]")
			return int(exitcodes.ExitNoInput)
		}
		chkID := fs.Arg(0)
		id := resolveIdentity(*idFlag)
		ev, err := run.ResolveCheckpoint(roomDir(), chkID, *answerFlag, id)
		if err != nil {
			if errors.Is(err, run.ErrAgentCheckpointResolveForbidden) {
				_, _ = fmt.Fprintln(stderr, "Error: agent identity is forbidden from resolving checkpoints")
				return int(exitcodes.ExitNoInput)
			}
			_, _ = fmt.Fprintf(stderr, "Error resolving checkpoint: %v\n", err)
			return int(exitcodes.ExitGeneric)
		}
		_, _ = fmt.Fprintln(stdout, ev.ID)
		return int(exitcodes.ExitOK)

	default:
		_, _ = fmt.Fprintf(stderr, "Unknown checkpoint action: %s\n", sub)
		return int(exitcodes.ExitNoInput)
	}
}
