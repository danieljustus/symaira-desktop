package main

import (
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

// Preserve a bounded first difference instead of hiding the failing field behind
// "fixture stale". This is diagnostic only: exact byte comparison stays strict.
func portProcessFixtureDifference(t *testing.T, message string, expected, actual []byte) {
	t.Helper()
	offset := 0
	for offset < len(expected) && offset < len(actual) && expected[offset] == actual[offset] {
		offset++
	}
	from := max(offset-160, 0)
	t.Fatalf("%s; first difference at byte %d\ncommitted: %q\nobserved: %q", message, offset,
		expected[from:min(offset+240, len(expected))], actual[from:min(offset+240, len(actual))])
}

func TestNormalizeConflictWindowsSlogPaths(t *testing.T) {
	// Produce the native Go slog quoting on this host from actual Windows-shaped
	// paths, rather than hand-writing a plausible log. Native execution is still
	// required to establish the Windows replay result.
	root := `C:\Users\runner\Temp\fixture`
	var output strings.Builder
	logger := slog.New(slog.NewTextHandler(&output, nil))
	logger.Info("seeded per-vault retrieval index from standalone store",
		"standalone_path", root+`\home\data\symdesk\retrieval.db`,
		"new_path", root+`\home\data\symdesk\vaults\0123456789abcdef\retrieval.db`)
	got := normalizeConflictLog(normalizeRetryText(normalizeConflictLog(output.String()), root))
	want := "time=$NOW level=INFO msg=\"seeded per-vault retrieval index from standalone store\" standalone_path=$ROOT/home/data/symdesk/retrieval.db new_path=$ROOT/home/data/symdesk/vaults/$VAULT_ID/retrieval.db\n"
	if got != want {
		t.Fatalf("Windows slog normalization: got %q, want %q", got, want)
	}
	for _, text := range []string{`unrelated path="C:\\keep\\literal"`, fmt.Sprintf("error %q", `bad\ninput`)} {
		if got := normalizeConflictLog(text); got != text {
			t.Fatalf("unrelated diagnostic changed: %q", got)
		}
	}
}
