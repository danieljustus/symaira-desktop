package main

import (
	"reflect"
	"runtime"
	"testing"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

func TestWindowsConfigFixtureRegistrationAndNativeReplay(t *testing.T) {
	if err := validateFixtureCheckCoverage(); err != nil {
		t.Fatal(err)
	}
	registered := 0
	for _, target := range fixtureGeneratorTargets {
		if target.name == "native Windows config paths" {
			registered++
			want := []string{"run", "./scripts/rust-port/cmd/windows-config-paths-gen", "-check", "testdata/port/config/windows-verbatim-paths.json"}
			if !reflect.DeepEqual(target.args, want) || !reflect.DeepEqual(target.outputs, want[3:]) {
				t.Fatalf("native check does not replay the registered capture: %#v", target)
			}
		}
	}
	if registered != 1 {
		t.Fatalf("native Windows fixture targets = %d, want 1", registered)
	}
	original := runFixtureCheckTarget
	t.Cleanup(func() { runFixtureCheckTarget = original })
	calls := 0
	runFixtureCheckTarget = func(_ string, _ string, _ []string, target fixtureCheckTarget) error {
		if target.name == "native Windows config paths" {
			calls++
		}
		return nil
	}
	if err := runFixtureChecks(t.TempDir(), inventory.Oracle{}); err != nil {
		t.Fatal(err)
	}
	want := 0
	if runtime.GOOS == "windows" {
		want = 1
	}
	if calls != want {
		t.Fatalf("native Windows replay calls = %d, want %d on %s", calls, want, runtime.GOOS)
	}
}
