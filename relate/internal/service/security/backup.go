package security

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/crypto/argon2"

	"github.com/danieljustus/symaira-relate/internal/errs"
	"github.com/danieljustus/symaira-relate/internal/storage/sqlite"
)

// backupMagic identifies a symrelate backup file and its format version.
var backupMagic = [7]byte{'S', 'Y', 'M', 'R', 'B', '1', 0}

const (
	saltSize   = 16
	keySize    = 32                               // AES-256
	headerSize = len(backupMagic) + saltSize + 12 // 12 = AES-GCM standard nonce size
)

// ErrWrongPassphrase is returned by Restore when the AEAD authentication
// tag does not verify — either the passphrase is wrong or the backup is
// corrupted; the two are indistinguishable by design.
var ErrWrongPassphrase = errors.New("security: wrong passphrase or corrupted backup")

// Backup writes an encrypted, self-contained snapshot of the live
// database to w. It uses SQLite's VACUUM INTO to take a consistent
// point-in-time copy — safe under WAL, unlike copying the database file
// directly — then encrypts that copy with AES-256-GCM using a key derived
// from passphrase via Argon2id.
func (s *Service) Backup(ctx context.Context, passphrase []byte, w io.Writer) error {
	const op = "security.Backup"
	if len(passphrase) == 0 {
		return errs.Invalid(op, "a passphrase is required to create a backup", nil)
	}

	tmp, err := os.CreateTemp("", "symrelate-backup-*.db")
	if err != nil {
		return errs.Internal(op, "failed to create temp snapshot file", err)
	}
	tmpPath := tmp.Name()
	tmp.Close()
	os.Remove(tmpPath) // VACUUM INTO requires the target not to exist yet
	defer os.Remove(tmpPath)

	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", tmpPath); err != nil {
		return errs.Internal(op, "failed to snapshot database", err)
	}

	plaintext, err := os.ReadFile(tmpPath)
	if err != nil {
		return errs.Internal(op, "failed to read database snapshot", err)
	}

	ciphertext, salt, nonce, err := encrypt(passphrase, plaintext)
	if err != nil {
		return errs.Internal(op, "failed to encrypt backup", err)
	}

	for _, chunk := range [][]byte{backupMagic[:], salt, nonce, ciphertext} {
		if _, err := w.Write(chunk); err != nil {
			return errs.Internal(op, "failed to write backup", err)
		}
	}

	return recordAudit(ctx, s.db, "backup_created", "", "", fmt.Sprintf("bytes=%d", len(ciphertext)))
}

// Restore decrypts a backup produced by Backup into targetDBPath. It never
// partially writes: the plaintext is fully decrypted and authenticated in
// memory before anything touches disk, and the final file is produced by
// writing to a temp file and renaming — so a wrong passphrase or a
// corrupted backup leaves targetDBPath untouched. targetDBPath is expected
// to be a clean profile path (no existing database the caller cares
// about); Restore does not merge into an existing database.
//
// Restore is a package-level function, not a Service method: it never
// reads or writes the currently-open database (it opens its own short-
// lived connection to the restored file to record the audit event), so
// callers should not have to open one just to call it.
func Restore(ctx context.Context, passphrase []byte, r io.Reader, targetDBPath string) error {
	const op = "security.Restore"
	if len(passphrase) == 0 {
		return errs.Invalid(op, "a passphrase is required to restore a backup", nil)
	}

	data, err := io.ReadAll(r)
	if err != nil {
		return errs.Internal(op, "failed to read backup", err)
	}
	if len(data) < headerSize {
		return errs.Invalid(op, "backup is truncated or not a symrelate backup", nil)
	}
	if string(data[:len(backupMagic)]) != string(backupMagic[:]) {
		return errs.Invalid(op, "not a symrelate backup file", nil)
	}
	salt := data[len(backupMagic) : len(backupMagic)+saltSize]
	nonce := data[len(backupMagic)+saltSize : headerSize]
	ciphertext := data[headerSize:]

	plaintext, err := decrypt(passphrase, salt, nonce, ciphertext)
	if err != nil {
		return errs.Invalid(op, "wrong passphrase or corrupted backup", ErrWrongPassphrase)
	}

	tmpPath := targetDBPath + ".restoring"
	if err := os.WriteFile(tmpPath, plaintext, 0o600); err != nil {
		return errs.Internal(op, "failed to write restored database", err)
	}
	if err := os.Rename(tmpPath, targetDBPath); err != nil {
		os.Remove(tmpPath)
		return errs.Internal(op, "failed to finalize restored database", err)
	}

	// Record the restore in the *restored* database's own audit trail
	// (not the currently-open one, which may be unrelated to a clean
	// target profile) and bring it up to the current schema.
	restoredDB, err := sqlite.Open(ctx, targetDBPath)
	if err != nil {
		return errs.Internal(op, "restored database failed to open", err)
	}
	defer restoredDB.Close()
	return recordAudit(ctx, restoredDB, "backup_restored", "", "", fmt.Sprintf("bytes=%d", len(plaintext)))
}

func deriveKey(passphrase, salt []byte) []byte {
	return argon2.IDKey(passphrase, salt, 1, 64*1024, 4, keySize)
}

func encrypt(passphrase, plaintext []byte) (ciphertext, salt, nonce []byte, err error) {
	salt = make([]byte, saltSize)
	if _, err = rand.Read(salt); err != nil {
		return nil, nil, nil, err
	}
	gcm, err := newAEAD(passphrase, salt)
	if err != nil {
		return nil, nil, nil, err
	}
	nonce = make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, nil, nil, err
	}
	ciphertext = gcm.Seal(nil, nonce, plaintext, nil)
	return ciphertext, salt, nonce, nil
}

func decrypt(passphrase, salt, nonce, ciphertext []byte) ([]byte, error) {
	gcm, err := newAEAD(passphrase, salt)
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, nonce, ciphertext, nil)
}

func newAEAD(passphrase, salt []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(deriveKey(passphrase, salt))
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
