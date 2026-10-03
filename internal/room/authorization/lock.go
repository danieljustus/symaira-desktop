// Package authorization serializes membership mutations and approval decisions
// sharing one local room, including independent processes and root aliases.
// Imported/offline journal events still require ordered signed replay.
package authorization

import (
	"errors"
	"fmt"
	"os"
	"sync"
)

var ErrBusy = errors.New("room authorization transaction is busy")

// WithRoom holds a kernel lock across the complete read/authorize/sign/append
// operation. The non-authoritative lock lives in ignored local .symroom state.
// Never remove the file: another process may already be waiting on its inode.
func WithRoom(roomDir string, operation func() error) (err error) {
	release, err := acquire(roomDir, true)
	if err != nil {
		return err
	}
	defer func() {
		if releaseErr := release(); releaseErr != nil {
			err = errors.Join(err, releaseErr)
		}
	}()
	return operation()
}

// TryAcquire attempts a transaction without waiting. A successful caller must
// release it; ErrBusy means another writer currently owns the room transaction.
func TryAcquire(roomDir string) (func() error, error) { return acquire(roomDir, false) }

func acquire(roomDir string, wait bool) (func() error, error) {
	root, err := os.OpenRoot(roomDir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	if err := root.Mkdir(".symroom", 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	info, err := root.Stat(".symroom")
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("room authorization state is not a directory")
	}
	file, err := root.OpenFile(".symroom/authorization.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lock(file, wait); err != nil {
		_ = file.Close()
		return nil, err
	}
	var once sync.Once
	var closeErr error
	return func() error {
		once.Do(func() { closeErr = errors.Join(unlock(file), file.Close()) })
		return closeErr
	}, nil
}
