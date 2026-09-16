package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRewriteSidecarOraclePreservesOtherFixtureBytes(t *testing.T) {
	original := []byte("{\n  \"schema_version\": 1,\n  \"oracle\": {\n    \"commit\": \"old\",\n    \"release\": \"old-release\"\n  },\n  \"mtime_ns\": 1768478400123456789\n}\n")
	commit := strings.Repeat("a", 40)
	updated, err := rewriteSidecarOracle(original, commit, "new-release")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updated), `"mtime_ns": 1768478400123456789`) {
		t.Fatalf("rewrite altered unrelated integer bytes:\n%s", updated)
	}
	if strings.Contains(string(updated), "old-release") || strings.Contains(string(updated), `"commit": "old"`) {
		t.Fatalf("rewrite left stale oracle data:\n%s", updated)
	}
}

func TestSyncSidecarOracleMetadataUpdatesAllDerivedFixtures(t *testing.T) {
	repoRoot := t.TempDir()
	for _, rel := range sidecarOracleFixturePaths {
		path := filepath.Join(repoRoot, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(`{"oracle":{"commit":"old","release":"old"},"payload":1}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	commit := strings.Repeat("b", 40)
	if err := syncSidecarOracleMetadata(repoRoot, commit, "new-release"); err != nil {
		t.Fatal(err)
	}
	for _, rel := range sidecarOracleFixturePaths {
		data, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		var value struct {
			Oracle struct {
				Commit  string `json:"commit"`
				Release string `json:"release"`
			} `json:"oracle"`
		}
		if err := json.Unmarshal(data, &value); err != nil {
			t.Fatal(err)
		}
		if value.Oracle.Commit != commit || value.Oracle.Release != "new-release" {
			t.Fatalf("%s oracle = %#v", rel, value.Oracle)
		}
	}
}
