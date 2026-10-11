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
	"go.mod",
	"go.sum",
	"cmd/symdesk/main.go",
	"cmd/symdesk/commands.go",
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

var harnessSourceFiles = []string{
	"scripts/rust-port/cmd/config-paths-diff/main.go",
	"scripts/rust-port/cmd/config-paths-diff/main_test.go",
	"scripts/rust-port/cmd/config-paths-diff/preflight.go",
	"scripts/rust-port/cmd/config-paths-diff/cases.go",
	"scripts/rust-port/internal/diff/case.go",
	"scripts/rust-port/internal/diff/compare.go",
	"scripts/rust-port/internal/diff/capture.go",
	"scripts/rust-port/internal/diff/config_paths_test.go",
	"scripts/rust-port/internal/diff/exec.go",
	"scripts/rust-port/internal/diff/manifest.go",
	"scripts/rust-port/internal/diff/process_unix.go",
	"scripts/rust-port/internal/diff/process_windows.go",
	"scripts/rust-port/internal/diff/signal_unix.go",
	"scripts/rust-port/internal/diff/signal_windows.go",
}

var corekitSourceFiles = []string{"go.mod", "configkit/configkit.go", "exitcodes/exitcodes.go"}

type sourceIdentity struct {
	Commit          string            `json:"commit"`
	Tree            string            `json:"tree"`
	Inputs          []string          `json:"inputs"`
	Missing         []string          `json:"missing_inputs,omitempty"`
	InventoryErrors []string          `json:"inventory_errors,omitempty"`
	Files           map[string]string `json:"files_sha256"`
}

type binaryIdentity struct {
	Path             string `json:"path"`
	SHA256           string `json:"sha256"`
	GoVersion        string `json:"go_version,omitempty"`
	BuildPath        string `json:"build_path,omitempty"`
	ModulePath       string `json:"module_path,omitempty"`
	Version          string `json:"module_version,omitempty"`
	Revision         string `json:"vcs_revision,omitempty"`
	Modified         string `json:"vcs_modified,omitempty"`
	CoreKitVersion   string `json:"corekit_version,omitempty"`
	CoreKitModuleSum string `json:"corekit_module_sum,omitempty"`
}

type moduleIdentity struct {
	Path      string            `json:"path"`
	Version   string            `json:"version"`
	ModuleSum string            `json:"module_sum"`
	Directory string            `json:"directory"`
	Files     map[string]string `json:"files_sha256"`
}

type namedCase struct {
	ID       string           `json:"id"`
	Category string           `json:"category"`
	Platform string           `json:"platform"`
	Input    portdiff.Case    `json:"input"`
	Go       *processEvidence `json:"go,omitempty"`
	Rust     *processEvidence `json:"rust,omitempty"`
	Result   string           `json:"result"`
	Error    string           `json:"error,omitempty"`
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
	SchemaVersion  int              `json:"schema_version"`
	Worktree       string           `json:"worktree"`
	Origin         string           `json:"origin"`
	Branch         string           `json:"branch"`
	Head           string           `json:"head"`
	GoSource       sourceIdentity   `json:"go_source"`
	RustSource     sourceIdentity   `json:"rust_source"`
	HarnessSource  sourceIdentity   `json:"harness_source"`
	GoCoreKit      moduleIdentity   `json:"go_corekit_source"`
	GoCoreKitAfter moduleIdentity   `json:"go_corekit_source_after"`
	GoBinary       binaryIdentity   `json:"go_binary"`
	HarnessBinary  binaryIdentity   `json:"harness_binary"`
	RustBinary     binaryIdentity   `json:"rust_binary"`
	GoRunner       string           `json:"go_runner_version"`
	HostOS         string           `json:"host_os"`
	HostArch       string           `json:"host_arch"`
	CaseCount      int              `json:"declared_case_count"`
	ExecutedCount  int              `json:"executed_case_count"`
	SkippedCount   int              `json:"skipped_platform_case_count"`
	PassedCount    int              `json:"passed_case_count"`
	Mutation       mutationEvidence `json:"mutation_control"`
	Cases          []namedCase      `json:"cases"`
	SourceAfter    sourceIdentity   `json:"source_after"`
	HeadAfter      string           `json:"head_after"`
	StatusBefore   string           `json:"git_status_before"`
	StatusAfter    string           `json:"git_status_after"`
	CandidateClean bool             `json:"candidate_clean"`
}

