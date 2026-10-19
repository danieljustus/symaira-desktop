package inventory

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ComputeProductionSourceDigest hashes production inputs that define the Go
// oracle. Besides Go source this includes embedded migrations/templates/fonts
// and the release/data contracts. Harness, test, documentation, and future Rust
// files deliberately do not affect this digest.
func ComputeProductionSourceDigest(repoRoot string) (string, error) {
	return computeProductionSourceDigest(repoRoot, func(args ...string) ([]byte, error) {
		return inventoryGitOutput(repoRoot, args...)
	})
}

func computeProductionSourceDigest(repoRoot string, gitOutput func(...string) ([]byte, error)) (string, error) {
	args := []string{"ls-files", "--cached", "--others", "--exclude-standard", "--", "cmd", "internal"}
	args = append(args, productionContractFiles()...)
	output, err := gitOutput(args...)
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
	git, err := oracleGitExecutable()
	if err != nil {
		return "", err
	}
	return oracleRevisionDigest(repoRoot, git, revision)
}

func computeGitRevisionProductionSourceDigest(repoRoot, git, revision string) (string, error) {
	args := []string{"ls-tree", "-r", "--name-only", revision, "--", "cmd", "internal"}
	args = append(args, productionContractFiles()...)
	output, err := inventoryGitOutputWithExecutable(repoRoot, git, args...)
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
	err = inventoryGitBlobs(repoRoot, git, revision, files, func(rel string, content io.Reader) error {
		_, _ = io.WriteString(hasher, rel+"\n")
		_, err := io.Copy(hasher, content)
		return err
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// inventoryGitBlobs streams revision:path blobs in order through one
// `git cat-file --batch` process (#1243). A process per file dominated native
// Windows oracle verification; the bytes and their order are unchanged.
func inventoryGitBlobs(repoRoot, git, revision string, files []string, visit func(string, io.Reader) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	configPath, cleanup, err := PrivateGitConfig()
	if err != nil {
		return err
	}
	defer cleanup()
	//nolint:gosec // fixed Git subcommand; object names are repository-derived revision/path inputs.
	command := exec.CommandContext(ctx, git, "-c", "safe.directory="+filepath.ToSlash(repoRoot), "--no-replace-objects", "--no-lazy-fetch", "cat-file", "--batch")
	command.WaitDelay = 2 * time.Second
	command.Dir = repoRoot
	command.Env = inventoryGitEnvironment(os.Environ(), configPath)
	var stderr strings.Builder
	command.Stderr = &stderr
	stdin, err := command.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	if err := command.Start(); err != nil {
		return fmt.Errorf("git cat-file --batch: %w", err)
	}
	go func() {
		defer func() { _ = stdin.Close() }()
		for _, rel := range files {
			if _, err := io.WriteString(stdin, revision+":"+rel+"\n"); err != nil {
				return
			}
		}
	}()
	readErr := readGitBlobs(bufio.NewReader(stdout), revision, files, visit)
	if readErr != nil {
		cancel()
	}
	waitErr := command.Wait()
	if readErr != nil {
		return readErr
	}
	if waitErr != nil {
		return fmt.Errorf("git cat-file --batch: %w: %s", errors.Join(waitErr, ctx.Err()), strings.TrimSpace(stderr.String()))
	}
	return nil
}

func readGitBlobs(output *bufio.Reader, revision string, files []string, visit func(string, io.Reader) error) error {
	for _, rel := range files {
		header, err := output.ReadString('\n')
		if err != nil {
			return fmt.Errorf("read %s at %s: %w", rel, revision, err)
		}
		fields := strings.Fields(header)
		if len(fields) != 3 || fields[1] != "blob" {
			return fmt.Errorf("read %s at %s: unexpected object %q", rel, revision, strings.TrimSpace(header))
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || size < 0 {
			return fmt.Errorf("read %s at %s: invalid object size %q", rel, revision, fields[2])
		}
		content := io.LimitReader(output, size)
		if err := visit(rel, content); err != nil {
			return fmt.Errorf("read %s at %s: %w", rel, revision, err)
		}
		if _, err := io.Copy(io.Discard, content); err != nil {
			return fmt.Errorf("read %s at %s: %w", rel, revision, err)
		}
		if terminator, err := output.ReadByte(); err != nil || terminator != '\n' {
			return fmt.Errorf("read %s at %s: truncated object", rel, revision)
		}
	}
	return nil
}

// NewProductionSourceVerifier captures the source root and Git executable without
// spawning processes. Its returned function validates complete source bytes on
// demand, even after a test isolates PATH or its working directory.
func NewProductionSourceVerifier(repoRoot string) (func(string) error, error) {
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return nil, err
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return nil, err
	}
	git, err = filepath.Abs(git)
	if err != nil {
		return nil, err
	}
	output := func(args ...string) ([]byte, error) {
		return inventoryGitOutputWithExecutable(root, git, args...)
	}
	return func(revision string) error {
		actual, err := computeProductionSourceDigest(root, output)
		if err != nil {
			return err
		}
		pinned, err := oracleRevisionDigest(root, git, revision)
		if err != nil {
			return err
		}
		if actual != pinned {
			return fmt.Errorf("fixture production source differs from selected P %s: actual=%s pinned=%s", revision, actual, pinned)
		}
		return nil
	}, nil
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
		content, showErr := inventoryGitOutput(repoRoot, "show", revision+":"+rel)
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
		"testdata/port/cli/cases.json",
		"testdata/port/cli/retention-cases.json",
		"crates/symdesk-index/tests/data/dataset_sync_time",
		"scripts/rust-port",
		"cmd",
		"internal",
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
	output, err := inventoryGitOutput(repoRoot, args...)
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
		// Package-local fixtures compile their test helpers and can read testdata.
		// Cover the complement of the production digest without a stale allowlist.
		if (strings.HasPrefix(rel, "cmd/") || strings.HasPrefix(rel, "internal/")) && isProductionContractInput(rel) {
			continue
		}
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

func inventoryGitOutput(repoRoot string, args ...string) ([]byte, error) {
	return inventoryGitOutputWithExecutable(repoRoot, "git", args...)
}

func inventoryGitOutputWithExecutable(repoRoot, git string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	configPath, cleanup, err := PrivateGitConfig()
	if err != nil {
		return nil, err
	}
	defer cleanup()
	//nolint:gosec // callers use fixed Git subcommands and repository-derived revision/path inputs.
	command := exec.CommandContext(ctx, git, append([]string{"-c", "safe.directory=" + filepath.ToSlash(repoRoot), "--no-replace-objects", "--no-lazy-fetch"}, args...)...)
	command.WaitDelay = 2 * time.Second
	command.Dir = repoRoot
	command.Env = inventoryGitEnvironment(os.Environ(), configPath)
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), errors.Join(err, ctx.Err()), strings.TrimSpace(string(output)))
	}
	return output, nil
}

// PrivateGitConfig returns a real empty config file: os.DevNull (NUL) is not
// accepted as GIT_CONFIG_GLOBAL by native Git for Windows.
func PrivateGitConfig() (string, func(), error) {
	dir, err := os.MkdirTemp("", "portgen-git-config-")
	if err != nil {
		return "", nil, fmt.Errorf("create private Git config directory: %w", err)
	}
	configPath := filepath.Join(dir, "config")
	if err := os.WriteFile(configPath, nil, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", nil, fmt.Errorf("create empty private Git config: %w", err)
	}
	return configPath, func() { _ = os.RemoveAll(dir) }, nil
}

func inventoryGitEnvironment(environment []string, configPath string) []string {
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
		"GIT_CONFIG_GLOBAL="+configPath,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_NO_LAZY_FETCH=1",
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
