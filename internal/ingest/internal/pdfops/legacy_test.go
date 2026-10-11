package pdfops

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Controlled tools test the operation boundary, not real PDF validity.
func TestLegacySplitSafety(t *testing.T) {
	for _, tc := range []struct {
		name       string
		pages      string
		boundaries []int
		mode       string
		wantErr    string
	}{
		{"success", "4", []int{1, 3}, "", ""},
		{"page-limit", "10001", []int{1}, "", "10000 pages"},
		{"part-limit", "4", make([]int, maxPages), "", "10000 parts"},
		{"invalid-count", "0", []int{1}, "", "positive page count"},
		{"duplicate", "4", []int{1, 1}, "", "boundaries must increase"},
		{"descending", "4", []int{2, 1}, "", "boundaries must increase"},
		{"zero", "4", []int{0}, "", "boundaries must increase"},
		{"outside", "4", []int{4}, "", "boundaries must increase"},
		{"late-alias", "4", []int{1}, "alias", "aliases input"},
		{"invalid-output", "4", []int{1}, "check-fails", "validate PDF output"},
		{"shared-deadline", "4", []int{1}, "slow", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, output := t.TempDir(), t.TempDir()
			input, trace := filepath.Join(dir, "input.pdf"), filepath.Join(dir, "trace")
			original := []byte("controlled original")
			if err := os.WriteFile(input, original, 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("LEGACY_TRACE", trace)
			t.Setenv("LEGACY_PAGES", tc.pages)
			t.Setenv("LEGACY_MODE", tc.mode)
			tool := writeExecutable(t, dir, "qpdf", `
if [ "$1" = --show-npages ]; then printf '%s\n' "$LEGACY_PAGES"; exit 0; fi
printf '%s\n' "$1" >> "$LEGACY_TRACE"
if [ "$LEGACY_MODE" = slow ]; then /bin/sleep 1; fi
if [ "$1" = --check ]; then
  if [ "$LEGACY_MODE" = check-fails ]; then exit 2; fi
  exit 0
fi
printf 'range=%s\n' "$4" > "$6"
`)
			if tc.mode == "alias" {
				if err := os.Link(input, filepath.Join(output, "part-2.pdf")); err != nil {
					t.Fatal(err)
				}
			}
			ctx := context.Background()
			if tc.mode == "slow" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 3500*time.Millisecond)
				defer cancel()
			}
			start := time.Now()
			parts, err := (Tools{QPDF: tool}).SplitLegacy(ctx, input, tc.boundaries, output)
			elapsed := time.Since(start)
			if tc.mode == "slow" {
				calls, readErr := os.ReadFile(trace)
				if err == nil || ctx.Err() != context.DeadlineExceeded || elapsed > 6*time.Second || readErr != nil || strings.Count(string(calls), "\n") < 3 {
					t.Fatalf("shared deadline: elapsed=%s parts=%v error=%v calls=%q read=%v", elapsed, parts, err, calls, readErr)
				}
			} else if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want %q, got %v", tc.wantErr, err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if tc.name == "success" {
				want := []string{filepath.Join(output, "part-1.pdf"), filepath.Join(output, "part-2.pdf"), filepath.Join(output, "part-3.pdf")}
				if !reflect.DeepEqual(parts, want) {
					t.Fatalf("parts=%v want=%v", parts, want)
				}
				for i, text := range []string{"range=1-1\n", "range=2-3\n", "range=4-4\n"} {
					data, err := os.ReadFile(parts[i])
					if err != nil || string(data) != text {
						t.Fatalf("part: %q %v", data, err)
					}
				}
			} else {
				if len(parts) != 0 {
					t.Fatalf("failed split returned parts: %v", parts)
				}
				entries, err := os.ReadDir(output)
				if err != nil {
					t.Fatal(err)
				}
				want := 0
				if tc.mode == "alias" {
					want = 1
				}
				if len(entries) != want {
					t.Fatalf("failed split published/leaked files: %v", entries)
				}
				if tc.mode != "slow" && tc.mode != "check-fails" {
					if _, err := os.Stat(trace); !os.IsNotExist(err) {
						t.Fatalf("preflight ran a writer: %v", err)
					}
				}
			}
			data, err := os.ReadFile(input)
			if err != nil || string(data) != string(original) {
				t.Fatalf("input changed: %q %v", data, err)
			}
		})
	}
}
