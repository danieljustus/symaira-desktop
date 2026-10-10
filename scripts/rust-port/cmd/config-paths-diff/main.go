package main

import (
	"bytes"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	portdiff "github.com/danieljustus/symaira-desktop/scripts/rust-port/internal/diff"
)

const (
	modulePath      = "github.com/danieljustus/symaira-desktop"
	originURL       = "https://github.com/danieljustus/symaira-desktop.git"
	foundation      = "47dd417ec5632c8e6a2edb32ead09db09e929ed9"
	oracleGoVersion = "go1.26.9"
)

var goSourceFiles = []string{
	"cmd/symdesk/main.go",
	"cmd/symdesk/config.go",
	"internal/config/config.go",
	"internal/config/paths.go",
	"internal/sidecar/db.go",
	"internal/retrieval/maintenance.go",
	"internal/retrieval/internal/config/config.go",
	"internal/ingest/api.go",
	"internal/ingest/internal/config/config.go",
}

var rustSourceFiles = []string{
	"Cargo.toml",
	"Cargo.lock",
	"crates/symdesk-cli/Cargo.toml",
	"crates/symdesk-cli/src/main.rs",
	"crates/symdesk-cli/src/config_paths.rs",
	"crates/symdesk-core/src/config.rs",
	"crates/symdesk-index/src/lib.rs",
	"crates/symdesk-index/src/retrieval_config.rs",
}

type sourceIdentity struct {
	Commit string            `json:"commit"`
	Tree   string            `json:"tree"`
	Files  map[string]string `json:"files_sha256"`
}

type binaryIdentity struct {
	Path       string `json:"path"`
	SHA256     string `json:"sha256"`
	GoVersion  string `json:"go_version,omitempty"`
	BuildPath  string `json:"build_path,omitempty"`
	ModulePath string `json:"module_path,omitempty"`
	Version    string `json:"module_version,omitempty"`
	Revision   string `json:"vcs_revision,omitempty"`
	Modified   string `json:"vcs_modified,omitempty"`
}

type namedCase struct {
	ID     string           `json:"id"`
	Input  portdiff.Case    `json:"input"`
	Go     *processEvidence `json:"go,omitempty"`
	Rust   *processEvidence `json:"rust,omitempty"`
	Result string           `json:"result"`
	Error  string           `json:"error,omitempty"`
}

type processEvidence struct {
	ExitCode     int                      `json:"exit_code"`
	Signal       string                   `json:"signal,omitempty"`
	TimedOut     bool                     `json:"timed_out"`
	StdoutBytes  int                      `json:"stdout_bytes"`
	StdoutSHA256 string                   `json:"stdout_sha256"`
	StderrBytes  int                      `json:"stderr_bytes"`
	StderrSHA256 string                   `json:"stderr_sha256"`
	FilesBefore  []portdiff.ManifestEntry `json:"files_before"`
	FilesAfter   []portdiff.ManifestEntry `json:"files_after"`
}

type mutationEvidence struct {
	CaseID                 string `json:"case_id"`
	Field                  string `json:"field"`
	ComparatorRejected     bool   `json:"comparator_rejected"`
	RetainedInputUnchanged bool   `json:"retained_input_unchanged"`
	Reason                 string `json:"reason"`
}

type report struct {
	SchemaVersion int              `json:"schema_version"`
	Worktree      string           `json:"worktree"`
	Origin        string           `json:"origin"`
	Branch        string           `json:"branch"`
	Head          string           `json:"head"`
	GoSource      sourceIdentity   `json:"go_source"`
	RustSource    sourceIdentity   `json:"rust_source"`
	GoBinary      binaryIdentity   `json:"go_binary"`
	RustBinary    binaryIdentity   `json:"rust_binary"`
	GoRunner      string           `json:"go_runner_version"`
	CaseCount     int              `json:"declared_case_count"`
	ExecutedCount int              `json:"executed_case_count"`
	PassedCount   int              `json:"passed_case_count"`
	Mutation      mutationEvidence `json:"mutation_control"`
	Cases         []namedCase      `json:"cases"`
	SourceAfter   sourceIdentity   `json:"source_after"`
	HeadAfter     string           `json:"head_after"`
	StatusBefore  string           `json:"git_status_before"`
	StatusAfter   string           `json:"git_status_after"`
}

