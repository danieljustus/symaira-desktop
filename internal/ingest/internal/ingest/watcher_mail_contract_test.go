package ingest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/ingest/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/ingest/internal/store"
	"github.com/emersion/go-imap/v2"
)

func TestWatcher_CheckStabilityReschedulesAfterFileChange(t *testing.T) {
	w, s, inbox, clk := newTestWatcher(t)
	ctx := context.Background()
	path := filepath.Join(inbox, "changing.txt")
	if err := os.WriteFile(path, []byte("initial"), 0o600); err != nil {
		t.Fatal(err)
	}

	w.debounceFile(ctx, path)
	w.mu.Lock()
	initialTimer := w.pending[path].timer
	w.mu.Unlock()

	changed := []byte("changed while waiting")
	if err := os.WriteFile(path, changed, 0o600); err != nil {
		t.Fatal(err)
	}

	// The first stability check observes the changed size, updates the
	// pending state, and schedules a fresh stability timer.
	clk.Advance(time.Second)

	w.mu.Lock()
	state, ok := w.pending[path]
	if ok {
		if state.lastSize != int64(len(changed)) {
			t.Errorf("lastSize = %d, want %d", state.lastSize, len(changed))
		}
		if state.timer == initialTimer {
			t.Error("expected changed file to receive a new stability timer")
		}
	}
	w.mu.Unlock()
	if !ok {
		t.Fatal("expected changed file to remain pending for a second stability check")
	}

	// The replacement timer now sees an unchanged file and enqueues it.
	clk.Advance(time.Second)

	w.mu.Lock()
	_, stillPending := w.pending[path]
	w.mu.Unlock()
	if stillPending {
		t.Fatal("expected stable file to be removed from pending")
	}

	jobs, err := s.ListJobs(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].SourcePath != path {
		t.Fatalf("expected one job for %s, got %+v", path, jobs)
	}
}

func TestMailPoller_PollAccount_StartUIDContract(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "mail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { closeTestResource(t, "store", s) }()

	acc := config.IMAPAccount{
		Username:       "test@example.com",
		PasswordSecret: "myplaintextpw",
		Host:           "imap.example.com",
		Port:           993,
	}
	poller, err := NewMailPoller(s, []config.IMAPAccount{acc}, MailPollerOptions{})
	if err != nil {
		t.Fatal(err)
	}

	fakeClient := &fakeIMAPClient{
		selectStatus: &mailboxStatus{UIDValidity: 7, UIDNext: 11},
	}
	poller.dialIMAP = func(context.Context, string, string) (imapClient, error) {
		return fakeClient, nil
	}
	ctx := context.Background()

	// A first poll has no cursor and must search from UID 1. It records the
	// highest known UID so the next poll can resume from it.
	if err := poller.pollAccount(ctx, acc); err != nil {
		t.Fatalf("first poll: %v", err)
	}
	assertSearchStartUID(t, fakeClient.lastSearchCriteria, 1)
	cursor, err := s.GetMailPollCursor(ctx, config.AccountID(acc))
	if err != nil {
		t.Fatal(err)
	}
	if cursor == nil || cursor.UIDValidity != 7 || cursor.LastUID != 10 {
		t.Fatalf("unexpected first cursor: %+v", cursor)
	}

	// An unchanged UIDValidity resumes strictly after the stored LastUID.
	fakeClient.selectStatus = &mailboxStatus{UIDValidity: 7, UIDNext: 20}
	if err := poller.pollAccount(ctx, acc); err != nil {
		t.Fatalf("resumed poll: %v", err)
	}
	assertSearchStartUID(t, fakeClient.lastSearchCriteria, 11)

	// A new UIDValidity invalidates the cursor and forces a full rescan.
	fakeClient.selectStatus = &mailboxStatus{UIDValidity: 8, UIDNext: 3}
	if err := poller.pollAccount(ctx, acc); err != nil {
		t.Fatalf("reset poll: %v", err)
	}
	assertSearchStartUID(t, fakeClient.lastSearchCriteria, 1)
}

func TestMailPoller_PollAccount_CursorLoadError(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "mail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { closeTestResource(t, "store", s) }()

	acc := config.IMAPAccount{
		Username:       "test@example.com",
		PasswordSecret: "myplaintextpw",
		Host:           "imap.example.com",
		Port:           993,
	}
	poller, err := NewMailPoller(s, []config.IMAPAccount{acc}, MailPollerOptions{})
	if err != nil {
		t.Fatal(err)
	}

	fakeClient := &fakeIMAPClient{
		selectStatus: &mailboxStatus{UIDValidity: 7, UIDNext: 11},
	}
	poller.dialIMAP = func(context.Context, string, string) (imapClient, error) {
		return fakeClient, nil
	}

	cursorErr := errors.New("cursor store unavailable")
	poller.getMailPollCursor = func(context.Context, string) (*store.MailPollCursor, error) {
		return nil, cursorErr
	}

	err = poller.pollAccount(context.Background(), acc)
	if err == nil {
		t.Fatal("expected cursor-load error, got nil")
	}
	if !errors.Is(err, cursorErr) {
		t.Fatalf("error = %v, want underlying cursor error", err)
	}
	if got, want := err.Error(), "load poll cursor: cursor store unavailable"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
	if fakeClient.lastSearchCriteria != nil {
		t.Fatal("expected poll to stop before searching after cursor-load failure")
	}
}

func assertSearchStartUID(t *testing.T, criteria *imap.SearchCriteria, want uint32) {
	t.Helper()
	if criteria == nil || len(criteria.UID) != 1 || len(criteria.UID[0]) != 1 {
		t.Fatalf("unexpected UID search criteria: %+v", criteria)
	}
	rng := criteria.UID[0][0]
	if got := uint32(rng.Start); got != want {
		t.Errorf("UID search starts at %d, want %d", got, want)
	}
	if rng.Stop != 0 {
		t.Errorf("UID search stops at %d, want open-ended range", rng.Stop)
	}
}
