package ingest

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/ingest/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/ingest/internal/store"
)

func TestMailPoller_UnknownSecretSchemeNotPassedToDial(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	account := config.IMAPAccount{
		Username:       "user@example.com",
		PasswordSecret: "vault://legacy/path",
		Host:           "imap.example.com",
		Port:           993,
	}
	poller, err := NewMailPoller(s, []config.IMAPAccount{account}, MailPollerOptions{ProcessingDir: dir})
	if err != nil {
		t.Fatal(err)
	}

	dialCalled := false
	poller.dialIMAP = func(context.Context, string, string) (imapClient, error) {
		dialCalled = true
		return nil, nil
	}

	err = poller.pollAccount(context.Background(), account)
	if err == nil {
		t.Fatal("pollAccount accepted an unknown secret scheme")
	}
	if !strings.Contains(err.Error(), "vault://legacy/path") {
		t.Errorf("error %q does not identify the configured reference", err)
	}
	if dialCalled {
		t.Fatal("pollAccount attempted IMAP login after secret resolution failed")
	}
}