func main() {
	goBinary := flag.String("go-binary", "", "pinned production symdesk binary")
	rustBinary := flag.String("rust-binary", "", "candidate symdesk binary")
	repoRoot := flag.String("repo-root", "", "assigned candidate checkout root")
	rustManifest := flag.String("rust-manifest", "", "absolute symdesk-cli Cargo manifest")
	evidenceDir := flag.String("evidence-dir", "", "new external directory for retained evidence")
	flag.Parse()
	if *goBinary == "" || *rustBinary == "" || *repoRoot == "" || *rustManifest == "" || *evidenceDir == "" {
		fatal("--go-binary, --rust-binary, --repo-root, --rust-manifest and --evidence-dir are required")
	}

	reportValue, captureErr := capture(*goBinary, *rustBinary, *repoRoot, *rustManifest, *evidenceDir)
	if reportValue.CaseCount != 0 {
		content, err := json.MarshalIndent(reportValue, "", "  ")
		if err != nil {
			fatal("marshal retained report: %v (capture failure: %v)", err, captureErr)
		}
		if err := writePrivate(filepath.Join(*evidenceDir, "report.json"), append(content, '\n')); err != nil {
			fatal("retain report: %v (capture failure: %v)", err, captureErr)
		}
	}
	if captureErr != nil {
		fatal("capture failed; retained evidence at %s: %v", *evidenceDir, captureErr)
	}
	fmt.Printf("PASS %d/%d config paths real-process cases; evidence=%s\n", reportValue.PassedCount, reportValue.CaseCount, *evidenceDir)
}

