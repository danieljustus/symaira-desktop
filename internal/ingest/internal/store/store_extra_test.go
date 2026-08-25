package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestValidateClassificationRule_AllBranches(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		kind    string
		value   string
		wantErr string
	}{
		{"empty pattern", "", "category", "Finance", "pattern cannot be empty"},
		{"invalid kind", "invoice", "invalid", "Finance", "invalid rule kind"},
		{"empty value", "invoice", "category", "", "value cannot be empty"},
		{"valid category", "invoice", "category", "Finance", ""},
		{"valid tag", "invoice", "tag", "financial", ""},
		{"valid correspondent", "invoice", "correspondent", "Acme", ""},
		{"valid document_type", "invoice", "document_type", "Invoice", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateClassificationRule(tc.pattern, tc.kind, tc.value)
			if tc.wantErr == "" {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			} else {
				if err == nil {
					t.Error("expected error, got nil")
				} else if !containsStr(err.Error(), tc.wantErr) {
					t.Errorf("error = %q, want containing %q", err.Error(), tc.wantErr)
				}
			}
		})
	}
}

func TestListDocuments_ReturnsDocumentsWithPath(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	// Create a document and set vault path
	doc, _, err := s.CreateOrGet(ctx, "/tmp/doc.pdf", "hash1", "application/pdf")
	if err != nil {
		t.Fatal(err)
	}
	vaultPath := "/vault/doc.pdf.md"
	archivePath := "/archive/doc.pdf"
	if err := s.SetVaultAndArchivePath(ctx, doc.ID, vaultPath, archivePath, "invoice", []string{"financial"}, "Acme", "Invoice"); err != nil {
		t.Fatal(err)
	}

	// Create another document WITHOUT vault path — should not appear in ListDocuments
	_, _, err = s.CreateOrGet(ctx, "/tmp/doc2.pdf", "hash2", "application/pdf")
	if err != nil {
		t.Fatal(err)
	}

	docs, err := s.ListDocuments(ctx)
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected 1 document, got %d", len(docs))
	}
	if docs[0].VaultPath == nil || *docs[0].VaultPath != vaultPath {
		t.Errorf("VaultPath = %v, want %s", docs[0].VaultPath, vaultPath)
	}
	if docs[0].ArchivePath == nil || *docs[0].ArchivePath != archivePath {
		t.Errorf("ArchivePath = %v, want %s", docs[0].ArchivePath, archivePath)
	}
	if docs[0].Category != "invoice" {
		t.Errorf("Category = %q, want invoice", docs[0].Category)
	}
	if len(docs[0].Tags) != 1 || docs[0].Tags[0] != "financial" {
		t.Errorf("Tags = %v, want [financial]", docs[0].Tags)
	}
	if docs[0].Correspondent != "Acme" {
		t.Errorf("Correspondent = %q, want Acme", docs[0].Correspondent)
	}
	if docs[0].DocumentType != "Invoice" {
		t.Errorf("DocumentType = %q, want Invoice", docs[0].DocumentType)
	}
}

func TestListDocuments_Empty(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	docs, err := s.ListDocuments(context.Background())
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	if len(docs) != 0 {
		t.Errorf("expected 0 documents, got %d", len(docs))
	}
}

func TestListDocuments_OrderedByVaultPathThenID(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	// Create two documents with different vault paths
	doc1, _, _ := s.CreateOrGet(ctx, "/tmp/a.pdf", "hash-a", "application/pdf")
	s.SetVaultAndArchivePath(ctx, doc1.ID, "/vault/b.md", "", "", nil, "", "")
	doc2, _, _ := s.CreateOrGet(ctx, "/tmp/b.pdf", "hash-b", "application/pdf")
	s.SetVaultAndArchivePath(ctx, doc2.ID, "/vault/a.md", "", "", nil, "", "")

	docs, err := s.ListDocuments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 {
		t.Fatalf("expected 2, got %d", len(docs))
	}
	// Should be sorted by vault_path ASC
	if *docs[0].VaultPath != "/vault/a.md" || *docs[1].VaultPath != "/vault/b.md" {
		t.Errorf("order wrong: %v, %v", *docs[0].VaultPath, *docs[1].VaultPath)
	}
}

