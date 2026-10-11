//go:build unix

package pdfprocess

import (
	"errors"
	"os/exec"
	"syscall"
)

func prepare(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return nil
}

func terminate(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
