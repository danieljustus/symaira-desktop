//go:build windows

package main

import "os/exec"

func prepareIndexStatusWorker(cmd *exec.Cmd) {
	// No process group needed on Windows fallback.
}

func killIndexStatusWorker(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}

func processIsGone(pid int) bool {
	_ = pid
	return true
}
