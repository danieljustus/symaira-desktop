package ingest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mockSymingestBinary(t *testing.T, extra string) {
	t.Helper()
	tempDir := t.TempDir()
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"version\" ]; then\n" +
		"	echo '{\"schema_version\": 1, \"version\": \"0.7.0\"}'\n" +
		"	exit 0\n" +
		"fi\n" +
		extra +
		"exit 1\n"
	if err := os.WriteFile(filepath.Join(tempDir, "symingest"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tempDir+":"+os.Getenv("PATH"))
}

func TestIngestJobsWithoutSymingest(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")

	jobs, err := IngestJobs()
	if err == nil {
		t.Fatal("expected an error when symingest is not installed")
	}
	if jobs != "[]" {
		t.Errorf("expected empty JSON array fallback, got %q", jobs)
	}
}

func TestIngestJobsSuccess(t *testing.T) {
	mockSymingestBinary(t, "if [ \"$1\" = \"jobs\" ] && [ \"$2\" = \"--json\" ]; then\n"+
		"	echo '[{\"id\":\"1\",\"status\":\"done\"}]'\n"+
		"	exit 0\n"+
		"fi\n")

	jobs, err := IngestJobs()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(jobs, `"id":"1"`) {
		t.Errorf("expected job list JSON, got %q", jobs)
	}
}

func TestIngestJobsCommandFailure(t *testing.T) {
	mockSymingestBinary(t, "if [ \"$1\" = \"jobs\" ]; then\n"+
		"	exit 1\n"+
		"fi\n")

	jobs, err := IngestJobs()
	if err == nil {
		t.Fatal("expected an error when the symingest command fails")
	}
	if jobs != "[]" {
		t.Errorf("expected empty JSON array fallback on command failure, got %q", jobs)
	}
}

func TestIngestRetryWithoutSymingest(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")

	if err := IngestRetry("job-1"); err == nil {
		t.Fatal("expected an error when symingest is not installed")
	}
}

func TestIngestRetrySuccessAndFailure(t *testing.T) {
	mockSymingestBinary(t, "if [ \"$1\" = \"retry\" ]; then\n"+
		"	if [ \"$2\" = \"job-ok\" ]; then\n"+
		"		exit 0\n"+
		"	fi\n"+
		"	exit 1\n"+
		"fi\n")

	if err := IngestRetry("job-ok"); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if err := IngestRetry("job-bad"); err == nil {
		t.Fatal("expected an error for a failing retry")
	}
}