func TestListDocuments_WithSourceMailID(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	doc, _, _ := s.CreateOrGet(ctx, "/tmp/mail.pdf", "hash-mail", "application/pdf")
	s.SetVaultAndArchivePath(ctx, doc.ID, "/vault/mail.pdf.md", "/archive/mail.pdf", "", nil, "", "")
	s.SetProvenance(ctx, doc.ID, "msg-123", "sender@example.com")

	docs, err := s.ListDocuments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected 1, got %d", len(docs))
	}
	if docs[0].SourceMailID == nil || *docs[0].SourceMailID != "msg-123" {
		t.Errorf("SourceMailID = %v, want msg-123", docs[0].SourceMailID)
	}
}

func TestByArchivePath_FoundAndNotFound(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	doc, _, _ := s.CreateOrGet(ctx, "/tmp/doc.pdf", "hash-archive", "application/pdf")
	s.SetVaultAndArchivePath(ctx, doc.ID, "/vault/doc.pdf.md", "/archive/doc.pdf", "", nil, "", "")

	// Found
	found, err := s.ByArchivePath(ctx, "/archive/doc.pdf")
	if err != nil {
		t.Fatalf("ByArchivePath: %v", err)
	}
	if found == nil || found.ID != doc.ID {
		t.Errorf("found.ID = %v, want %d", found, doc.ID)
	}

	// Not found
	_, err = s.ByArchivePath(ctx, "/archive/nonexistent.pdf")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("expected sql.ErrNoRows for nonexistent path, got %v", err)
	}
}

func TestEnqueueReprocessJob_CreatesAndDeduplicates(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	doc, _, _ := s.CreateOrGet(ctx, "/tmp/doc.pdf", "hash-reprocess", "application/pdf")
	s.SetVaultAndArchivePath(ctx, doc.ID, "/vault/doc.pdf.md", "/archive/doc.pdf", "", nil, "", "")

	// First call creates
	job, created, err := s.EnqueueReprocessJob(ctx, doc.ID)
	if err != nil {
		t.Fatalf("EnqueueReprocessJob: %v", err)
	}
	if !created {
		t.Error("expected created=true on first call")
	}
	if job == nil {
		t.Fatal("expected non-nil job")
	}
	if job.Kind != "reocr" {
		t.Errorf("Kind = %q, want reocr", job.Kind)
	}

	// Second call deduplicates
	job2, created2, err := s.EnqueueReprocessJob(ctx, doc.ID)
	if err != nil {
		t.Fatalf("EnqueueReprocessJob second: %v", err)
	}
	if created2 {
		t.Error("expected created=false on second call")
	}
	if job2.ID != job.ID {
		t.Errorf("second call returned different job ID: %d vs %d", job2.ID, job.ID)
	}
}

func TestSetProvenance_And_HasMailMessage_And_TrackMailMessage(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	doc, _, _ := s.CreateOrGet(ctx, "/tmp/mail.pdf", "hash-prov", "application/pdf")
	s.SetVaultAndArchivePath(ctx, doc.ID, "/vault/mail.pdf.md", "/archive/mail.pdf", "", nil, "", "")

	if err := s.SetProvenance(ctx, doc.ID, "msg-456", "alice@example.com"); err != nil {
		t.Fatalf("SetProvenance: %v", err)
	}

	// Verify via ListDocuments (ByID doesn't select source_mail_id)
	docs, err := s.ListDocuments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected 1 document, got %d", len(docs))
	}
	if docs[0].SourceMailID == nil || *docs[0].SourceMailID != "msg-456" {
		t.Errorf("SourceMailID = %v, want msg-456", docs[0].SourceMailID)
	}
	if docs[0].Correspondent != "alice@example.com" {
		t.Errorf("Correspondent = %q, want alice@example.com", docs[0].Correspondent)
	}

	// HasMailMessage — false initially
	has, err := s.HasMailMessage(ctx, "msg-789")
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Error("expected HasMailMessage=false for unknown message")
	}

	// TrackMailMessage
	if err := s.TrackMailMessage(ctx, "msg-789", "inbox"); err != nil {
		t.Fatalf("TrackMailMessage: %v", err)
	}

	// HasMailMessage — true after tracking
	has, err = s.HasMailMessage(ctx, "msg-789")
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Error("expected HasMailMessage=true after tracking")
	}

	// TrackMailMessage idempotency (INSERT OR IGNORE)
	if err := s.TrackMailMessage(ctx, "msg-789", "inbox"); err != nil {
		t.Fatalf("TrackMailMessage idempotent: %v", err)
	}
}

