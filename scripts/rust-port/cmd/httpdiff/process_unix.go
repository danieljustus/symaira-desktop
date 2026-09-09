//go:build !windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func isWindowsProcess() bool { return false }

// Unix cleanup signals only the direct process; descendants are not claimed.
func terminateProcessTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return os.ErrProcessDone
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err == nil {
		return nil
	} else if errors.Is(err, os.ErrProcessDone) {
		return err
	} else {
		if killErr := cmd.Process.Kill(); killErr != nil {
			return killErr
		}
	}
	return nil
}
