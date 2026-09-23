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
	listCommand := inventoryGitCommand(repoRoot, args...)
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
	listCommand := inventoryGitCommand(repoRoot, args...)
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
		show := inventoryGitCommand(repoRoot, "show", revision+":"+rel)
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
		show := inventoryGitCommand(repoRoot, "show", revision+":"+rel)
		content, showErr := show.Output()
		if showErr != nil {
			return nil, fmt.Errorf("read generator input %s at %s: %w", rel, revision, showErr)
		}
		return content, nil
	})
}

func generatorSourcePaths() []string {
	return []string{
		"go.mod",
		"go.sum",
		"Makefile",
		".gitattributes",
		"scripts/rust-port",
		"cmd/symdesk/port_inventory_test.go",
		"cmd/symdesk/history_tasks_port_test.go",
		"cmd/symroom/port_grammar_test.go",
		"internal/tools/port_mcp_test.go",
		"internal/selfhost/port_http_test.go",
		"internal/service/port_resolution_test.go",
		"internal/service/port_dataset_contract_test.go",
		"internal/service/port_dataset_sync_service_contract_test.go",
		"internal/service/port_dataset_import_contract_test.go",
		"internal/service/port_retention_state_contract_test.go",
		"internal/room/run/port_projection_contract_test.go",
		"internal/service/port_noteops_contract_test.go",
		"internal/history/port_lifecycle_contract_test.go",
		"internal/retention/port_retention_contract_test.go",
		"internal/retention/port_retention_rules_contract_test.go",
		"internal/health/port_links_test.go",
		"internal/notebook/port_parse_test.go",
		"internal/retrieval/internal/engine/port_metadata_test.go",
		"internal/vault/port_mobile_test.go",
		"internal/vault/port_writefs_contract_test.go",
		"internal/sidecar/port_contract_test.go",
		"internal/sidecar/port_lifecycle_contract_test.go",
		"crates/symdesk-index/src/contract_tests.rs",
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
	command := inventoryGitCommand(repoRoot, args...)
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

func inventoryGitCommand(repoRoot string, args ...string) *exec.Cmd {
	//nolint:gosec // callers use fixed Git subcommands and repository-derived revision/path inputs.
	command := exec.Command("git", append([]string{"--no-replace-objects"}, args...)...)
	command.Dir = repoRoot
	command.Env = inventoryGitEnvironment(os.Environ())
	return command
}

func inventoryGitEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment)+3)
	for _, item := range environment {
		name, _, found := strings.Cut(item, "=")
		if found && strings.HasPrefix(strings.ToUpper(name), "GIT_") {
			continue
		}
		result = append(result, item)
	}
	return append(result,
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
	)
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
