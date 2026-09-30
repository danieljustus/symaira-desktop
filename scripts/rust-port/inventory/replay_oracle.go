package inventory

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
)

var replayCommit = regexp.MustCompile(`^[0-9a-f]{40}$`)

// ResolveCheckOracle supplies only unspecified identity flags from the recorded
// corpus. Explicit expectations remain authoritative. Callers still compare the
// complete regenerated documents; this never writes or transforms a fixture.
func ResolveCheckOracle(oracle Oracle, flags *flag.FlagSet, paths ...string) (Oracle, error) {
	if len(paths) == 0 {
		return Oracle{}, fmt.Errorf("replay corpus has no outputs")
	}
	var recorded Oracle
	for i, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			return Oracle{}, err
		}
		var document struct {
			Oracle Oracle `json:"oracle"`
		}
		if err := json.Unmarshal(content, &document); err != nil {
			return Oracle{}, err
		}
		if !replayCommit.MatchString(document.Oracle.Commit) || document.Oracle.Release == "" {
			return Oracle{}, fmt.Errorf("invalid replay oracle in %s", path)
		}
		if i == 0 {
			recorded = document.Oracle
		} else if recorded != document.Oracle {
			return Oracle{}, fmt.Errorf("inconsistent replay oracle in %s", path)
		}
	}
	commitSet, releaseSet := false, false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "oracle-commit" {
			commitSet = true
		}
		if f.Name == "oracle-release" {
			releaseSet = true
		}
	})
	if !commitSet {
		oracle.Commit = recorded.Commit
	}
	if !releaseSet {
		oracle.Release = recorded.Release
	}
	return oracle, nil
}
