package inventory

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// ComputeProductionSourceDigest hashes production inputs that define the Go
// oracle. Besides Go source this includes embedded migrations/templates/fonts
// and the release/data contracts. Harness, test, documentation, and future Rust
// files deliberately do not affect this digest.
func ComputeProductionSourceDigest(repoRoot string) (string, error) {
	args := []string{"ls-files", "--cached", "--others", "--exclude-standard", "--", "cmd", "internal"}
	args = append(args, productionContractFiles()...)
	listCommand := exec.Command("git", args...)
	listCommand.Dir = repoRoot
	output, err := listCommand.Output()
	if err != nil {
		return "", fmt.Errorf("list working-tree production inputs: %w", err)
	}
	var files []string
	for _, rel := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if !isProductionContractInput(rel) {
			continue
		}
		files = append(files, filepath.ToSlash(rel))
	}
	sort.Strings(files)

	hasher := sha256.New()
	for _, rel := range files {
		hasher.Write([]byte(rel + "\n"))
		//nolint:gosec // rel comes from git ls-files within repoRoot
		content, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(rel)))
		if err != nil {
			return "", fmt.Errorf("open %s: %w", rel, err)
		}
		_, _ = hasher.Write(content)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// ComputeGitRevisionProductionSourceDigest hashes the same production inputs
// directly from a Git revision. Fixture generation uses this to prove that an
// operator cannot label arbitrary working-tree bytes with a trusted oracle SHA.
func ComputeGitRevisionProductionSourceDigest(repoRoot, revision string) (string, error) {
	args := []string{"ls-tree", "-r", "--name-only", revision, "--", "cmd", "internal"}
	args = append(args, productionContractFiles()...)
	//nolint:gosec // fixed git ls-tree arguments for the pinned revision
	listCommand := exec.Command("git", args...)
	listCommand.Dir = repoRoot
	output, err := listCommand.Output()
	if err != nil {
		return "", fmt.Errorf("list production inputs at %s: %w", revision, err)
	}
	var files []string
	for _, rel := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if !isProductionContractInput(rel) {
			continue
		}
		files = append(files, filepath.ToSlash(rel))
	}
	sort.Strings(files)
	hasher := sha256.New()
	for _, rel := range files {
		_, _ = io.WriteString(hasher, rel+"\n")
		//nolint:gosec // fixed git show arguments for the pinned revision
		show := exec.Command("git", "show", revision+":"+rel)
		show.Dir = repoRoot
		content, showErr := show.Output()
		if showErr != nil {
			return "", fmt.Errorf("read %s at %s: %w", rel, revision, showErr)
		}
		_, _ = hasher.Write(content)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// ComputeGeneratorSourceDigest fingerprints the live code that derives and
// checks fixtures. It includes every non-ignored generator control file rather
// than only Go files, so Make, Python, Swift, and fixture harness changes also
// require deliberate regeneration and review.
func ComputeGeneratorSourceDigest(repoRoot string) (string, error) {
	files, err := listGeneratorDigestInputs(repoRoot, "")
	if err != nil {
		return "", err
	}
	return hashGeneratorDigestInputs(files, func(rel string) ([]byte, error) {
		//nolint:gosec // rel is constrained by Git's repository-local file list.
		content, readErr := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(rel)))
		if readErr != nil {
			return nil, fmt.Errorf("read generator input %s: %w", rel, readErr)
		}
		return content, nil
	})
}

// ComputeGitRevisionGeneratorSourceDigest computes the same digest from
// immutable Git objects. Provenance verification uses this instead of live
// worktree bytes so Q cannot be relabeled by ignored or concurrent local files.
func ComputeGitRevisionGeneratorSourceDigest(repoRoot, revision string) (string, error) {
	files, err := listGeneratorDigestInputs(repoRoot, revision)
	if err != nil {
		return "", err
	}
	return hashGeneratorDigestInputs(files, func(rel string) ([]byte, error) {
		//nolint:gosec // revision and rel are validated repository-local provenance inputs.
		show := exec.Command("git", "show", revision+":"+rel)
		show.Dir = repoRoot
		content, showErr := show.Output()
		if showErr != nil {
			return nil, fmt.Errorf("read generator input %s at %s: %w", rel, revision, showErr)
		}
		return content, nil
	})
}

func generatorSourcePaths() []string {
	return []string{
		"Makefile",
		"scripts/rust-port",
		"cmd/symdesk/port_inventory_test.go",
		"cmd/symroom/port_grammar_test.go",
		"internal/tools/port_mcp_test.go",
		"internal/selfhost/port_http_test.go",
		"internal/service/port_resolution_test.go",
		"internal/health/port_links_test.go",
		"internal/notebook/port_parse_test.go",
		"internal/retrieval/internal/engine/port_metadata_test.go",
		"internal/vault/port_mobile_test.go",
		"internal/sidecar/port_contract_test.go",
		"Tests/SymDeskMobileTests/MobileRustPortContractTests.swift",
	}
}

func listGeneratorDigestInputs(repoRoot, revision string) ([]string, error) {
	args := []string{}
	if revision == "" {
		args = append(args, "ls-files", "--cached", "--others", "--exclude-standard", "--")
	} else {
		args = append(args, "ls-tree", "-r", "--name-only", revision, "--")
	}
	args = append(args, generatorSourcePaths()...)
	command := exec.Command("git", args...)
	command.Dir = repoRoot
	output, err := command.Output()
	if err != nil {
		if revision == "" {
			return nil, fmt.Errorf("list fixture generator inputs: %w", err)
		}
		return nil, fmt.Errorf("list fixture generator inputs at %s: %w", revision, err)
	}

	seen := make(map[string]struct{})
	files := make([]string, 0)
	for _, rel := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if rel == "" {
			continue
		}
		rel = filepath.ToSlash(rel)
		if _, exists := seen[rel]; exists {
			continue
		}
		seen[rel] = struct{}{}
		files = append(files, rel)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("fixture generator input set is empty")
	}
	sort.Strings(files)
	return files, nil
}

func hashGeneratorDigestInputs(files []string, read func(string) ([]byte, error)) (string, error) {
	hasher := sha256.New()
	for _, rel := range files {
		_, _ = io.WriteString(hasher, rel+"\n")
		content, err := read(rel)
		if err != nil {
			return "", err
		}
		_, _ = hasher.Write(content)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func isProductionContractInput(path string) bool {
	path = filepath.ToSlash(path)
	return path != "" && !strings.HasSuffix(path, "_test.go") && !strings.Contains(path, "/testdata/")
}

func productionContractFiles() []string {
	return []string{
		"go.mod",
		"go.sum",
		".goreleaser.yml",
		"Dockerfile",
		"VAULT.md",
		".github/workflows/release.yml",
		"home-assistant-addon/symdesk/config.yaml",
	}
}

// ComputeFileChecksum computes the SHA-256 hex string of a file.
func ComputeFileChecksum(path string) (string, error) {
	//nolint:gosec // caller-supplied explicit path
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
