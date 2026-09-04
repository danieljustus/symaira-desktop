//go:build !windows

package vault

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// WithASNLock serializes ASN allocation for a vault across concurrent SymDesk
// processes. The lock lives in a hidden directory and is therefore ignored by
// the vault walker and every contract-compliant indexer.
func WithASNLock(vaultRoot string, fn func() error) error {
	lockDir := filepath.Join(vaultRoot, ".symdesk")
	if err := os.MkdirAll(lockDir, 0700); err != nil {
		return fmt.Errorf("create ASN lock directory: %w", err)
	}

	lockPath := filepath.Join(lockDir, "asn.lock")
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600) //nolint:gosec // lockPath is derived from the explicitly selected vault root
	if err != nil {
		return fmt.Errorf("open ASN lock: %w", err)
	}
	defer func() { _ = file.Close() }()

	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock ASN allocation: %w", err)
	}
	defer func() { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN) }()

	return fn()
}