func capture(goBinary, rustBinary, repoRoot, rustManifest, evidenceDir string) (report, error) {
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return report{}, err
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	manifest, err := filepath.Abs(rustManifest)
	if err != nil {
		return report{}, err
	}
	if manifest != filepath.Join(root, "crates", "symdesk-cli", "Cargo.toml") {
		return report{}, fmt.Errorf("Rust manifest is not the assigned symdesk-cli manifest: %s", manifest)
	}
	evidenceDir, err = filepath.Abs(evidenceDir)
	if err != nil {
		return report{}, err
	}
	if _, err := os.Lstat(evidenceDir); err == nil {
		return report{}, fmt.Errorf("evidence directory already exists; refusing overwrite: %s", evidenceDir)
	} else if !errors.Is(err, os.ErrNotExist) {
		return report{}, fmt.Errorf("inspect evidence directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(evidenceDir), 0o700); err != nil {
		return report{}, err
	}
	if err := os.Mkdir(evidenceDir, 0o700); err != nil {
		return report{}, err
	}

	origin, err := git(root, "remote", "get-url", "origin")
	if err != nil {
		return report{}, err
	}
	if origin != originURL {
		return report{}, fmt.Errorf("unexpected origin %q", origin)
	}
	branch, err := git(root, "branch", "--show-current")
	if err != nil {
		return report{}, err
	}
	if branch == "" || branch == "main" {
		return report{}, fmt.Errorf("refusing non-isolated branch %q", branch)
	}
	head, err := git(root, "rev-parse", "HEAD")
	if err != nil {
		return report{}, err
	}
	if _, err := git(root, "merge-base", "--is-ancestor", foundation, head); err != nil {
		return report{}, fmt.Errorf("foundation is not an ancestor of candidate HEAD: %w", err)
	}
	statusBefore, err := git(root, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return report{}, err
	}
	goSource, err := sourceManifest(root, foundation, goSourceFiles)
	if err != nil {
		return report{}, err
	}
	if err := verifyFilesAtCommit(root, foundation, goSourceFiles, goSource.Files); err != nil {
		return report{}, err
	}
	rustSource, err := sourceManifest(root, head, rustSourceFiles)
	if err != nil {
		return report{}, err
	}
	goInfo, err := inspectGoBinary(goBinary)
	if err != nil {
		return report{}, err
	}
	if goInfo.GoVersion != oracleGoVersion || goInfo.BuildPath != modulePath+"/cmd/symdesk" || goInfo.ModulePath != modulePath || goInfo.Revision != foundation || goInfo.Modified != "false" {
		return report{}, fmt.Errorf("Go oracle identity mismatch: version=%s build_path=%s module=%s revision=%s modified=%s", goInfo.GoVersion, goInfo.BuildPath, goInfo.ModulePath, goInfo.Revision, goInfo.Modified)
	}
	rustInfo, err := inspectFileBinary(rustBinary)
	if err != nil {
		return report{}, err
	}

	cases := []namedCase{
		{
			ID: "no-vault-default-text",
			Input: portdiff.Case{
				Args:         []string{"config", "paths"},
				CompareFiles: true,
			},
		},
		{
			ID: "effective-vault-flag-json",
			Input: portdiff.Case{
				Args:         []string{"--vault=${WORKSPACE}/vault", "config", "paths", "--output=json"},
				Setup:        []portdiff.SetupFile{{Path: "vault/marker.md", Content: "# differential vault\n"}},
				CompareFiles: true,
			},
		},
	}
	if len(cases) == 0 {
		return report{}, errors.New("config paths case inventory is empty")
	}
	result := report{
		SchemaVersion: 1,
		Worktree:      root,
		Origin:        origin,
		Branch:        branch,
		Head:          head,
		GoSource:      goSource,
		RustSource:    rustSource,
		GoBinary:      goInfo,
		RustBinary:    rustInfo,
		GoRunner:      runtime.Version(),
		CaseCount:     len(cases),
		Cases:         cases,
		StatusBefore:  statusBefore,
	}
	if runtime.Version() != oracleGoVersion {
		return result, fmt.Errorf("capture producer must run on %s, got %s", oracleGoVersion, runtime.Version())
	}

	for index := range result.Cases {
		entry := &result.Cases[index]
		caseDir := filepath.Join(evidenceDir, "cases", entry.ID)
		if err := os.MkdirAll(caseDir, 0o700); err != nil {
			return result, err
		}
		if err := writePrivate(filepath.Join(caseDir, "input.json"), append(mustJSON(entry.Input), '\n')); err != nil {
			return result, err
		}
		runRoot := filepath.Join(caseDir, "sandbox")
		goResult, goErr := portdiff.RunAt(goInfo.Path, entry.Input, runRoot)
		if goErr == nil {
			entry.Go = describe(goResult)
			if err := writeResult(caseDir, "go", goResult); err != nil {
				return result, err
			}
		} else {
			entry.Error = "Go: " + goErr.Error()
		}
		if err := os.RemoveAll(runRoot); err != nil {
			return result, fmt.Errorf("reset same-path sandbox for %s: %w", entry.ID, err)
		}
		rustResult, rustErr := portdiff.RunAt(rustInfo.Path, entry.Input, runRoot)
		if rustErr == nil {
			entry.Rust = describe(rustResult)
			if err := writeResult(caseDir, "rust", rustResult); err != nil {
				return result, err
			}
		} else {
			if entry.Error != "" {
				entry.Error += "; "
			}
			entry.Error += "Rust: " + rustErr.Error()
		}
		result.ExecutedCount++
		if goErr != nil || rustErr != nil {
			entry.Result = "failed"
			continue
		}
		if err := portdiff.Compare(entry.Input, goResult, rustResult); err != nil {
			entry.Result = "failed"
			entry.Error = err.Error()
			continue
		}
		entry.Result = "passed"
		result.PassedCount++
		if result.Mutation.CaseID == "" {
			result.Mutation = mutationControl(caseDir, entry.ID, entry.Input, goResult, rustResult)
		}
	}
	if result.ExecutedCount == 0 {
		return result, errors.New("zero config paths cases executed")
	}
	if result.ExecutedCount != result.CaseCount {
		return result, fmt.Errorf("executed case count %d differs from declared count %d", result.ExecutedCount, result.CaseCount)
	}
	if result.Mutation.CaseID == "" || !result.Mutation.ComparatorRejected || !result.Mutation.RetainedInputUnchanged {
		return result, errors.New("mutation control did not prove comparator rejection without changing retained input")
	}
	if result.PassedCount != result.CaseCount {
		return result, fmt.Errorf("differential mismatch: %d of %d cases passed", result.PassedCount, result.CaseCount)
	}
	result.SourceAfter, err = sourceManifest(root, head, append(append([]string(nil), goSourceFiles...), rustSourceFiles...))
	if err != nil {
		return result, err
	}
	result.HeadAfter, err = git(root, "rev-parse", "HEAD")
	if err != nil {
		return result, err
	}
	result.StatusAfter, err = git(root, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return result, err
	}
	if result.HeadAfter != head {
		return result, errors.New("candidate HEAD changed during differential execution")
	}
	for _, name := range goSourceFiles {
		if result.SourceAfter.Files[name] != goSource.Files[name] {
			return result, fmt.Errorf("Go source changed during execution: %s", name)
		}
	}
	for _, name := range rustSourceFiles {
		if result.SourceAfter.Files[name] != rustSource.Files[name] {
			return result, fmt.Errorf("Rust source changed during execution: %s", name)
		}
	}
	return result, nil
}

func mutationControl(caseDir, caseID string, testCase portdiff.Case, oracle, candidate portdiff.Result) mutationEvidence {
	inputPath := filepath.Join(caseDir, "go.stdout")
	original, err := os.ReadFile(inputPath)
	if err != nil {
		return mutationEvidence{CaseID: caseID, Field: "go.stdout[0]", Reason: "read retained source: " + err.Error()}
	}
	mutated := cloneResult(oracle)
	if len(mutated.Stdout) == 0 {
		mutated.Stdout = []byte{0x01}
	} else {
		mutated.Stdout[0] ^= 0x01
	}
	compareErr := portdiff.Compare(testCase, mutated, candidate)
	readback, readErr := os.ReadFile(inputPath)
	return mutationEvidence{
		CaseID:                 caseID,
		Field:                  "go.stdout[0]",
		ComparatorRejected:     compareErr != nil,
		RetainedInputUnchanged: readErr == nil && bytes.Equal(original, readback),
		Reason:                 errorString(compareErr),
	}
}

func cloneResult(result portdiff.Result) portdiff.Result {
	result.Stdout = append([]byte(nil), result.Stdout...)
	result.Stderr = append([]byte(nil), result.Stderr...)
	result.FilesBefore = append([]portdiff.ManifestEntry(nil), result.FilesBefore...)
	result.Files = append([]portdiff.ManifestEntry(nil), result.Files...)
	return result
}

func describe(result portdiff.Result) *processEvidence {
	return &processEvidence{
		ExitCode:     result.ExitCode,
		Signal:       result.Signal,
		TimedOut:     result.TimedOut,
		StdoutBytes:  len(result.Stdout),
		StdoutSHA256: digest(result.Stdout),
		StderrBytes:  len(result.Stderr),
		StderrSHA256: digest(result.Stderr),
		FilesBefore:  result.FilesBefore,
		FilesAfter:   result.Files,
	}
}

func writeResult(directory, side string, result portdiff.Result) error {
	files := []struct {
		name string
		data []byte
	}{
		{side + ".stdout", result.Stdout},
		{side + ".stderr", result.Stderr},
		{side + ".files.before.json", mustJSON(result.FilesBefore)},
		{side + ".files.after.json", mustJSON(result.Files)},
	}
	for _, file := range files {
		if err := writePrivate(filepath.Join(directory, file.name), file.data); err != nil {
			return err
		}
	}
	status := struct {
		ExitCode int    `json:"exit_code"`
		Signal   string `json:"signal,omitempty"`
		TimedOut bool   `json:"timed_out"`
	}{result.ExitCode, result.Signal, result.TimedOut}
	return writePrivate(filepath.Join(directory, side+".status.json"), append(mustJSON(status), '\n'))
}

func sourceManifest(root, commit string, files []string) (sourceIdentity, error) {
	values := make(map[string]string, len(files))
	for _, relative := range files {
		content, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil {
			return sourceIdentity{}, fmt.Errorf("read source %s: %w", relative, err)
		}
		values[relative] = digest(content)
	}
	tree, err := git(root, "rev-parse", commit+"^{tree}")
	if err != nil {
		return sourceIdentity{}, err
	}
	return sourceIdentity{Commit: commit, Tree: tree, Files: values}, nil
}

func verifyFilesAtCommit(root, commit string, files []string, hashes map[string]string) error {
	for _, relative := range files {
		command := exec.Command("git", "-C", root, "show", commit+":"+relative)
		content, err := command.Output()
		if err != nil {
			return fmt.Errorf("read pinned Go source %s: %w", relative, err)
		}
		if got := digest(content); got != hashes[relative] {
			return fmt.Errorf("Go production source differs from pinned commit at %s", relative)
		}
	}
	return nil
}

func inspectGoBinary(path string) (binaryIdentity, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return binaryIdentity{}, err
	}
	info, err := buildinfo.ReadFile(absolute)
	if err != nil {
		return binaryIdentity{}, fmt.Errorf("read Go oracle build info: %w", err)
	}
	identity, err := inspectFileBinary(absolute)
	if err != nil {
		return binaryIdentity{}, err
	}
	identity.GoVersion = info.GoVersion
	identity.BuildPath = info.Path
	identity.ModulePath = info.Main.Path
	identity.Version = info.Main.Version
	identity.Revision = buildSetting(info, "vcs.revision")
	identity.Modified = buildSetting(info, "vcs.modified")
	return identity, nil
}

func inspectFileBinary(path string) (binaryIdentity, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return binaryIdentity{}, err
	}
	content, err := os.ReadFile(absolute)
	if err != nil {
		return binaryIdentity{}, err
	}
	return binaryIdentity{Path: absolute, SHA256: digest(content)}, nil
}

func buildSetting(info *buildinfo.BuildInfo, key string) string {
	for _, setting := range info.Settings {
		if setting.Key == key {
			return setting.Value
		}
	}
	return ""
}

func git(root string, args ...string) (string, error) {
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func digest(content []byte) string {
	value := sha256.Sum256(content)
	return hex.EncodeToString(value[:])
}

func mustJSON(value any) []byte {
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		panic(err)
	}
	return append(content, '\n')
}

func writePrivate(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, content, 0o600)
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func fatal(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, "FAIL "+format+"\n", args...)
	os.Exit(1)
}