type preflightFailure struct {
	SchemaVersion        int               `json:"schema_version"`
	Status               string            `json:"status"`
	Phase                string            `json:"phase"`
	Error                string            `json:"error"`
	ExitCode             int               `json:"exit_code"`
	Worktree             string            `json:"worktree"`
	Origin               string            `json:"origin"`
	Branch               string            `json:"branch"`
	Head                 string            `json:"head"`
	GitStatus            string            `json:"git_status"`
	GoSource             sourceIdentity    `json:"go_source"`
	RustSource           sourceIdentity    `json:"rust_source"`
	HarnessSource        sourceIdentity    `json:"harness_source"`
	GoCoreKit            moduleIdentity    `json:"go_corekit_source"`
	GoCoreKitInputs      []string          `json:"go_corekit_inputs"`
	GoCoreKitMissing     []string          `json:"go_corekit_missing_inputs,omitempty"`
	GoCoreKitFiles       map[string]string `json:"go_corekit_files_sha256"`
	GoBinary             binaryIdentity    `json:"go_binary"`
	GoBinaryError        string            `json:"go_binary_error,omitempty"`
	HarnessBinary        binaryIdentity    `json:"harness_binary"`
	HarnessBinaryError   string            `json:"harness_binary_error,omitempty"`
	RustBinary           binaryIdentity    `json:"rust_binary"`
	RustBinaryError      string            `json:"rust_binary_error,omitempty"`
	IdentityErrors       []string          `json:"identity_errors,omitempty"`
	GoRunner             string            `json:"go_runner_version"`
	HostOS               string            `json:"host_os"`
	HostArch             string            `json:"host_arch"`
	DeclaredCaseCount    int               `json:"declared_case_count"`
	ExecutedCaseCount    int               `json:"executed_case_count"`
	NativeCaptureClaimed bool              `json:"native_capture_claimed"`
	StdoutFile           string            `json:"stdout_file"`
	StdoutBytes          int               `json:"stdout_bytes"`
	StdoutSHA256         string            `json:"stdout_sha256"`
	StderrFile           string            `json:"stderr_file"`
	StderrBytes          int               `json:"stderr_bytes"`
	StderrSHA256         string            `json:"stderr_sha256"`
}

func main() {
	goBinary := flag.String("go-binary", "", "pinned production symdesk binary")
	rustBinary := flag.String("rust-binary", "", "candidate symdesk binary")
	repoRoot := flag.String("repo-root", "", "assigned candidate checkout root")
	rustManifest := flag.String("rust-manifest", "", "absolute symdesk-cli Cargo manifest")
	evidenceDir := flag.String("evidence-dir", "", "new external directory for retained evidence")
	corekitDir := flag.String("corekit-dir", "", "actual directory for the pinned symaira-corekit Go module")
	flag.Parse()
	if *goBinary == "" || *rustBinary == "" || *repoRoot == "" || *rustManifest == "" || *evidenceDir == "" || *corekitDir == "" {
		fatal("--go-binary, --rust-binary, --repo-root, --rust-manifest, --evidence-dir and --corekit-dir are required")
	}

	reportValue, captureErr := capture(*goBinary, *rustBinary, *repoRoot, *rustManifest, *evidenceDir, *corekitDir)
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
		failureStderr := []byte(fmt.Sprintf("FAIL capture failed; retained evidence at %s: %v\n", *evidenceDir, captureErr))
		if reportValue.ExecutedCount == 0 {
			if err := retainPreflightFailure(
				*evidenceDir,
				*goBinary,
				*rustBinary,
				*repoRoot,
				*corekitDir,
				reportValue,
				captureErr,
				[]byte{},
				failureStderr,
			); err != nil {
				failureStderr = []byte(fmt.Sprintf("FAIL capture failed; failed to retain preflight evidence at %s: %v; capture error: %v\n", *evidenceDir, err, captureErr))
			}
		}
		_, _ = os.Stderr.Write(failureStderr)
		os.Exit(1)
	}
	fmt.Printf("PASS %d/%d config paths real-process cases; evidence=%s\n", reportValue.PassedCount, reportValue.CaseCount, *evidenceDir)
}

