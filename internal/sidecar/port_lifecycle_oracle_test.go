package sidecar

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolvePortLifecycleOracle(t *testing.T) {
	provenancePath := filepath.Join(t.TempDir(), "provenance.json")
	committed := strings.Repeat("a", 40)
	if err := os.WriteFile(provenancePath, []byte(`{"oracle":{"commit":"`+committed+`","release":"committed-release"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("uses committed provenance without explicit portgen input", func(t *testing.T) {
		oracle, err := resolvePortLifecycleOracle(func(string) (string, bool) { return "", false }, provenancePath)
		if err != nil {
			t.Fatal(err)
		}
		if oracle != (portOracle{Commit: committed, Release: "committed-release"}) {
			t.Fatalf("oracle = %#v", oracle)
		}
	})

	t.Run("explicit pair overrides committed provenance", func(t *testing.T) {
		expected := portOracle{Commit: strings.Repeat("b", 40), Release: "generated-release"}
		values := map[string]string{
			portgenSidecarOracleCommitEnv:  expected.Commit,
			portgenSidecarOracleReleaseEnv: expected.Release,
		}
		oracle, err := resolvePortLifecycleOracle(func(name string) (string, bool) {
			value, ok := values[name]
			return value, ok
		}, provenancePath)
		if err != nil {
			t.Fatal(err)
		}
		if oracle != expected {
			t.Fatalf("oracle = %#v, want %#v", oracle, expected)
		}
	})

	for _, test := range []struct {
		name   string
		values map[string]string
	}{
		{"commit without release", map[string]string{portgenSidecarOracleCommitEnv: strings.Repeat("c", 40)}},
		{"release without commit", map[string]string{portgenSidecarOracleReleaseEnv: "release"}},
		{"uppercase commit", map[string]string{portgenSidecarOracleCommitEnv: strings.Repeat("A", 40), portgenSidecarOracleReleaseEnv: "release"}},
		{"empty release", map[string]string{portgenSidecarOracleCommitEnv: strings.Repeat("d", 40), portgenSidecarOracleReleaseEnv: ""}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := resolvePortLifecycleOracle(func(name string) (string, bool) {
				value, ok := test.values[name]
				return value, ok
			}, provenancePath); err == nil {
				t.Fatal("resolvePortLifecycleOracle() accepted invalid explicit input")
			}
		})
	}
}
