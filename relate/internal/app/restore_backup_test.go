package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-relate/internal/domain/contact"
	"github.com/danieljustus/symaira-relate/internal/errs"
	securitysvc "github.com/danieljustus/symaira-relate/internal/service/security"
	"github.com/danieljustus/symaira-relate/internal/xdg"
)

const restoreBackupPassphrase = "correct horse battery staple"

func countPersons(t *testing.T, a *App) int {
	t.Helper()
	res, err := a.Contacts.ListPersons(context.Background(), ListPersonsOptions{})
	if err != nil {
		t.Fatalf("ListPersons() error = %v", err)
	}
	return len(res.Items)
}

func TestRestoreBackup_RoundTripsIntoCleanProfile(t *testing.T) {
	ctx := context.Background()

	src, err := OpenMemory(ctx)
	if err != nil {
		t.Fatalf("OpenMemory() error = %v", err)
	}
	defer src.Close()

	for _, name := range []string{"Ada Lovelace", "Grace Hopper"} {
		if _, err := src.CreatePersonWithContactPoints(ctx, contact.PersonInput{DisplayName: name}, "", ""); err != nil {
			t.Fatalf("CreatePersonWithContactPoints(%q) error = %v", name, err)
		}
	}
	want := countPersons(t, src)
	if want == 0 {
		t.Fatal("seed persons missing")
	}

	var buf bytes.Buffer
	if err := src.Security.Backup(ctx, []byte(restoreBackupPassphrase), &buf); err != nil {
		t.Fatalf("Backup() error = %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("Backup() wrote no data")
	}

	targetPath := filepath.Join(t.TempDir(), "restored.db")
	if err := RestoreBackup(ctx, []byte(restoreBackupPassphrase), bytes.NewReader(buf.Bytes()), targetPath); err != nil {
		t.Fatalf("RestoreBackup() error = %v", err)
	}

	restored, err := OpenAt(ctx, targetPath, xdg.Paths{})
	if err != nil {
		t.Fatalf("OpenAt(restored) error = %v", err)
	}
	defer restored.Close()
	if got := countPersons(t, restored); got != want {
		t.Fatalf("restored person count = %d, want %d", got, want)
	}
}

func TestRestoreBackup_WrongPassphraseReturnsInvalidAndLeavesNoFile(t *testing.T) {
	ctx := context.Background()

	src, err := OpenMemory(ctx)
	if err != nil {
		t.Fatalf("OpenMemory() error = %v", err)
	}
	defer src.Close()

	if _, err := src.CreatePersonWithContactPoints(ctx, contact.PersonInput{DisplayName: "Ada Lovelace"}, "", ""); err != nil {
		t.Fatalf("CreatePersonWithContactPoints() error = %v", err)
	}

	var buf bytes.Buffer
	if err := src.Security.Backup(ctx, []byte(restoreBackupPassphrase), &buf); err != nil {
		t.Fatalf("Backup() error = %v", err)
	}

	targetPath := filepath.Join(t.TempDir(), "restored.db")
	err = RestoreBackup(ctx, []byte("wrong-passphrase"), bytes.NewReader(buf.Bytes()), targetPath)
	if err == nil {
		t.Fatal("RestoreBackup() with wrong passphrase succeeded, want error")
	}
	if !errors.Is(err, securitysvc.ErrWrongPassphrase) {
		t.Errorf("RestoreBackup() error = %v, want it to wrap ErrWrongPassphrase", err)
	}
	if errs.KindOf(err) != errs.KindInvalid {
		t.Errorf("RestoreBackup() kind = %v, want invalid", errs.KindOf(err))
	}
	if _, statErr := os.Stat(targetPath); !os.IsNotExist(statErr) {
		t.Errorf("target file exists after failed restore (partial write): %v", statErr)
	}
}
