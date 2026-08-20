package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-ingest/internal/annotate"
)

func TestCreateOrGet_Deduplicates(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	d1, created1, err := s.CreateOrGet(ctx, "/tmp/a.pdf", "abc123", "application/pdf")
	if err != nil {
		t.Fatalf("CreateOrGet first: %v", err)
	}
	if !created1 {
		t.Fatal("expected first document to be created")
	}
	d2, created2, err := s.CreateOrGet(ctx, "/tmp/b.pdf", "abc123", "application/pdf")
	if err != nil {
		t.Fatalf("CreateOrGet second: %v", err)
	}
	if created2 {
		t.Fatal("expected second duplicate document not to be created")
	}
	if d1.ID != d2.ID {
		t.Fatal("expected duplicate hash to return same document")
	}
}

func TestSetVaultAndArchivePath(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	d, _, err := s.CreateOrGet(ctx, "/tmp/a.pdf", "abc", "application/pdf")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetVaultAndArchivePath(ctx, d.ID, "/vault/a.pdf.md", "/archive/abc.pdf", "invoices", []string{"tax", "2026"}, "Internal Revenue Service", "Tax Form"); err != nil {
		t.Fatalf("SetVaultAndArchivePath: %v", err)
	}
	got, err := s.ByHash(ctx, "abc")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "done" {
		t.Fatalf("status = %q, want done", got.Status)
	}
	if got.VaultPath == nil || *got.VaultPath != "/vault/a.pdf.md" {
		t.Fatalf("vault_path mismatch")
	}
	if got.ArchivePath == nil || *got.ArchivePath != "/archive/abc.pdf" {
		t.Fatalf("archive_path mismatch: got %v, want /archive/abc.pdf", got.ArchivePath)
	}
	if got.Category != "invoices" {
		t.Fatalf("category = %q, want invoices", got.Category)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "tax" || got.Tags[1] != "2026" {
		t.Fatalf("tags = %v, want [tax, 2026]", got.Tags)
	}
	if got.Correspondent != "Internal Revenue Service" {
		t.Fatalf("correspondent = %q, want Internal Revenue Service", got.Correspondent)
	}
	if got.DocumentType != "Tax Form" {
		t.Fatalf("document_type = %q, want Tax Form", got.DocumentType)
	}
}

func TestStore_JobsQueue(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	ctx := context.Background()

	// 1. Create a document and enqueue a job.
	d, _, err := s.CreateOrGet(ctx, "/tmp/doc1.txt", "hash1", "text/plain")
	if err != nil {
		t.Fatalf("CreateOrGet: %v", err)
	}

	job, err := s.EnqueueJob(ctx, d.ID, "text")
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	if job.Status != "pending" || job.DocumentID != d.ID || job.Attempts != 0 {
		t.Fatalf("unexpected job state: %+v", job)
	}

	// 2. Claim the job.
	claimed, err := s.ClaimJob(ctx)
	if err != nil {
		t.Fatalf("ClaimJob: %v", err)
	}
	if claimed == nil {
		t.Fatal("expected to claim a job, got nil")
	}
	if claimed.ID != job.ID || claimed.Status != "running" || claimed.Attempts != 1 {
		t.Fatalf("unexpected claimed job state: %+v", claimed)
	}

	// 3. Fail the job (non-terminally since attempts = 1 < 3).
	err = s.FailJob(ctx, claimed.ID, "first error")
	if err != nil {
		t.Fatalf("FailJob: %v", err)
	}

	// Verify document is still pending.
	doc, err := s.ByID(ctx, d.ID)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if doc.Status != "pending" {
		t.Fatalf("expected document to remain pending, got %s", doc.Status)
	}

	// 4. Claim again. Since we failed it, and backoff is 10s, immediate claim should return nil!
	claimed2, err := s.ClaimJob(ctx)
	if err != nil {
		t.Fatalf("ClaimJob retry: %v", err)
	}
	if claimed2 != nil {
		t.Fatalf("expected nil claim due to 10s cooldown, got %+v", claimed2)
	}

	// 5. Retry the job manually.
	err = s.RetryJob(ctx, claimed.ID)
	if err != nil {
		t.Fatalf("RetryJob: %v", err)
	}

	// Check status has reset to pending, and attempts is 0.
	jobs, err := s.ListJobs(ctx, 0)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != 1 || jobs[0].Status != "pending" || jobs[0].Attempts != 0 {
		t.Fatalf("unexpected job list/state: %+v", jobs[0])
	}

	// 6. Claim again (now that it is pending and attempts = 0).
	claimed3, err := s.ClaimJob(ctx)
	if err != nil {
		t.Fatalf("ClaimJob after retry: %v", err)
	}
	if claimed3 == nil || claimed3.ID != job.ID || claimed3.Attempts != 1 {
		t.Fatalf("unexpected claim after retry: %+v", claimed3)
	}

	// 7. Fail it. Attempts was 1.
	if err := s.FailJob(ctx, claimed3.ID, "second error"); err != nil {
		t.Fatal(err)
	}

	// 8. We manually update the updated_at in the db to bypass the 10s backoff for testing.
	_, err = s.db.ExecContext(ctx, "UPDATE jobs SET updated_at = datetime('now', '-20 seconds') WHERE id = ?", claimed3.ID)
	if err != nil {
		t.Fatalf("bypass backoff: %v", err)
	}

	// 9. Claim it again. Attempts becomes 2.
	claimed4, err := s.ClaimJob(ctx)
	if err != nil {
		t.Fatalf("ClaimJob (attempts=2): %v", err)
	}
	if claimed4 == nil || claimed4.Attempts != 2 {
		t.Fatalf("expected to claim job for 2nd attempt, got %+v", claimed4)
	}

	// 10. Fail it again.
	if err := s.FailJob(ctx, claimed4.ID, "third error"); err != nil {
		t.Fatal(err)
	}

	// 11. We manually update the updated_at in the db to bypass the 10s backoff for testing.
	_, err = s.db.ExecContext(ctx, "UPDATE jobs SET updated_at = datetime('now', '-20 seconds') WHERE id = ?", claimed4.ID)
	if err != nil {
		t.Fatalf("bypass backoff: %v", err)
	}

	// 12. Claim it again. Attempts becomes 3.
	claimed5, err := s.ClaimJob(ctx)
	if err != nil {
		t.Fatalf("ClaimJob (attempts=3): %v", err)
	}
	if claimed5 == nil || claimed5.Attempts != 3 {
		t.Fatalf("expected to claim job for 3rd attempt, got %+v", claimed5)
	}

	// 13. Fail it terminally (attempts = 3).
	if err := s.FailJob(ctx, claimed5.ID, "third terminal error"); err != nil {
		t.Fatal(err)
	}

	// Verify document is now terminally failed.
	doc, err = s.ByID(ctx, d.ID)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if doc.Status != "failed" {
		t.Fatalf("expected document status to be failed, got %s", doc.Status)
	}

	// 11. Complete a different job.
	d2, _, err := s.CreateOrGet(ctx, "/tmp/doc2.txt", "hash2", "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	job2, err := s.EnqueueJob(ctx, d2.ID, "text")
	if err != nil {
		t.Fatal(err)
	}
	claimed6, err := s.ClaimJob(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if claimed6.ID != job2.ID {
		t.Fatalf("expected job 2, got %d", claimed6.ID)
	}
	if err := s.CompleteJob(ctx, claimed6.ID); err != nil {
		t.Fatalf("CompleteJob: %v", err)
	}

	doc2, err := s.ByID(ctx, d2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if doc2.Status != "done" {
		t.Fatalf("expected doc2 status to be done, got %s", doc2.Status)
	}

	// 12. List jobs and check.
	allJobs, err := s.ListJobs(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(allJobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(allJobs))
	}
}

func TestStore_ClaimJobByID(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	ctx := context.Background()

	d1, _, err := s.CreateOrGet(ctx, "/tmp/doc1.txt", "hash1", "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	d2, _, err := s.CreateOrGet(ctx, "/tmp/doc2.txt", "hash2", "text/plain")
	if err != nil {
		t.Fatal(err)
	}

	job1, err := s.EnqueueJob(ctx, d1.ID, "text")
	if err != nil {
		t.Fatal(err)
	}
	job2, err := s.EnqueueJob(ctx, d2.ID, "text")
	if err != nil {
		t.Fatal(err)
	}

	claimed, err := s.ClaimJobByID(ctx, job2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil || claimed.ID != job2.ID {
		t.Fatalf("expected to claim job2, got %+v", claimed)
	}
	if claimed.Status != "running" || claimed.Attempts != 1 {
		t.Fatalf("unexpected state: status=%s attempts=%d", claimed.Status, claimed.Attempts)
	}

	other, err := s.ClaimJobByID(ctx, job1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if other == nil || other.ID != job1.ID {
		t.Fatalf("expected to claim job1, got %+v", other)
	}

	claimedAgain, err := s.ClaimJobByID(ctx, job2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if claimedAgain != nil {
		t.Fatalf("expected nil when re-claiming running job, got %+v", claimedAgain)
	}

	nonexistent, err := s.ClaimJobByID(ctx, 99999)
	if err != nil {
		t.Fatal(err)
	}
	if nonexistent != nil {
		t.Fatalf("expected nil for nonexistent job, got %+v", nonexistent)
	}
}

func TestStore_Rules(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	ctx := context.Background()

	// 1. Initially empty
	rules, err := s.ListRules(ctx)
	if err != nil {
		t.Fatalf("ListRules: %v", err)
	}
	if len(rules) != 0 {
		t.Fatalf("expected 0 rules, got %d", len(rules))
	}

	// 2. Add rule
	r1, err := s.AddRule(ctx, "acme", "category", "invoices")
	if err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	if r1.Pattern != "acme" || r1.Kind != "category" || r1.Value != "invoices" {
		t.Fatalf("unexpected rule values: %+v", r1)
	}

	// 3. Add second rule
	r2, err := s.AddRule(ctx, "tax", "tag", "financial")
	if err != nil {
		t.Fatalf("AddRule: %v", err)
	}

	// 4. List rules
	rules, err = s.ListRules(ctx)
	if err != nil {
		t.Fatalf("ListRules: %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(rules))
	}
	if rules[0].ID != r1.ID || rules[1].ID != r2.ID {
		t.Fatalf("rule order/ID mismatch")
	}

	// 5. Update rule
	updated, err := s.UpdateRule(ctx, r1.ID, "acme inc", "correspondent", "Acme Inc")
	if err != nil {
		t.Fatalf("UpdateRule: %v", err)
	}
	if updated.Pattern != "acme inc" || updated.Kind != "correspondent" || updated.Value != "Acme Inc" {
		t.Fatalf("unexpected updated rule: %+v", updated)
	}

	// 6. Delete rule
	if err := s.DeleteRule(ctx, r1.ID); err != nil {
		t.Fatalf("DeleteRule: %v", err)
	}

	// 7. Verify deletion
	rules, err = s.ListRules(ctx)
	if err != nil {
		t.Fatalf("ListRules: %v", err)
	}
	if len(rules) != 1 || rules[0].ID != r2.ID {
		t.Fatalf("expected only r2 rule to remain, got: %+v", rules)
	}

	// 7. Delete non-existent rule should fail
	if err := s.DeleteRule(ctx, 9999); err == nil {
		t.Fatal("expected error deleting non-existent rule, got nil")
	}
}

func TestPaperlessImportState_UpsertAndStatus(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	ctx := context.Background()

	const baseURL = "https://paperless.local"

	// No status recorded yet.
	_, found, err := s.PaperlessImportStatus(ctx, baseURL, 1)
	if err != nil {
		t.Fatalf("PaperlessImportStatus: %v", err)
	}
	if found {
		t.Fatal("expected found=false before any upsert")
	}

	// Record a failure.
	if err := s.UpsertPaperlessImportState(ctx, baseURL, 1, "failed", "download timeout"); err != nil {
		t.Fatalf("UpsertPaperlessImportState: %v", err)
	}
	status, found, err := s.PaperlessImportStatus(ctx, baseURL, 1)
	if err != nil {
		t.Fatalf("PaperlessImportStatus: %v", err)
	}
	if !found || status != "failed" {
		t.Fatalf("status = %q, found = %v, want failed/true", status, found)
	}

	// A retry overwrites the previous status (resumability).
	if err := s.UpsertPaperlessImportState(ctx, baseURL, 1, "imported", ""); err != nil {
		t.Fatalf("UpsertPaperlessImportState retry: %v", err)
	}
	status, found, err = s.PaperlessImportStatus(ctx, baseURL, 1)
	if err != nil {
		t.Fatalf("PaperlessImportStatus: %v", err)
	}
	if !found || status != "imported" {
		t.Fatalf("status = %q, found = %v, want imported/true", status, found)
	}

	// A different base URL is tracked independently.
	if err := s.UpsertPaperlessImportState(ctx, "https://other.local", 1, "failed", "auth error"); err != nil {
		t.Fatalf("UpsertPaperlessImportState other base: %v", err)
	}
	status, _, err = s.PaperlessImportStatus(ctx, baseURL, 1)
	if err != nil {
		t.Fatalf("PaperlessImportStatus: %v", err)
	}
	if status != "imported" {
		t.Fatalf("status for original base URL changed unexpectedly: %q", status)
	}

	if err := s.UpsertPaperlessImportState(ctx, baseURL, 2, "failed", "ocr error"); err != nil {
		t.Fatalf("UpsertPaperlessImportState doc 2: %v", err)
	}

	// Invalid status is rejected.
	if err := s.UpsertPaperlessImportState(ctx, baseURL, 3, "bogus", ""); err == nil {
		t.Fatal("expected error for invalid status")
	}

	all, err := s.ListPaperlessImportState(ctx, baseURL, "")
	if err != nil {
		t.Fatalf("ListPaperlessImportState: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 states for %s, got %d", baseURL, len(all))
	}
	if all[0].PaperlessDocumentID != 1 || all[1].PaperlessDocumentID != 2 {
		t.Fatalf("expected ordering by document ID, got %+v", all)
	}

	failedOnly, err := s.ListPaperlessImportState(ctx, baseURL, "failed")
	if err != nil {
		t.Fatalf("ListPaperlessImportState filtered: %v", err)
	}
	if len(failedOnly) != 1 || failedOnly[0].PaperlessDocumentID != 2 || failedOnly[0].LastError != "ocr error" {
		t.Fatalf("expected only document 2 with failed status, got %+v", failedOnly)
	}
}

func TestPaperlessImportState_TargetsAreIndependent(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	ctx := context.Background()

	const baseURL = "https://paperless.local"
	vaultA := filepath.Join(dir, "vault-a")
	vaultB := filepath.Join(dir, "vault-b")
	archive := filepath.Join(dir, "archive")

	if err := s.UpsertPaperlessImportStateForTarget(ctx, baseURL, vaultA, archive, 42, "imported", "", filepath.Join(vaultA, "doc.md"), filepath.Join(archive, "a.txt"), "sha-a"); err != nil {
		t.Fatalf("upsert target A: %v", err)
	}
	if err := s.UpsertPaperlessImportStateForTarget(ctx, baseURL, vaultB, archive, 42, "failed", "ocr failed", "", "", ""); err != nil {
		t.Fatalf("upsert target B: %v", err)
	}

	statusA, foundA, err := s.PaperlessImportStatusForTarget(ctx, baseURL, vaultA, archive, 42)
	if err != nil {
		t.Fatalf("status target A: %v", err)
	}
	if !foundA || statusA != "imported" {
		t.Fatalf("target A status = %q found=%v, want imported/true", statusA, foundA)
	}
	statusB, foundB, err := s.PaperlessImportStatusForTarget(ctx, baseURL, vaultB, archive, 42)
	if err != nil {
		t.Fatalf("status target B: %v", err)
	}
	if !foundB || statusB != "failed" {
		t.Fatalf("target B status = %q found=%v, want failed/true", statusB, foundB)
	}
	if status, found, err := s.PaperlessImportStatusForTarget(ctx, baseURL, filepath.Join(dir, "other"), archive, 42); err != nil || found || status != "" {
		t.Fatalf("other target status = %q found=%v err=%v, want not found", status, found, err)
	}

	stateA, err := s.PaperlessImportStateForTarget(ctx, baseURL, vaultA, archive, 42)
	if err != nil {
		t.Fatalf("state target A: %v", err)
	}
	if stateA.VaultPath != filepath.Join(vaultA, "doc.md") || stateA.SHA256 != "sha-a" {
		t.Fatalf("unexpected target A state: %+v", stateA)
	}

	all, err := s.ListPaperlessImportState(ctx, baseURL, "")
	if err != nil {
		t.Fatalf("ListPaperlessImportState: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 target-specific rows, got %d: %+v", len(all), all)
	}
}

func TestEnqueueSkippedJob(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	doc, _, err := s.CreateOrGet(ctx, "/tmp/a.pdf", "hash1", "application/pdf")
	if err != nil {
		t.Fatalf("CreateOrGet: %v", err)
	}

	job, err := s.EnqueueSkippedJob(ctx, doc.ID, "ingest", "unsupported mime type")
	if err != nil {
		t.Fatalf("EnqueueSkippedJob: %v", err)
	}
	if job.Status != "skipped" {
		t.Fatalf("status = %q, want skipped", job.Status)
	}
	if job.LastError == nil || *job.LastError != "unsupported mime type" {
		t.Fatalf("last_error = %v, want 'unsupported mime type'", job.LastError)
	}
	if job.DocumentID != doc.ID {
		t.Fatalf("document_id = %d, want %d", job.DocumentID, doc.ID)
	}
	if job.Kind != "ingest" {
		t.Fatalf("kind = %q, want ingest", job.Kind)
	}

	jobs, err := s.ListJobs(ctx, 0)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	found := false
	for _, j := range jobs {
		if j.ID == job.ID {
			found = true
			if j.Status != "skipped" {
				t.Fatalf("listed job status = %q, want skipped", j.Status)
			}
		}
	}
	if !found {
		t.Fatal("skipped job not found in ListJobs")
	}
}

func TestResetRunningJobs(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	doc, _, err := s.CreateOrGet(ctx, "/tmp/a.pdf", "hash-reset", "application/pdf")
	if err != nil {
		t.Fatalf("CreateOrGet: %v", err)
	}

	job, err := s.EnqueueJob(ctx, doc.ID, "ingest")
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	claimed, err := s.ClaimJobByID(ctx, job.ID)
	if err != nil {
		t.Fatalf("ClaimJobByID: %v", err)
	}
	if claimed.Status != "running" {
		t.Fatalf("claimed status = %q, want running", claimed.Status)
	}

	if err := s.ResetRunningJobs(ctx); err != nil {
		t.Fatalf("ResetRunningJobs: %v", err)
	}

	jobs, err := s.ListJobs(ctx, 0)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	for _, j := range jobs {
		if j.ID == job.ID {
			if j.Status != "pending" {
				t.Fatalf("after reset, job status = %q, want pending", j.Status)
			}
			return
		}
	}
	t.Fatal("job not found after reset")
}

func TestRecordAndListExtractions(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	doc, _, err := s.CreateOrGet(ctx, "/tmp/invoice.pdf", "hash-ext", "application/pdf")
	if err != nil {
		t.Fatalf("CreateOrGet: %v", err)
	}

	extractions := []annotate.Extraction{
		{Field: "date", Type: "date", Value: "2026-03-12", Span: &annotate.Span{Start: 10, End: 20, Snippet: "2026-03-12"}, Matched: true},
		{Field: "amount", Type: "amount", Value: "$284.50"},
	}

	if err := s.RecordExtractions(ctx, doc.ID, "invoice", extractions); err != nil {
		t.Fatalf("RecordExtractions: %v", err)
	}

	got, err := s.ListExtractions(ctx, doc.ID)
	if err != nil {
		t.Fatalf("ListExtractions: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}

	if got[0].Field != "date" || got[0].Value != "2026-03-12" {
		t.Fatalf("extraction[0] = %+v, want date/2026-03-12", got[0])
	}
	if got[0].Span == nil {
		t.Fatal("extraction[0].Span is nil, want non-nil")
	}
	if got[0].Span.Start != 10 || got[0].Span.End != 20 {
		t.Fatalf("span = %+v, want 10:20", got[0].Span)
	}
	if !got[0].Matched {
		t.Fatal("extraction[0].Matched should be true")
	}

	if got[1].Field != "amount" || got[1].Value != "$284.50" {
		t.Fatalf("extraction[1] = %+v, want amount/$284.50", got[1])
	}
	if got[1].Span != nil {
		t.Fatalf("extraction[1].Span = %+v, want nil", got[1].Span)
	}
}

func TestListExtractions_Empty(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	doc, _, err := s.CreateOrGet(ctx, "/tmp/empty.pdf", "hash-empty", "application/pdf")
	if err != nil {
		t.Fatalf("CreateOrGet: %v", err)
	}

	got, err := s.ListExtractions(ctx, doc.ID)
	if err != nil {
		t.Fatalf("ListExtractions: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("len = %d, want 0", len(got))
	}
}

func TestPaperlessImportStateByID(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	baseURL := "https://paperless.example"

	if err := s.UpsertPaperlessImportState(ctx, baseURL, 42, "imported", ""); err != nil {
		t.Fatalf("UpsertPaperlessImportState: %v", err)
	}

	state, err := s.PaperlessImportStateByID(ctx, baseURL, 42)
	if err != nil {
		t.Fatalf("PaperlessImportStateByID: %v", err)
	}
	if state == nil {
		t.Fatal("state is nil")
	}
	if state.PaperlessDocumentID != 42 {
		t.Errorf("PaperlessDocumentID = %d, want 42", state.PaperlessDocumentID)
	}
	if state.Status != "imported" {
		t.Errorf("Status = %q, want imported", state.Status)
	}
	if state.BaseURL != baseURL {
		t.Errorf("BaseURL = %q, want %q", state.BaseURL, baseURL)
	}
}

func TestPaperlessImportStateByID_NotFound(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	state, err := s.PaperlessImportStateByID(ctx, "https://paperless.example", 999)
	if err == nil {
		t.Fatal("expected error for non-existent ID")
	}
	if state != nil {
		t.Errorf("state = %+v, want nil for non-existent ID", state)
	}
}

func TestStore_MailPollCursor(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	ctx := context.Background()

	// 1. Get cursor for non-existent account should return nil, nil
	c, err := s.GetMailPollCursor(ctx, "account-1")
	if err != nil {
		t.Fatalf("GetMailPollCursor empty: %v", err)
	}
	if c != nil {
		t.Fatalf("expected nil cursor, got %+v", c)
	}

	// 2. Set cursor
	err = s.SetMailPollCursor(ctx, "account-1", "INBOX", 12345, 100)
	if err != nil {
		t.Fatalf("SetMailPollCursor: %v", err)
	}

	// 3. Get cursor and verify
	c, err = s.GetMailPollCursor(ctx, "account-1")
	if err != nil {
		t.Fatalf("GetMailPollCursor: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil cursor")
	}
	if c.AccountID != "account-1" || c.Folder != "INBOX" || c.UIDValidity != 12345 || c.LastUID != 100 {
		t.Errorf("GetMailPollCursor returned unexpected values: %+v", c)
	}

	// 4. Update cursor (tests upsert/ON CONFLICT conflict path)
	err = s.SetMailPollCursor(ctx, "account-1", "Archive", 12345, 105)
	if err != nil {
		t.Fatalf("SetMailPollCursor update: %v", err)
	}

	// 5. Get cursor and verify updated values
	c, err = s.GetMailPollCursor(ctx, "account-1")
	if err != nil {
		t.Fatalf("GetMailPollCursor updated: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil cursor")
	}
	if c.AccountID != "account-1" || c.Folder != "Archive" || c.UIDValidity != 12345 || c.LastUID != 105 {
		t.Errorf("GetMailPollCursor updated returned unexpected values: %+v", c)
	}
}

