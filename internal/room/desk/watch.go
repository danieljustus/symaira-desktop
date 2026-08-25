package desk

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

type EventStreamItem struct {
	Event string `json:"event"`
	Path  string `json:"path"`
}

type StreamHandler func(item *EventStreamItem) error

func WatchStream(ctx context.Context, r io.Reader, handler StreamHandler) error {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var item EventStreamItem
		if err := json.Unmarshal(line, &item); err != nil {
			continue
		}

		if item.Path == "" {
			continue
		}

		_ = handler(&item)
	}
	return scanner.Err()
}

func WatchDesk(ctx context.Context, vault string, handler StreamHandler) error {
	symdeskBin, err := exec.LookPath("symdesk")
	if err != nil {
		return ErrSymdeskNotFound
	}

	backoff := 100 * time.Millisecond
	maxBackoff := 2 * time.Second

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		cmd := exec.CommandContext(ctx, symdeskBin, "events", vault)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return err
		}

		if err := cmd.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "symdesk events start error: %v, retrying in %v...\n", err, backoff)
			time.Sleep(backoff)
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}

		_ = WatchStream(ctx, stdout, handler)
		_ = cmd.Wait()

		if ctx.Err() != nil {
			return ctx.Err()
		}

		fmt.Fprintf(os.Stderr, "symdesk events exited, retrying in %v...\n", backoff)
		time.Sleep(backoff)
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}
