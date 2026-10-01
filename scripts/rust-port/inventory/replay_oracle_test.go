package inventory

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveCheckOracle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.json")
	recorded := Oracle{Commit: strings.Repeat("a", 40), Release: "recorded-release"}
	write := func(commit, release string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(`{"oracle":{"commit":"`+commit+`","release":"`+release+`"},"cases":[1]}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(recorded.Commit, recorded.Release)
	for _, args := range [][]string{nil, {"--oracle-commit", strings.Repeat("b", 40)}, {"--oracle-release", "explicit-release"}, {"--oracle-commit", "", "--oracle-release", ""}} {
		flags := flag.NewFlagSet("test", flag.ContinueOnError)
		commit := flags.String("oracle-commit", "historical", "commit")
		release := flags.String("oracle-release", "historical", "release")
		if err := flags.Parse(args); err != nil {
			t.Fatal(err)
		}
		got, err := ResolveCheckOracle(Oracle{Commit: *commit, Release: *release}, flags, path)
		want := recorded
		flags.Visit(func(f *flag.Flag) {
			if f.Name == "oracle-commit" {
				want.Commit = *commit
			}
			if f.Name == "oracle-release" {
				want.Release = *release
			}
		})
		if err != nil || got != want {
			t.Fatalf("args %v: got %#v, %v; want %#v", args, got, err, want)
		}
	}
	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	for _, oracle := range []Oracle{{Commit: "HEAD", Release: "release"}, {Commit: recorded.Commit}, {Commit: strings.Repeat("A", 40), Release: "release"}} {
		write(oracle.Commit, oracle.Release)
		if _, err := ResolveCheckOracle(Oracle{}, flags, path); err == nil {
			t.Fatalf("accepted %#v", oracle)
		}
	}
	write(recorded.Commit, recorded.Release)
	other := filepath.Join(t.TempDir(), "other.json")
	if err := os.WriteFile(other, []byte(`{"oracle":{"commit":"`+recorded.Commit+`","release":"other-release"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveCheckOracle(Oracle{}, flags, path, other); err == nil {
		t.Fatal("accepted mixed identities")
	}
	if _, err := ResolveCheckOracle(Oracle{}, flags); err == nil {
		t.Fatal("accepted missing outputs")
	}
	if _, err := ResolveCheckOracle(Oracle{}, flags, path+".missing"); err == nil {
		t.Fatal("accepted missing fixture")
	}
}
