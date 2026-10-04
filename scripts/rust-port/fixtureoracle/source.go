// Package fixtureoracle selects the production source identity for live port
// fixtures. It is fixture tooling, never imported by production entrypoints.
package fixtureoracle

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

const (
	CommitEnvironment  = "PORTGEN_FIXTURE_SOURCE_COMMIT"
	ReleaseEnvironment = "PORTGEN_FIXTURE_SOURCE_RELEASE"
)

var fullCommit = regexp.MustCompile(`^[0-9a-f]{40}$`)

var current, currentError = compiledSource()

func compiledSource() (inventory.Oracle, error) {
	// Helpers re-execute the test image from synthetic directories, and tests
	// deliberately change PATH/cwd. Bind the source to the compiled harness
	// before those changes, rather than treating a helper's cwd as the source.
	if _, file, _, ok := runtime.Caller(0); ok && filepath.IsAbs(file) {
		if root, err := FindRepositoryRoot(filepath.Dir(file)); err == nil {
			return Source(root)
		}
	}
	// Trimmed source paths are not filesystem locators; ordinary package runs
	// can still resolve the generating checkout from their initial cwd.
	cwd, err := os.Getwd()
	if err != nil {
		return inventory.Oracle{}, err
	}
	root, err := FindRepositoryRoot(cwd)
	if err != nil {
		return inventory.Oracle{}, err
	}
	return Source(root)
}

// Current returns the source validated before test-local environment changes.
// Defer errors until a fixture writer requests it: isolated helper processes
// that never record source identities must still run without Git/source access.
// Production mutation controls use uncached Source on their own input roots.
func Current() inventory.Oracle {
	if currentError != nil {
		panic(currentError)
	}
	return current
}

// Source reads P from the central provenance document. A generation process
// may select a replacement P explicitly, but it must describe the complete Go
// production bytes actually read. Replay never accepts ambient overrides.
func Source(repoRoot string) (inventory.Oracle, error) {
	return source(repoRoot, os.LookupEnv)
}

func source(repoRoot string, lookup func(string) (string, bool)) (inventory.Oracle, error) {
	oracle, err := selected(repoRoot, lookup)
	if err != nil {
		return inventory.Oracle{}, err
	}
	if err := ValidateSource(repoRoot, oracle); err != nil {
		return inventory.Oracle{}, err
	}
	return oracle, nil
}

// Defaults supplies central identity defaults before a generator parses an
// explicit replacement P. It does not certify production bytes; writers call
// ValidateSource after selecting their flags, or use the validated Current.
func Defaults() inventory.Oracle {
	cwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	root, err := FindRepositoryRoot(cwd)
	if err != nil {
		panic(err)
	}
	oracle, err := selected(root, os.LookupEnv)
	if err != nil {
		panic(err)
	}
	return oracle
}

func selected(repoRoot string, lookup func(string) (string, bool)) (inventory.Oracle, error) {
	root, err := os.OpenRoot(repoRoot)
	if err != nil {
		return inventory.Oracle{}, fmt.Errorf("open fixture source root: %w", err)
	}
	defer func() { _ = root.Close() }()
	data, err := root.ReadFile("testdata/port/provenance.json")
	if err != nil {
		return inventory.Oracle{}, fmt.Errorf("read fixture source provenance: %w", err)
	}
	var provenance struct {
		Oracle inventory.Oracle `json:"oracle"`
	}
	if err := json.Unmarshal(data, &provenance); err != nil {
		return inventory.Oracle{}, fmt.Errorf("decode fixture source provenance: %w", err)
	}
	oracle := provenance.Oracle
	commit, hasCommit := lookup(CommitEnvironment)
	release, hasRelease := lookup(ReleaseEnvironment)
	if hasCommit || hasRelease {
		activation, _ := lookup("PORT_GENERATE")
		if activation != "1" || !hasCommit || !hasRelease {
			return inventory.Oracle{}, fmt.Errorf("fixture source overrides require PORT_GENERATE=1 and both %s and %s", CommitEnvironment, ReleaseEnvironment)
		}
		oracle = inventory.Oracle{Commit: commit, Release: release}
	}
	if !fullCommit.MatchString(oracle.Commit) || strings.TrimSpace(oracle.Release) == "" {
		return inventory.Oracle{}, fmt.Errorf("fixture source requires a full lowercase commit and a release")
	}
	return oracle, nil
}

// ValidateSource binds a selected P to the actual complete production inputs.
func ValidateSource(repoRoot string, oracle inventory.Oracle) error {
	if !fullCommit.MatchString(oracle.Commit) || strings.TrimSpace(oracle.Release) == "" {
		return fmt.Errorf("invalid selected fixture source identity")
	}
	actual, err := inventory.ComputeProductionSourceDigest(repoRoot)
	if err != nil {
		return err
	}
	pinned, err := inventory.ComputeGitRevisionProductionSourceDigest(repoRoot, oracle.Commit)
	if err != nil {
		return err
	}
	if actual != pinned {
		return fmt.Errorf("fixture production source differs from selected P %s: actual=%s pinned=%s", oracle.Commit, actual, pinned)
	}
	return nil
}

// GenerationEnvironment binds package-local writers to the same reviewed P
// selected by portgen's flags. The caller still owns explicit write activation.
func GenerationEnvironment(environment []string, oracle inventory.Oracle) []string {
	result := make([]string, 0, len(environment)+2)
	for _, item := range environment {
		name, _, _ := strings.Cut(item, "=")
		if strings.EqualFold(name, CommitEnvironment) || strings.EqualFold(name, ReleaseEnvironment) {
			continue
		}
		result = append(result, item)
	}
	return append(result, CommitEnvironment+"="+oracle.Commit, ReleaseEnvironment+"="+oracle.Release)
}

// FindRepositoryRoot resolves the source tree from a package or generator cwd.
func FindRepositoryRoot(start string) (string, error) {
	root, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		//nolint:gosec // Fixture root discovery reads only fixed go.mod names in cwd parents.
		if data, err := os.ReadFile(filepath.Join(root, "go.mod")); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				fields := strings.Fields(line)
				if len(fields) >= 2 && fields[0] == "module" && fields[1] == "github.com/danieljustus/symaira-desktop" {
					return root, nil
				}
			}
		}
		parent := filepath.Dir(root)
		if parent == root {
			return "", fmt.Errorf("fixture repository root not found from %s", start)
		}
		root = parent
	}
}
