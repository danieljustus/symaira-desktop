//go:build windows

package main

// processAlive always reports false on Windows: service management is
// macOS-LaunchAgents-only, so no watcher PID can exist here.
func processAlive(pid int) bool {
	return false
}
