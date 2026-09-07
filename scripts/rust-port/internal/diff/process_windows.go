//go:build windows

package diff

import (
	"errors"
	"fmt"
	"os/exec"
)

func configureProcessTree(_ *exec.Cmd) {}

func killProcessTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := exec.Command("taskkill", "/T", "/F", "/PID", fmt.Sprint(cmd.Process.Pid)).Run(); err == nil {
		return nil
	} else {
		killErr := cmd.Process.Kill()
		if killErr != nil {
			return errors.Join(fmt.Errorf("taskkill process tree: %w", err), killErr)
		}
		return fmt.Errorf("taskkill process tree: %w", err)
	}
}
