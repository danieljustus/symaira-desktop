package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

func TestNativeStorePathsCaptureIsReadOnlyAndRequiresFreshRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "capture")
	t.Setenv("HOME", filepath.Join(parent, "outer-home"))
	t.Setenv("USERPROFILE", filepath.Join(parent, "outer-profile"))
	t.Setenv("SYMRELATE_DATA_HOME", filepath.Join(parent, "outer-data"))
	oracle := inventory.Oracle{Commit: "unit-fixture-only", Release: "unit-fixture-only"}
	captured, err := buildStorePaths(oracle, root)
	if err != nil {
		t.Fatal(err)
	}
	if !captured.Complete || len(captured.Cases) != 20 {
		t.Fatalf("complete=%t, capture has %d layouts, want 20", captured.Complete, len(captured.Cases))
	}
	for _, item := range captured.Cases {
		if len(item.Ingest) != 19 || !reflect.DeepEqual(item.Before, item.After) {
			t.Fatalf("incomplete or mutable layout %s", item.ID)
		}
		if strings.Contains(item.ID, "symlink") {
			links := 0
			for _, entry := range item.Before {
				if strings.HasPrefix(entry, "symlink:") {
					links++
				}
			}
			if links != 3 {
				t.Fatalf("layout %s lost native link identity: %d links, want 3", item.ID, links)
			}
		}
	}
	if os.Getenv("HOME") != filepath.Join(parent, "outer-home") || os.Getenv("USERPROFILE") != filepath.Join(parent, "outer-profile") || os.Getenv("SYMRELATE_DATA_HOME") != filepath.Join(parent, "outer-data") {
		t.Fatal("capture leaked an isolated environment into the caller")
	}
	before, err := storePathSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := buildStorePaths(oracle, root); err == nil {
		t.Fatal("existing root must be rejected, never overwritten")
	}
	after, err := storePathSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("rejected capture changed the existing root")
	}
}

func TestNativeStorePathsCaptureFailureRetainsNestedPartial(t *testing.T) {
	if root := os.Getenv("SYMDESK_CONFIGGEN_CAPTURE_TEST_ROOT"); root != "" {
		// Fault injection stays in this bounded child; actual Go observations remain intact.
		nativeStorePaths = func(oracle inventory.Oracle, root string) (storePathDocument, error) {
			value, err := buildStorePaths(oracle, root)
			if err != nil {
				return value, err
			}
			value.Complete = false
			value.Cases = value.Cases[:1]
			return value, errors.New("injected capture failure")
		}
		flag.CommandLine = flag.NewFlagSet("configgen", flag.ExitOnError)
		os.Args = []string{"configgen", "--store-paths-root", root, "--output", os.Getenv("SYMDESK_CONFIGGEN_CAPTURE_TEST_OUTPUT")}
		main()
		t.Fatal("failed capture unexpectedly returned")
	}
	root := filepath.Join(t.TempDir(), "capture")
	output := filepath.Join(root, "nested", "observations", "partial.json")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var original []byte
	for attempt, reason := range []string{"injected capture failure", "capture root must be fresh"} {
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		//nolint:gosec // Current test executable only; no caller-supplied binary, bounded child.
		command := exec.CommandContext(ctx, binary, "-test.run=^TestNativeStorePathsCaptureFailureRetainsNestedPartial$")
		command.Env = append(os.Environ(), "SYMDESK_CONFIGGEN_CAPTURE_TEST_ROOT="+root, "SYMDESK_CONFIGGEN_CAPTURE_TEST_OUTPUT="+output)
		message, runErr := command.CombinedOutput()
		cancel()
		var exit *exec.ExitError
		if !errors.As(runErr, &exit) || exit.ExitCode() != 1 || !strings.Contains(string(message), reason) {
			t.Fatalf("capture failure %d: exit=%v, output=%s", attempt, runErr, message)
		}
		//nolint:gosec // Fixed output inside this test's fresh private capture root.
		raw, err := os.ReadFile(output)
		if err != nil {
			t.Fatalf("partial observations were lost: %v", err)
		}
		var retained storePathDocument
		if err := json.Unmarshal(raw, &retained); err != nil || retained.Complete || len(retained.Cases) != 1 {
			t.Fatalf("partial capture must retain one genuine case and complete=false: %v", err)
		}
		if attempt == 0 {
			original = raw
		} else if !reflect.DeepEqual(raw, original) {
			t.Fatal("rejected existing root overwrote its retained partial observations")
		}
	}
}
