package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-ingest/internal/store"
)

func withCapturedStdout(t *testing.T) *strings.Builder {
	t.Helper()
	var sb strings.Builder
	old := stdout
	stdout = &sb
	t.Cleanup(func() { stdout = old })
	return &sb
}

func TestRun_Ingest_HappyPath(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")
	vault := t.TempDir()
	source := filepath.Join(t.TempDir(), "doc.txt")
	if err := os.WriteFile(source, []byte("hello world"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	sb := withCapturedStdout(t)

	err := run([]string{"ingest", "-db", tempDB, "-vault", vault, "-archive", t.TempDir(), source})
	if err != nil {
		t.Fatalf("run(ingest): %v", err)
	}

	out := sb.String()
	if !strings.Contains(out, "ingested: "+source) {
		t.Errorf("output missing ingested line, got %q", out)
	}
	if !strings.Contains(out, "engine: text") {
		t.Errorf("output missing engine line, got %q", out)
	}
}

func TestRun_Ingest_MissingVault(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")
	source := filepath.Join(t.TempDir(), "doc.txt")
	if err := os.WriteFile(source, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	withCapturedStdout(t)

	err := run([]string{"ingest", "-db", tempDB, "-vault", "", source})
	if err == nil {
		t.Fatal("expected error for missing vault")
	}
	if !strings.Contains(err.Error(), "no vault configured") {
		t.Errorf("error = %v, want mention of 'no vault configured'", err)
	}
}

func TestRun_Ingest_DuplicateMessage(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")
	vault := t.TempDir()
	archive := t.TempDir()
	source := filepath.Join(t.TempDir(), "doc.txt")
	if err := os.WriteFile(source, []byte("duplicate me"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	sb := withCapturedStdout(t)

	if err := run([]string{"ingest", "-db", tempDB, "-vault", vault, "-archive", archive, source}); err != nil {
		t.Fatalf("first ingest: %v", err)
	}

	sb.Reset()
	if err := run([]string{"ingest", "-db", tempDB, "-vault", vault, "-archive", archive, source}); err != nil {
		t.Fatalf("second ingest: %v", err)
	}

	out := sb.String()
	if !strings.Contains(out, "already ingested: "+source) {
		t.Errorf("output missing duplicate message, got %q", out)
	}
	if !strings.Contains(out, "existing vault path:") {
		t.Errorf("output missing existing vault path, got %q", out)
	}
}

func TestRun_Retry_HappyPath(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")
	vault := t.TempDir()
	archive := t.TempDir()
	source := filepath.Join(t.TempDir(), "doc.txt")
	if err := os.WriteFile(source, []byte("retry me"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	withCapturedStdout(t)
	if err := run([]string{"ingest", "-db", tempDB, "-vault", vault, "-archive", archive, source}); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	st, err := store.Open(tempDB)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	jobs, err := st.ListJobs(context.Background(), 0)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	if len(jobs) == 0 {
		t.Fatal("expected at least one job after ingest")
	}

	sb := withCapturedStdout(t)
	jobIDStr := strconv.FormatInt(jobs[0].ID, 10)
	if err := run([]string{"retry", "-db", tempDB, jobIDStr}); err != nil {
		t.Fatalf("run(retry): %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "status set to pending") {
		t.Errorf("output missing pending message, got %q", out)
	}
}

func TestRun_Retry_InvalidJobID(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")
	withCapturedStdout(t)

	err := run([]string{"retry", "-db", tempDB, "not-a-number"})
	if err == nil {
		t.Fatal("expected error for invalid job ID")
	}
	if !strings.Contains(err.Error(), "invalid job ID") {
		t.Errorf("error = %v, want mention of 'invalid job ID'", err)
	}
}

func TestRun_Retry_UnknownJobID(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")
	withCapturedStdout(t)

	err := run([]string{"retry", "-db", tempDB, "999999"})
	if err == nil {
		t.Fatal("expected error for unknown job ID")
	}
	if !strings.Contains(err.Error(), "failed to retry job") {
		t.Errorf("error = %v, want mention of 'failed to retry job'", err)
	}
}

func TestRun_Rules_Usage(t *testing.T) {
	sb := withCapturedStdout(t)
	if err := run([]string{"rules"}); err != nil {
		t.Fatalf("run(rules): %v", err)
	}
	out := sb.String()
	for _, want := range []string{"Usage: symingest rules", "list", "add <pattern>", "delete <id>"} {
		if !strings.Contains(out, want) {
			t.Errorf("usage output missing %q, got %q", want, out)
		}
	}
}

func TestRun_Rules_UnknownSubcommand(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")
	withCapturedStdout(t)

	err := run([]string{"rules", "-db", tempDB, "bogus"})
	if err == nil {
		t.Fatal("expected error for unknown rules subcommand")
	}
	if !strings.Contains(err.Error(), "unknown rules subcommand") {
		t.Errorf("error = %v, want mention of 'unknown rules subcommand'", err)
	}
}

func TestRun_Rules_ListEmpty(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")
	sb := withCapturedStdout(t)

	if err := run([]string{"rules", "-db", tempDB, "list"}); err != nil {
		t.Fatalf("run(rules list): %v", err)
	}
	if got := strings.TrimSpace(sb.String()); got != "No classification rules defined." {
		t.Fatalf("expected no-rules message, got %q", got)
	}
}

func TestRun_Rules_AddListDelete(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")

	sb := withCapturedStdout(t)
	if err := run([]string{"rules", "-db", tempDB, "add", "*.pdf", "category", "Invoices"}); err != nil {
		t.Fatalf("run(rules add): %v", err)
	}
	addOut := sb.String()
	if !strings.Contains(addOut, "Added classification rule") {
		t.Errorf("add output missing confirmation, got %q", addOut)
	}

	sb.Reset()
	if err := run([]string{"rules", "-db", tempDB, "list"}); err != nil {
		t.Fatalf("run(rules list): %v", err)
	}
	listOut := sb.String()
	if !strings.Contains(listOut, "*.pdf") || !strings.Contains(listOut, "Invoices") {
		t.Errorf("list output missing rule, got %q", listOut)
	}

	st, err := store.Open(tempDB)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	rules, err := st.ListRules(context.Background())
	if err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}

	sb.Reset()
	if err := run([]string{"rules", "-db", tempDB, "delete", strconv.FormatInt(rules[0].ID, 10)}); err != nil {
		t.Fatalf("run(rules delete): %v", err)
	}
	deleteOut := sb.String()
	if !strings.Contains(deleteOut, "Deleted classification rule") {
		t.Errorf("delete output missing confirmation, got %q", deleteOut)
	}
}

func TestRun_Rules_AddMissingArgs(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")
	withCapturedStdout(t)

	err := run([]string{"rules", "-db", tempDB, "add", "*.pdf"})
	if err == nil {
		t.Fatal("expected error for missing add arguments")
	}
	if !strings.Contains(err.Error(), "missing arguments") {
		t.Errorf("error = %v, want mention of 'missing arguments'", err)
	}
}

func TestRun_Rules_DeleteMissingArgs(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")
	withCapturedStdout(t)

	err := run([]string{"rules", "-db", tempDB, "delete"})
	if err == nil {
		t.Fatal("expected error for missing delete argument")
	}
	if !strings.Contains(err.Error(), "missing rule ID") {
		t.Errorf("error = %v, want mention of 'missing rule ID'", err)
	}
}

func TestRun_Rules_DeleteInvalidID(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.db")
	withCapturedStdout(t)

	err := run([]string{"rules", "-db", tempDB, "delete", "not-a-number"})
	if err == nil {
		t.Fatal("expected error for invalid delete ID")
	}
	if !strings.Contains(err.Error(), "invalid rule ID") {
		t.Errorf("error = %v, want mention of 'invalid rule ID'", err)
	}
}