func TestRecordAndGetMailPollStatus(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	// GetMailPollStatus — nil for an account never polled.
	got, err := s.GetMailPollStatus(ctx, "user@imap.example.com:993/INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("expected nil status for unpolled account, got %+v", got)
	}

	firstPoll := time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)
	if err := s.RecordMailPollStatus(ctx, "user@imap.example.com:993/INBOX", firstPoll, "ok", ""); err != nil {
		t.Fatalf("RecordMailPollStatus: %v", err)
	}

	got, err = s.GetMailPollStatus(ctx, "user@imap.example.com:993/INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected a status after recording a poll")
	}
	if got.Status != "ok" || got.LastError != "" || !got.LastPolledAt.Equal(firstPoll) {
		t.Errorf("unexpected status after first record: %+v", got)
	}

	// A second poll upserts the row rather than accumulating history.
	secondPoll := firstPoll.Add(5 * time.Minute)
	if err := s.RecordMailPollStatus(ctx, "user@imap.example.com:993/INBOX", secondPoll, "error", "network timeout"); err != nil {
		t.Fatalf("RecordMailPollStatus (update): %v", err)
	}

	got, err = s.GetMailPollStatus(ctx, "user@imap.example.com:993/INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected a status after the second record")
	}
	if got.Status != "error" || got.LastError != "network timeout" || !got.LastPolledAt.Equal(secondPoll) {
		t.Errorf("expected upserted status, got %+v", got)
	}

	// A different account's status is independent.
	otherGot, err := s.GetMailPollStatus(ctx, "other@imap.example.com:993/INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if otherGot != nil {
		t.Errorf("expected nil status for a different account, got %+v", otherGot)
	}
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstring(s, substr))
}

func containsSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestUpdateRule_ValidationErrors(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(filepath.Join(dir, "test.db"))
	defer s.Close()
	ctx := context.Background()

	rule, _ := s.AddRule(ctx, "test", "category", "Finance")

	_, err := s.UpdateRule(ctx, rule.ID, "", "category", "Finance")
	if err == nil {
		t.Error("expected error for empty pattern")
	}

	_, err = s.UpdateRule(ctx, rule.ID, "test", "invalid_kind", "Finance")
	if err == nil {
		t.Error("expected error for invalid kind")
	}

	_, err = s.UpdateRule(ctx, rule.ID, "test", "category", "")
	if err == nil {
		t.Error("expected error for empty value")
	}

	_, err = s.UpdateRule(ctx, 9999, "test", "category", "Finance")
	if err == nil {
		t.Error("expected error for nonexistent rule")
	}

	updated, err := s.UpdateRule(ctx, rule.ID, "updated", "tag", "new-tag")
	if err != nil {
		t.Fatalf("UpdateRule: %v", err)
	}
	if updated.Pattern != "updated" || updated.Kind != "tag" || updated.Value != "new-tag" {
		t.Errorf("updated = %+v", updated)
	}
}

func TestByID_NotFound(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(filepath.Join(dir, "test.db"))
	defer s.Close()

	_, err := s.ByID(context.Background(), 9999)
	if err == nil {
		t.Error("expected error for nonexistent document")
	}
}

func TestFailJob(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(filepath.Join(dir, "test.db"))
	defer s.Close()
	ctx := context.Background()

	doc, _, _ := s.CreateOrGet(ctx, "/tmp/doc.pdf", "hash-fail", "application/pdf")
	job, _ := s.EnqueueJob(ctx, doc.ID, "text/plain")

	if err := s.FailJob(ctx, job.ID, "test error"); err != nil {
		t.Fatalf("FailJob: %v", err)
	}

	jobs, _ := s.ListJobs(ctx, 10)
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	if jobs[0].LastError == nil || *jobs[0].LastError != "test error" {
		t.Errorf("last_error = %v", jobs[0].LastError)
	}
}

func TestCompleteJob(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(filepath.Join(dir, "test.db"))
	defer s.Close()
	ctx := context.Background()

	doc, _, _ := s.CreateOrGet(ctx, "/tmp/doc.pdf", "hash-complete", "application/pdf")
	job, _ := s.EnqueueJob(ctx, doc.ID, "text/plain")

	if err := s.CompleteJob(ctx, job.ID); err != nil {
		t.Fatalf("CompleteJob: %v", err)
	}

	jobs, _ := s.ListJobs(ctx, 10)
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	if jobs[0].Status != "completed" {
		t.Errorf("status = %q, want completed", jobs[0].Status)
	}
}
