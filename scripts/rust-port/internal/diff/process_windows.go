//go:build windows

package diff

import (
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
	}
	return cmd.Process.Kill()
}
