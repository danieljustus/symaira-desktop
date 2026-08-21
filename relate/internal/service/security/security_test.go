package security

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contactdomain "github.com/danieljustus/symaira-relate/internal/domain/contact"
	"github.com/danieljustus/symaira-relate/internal/errs"
	contactsvc "github.com/danieljustus/symaira-relate/internal/service/contact"
	"github.com/danieljustus/symaira-relate/internal/storage/sqlite"
)

type fixture struct {
	contacts *contactsvc.Service
	security *Service
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db, err := sqlite.OpenMemory(context.Background())
	if err != nil {
		t.Fatalf("OpenMemory() error = %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return &fixture{contacts: contactsvc.New(db), security: New(db)}
}

func (f *fixture) person(t *testing.T, name, email string) string {
	t.Helper()
	p, err := f.contacts.CreatePerson(context.Background(), contactdomain.PersonInput{DisplayName: name})
	if err != nil {
		t.Fatalf("CreatePerson() error = %v", err)
	}
	if email != "" {
		if _, err := f.contacts.AddPersonContactPoint(context.Background(), p.ID, contactdomain.ContactPointInput{Kind: contactdomain.ContactPointEmail, RawValue: email}); err != nil {
			t.Fatalf("AddPersonContactPoint() error = %v", err)
		}
	}
	return p.ID
}

func TestBackup_RequiresPassphrase(t *testing.T) {
	f := newFixture(t)
	var buf bytes.Buffer
	err := f.security.Backup(context.Background(), nil, &buf)
	if errs.KindOf(err) != errs.KindInvalid {
		t.Fatalf("Backup() with no passphrase: kind = %v, want invalid", errs.KindOf(err))
	}
}

func TestBackupRestore_RoundTrips(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	personID := f.person(t, "Ada Lovelace", "ada@example.com")

	var buf bytes.Buffer
	if err := f.security.Backup(ctx, []byte("correct horse battery staple"), &buf); err != nil {
		t.Fatalf("Backup() error = %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("Backup() wrote no data")
	}

	targetPath := filepath.Join(t.TempDir(), "restored.db")
	if err := Restore(ctx, []byte("correct horse battery staple"), bytes.NewReader(buf.Bytes()), targetPath); err != nil {
		t.Fatalf("Restore() error = %v", err)
	}

	restoredDB, err := sqlite.Open(ctx, targetPath)
	if err != nil {
		t.Fatalf("open restored db error = %v", err)
	}
	defer restoredDB.Close()

	var displayName string
	if err := restoredDB.QueryRowContext(ctx, "SELECT display_name FROM persons WHERE id = ?", personID).Scan(&displayName); err != nil {
		t.Fatalf("query restored person error = %v", err)
	}
	if displayName != "Ada Lovelace" {
		t.Errorf("restored display_name = %q, want Ada Lovelace", displayName)
	}
}

func TestRestore_WrongPassphraseLeavesNoPartialWrite(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.person(t, "Ada Lovelace", "ada@example.com")

	var buf bytes.Buffer
	if err := f.security.Backup(ctx, []byte("right-passphrase"), &buf); err != nil {
		t.Fatalf("Backup() error = %v", err)
	}

	targetPath := filepath.Join(t.TempDir(), "restored.db")
	err := Restore(ctx, []byte("wrong-passphrase"), bytes.NewReader(buf.Bytes()), targetPath)
	if err == nil {
		t.Fatal("Restore() with wrong passphrase succeeded, want error")
	}

	if _, statErr := os.Stat(targetPath); !os.IsNotExist(statErr) {
		t.Errorf("target file exists after failed restore (partial write): %v", statErr)
	}
	if _, statErr := os.Stat(targetPath + ".restoring"); !os.IsNotExist(statErr) {
		t.Errorf("temp restore file left behind after failure: %v", statErr)
	}
}

func TestRestore_CorruptedCiphertextRejected(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.person(t, "Ada Lovelace", "ada@example.com")

	var buf bytes.Buffer
	if err := f.security.Backup(ctx, []byte("passphrase"), &buf); err != nil {
		t.Fatalf("Backup() error = %v", err)
	}

	corrupted := buf.Bytes()
	// Flip a byte well inside the ciphertext region (past the header).
	corrupted[len(corrupted)-1] ^= 0xFF

	targetPath := filepath.Join(t.TempDir(), "restored.db")
	err := Restore(ctx, []byte("passphrase"), bytes.NewReader(corrupted), targetPath)
	if err == nil {
		t.Fatal("Restore() with corrupted ciphertext succeeded, want error")
	}
	if _, statErr := os.Stat(targetPath); !os.IsNotExist(statErr) {
		t.Errorf("target file exists after corrupted restore: %v", statErr)
	}
}

func TestRestore_TruncatedOrNonBackupInputRejected(t *testing.T) {
	ctx := context.Background()
	targetPath := filepath.Join(t.TempDir(), "restored.db")

	err := Restore(ctx, []byte("passphrase"), bytes.NewReader([]byte("not a backup")), targetPath)
	if errs.KindOf(err) != errs.KindInvalid {
		t.Fatalf("Restore() with garbage input: kind = %v, want invalid", errs.KindOf(err))
	}
	if _, statErr := os.Stat(targetPath); !os.IsNotExist(statErr) {
		t.Errorf("target file exists after invalid restore: %v", statErr)
	}
}

func TestEraseContact_RemovesLinkedDataAndRecordsAudit(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	personID := f.person(t, "Grace Hopper", "grace@example.com")
	if err := f.contacts.AddPersonAlias(ctx, personID, "Amazing Grace"); err != nil {
		t.Fatalf("AddPersonAlias() error = %v", err)
	}

	summary, err := f.security.EraseContact(ctx, personID)
	if err != nil {
		t.Fatalf("EraseContact() error = %v", err)
	}
	if summary.ContactPointsRemoved != 1 {
		t.Errorf("ContactPointsRemoved = %d, want 1", summary.ContactPointsRemoved)
	}
	if summary.AliasesRemoved != 1 {
		t.Errorf("AliasesRemoved = %d, want 1", summary.AliasesRemoved)
	}

	// Deletion recovery check: nothing referencing the erased person
	// should remain anywhere in the database.
	if _, err := f.contacts.GetPerson(ctx, personID); errs.KindOf(err) != errs.KindNotFound {
		t.Fatalf("GetPerson() after erase: kind = %v, want not_found", errs.KindOf(err))
	}
	var orphanCount int
	for _, table := range []string{"contact_points", "aliases", "entity_tags", "entity_classifications", "organization_memberships", "interactions", "follow_ups"} {
		if err := f.security.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE person_id = ?", personID).Scan(&orphanCount); err != nil {
			t.Fatalf("query %s error = %v", table, err)
		}
		if orphanCount != 0 {
			t.Errorf("table %s has %d orphaned rows for erased person", table, orphanCount)
		}
	}

	events, err := f.security.ListAuditEvents(ctx)
	if err != nil {
		t.Fatalf("ListAuditEvents() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	ev := events[0]
	if ev.Operation != "erase_contact" || ev.EntityID != personID {
		t.Errorf("audit event = %+v, want erase_contact for %s", ev, personID)
	}
	if strings.Contains(ev.Detail, "Grace") || strings.Contains(ev.Detail, "grace@example.com") {
		t.Errorf("audit detail leaked contact data: %q", ev.Detail)
	}
}

func TestEraseContact_NotFound(t *testing.T) {
	f := newFixture(t)
	_, err := f.security.EraseContact(context.Background(), "does-not-exist")
	if errs.KindOf(err) != errs.KindNotFound {
		t.Fatalf("EraseContact() kind = %v, want not_found", errs.KindOf(err))
	}
}

func TestExportJSON_ContainsContactDataButNotNotes(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.person(t, "Ada Lovelace", "ada@example.com")

	var buf bytes.Buffer
	if err := f.security.ExportJSON(ctx, &buf); err != nil {
		t.Fatalf("ExportJSON() error = %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Ada Lovelace") || !strings.Contains(out, "ada@example.com") {
		t.Errorf("export missing expected contact data: %s", out)
	}
	if strings.Contains(out, "\"notes\"") {
		t.Errorf("export includes notes field, want data-minimized export: %s", out)
	}
}

func TestExportCSV_HasHeaderAndRow(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.person(t, "Ada Lovelace", "ada@example.com")

	var buf bytes.Buffer
	if err := f.security.ExportCSV(ctx, &buf); err != nil {
		t.Fatalf("ExportCSV() error = %v", err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want header + 1 row: %q", len(lines), buf.String())
	}
	if !strings.Contains(lines[1], "Ada Lovelace") {
		t.Errorf("row missing expected name: %q", lines[1])
	}
}

func TestListAuditEvents_MostRecentFirst(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	p1 := f.person(t, "First", "")
	p2 := f.person(t, "Second", "")

	if _, err := f.security.EraseContact(ctx, p1); err != nil {
		t.Fatalf("EraseContact(p1) error = %v", err)
	}
	if _, err := f.security.EraseContact(ctx, p2); err != nil {
		t.Fatalf("EraseContact(p2) error = %v", err)
	}

	events, err := f.security.ListAuditEvents(ctx)
	if err != nil {
		t.Fatalf("ListAuditEvents() error = %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("len(events) = %d, want 2", len(events))
	}
	if events[0].EntityID != p2 {
		t.Errorf("events[0].EntityID = %s, want most-recent %s first", events[0].EntityID, p2)
	}
}
