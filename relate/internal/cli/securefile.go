package cli

import "os"

// createSecureFile opens path for writing with mode 0600 (owner read/write
// only). O_EXCL ensures the call fails if the file already exists — callers
// that intentionally overwrite should use os.Create or os.OpenFile directly.
func createSecureFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
}
