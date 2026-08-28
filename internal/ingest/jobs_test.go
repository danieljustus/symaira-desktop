package ingest

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// stubJobs points the job-queue seams at scripted doubles for one test.
func stubJobs(t *testing.T,
	list func() ([]Job, error),
	retry func(int64) error,
) {
	t.Helper()
	originalList, originalRetry := JobsFunc, RetryJobFunc
	t.Cleanup(func() { JobsFunc, RetryJobFunc = originalList, originalRetry })

	if list != nil {
		JobsFunc = func(context.Context, Options, int) ([]Job, error) { return list() }
	}
	if retry != nil {
		RetryJobFunc = func(_ context.Context, _ Options, id int64) error { return retry(id) }
	}
}

func TestIngestJobsPipelineFailure(t *testing.T) {
	stubJobs(t, func() ([]Job, error) { return nil, errors.New("store unavailable") }, nil)

	jobs, err := IngestJobs()
	if err == nil {
		t.Fatal("expected an error when the job queue cannot be read")
	}
	if jobs != "[]" {
		t.Errorf("expected empty JSON array fallback, got %q", jobs)
	}
}

func TestIngestJobsSuccess(t *testing.T) {
	stubJobs(t, func() ([]Job, error) {
		return []Job{{ID: 1, Status: "done", Kind: "pdf", SourcePath: "/tmp/a.pdf"}}, nil
	}, nil)

	jobs, err := IngestJobs()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(jobs, `"id": 1`) || !strings.Contains(jobs, `"status": "done"`) {
		t.Errorf("expected job list JSON, got %q", jobs)
	}
}

// An empty queue must serialize as [], never as JSON null: the MCP tool and
// the Swift client both decode an array.
func TestIngestJobsEmptyQueueIsEmptyArray(t *testing.T) {
	stubJobs(t, func() ([]Job, error) { return nil, nil }, nil)

	jobs, err := IngestJobs()
	if err != nil {
		t.Fatal(err)
	}
	if jobs != "[]" {
		t.Errorf("expected [], got %q", jobs)
	}
}

func TestIngestRetrySuccessAndFailure(t *testing.T) {
	stubJobs(t, nil, func(id int64) error {
		if id == 7 {
			return nil
		}
		return errors.New("no such job")
	})

	if err := IngestRetry("7"); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if err := IngestRetry("8"); err == nil {
		t.Fatal("expected an error for a failing retry")
	}
}

// Job IDs are numeric in the store; a non-numeric ID must be rejected before
// the pipeline is touched.
func TestIngestRetryRejectsNonNumericID(t *testing.T) {
	stubJobs(t, nil, func(int64) error {
		t.Fatal("retry seam must not be reached for a non-numeric job ID")
		return nil
	})

	err := IngestRetry("job-1")
	if err == nil || !strings.Contains(err.Error(), "invalid job ID") {
		t.Fatalf("expected an invalid-job-ID error, got %v", err)
	}
}

func TestIngestJobsForVaultPassesScopeAndLimit(t *testing.T) {
	var got Options
	var gotLimit int
	stubJobs(t, func() ([]Job, error) { return []Job{{ID: 2, SourcePath: "/vault-b/b.pdf"}}, nil }, nil)
	original := JobsFunc
	JobsFunc = func(_ context.Context, opts Options, limit int) ([]Job, error) {
		got, gotLimit = opts, limit
		return original(context.Background(), opts, limit)
	}

	jobs, err := IngestJobsForVault("/vault-b", 25)
	if err != nil {
		t.Fatal(err)
	}
	if got.Vault != "/vault-b" || gotLimit != 25 {
		t.Fatalf("JobsFunc received opts=%+v limit=%d", got, gotLimit)
	}
	if !strings.Contains(jobs, `"source_path": "/vault-b/b.pdf"`) {
		t.Fatalf("expected scoped job in JSON, got %s", jobs)
	}
}
