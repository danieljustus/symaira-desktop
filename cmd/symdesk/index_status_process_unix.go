//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

func prepareIndexStatusWorker(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
}

func killIndexStatusWorker(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	if pid > 0 {
		err := syscall.Kill(-pid, syscall.SIGKILL)
		if err != nil {
			_ = cmd.Process.Kill()
		}
		return
	}
	_ = cmd.Process.Kill()
}

func processIsGone(pid int) bool {
	if pid <= 0 {
		return true
	}
	err := syscall.Kill(pid, 0)
	return err != nil
}
