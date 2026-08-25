package main

import (
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/service"
	"github.com/spf13/cobra"
)

func TestFormatDryRun(t *testing.T) {
	if got := formatDryRun(true); got != "(dry-run)" {
		t.Errorf("formatDryRun(true) = %q, want %q", got, "(dry-run)")
	}
	if got := formatDryRun(false); got != "" {
		t.Errorf("formatDryRun(false) = %q, want empty", got)
	}
}

func TestOutputTagResultsHumanReadable(t *testing.T) {
	jsonFlag = false
	defer func() { jsonFlag = false }()

	results := []service.TagRenameResult{
		{File: "a.md", Status: "updated"},
		{File: "b.md", Status: "updated"},
		{File: "c.md", Status: "skipped"},
		{File: "d.md", Status: "error", Error: "read-only"},
	}
	if err := outputTagResults(&cobra.Command{}, results); err != nil {
		t.Fatalf("outputTagResults returned error: %v", err)
	}
}

func TestOutputTagResultsJSONMode(t *testing.T) {
	jsonFlag = true
	defer func() { jsonFlag = false }()

	results := []service.TagRenameResult{{File: "a.md", Status: "updated"}}
	if err := outputTagResults(&cobra.Command{}, results); err != nil {
		t.Fatalf("outputTagResults (json) returned error: %v", err)
	}
}
