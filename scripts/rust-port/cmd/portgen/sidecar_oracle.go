package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

var sidecarOracleFixturePaths = []string{
	"testdata/port/sidecar/roundtrip.json",
	"testdata/port/sidecar/large-corpus.json",
}

var sidecarOraclePattern = regexp.MustCompile(`(?s)("oracle"\s*:\s*\{\s*"commit"\s*:\s*")[^"]+("\s*,\s*"release"\s*:\s*")[^"]+(")`)

// syncSidecarOracleMetadata updates only the sidecar fixture oracle strings.
// It preserves every other byte (notably large integer fixture values), then
// proves the rewritten JSON has the requested identity.
func syncSidecarOracleMetadata(repoRoot, commit, release string) error {
	for _, rel := range sidecarOracleFixturePaths {
		path := filepath.Join(repoRoot, filepath.FromSlash(rel))
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("stat %s: %w", rel, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s must be a regular file", rel)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", rel, err)
		}
		updated, err := rewriteSidecarOracle(data, commit, release)
		if err != nil {
			return fmt.Errorf("rewrite %s: %w", rel, err)
		}
		if string(updated) == string(data) {
			continue
		}
		if err := os.WriteFile(path, updated, info.Mode().Perm()); err != nil {
			return fmt.Errorf("write %s: %w", rel, err)
		}
	}
	return nil
}

func rewriteSidecarOracle(data []byte, commit, release string) ([]byte, error) {
	matches := sidecarOraclePattern.FindAllSubmatchIndex(data, -1)
	if len(matches) != 1 {
		return nil, fmt.Errorf("expected exactly one ordered oracle object, found %d", len(matches))
	}
	match := matches[0]
	updated := make([]byte, 0, len(data)-match[4]+match[3]-match[6]+match[5]+len(commit)+len(release))
	updated = append(updated, data[:match[3]]...)
	updated = append(updated, commit...)
	updated = append(updated, data[match[4]:match[5]]...)
	updated = append(updated, release...)
	updated = append(updated, data[match[6]:]...)

	var parsed struct {
		Oracle struct {
			Commit  string `json:"commit"`
			Release string `json:"release"`
		} `json:"oracle"`
	}
	if err := json.Unmarshal(updated, &parsed); err != nil {
		return nil, fmt.Errorf("decode rewritten JSON: %w", err)
	}
	if parsed.Oracle.Commit != commit || parsed.Oracle.Release != release {
		return nil, fmt.Errorf("rewritten oracle commit=%q release=%q", parsed.Oracle.Commit, parsed.Oracle.Release)
	}
	return updated, nil
}
