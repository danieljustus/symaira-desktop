package main

import (
	"testing"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/service"
)

// Near-duplicate detection has one definition, not several. The `duplicates`,
// `similar` and `vault health` flags and the SimilarAll fallback all have to
// resolve to the same percentage, otherwise the same vault answers "which
// documents are near-identical" differently depending on the entry point
// used (issue #452).
func TestDuplicateThresholdDefaultsAgree(t *testing.T) {
	want := service.DefaultDuplicateThreshold

	rootCmd := &cobra.Command{Use: "test"}
	registerCommands(rootCmd)

	cases := []struct {
		path []string
		flag string
	}{
		{[]string{"duplicates"}, "threshold"},
		{[]string{"similar"}, "threshold"},
		{[]string{"vault", "health"}, "duplicate-threshold"},
	}
	for _, tc := range cases {
		cmd, _, err := rootCmd.Find(tc.path)
		if err != nil {
			t.Fatalf("command %v not found: %v", tc.path, err)
		}
		got, err := cmd.Flags().GetInt(tc.flag)
		if err != nil {
			t.Fatalf("%v --%s: %v", tc.path, tc.flag, err)
		}
		if got != want {
			t.Errorf("%v --%s default = %d, want %d", tc.path, tc.flag, got, want)
		}
	}

	if got := service.ResolveDuplicateThreshold(0); got != want {
		t.Errorf("SimilarAll fallback = %d, want %d", got, want)
	}
}
