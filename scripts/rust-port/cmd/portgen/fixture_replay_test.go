package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

func TestFixtureReplayOracleArguments(t *testing.T) {
	root := newProvenanceBaseRepository(t)
	ancestor := portgenGitOutput(t, root, "rev-parse", "HEAD")
	writePortgenTestFile(t, root, "unrelated.txt", "advance\n")
	portgenGit(t, root, "add", "--", "unrelated.txt")
	portgenGit(t, root, "commit", "-q", "-m", "test: advance")
	head := portgenGitOutput(t, root, "rev-parse", "HEAD")
	writeOracle := func(rel string, oracle inventory.Oracle) {
		t.Helper()
		content, err := json.Marshal(map[string]any{"oracle": oracle, "cases": []string{"unchanged"}})
		if err != nil {
			t.Fatal(err)
		}
		writePortgenTestFile(t, root, rel, string(content)+"\n")
	}
	for _, target := range fixtureGeneratorTargets {
		selected := len(target.args) > 1 && (target.args[1] == "./scripts/rust-port/cmd/configgen" || target.args[1] == "./scripts/rust-port/cmd/coregen" || target.args[1] == "./scripts/rust-port/cmd/querygen" || target.args[1] == "./scripts/rust-port/cmd/vaultgen" || target.args[1] == "./scripts/rust-port/cmd/vaultfsgen" || target.args[1] == "./scripts/rust-port/cmd/vaultwritegen")
		if !selected {
			got, err := fixtureReplayArgs(root, target)
			if err != nil || !reflect.DeepEqual(got, target.args) {
				t.Fatalf("independent target %s changed: %v %v", target.name, got, err)
			}
			continue
		}
		t.Run(target.name, func(t *testing.T) {
			for _, commit := range []string{ancestor, head} {
				oracle := inventory.Oracle{Commit: commit, Release: "independent-release"}
				for _, rel := range target.outputs {
					writeOracle(rel, oracle)
				}
				original := append([]string(nil), target.args...)
				got, err := fixtureReplayArgs(root, target)
				want := append(append([]string(nil), original...), "--oracle-commit", commit, "--oracle-release", oracle.Release)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("replay = %v, %v; want %v", got, err, want)
				}
				if !reflect.DeepEqual(target.args, original) {
					t.Fatal("registry mutated")
				}
			}
			for _, oracle := range []inventory.Oracle{{Commit: "HEAD", Release: "release"}, {Commit: head}, {Commit: strings.Repeat("a", 40), Release: "release"}} {
				writeOracle(target.outputs[0], oracle)
				if _, err := fixtureReplayArgs(root, target); err == nil {
					t.Fatalf("accepted invalid oracle %#v", oracle)
				}
			}
			writePortgenTestFile(t, root, target.outputs[0], "{invalid\n")
			if _, err := fixtureReplayArgs(root, target); err == nil {
				t.Fatal("accepted malformed document")
			}
			if len(target.outputs) > 1 {
				for _, rel := range target.outputs {
					writeOracle(rel, inventory.Oracle{Commit: head, Release: "release"})
				}
				writeOracle(target.outputs[1], inventory.Oracle{Commit: ancestor, Release: "release"})
				if _, err := fixtureReplayArgs(root, target); err == nil {
					t.Fatal("accepted mixed corpus identities")
				}
			}
		})
	}
	// The source guard runs before any generator process is started.
	writePortgenTestFile(t, root, "internal/core/core.go", "package core\n// changed production\n")
	err := runCompleteFixtureGeneration("must-not-execute", root, nil, inventory.Oracle{Commit: head, Release: "release"}, head, nil)
	if err == nil || !strings.Contains(err.Error(), "production source does not match") {
		t.Fatalf("source mismatch guard = %v", err)
	}
}
