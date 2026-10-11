// Package pdfprocess bounds the lifetime and captured output of PDF tools.
package pdfprocess

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const CaptureLimit = 16 * 1024 * 1024
const Timeout = 60 * time.Second

type capture struct {
	// Do not embed Buffer: its promoted ReadFrom bypasses Write in io.Copy.
	buffer   bytes.Buffer
	overflow chan struct{}
}

func (c *capture) Write(p []byte) (int, error) {
	n := len(p)
	remaining := CaptureLimit - c.buffer.Len()
	if n > remaining {
		select {
		case c.overflow <- struct{}{}:
		default:
		}
		p = p[:remaining]
	}
	_, _ = c.buffer.Write(p)
	return n, nil // Drain until the owning process group is terminated.
}

// Run owns a process group, not arbitrary processes which escape that group.
// Pipes are drained concurrently into bounded memory, never unbounded disk.
func Run(ctx context.Context, path string, args ...string) ([]byte, error) {
	label := filepath.Base(path)
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", label, err)
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...) //nolint:gosec // Caller supplies a resolved executable and separate argv, never a shell command.
	cmd.WaitDelay = time.Second
	if err := prepare(cmd); err != nil {
		return nil, err
	}
	stdout, outWriter, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer func() { _ = stdout.Close() }()
	defer func() { _ = outWriter.Close() }()
	stderr, errWriter, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer func() { _ = stderr.Close() }()
	defer func() { _ = errWriter.Close() }()
	cmd.Stdout, cmd.Stderr = outWriter, errWriter
	cmd.Cancel = func() error { return terminate(cmd) }
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%s: %w", label, err)
	}
	_ = outWriter.Close()
	_ = errWriter.Close()
	overflow := make(chan struct{}, 2)
	out, diagnostic := &capture{overflow: overflow}, &capture{overflow: overflow}
	readDone := make(chan error, 2)
	go func() { _, e := io.Copy(out, stdout); readDone <- e }()
	go func() { _, e := io.Copy(diagnostic, stderr); readDone <- e }()
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	var waitErr error
	limited := false
	select {
	case waitErr = <-waitDone:
	case <-overflow:
		limited = true
		cancel()
		waitErr = <-waitDone
	}
	// Also clean descendants after a normal leader exit. Never repeat this
	// group signal after returning; a later group could reuse the numeric ID.
	cleanupErr := terminate(cmd)
	for range 2 {
		select {
		case readErr := <-readDone:
			if readErr != nil && cleanupErr == nil {
				cleanupErr = readErr
			}
		case <-time.After(time.Second):
			_ = stdout.Close()
			_ = stderr.Close()
			<-readDone
			cleanupErr = errors.New("PDF process group capture did not close")
		}
	}
	select {
	case <-overflow:
		limited = true
	default:
	}
	if cleanupErr != nil {
		return nil, fmt.Errorf("PDF process cleanup: %w", cleanupErr)
	}
	if limited {
		return nil, errors.New("PDF tool output exceeds 16 MiB")
	}
	if waitErr != nil {
		message := strings.TrimSpace(diagnostic.buffer.String())
		if message == "" {
			message = waitErr.Error()
		}
		return nil, fmt.Errorf("%s: %s", label, message)
	}
	return out.buffer.Bytes(), nil
}
