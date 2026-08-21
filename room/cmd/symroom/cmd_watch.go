package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-room/internal/artifact"
	"github.com/danieljustus/symaira-room/internal/desk"
)

// runWatch implements the "watch" subcommand.
func runWatch(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("watch", flag.ExitOnError)
	deskVaultFlag := fs.String("desk", "", "Symdesk vault name to watch")
	idFlag := fs.String("identity", "", "Author identity name")
	if err := fs.Parse(args[2:]); err != nil {
		return int(exitcodes.ExitNoInput)
	}

	if *deskVaultFlag == "" {
		_, _ = fmt.Fprintln(stdout, "Usage: symroom watch --desk <vault> [--identity <name>]")
		return int(exitcodes.ExitOK)
	}

	id := resolveIdentity(*idFlag)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	handler := func(item *desk.EventStreamItem) error {
		return artifact.HandleDeskEvent(roomDir(), "", item, id)
	}

	_, _ = fmt.Fprintf(stdout, "Watching symdesk vault %s...\n", *deskVaultFlag)
	if err := desk.WatchDesk(ctx, *deskVaultFlag, handler); err != nil && !errors.Is(err, context.Canceled) {
		_, _ = fmt.Fprintf(stderr, "Watch error: %v\n", err)
		return int(exitcodes.ExitGeneric)
	}
	return int(exitcodes.ExitOK)
}
