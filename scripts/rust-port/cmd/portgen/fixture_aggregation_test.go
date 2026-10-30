package main

import (
	"errors"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

func TestFixtureChecksAggregateFailuresAndContinue(t *testing.T) {
	original := runFixtureCheckTarget
	t.Cleanup(func() { runFixtureCheckTarget = original })
	root := t.TempDir()
	for _, pair := range renderFixtureMirrors {
		writePortgenTestFile(t, root, pair.source, "{}\n")
		writePortgenTestFile(t, root, pair.mirror, "{}\n")
	}
	var expected []string
	for _, target := range append(append([]fixtureCheckTarget(nil), fixtureTestTargets...), fixtureGeneratorTargets...) {
		if target.name == "native Windows config paths" && runtime.GOOS != "windows" {
			continue
		}
		expected = append(expected, target.name)
	}
	first, last := errors.New("first drift diagnostic"), errors.New("last drift diagnostic")
	for _, fail := range []bool{true, false} {
		var calls []string
		runFixtureCheckTarget = func(_, _ string, _ []string, target fixtureCheckTarget) error {
			calls = append(calls, target.name)
			if fail && target.name == expected[0] {
				return first
			}
			if fail && target.name == expected[len(expected)-1] {
				return last
			}
			return nil
		}
		err := runFixtureChecks(root, inventory.Oracle{})
		if !reflect.DeepEqual(calls, expected) {
			t.Fatalf("executed targets = %v, want %v", calls, expected)
		}
		if !fail {
			if err != nil {
				t.Fatal(err)
			}
			continue
		}
		if !errors.Is(err, first) || !errors.Is(err, last) {
			t.Fatalf("did not preserve both errors: %v", err)
		}
		for _, text := range []string{"2 fixture checks failed", expected[0], expected[len(expected)-1], first.Error(), last.Error()} {
			if !strings.Contains(err.Error(), text) {
				t.Fatalf("missing aggregate diagnostic %q: %v", text, err)
			}
		}
	}
}
