//go:build windows

package vault

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// WithASNLock uses exclusive creation on Windows. The platform's advisory-lock
// API is not exposed by the standard library; a short-lived lock file keeps the
// same collision-safe allocation semantics without adding a CGO dependency.
func WithASNLock(vaultRoot string, fn func() error) error {
	lockDir := filepath.Join(vaultRoot, ".symdesk")
	if err := os.MkdirAll(lockDir, 0700); err != nil {
		return fmt.Errorf("create ASN lock directory: %w", err)
	}

	lockPath := filepath.Join(lockDir, "asn.lock")
	deadline := time.Now().Add(30 * time.Second)
	for {
		file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if err == nil {
			file.Close()
			defer os.Remove(lockPath)
			return fn()
		}
		if !os.IsExist(err) {
			return fmt.Errorf("create ASN lock: %w", err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("ASN allocation is already in progress")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
