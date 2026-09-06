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

// ComputeGeneratorSourceDigest fingerprints the code that derives and checks
// fixtures. It is separate from the pinned production digest: harness-only
// changes do not relabel the Go oracle, but they do require deliberate fixture
// regeneration and review.
func ComputeGeneratorSourceDigest(repoRoot string) (string, error) {
	paths := []string{
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
		"Tests/SymDeskMobileTests/MobileRustPortContractTests.swift",
	}
	args := []string{"ls-files", "--cached", "--others", "--exclude-standard", "--"}
	args = append(args, paths...)
	command := exec.Command("git", args...)
	command.Dir = repoRoot
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("list fixture generator inputs: %w", err)
	}
	files := strings.Split(strings.TrimSpace(string(output)), "\n")
	sort.Strings(files)
	hasher := sha256.New()
	for _, rel := range files {
		if rel == "" || !strings.HasSuffix(rel, ".go") {
			continue
		}
		_, _ = io.WriteString(hasher, rel+"\n")
		//nolint:gosec // rel comes from git ls-files within repoRoot
		content, readErr := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(rel)))
		if readErr != nil {
			return "", fmt.Errorf("read generator input %s: %w", rel, readErr)
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
