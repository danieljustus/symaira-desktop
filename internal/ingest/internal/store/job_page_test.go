package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestListJobsPageScopesVaultAndReportsTotal(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	vaultA := filepath.Join(t.TempDir(), "vault-a")
	vaultB := filepath.Join(t.TempDir(), "vault-b")
	docA, _, err := store.CreateOrGet(ctx, "/tmp/a.pdf", "hash-a", "application/pdf", vaultA)
	if err != nil {
		t.Fatal(err)
	}
	docB, _, err := store.CreateOrGet(ctx, "/tmp/b.pdf", "hash-b", "application/pdf", vaultB)
	if err != nil {
		t.Fatal(err)
	}
	jobA, err := store.EnqueueJob(ctx, docA.ID, "pdf")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnqueueJob(ctx, docB.ID, "pdf"); err != nil {
		t.Fatal(err)
	}

	jobs, total, err := store.ListJobsPage(ctx, vaultA, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(jobs) != 1 || jobs[0].ID != jobA.ID {
		t.Fatalf("vault A jobs = %+v, total = %d; want only job %d", jobs, total, jobA.ID)
	}
	if jobs[0].CreatedAt == "" || jobs[0].UpdatedAt == "" {
		t.Fatalf("job timestamps are empty: %+v", jobs[0])
	}

	jobs, total, err = store.ListJobsPage(ctx, vaultB, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(jobs) != 1 || jobs[0].ID == jobA.ID {
		t.Fatalf("vault B jobs = %+v, total = %d; want only vault B job", jobs, total)
	}

	jobs, total, err = store.ListJobsPage(ctx, vaultA, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(jobs) != 0 {
		t.Fatalf("past-end page = %+v, total = %d; want empty page with total 1", jobs, total)
	}
}
