//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"time"
)

const processTerminationTimeout = 5 * time.Second

// taskkillCommand is injected by native Windows tests. Keep the real command
// path explicit so cleanup failures are never silently discarded.
var taskkillCommand = exec.CommandContext

func isWindowsProcess() bool { return true }

func terminateProcessTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return os.ErrProcessDone
	}
	ctx, cancel := context.WithTimeout(context.Background(), processTerminationTimeout)
	defer cancel()
	taskkill := taskkillCommand(ctx, "taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	taskkill.WaitDelay = processTerminationTimeout
	taskkillErr := taskkill.Run()
	if taskkillErr == nil {
		return nil
	}
	// Preserve taskkill's failure even when the direct-process fallback wins.
	// The fallback is needed for minimal Windows environments and races.
	killErr := cmd.Process.Kill()
	if killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
		return errors.Join(fmt.Errorf("taskkill process tree: %w", taskkillErr), killErr)
	}
	return fmt.Errorf("taskkill process tree: %w", taskkillErr)
}