func capture(goBinary, rustBinary, repoRoot, rustManifest, evidenceDir, corekitDir string) (report, error) {
	evidenceDir, err := filepath.Abs(evidenceDir)
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
	corekitDir, err = filepath.Abs(corekitDir)
	if err != nil {
		return report{}, err
	}
	if resolved, err := filepath.EvalSymlinks(corekitDir); err == nil {
		corekitDir = resolved
	}
	if filepath.Base(corekitDir) != "symaira-corekit@v0.18.2" {
		return report{}, fmt.Errorf("CoreKit source is not the pinned v0.18.2 module directory: %s", corekitDir)
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
	harnessSource, err := sourceManifest(root, head, harnessSourceFiles)
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
	if goInfo.CoreKitVersion != "v0.18.2" || goInfo.CoreKitModuleSum == "" {
		return report{}, fmt.Errorf("Go oracle did not embed the pinned CoreKit dependency: version=%s sum=%s", goInfo.CoreKitVersion, goInfo.CoreKitModuleSum)
	}
	goCoreKit, err := moduleManifest(corekitDir, goInfo)
	if err != nil {
		return report{}, err
	}
	rustInfo, err := inspectFileBinary(rustBinary)
	if err != nil {
		return report{}, err
	}
	runnerPath, err := os.Executable()
	if err != nil {
		return report{}, fmt.Errorf("resolve differential harness executable: %w", err)
	}
	runnerInfo, err := inspectGoBinary(runnerPath)
	if err != nil {
		return report{}, fmt.Errorf("inspect differential harness binary: %w", err)
	}

	cases := configPathCases()
	if len(cases) == 0 {
		return report{}, errors.New("config paths case inventory is empty")
	}
	caseIDs := make(map[string]struct{}, len(cases))
	skippedCount := 0
	for index := range cases {
		entry := &cases[index]
		if entry.ID == "" || entry.Input.ID != entry.ID || entry.Category == "" {
			return report{}, fmt.Errorf("incomplete named config paths case at index %d", index)
		}
		if _, exists := caseIDs[entry.ID]; exists {
			return report{}, fmt.Errorf("duplicate config paths case ID %q", entry.ID)
		}
		caseIDs[entry.ID] = struct{}{}
		applicable, err := platformApplicable(entry.Platform, runtime.GOOS)
		if err != nil {
			return report{}, fmt.Errorf("case %s: %w", entry.ID, err)
		}
		if !applicable {
			entry.Result = "not_applicable"
			entry.Error = "requires " + entry.Platform + " filesystem semantics; host=" + runtime.GOOS
			skippedCount++
		}
	}
	result := report{
		SchemaVersion:  1,
		Worktree:       root,
		Origin:         origin,
		Branch:         branch,
		Head:           head,
		GoSource:       goSource,
		RustSource:     rustSource,
		HarnessSource:  harnessSource,
		GoCoreKit:      goCoreKit,
		GoBinary:       goInfo,
		HarnessBinary:  runnerInfo,
		RustBinary:     rustInfo,
		GoRunner:       runtime.Version(),
		HostOS:         runtime.GOOS,
		HostArch:       runtime.GOARCH,
		CaseCount:      len(cases),
		SkippedCount:   skippedCount,
		Cases:          cases,
		StatusBefore:   statusBefore,
		CandidateClean: statusBefore == "",
	}
	if runtime.Version() != oracleGoVersion {
		return result, fmt.Errorf("capture producer must run on %s, got %s", oracleGoVersion, runtime.Version())
	}

	for index := range result.Cases {
		entry := &result.Cases[index]
		if entry.Result == "not_applicable" {
			continue
		}
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
	if result.ExecutedCount+result.SkippedCount != result.CaseCount {
		return result, fmt.Errorf("executed plus explicitly skipped case count %d differs from declared count %d", result.ExecutedCount+result.SkippedCount, result.CaseCount)
	}
	if result.Mutation.CaseID == "" || !result.Mutation.ComparatorRejected || !result.Mutation.RetainedInputUnchanged {
		return result, errors.New("mutation control did not prove comparator rejection without changing retained input")
	}
	if result.PassedCount != result.ExecutedCount {
		return result, fmt.Errorf("differential mismatch: %d of %d executed cases passed", result.PassedCount, result.ExecutedCount)
	}
	allSourceFiles := append(append(append([]string(nil), goSourceFiles...), rustSourceFiles...), harnessSourceFiles...)
	result.SourceAfter, err = sourceManifest(root, head, allSourceFiles)
	if err != nil {
		return result, err
	}
	result.GoCoreKitAfter, err = moduleManifest(corekitDir, goInfo)
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
	result.CandidateClean = result.StatusAfter == ""
	if result.HeadAfter != head {
		return result, errors.New("candidate HEAD changed during differential execution")
	}
	if result.StatusAfter != statusBefore {
		return result, errors.New("candidate worktree status changed during differential execution")
	}
	for _, name := range allSourceFiles {
		before := goSource.Files[name]
		if _, ok := rustSource.Files[name]; ok {
			before = rustSource.Files[name]
		}
		if _, ok := harnessSource.Files[name]; ok {
			before = harnessSource.Files[name]
		}
		if result.SourceAfter.Files[name] != before {
			return result, fmt.Errorf("candidate source changed during execution: %s", name)
		}
	}
	if !sameModuleIdentity(goCoreKit, result.GoCoreKitAfter) {
		return result, errors.New("pinned CoreKit source changed during differential execution")
	}
	if !result.CandidateClean {
		return result, errors.New("candidate checkout was dirty; retained comparison is diagnostic, not clean-candidate evidence")
	}
	return result, nil
}

func platformApplicable(platform, goos string) (bool, error) {
	switch platform {
	case "any":
		return true, nil
	case "unix":
		return goos != "windows", nil
	default:
		return false, fmt.Errorf("unknown platform label %q", platform)
	}
}

func moduleManifest(directory string, binary binaryIdentity) (moduleIdentity, error) {
	const modulePath = "github.com/danieljustus/symaira-corekit"
	if binary.CoreKitVersion != "v0.18.2" || binary.CoreKitModuleSum == "" {
		return moduleIdentity{}, fmt.Errorf("unexpected CoreKit binary identity: %s %s", binary.CoreKitVersion, binary.CoreKitModuleSum)
	}
	files := make(map[string]string, len(corekitSourceFiles))
	for _, relative := range corekitSourceFiles {
		content, err := os.ReadFile(filepath.Join(directory, relative))
		if err != nil {
			return moduleIdentity{}, fmt.Errorf("read pinned CoreKit source %s: %w", relative, err)
		}
		files[relative] = digest(content)
		if relative == "go.mod" && !bytes.Contains(content, []byte("module "+modulePath+"\n")) {
			return moduleIdentity{}, fmt.Errorf("CoreKit go.mod does not declare %s", modulePath)
		}
	}
	return moduleIdentity{
		Path:      modulePath,
		Version:   binary.CoreKitVersion,
		ModuleSum: binary.CoreKitModuleSum,
		Directory: directory,
		Files:     files,
	}, nil
}

func sameModuleIdentity(left, right moduleIdentity) bool {
	if left.Path != right.Path || left.Version != right.Version || left.ModuleSum != right.ModuleSum || left.Directory != right.Directory || len(left.Files) != len(right.Files) {
		return false
	}
	for name, digest := range left.Files {
		if right.Files[name] != digest {
			return false
		}
	}
	return true
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
	identity := sourceIdentity{
		Commit: commit,
		Inputs: append([]string(nil), files...),
		Files:  make(map[string]string, len(files)),
	}
	tree, err := git(root, "rev-parse", commit+"^{tree}")
	if err != nil {
		identity.InventoryErrors = append(identity.InventoryErrors, "source tree: "+err.Error())
		return identity, err
	}
	identity.Tree = tree
	for _, relative := range files {
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			identity.Missing = append(identity.Missing, relative)
			identity.InventoryErrors = append(identity.InventoryErrors, fmt.Sprintf("read %s: %v", relative, err))
			return identity, fmt.Errorf("read source %s: %w", relative, err)
		}
		identity.Files[relative] = digest(content)
	}
	return identity, nil
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
	for _, dependency := range info.Deps {
		if dependency.Path != "github.com/danieljustus/symaira-corekit" {
			continue
		}
		if dependency.Replace != nil {
			return binaryIdentity{}, errors.New("Go oracle replaces the pinned CoreKit module")
		}
		identity.CoreKitVersion = dependency.Version
		identity.CoreKitModuleSum = dependency.Sum
	}
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
