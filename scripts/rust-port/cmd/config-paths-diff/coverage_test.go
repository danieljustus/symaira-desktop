package main

import (
	"bytes"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"

	portdiff "github.com/danieljustus/symaira-desktop/scripts/rust-port/internal/diff"
)

func TestConfigPathCaseInventoryIsWellFormed(t *testing.T) {
	cases := configPathCases()
	if len(cases) == 0 {
		t.Fatal("config paths case inventory is empty")
	}

	ids := make(map[string]struct{}, len(cases))
	platforms := make(map[string]bool)
	for _, entry := range cases {
		if entry.ID == "" || entry.Input.ID != entry.ID || entry.Category == "" {
			t.Errorf("incomplete named case: %#v", entry)
		}
		if _, exists := ids[entry.ID]; exists {
			t.Errorf("duplicate case ID %q", entry.ID)
		}
		ids[entry.ID] = struct{}{}
		if _, err := platformApplicable(entry.Platform, runtime.GOOS); err != nil {
			t.Errorf("case %q has invalid platform %q: %v", entry.ID, entry.Platform, err)
		}
		platforms[entry.Platform] = true
	}
	for _, required := range []string{
		"default-no-vault-text",
		"invalid-output-valid-config",
		"retrieval-json-cold-migration",
		"no-home-preflight-error-not-waived-by-overrides",
	} {
		if _, ok := ids[required]; !ok {
			t.Errorf("case inventory omitted %q", required)
		}
	}
	if !platforms["any"] || !platforms["unix"] {
		t.Fatalf("case inventory must retain portable and Unix-only cases; platforms=%v", platforms)
	}
}

func TestPlatformApplicableRejectsUnknownLabels(t *testing.T) {
	for _, test := range []struct {
		platform string
		goos     string
		want     bool
		wantErr  bool
	}{
		{platform: "any", goos: "windows", want: true},
		{platform: "unix", goos: "darwin", want: true},
		{platform: "unix", goos: "windows", want: false},
		{platform: "linux", goos: "linux", wantErr: true},
	} {
		got, err := platformApplicable(test.platform, test.goos)
		if test.wantErr {
			if err == nil || !strings.Contains(err.Error(), "unknown platform label") {
				t.Errorf("platformApplicable(%q, %q) error = %v", test.platform, test.goos, err)
			}
			continue
		}
		if err != nil || got != test.want {
			t.Errorf("platformApplicable(%q, %q) = (%t, %v), want (%t, nil)", test.platform, test.goos, got, err, test.want)
		}
	}
}

func TestCaptureRejectsUnsafeProvenanceInputs(t *testing.T) {
	t.Run("existing evidence is never overwritten", func(t *testing.T) {
		evidence := filepath.Join(t.TempDir(), "evidence")
		if err := os.Mkdir(evidence, 0o700); err != nil {
			t.Fatal(err)
		}
		original := []byte("retained report\n")
		if err := os.WriteFile(filepath.Join(evidence, "report.json"), original, 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := capture("", "", "", "", evidence, "")
		if err == nil || !strings.Contains(err.Error(), "evidence directory already exists") {
			t.Fatalf("capture error = %v, want refusal to overwrite evidence", err)
		}
		got, err := os.ReadFile(filepath.Join(evidence, "report.json")) // #nosec G304 -- fixed retained fixture inside t.TempDir.
		if err != nil || !bytes.Equal(got, original) {
			t.Fatalf("existing evidence changed: bytes=%q err=%v", got, err)
		}
	})

	t.Run("manifest must be inside the assigned checkout", func(t *testing.T) {
		root := t.TempDir()
		evidence := filepath.Join(t.TempDir(), "evidence")
		_, err := capture("", "", root, filepath.Join(root, "other", "Cargo.toml"), evidence, "")
		if err == nil || !strings.Contains(err.Error(), "not the assigned symdesk-cli manifest") {
			t.Fatalf("capture error = %v, want assigned-manifest rejection", err)
		}
		if info, statErr := os.Stat(evidence); statErr != nil || !info.IsDir() {
			t.Fatalf("capture did not isolate its retained evidence directory: info=%v err=%v", info, statErr)
		}
	})

	t.Run("CoreKit directory must carry the pinned version", func(t *testing.T) {
		root, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		evidence := filepath.Join(t.TempDir(), "evidence")
		manifest := filepath.Join(root, "crates", "symdesk-cli", "Cargo.toml")
		wrongCoreKit := filepath.Join(t.TempDir(), "corekit-unpinned")
		_, err = capture("", "", root, manifest, evidence, wrongCoreKit)
		if err == nil || !strings.Contains(err.Error(), "not the pinned "+corekitVersion+" module directory") {
			t.Fatalf("capture error = %v, want pinned-CoreKit rejection", err)
		}
	})

	t.Run("main branch is not an isolated candidate", func(t *testing.T) {
		root := newCoverageGitRepo(t, "main", originURL)
		evidence := filepath.Join(t.TempDir(), "evidence")
		corekit := filepath.Join(t.TempDir(), "symaira-corekit@"+corekitVersion)
		manifest := filepath.Join(root, "crates", "symdesk-cli", "Cargo.toml")
		_, err := capture("", "", root, manifest, evidence, corekit)
		if err == nil || !strings.Contains(err.Error(), "refusing non-isolated branch") {
			t.Fatalf("capture error = %v, want branch isolation rejection", err)
		}
	})

	t.Run("candidate must descend from the frozen foundation", func(t *testing.T) {
		root := newCoverageGitRepo(t, "candidate", originURL)
		evidence := filepath.Join(t.TempDir(), "evidence")
		corekit := filepath.Join(t.TempDir(), "symaira-corekit@"+corekitVersion)
		manifest := filepath.Join(root, "crates", "symdesk-cli", "Cargo.toml")
		_, err := capture("", "", root, manifest, evidence, corekit)
		if err == nil || !strings.Contains(err.Error(), "foundation is not an ancestor of candidate HEAD") {
			t.Fatalf("capture error = %v, want foundation ancestry rejection", err)
		}
	})
}

func TestSourceManifestAndPinnedCommitVerification(t *testing.T) {
	root := newCoverageGitRepo(t, "candidate", "")
	commit := mustCoverageGit(t, root, "rev-parse", "HEAD")
	files := []string{"source.txt"}

	identity, err := sourceManifest(root, commit, files)
	if err != nil {
		t.Fatalf("sourceManifest: %v", err)
	}
	if identity.Commit != commit || identity.Tree == "" || len(identity.Inputs) != 1 || identity.Inputs[0] != files[0] {
		t.Fatalf("incomplete source identity: %#v", identity)
	}
	if got, want := identity.Files[files[0]], coverageSHA256([]byte("initial source\n")); got != want {
		t.Fatalf("source hash = %q, want %q", got, want)
	}
	if err := verifyFilesAtCommit(root, commit, files, identity.Files); err != nil {
		t.Fatalf("pinned source verification: %v", err)
	}

	coverageWriteFile(t, root, files[0], "tampered working tree\n")
	workingTree, err := sourceManifest(root, commit, files)
	if err != nil {
		t.Fatalf("sourceManifest after tampering: %v", err)
	}
	if workingTree.Files[files[0]] == identity.Files[files[0]] {
		t.Fatal("working-tree hash did not change after source tampering")
	}
	if err := verifyFilesAtCommit(root, commit, files, workingTree.Files); err == nil || !strings.Contains(err.Error(), "differs from pinned commit") {
		t.Fatalf("tampered source hash verification error = %v, want provenance mismatch", err)
	}
	if err := verifyFilesAtCommit(root, commit, files, identity.Files); err != nil {
		t.Fatalf("original pinned source no longer verifies: %v", err)
	}

	missing, err := sourceManifest(root, commit, []string{"missing.txt"})
	if err == nil || len(missing.Missing) != 1 || missing.Missing[0] != "missing.txt" || len(missing.InventoryErrors) == 0 {
		t.Fatalf("missing source inventory = %#v, err=%v", missing, err)
	}
	if err := verifyFilesAtCommit(root, commit, []string{"missing.txt"}, map[string]string{}); err == nil {
		t.Fatal("pinned commit verification accepted a missing source")
	}
	invalidCommit, err := sourceManifest(root, "not-a-commit", files)
	if err == nil || len(invalidCommit.InventoryErrors) == 0 {
		t.Fatalf("invalid commit identity = %#v, err=%v", invalidCommit, err)
	}
	if _, err := git(root, "not-a-git-command"); err == nil || !strings.Contains(err.Error(), "git not-a-git-command") {
		t.Fatalf("git helper error = %v, want command context", err)
	}
}

func TestModuleManifestAndIdentityRejectUnpinnedInputs(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"go.mod":                 "module github.com/danieljustus/symaira-corekit\n",
		"configkit/configkit.go": "package configkit\n",
		"exitcodes/exitcodes.go": "package exitcodes\n",
	}
	for path, content := range files {
		coverageWriteFile(t, root, path, content)
	}
	binary := binaryIdentity{CoreKitVersion: corekitVersion, CoreKitModuleSum: "h1:locked-module-sum"}
	identity, err := moduleManifest(root, binary)
	if err != nil {
		t.Fatalf("moduleManifest: %v", err)
	}
	if identity.Path != "github.com/danieljustus/symaira-corekit" || identity.Version != binary.CoreKitVersion || identity.ModuleSum != binary.CoreKitModuleSum || identity.Directory != root {
		t.Fatalf("module identity metadata = %#v", identity)
	}
	if len(identity.Files) != len(corekitSourceFiles) {
		t.Fatalf("CoreKit source closure has %d files, want %d", len(identity.Files), len(corekitSourceFiles))
	}
	for path, content := range files {
		if identity.Files[path] != coverageSHA256([]byte(content)) {
			t.Errorf("CoreKit hash for %q = %q", path, identity.Files[path])
		}
	}
	if !sameModuleIdentity(identity, identity) {
		t.Fatal("identical CoreKit source identities differ")
	}
	changedFiles := make(map[string]string, len(identity.Files))
	for path, hash := range identity.Files {
		changedFiles[path] = hash
	}
	changedFiles["go.mod"] = "tampered"
	changed := identity
	changed.Files = changedFiles
	if sameModuleIdentity(identity, changed) {
		t.Fatal("CoreKit source hash mutation was accepted")
	}
	changed = identity
	changed.Version = "v0.18.1"
	if sameModuleIdentity(identity, changed) {
		t.Fatal("CoreKit version mutation was accepted")
	}

	wrongVersion := binary
	wrongVersion.CoreKitVersion = "v0.18.1"
	if _, err := moduleManifest(root, wrongVersion); err == nil || !strings.Contains(err.Error(), "unexpected CoreKit binary identity") {
		t.Fatalf("wrong binary version error = %v", err)
	}

	wrongModuleRoot := t.TempDir()
	coverageWriteFile(t, wrongModuleRoot, "go.mod", "module example.invalid/other\n")
	coverageWriteFile(t, wrongModuleRoot, "configkit/configkit.go", "package configkit\n")
	coverageWriteFile(t, wrongModuleRoot, "exitcodes/exitcodes.go", "package exitcodes\n")
	if _, err := moduleManifest(wrongModuleRoot, binary); err == nil || !strings.Contains(err.Error(), "does not declare github.com/danieljustus/symaira-corekit") {
		t.Fatalf("wrong module path error = %v", err)
	}

	missingRoot := t.TempDir()
	coverageWriteFile(t, missingRoot, "go.mod", files["go.mod"])
	if _, err := moduleManifest(missingRoot, binary); err == nil || !strings.Contains(err.Error(), "read pinned CoreKit source") {
		t.Fatalf("missing source error = %v", err)
	}
}

func TestMutationControlRejectsChangesWithoutMutatingRetainedEvidence(t *testing.T) {
	for _, stdout := range []string{"retained Go output", ""} {
		t.Run("stdout-bytes-"+strings.ReplaceAll(stdout, " ", "-"), func(t *testing.T) {
			caseDir := t.TempDir()
			original := []byte(stdout)
			if err := os.WriteFile(filepath.Join(caseDir, "go.stdout"), original, 0o600); err != nil {
				t.Fatal(err)
			}
			oracle := portdiff.Result{Stdout: append([]byte(nil), original...), Stderr: []byte("same stderr")}
			candidate := portdiff.Result{Stdout: append([]byte(nil), original...), Stderr: []byte("same stderr")}
			got := mutationControl(caseDir, "case-1", portdiff.Case{ID: "case-1"}, oracle, candidate)
			if got.CaseID != "case-1" || got.Field != "go.stdout[0]" || !got.ComparatorRejected || !got.RetainedInputUnchanged || got.Reason == "" {
				t.Fatalf("mutation control did not prove isolated rejection: %#v", got)
			}
			if !bytes.Equal(oracle.Stdout, original) || !bytes.Equal(candidate.Stdout, original) {
				t.Fatalf("mutation control changed an in-memory result: oracle=%q candidate=%q", oracle.Stdout, candidate.Stdout)
			}
			retained, err := os.ReadFile(filepath.Join(caseDir, "go.stdout")) // #nosec G304 -- fixed retained fixture inside t.TempDir.
			if err != nil || !bytes.Equal(retained, original) {
				t.Fatalf("mutation control changed retained source: bytes=%q err=%v", retained, err)
			}
		})
	}

	missing := mutationControl(t.TempDir(), "missing", portdiff.Case{ID: "missing"}, portdiff.Result{}, portdiff.Result{})
	if missing.ComparatorRejected || missing.RetainedInputUnchanged || !strings.Contains(missing.Reason, "read retained source") {
		t.Fatalf("missing retained input was not reported safely: %#v", missing)
	}
}

func TestEvidenceWritersPreservePayloadsAndPrivateModes(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "nested", "case")
	result := portdiff.Result{
		ExitCode:    3,
		Signal:      "SIGTERM",
		TimedOut:    true,
		Stdout:      []byte("stdout evidence\n"),
		Stderr:      []byte("stderr evidence\n"),
		FilesBefore: []portdiff.ManifestEntry{},
		Files:       []portdiff.ManifestEntry{},
	}
	if err := writeResult(directory, "go", result); err != nil {
		t.Fatalf("writeResult: %v", err)
	}
	for name, want := range map[string][]byte{
		"go.stdout":            result.Stdout,
		"go.stderr":            result.Stderr,
		"go.files.before.json": []byte("[]\n"),
		"go.files.after.json":  []byte("[]\n"),
	} {
		got, err := os.ReadFile(filepath.Join(directory, name)) // #nosec G304 -- fixed evidence names under the test's temporary directory.
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s = %q, err=%v; want %q", name, got, err, want)
		}
	}
	statusBytes, err := os.ReadFile(filepath.Join(directory, "go.status.json")) // #nosec G304 -- fixed test-owned status fixture.
	if err != nil {
		t.Fatal(err)
	}
	var status struct {
		ExitCode int    `json:"exit_code"`
		Signal   string `json:"signal"`
		TimedOut bool   `json:"timed_out"`
	}
	if err := json.Unmarshal(statusBytes, &status); err != nil {
		t.Fatalf("decode status evidence: %v", err)
	}
	if status.ExitCode != result.ExitCode || status.Signal != result.Signal || status.TimedOut != result.TimedOut {
		t.Fatalf("status evidence = %#v", status)
	}

	description := describe(result)
	if description.ExitCode != result.ExitCode || description.Signal != result.Signal || !description.TimedOut ||
		description.StdoutBytes != len(result.Stdout) || description.StdoutSHA256 != coverageSHA256(result.Stdout) ||
		description.StderrBytes != len(result.Stderr) || description.StderrSHA256 != coverageSHA256(result.Stderr) ||
		description.FilesBefore == nil || description.FilesAfter == nil {
		t.Fatalf("process evidence metadata = %#v", description)
	}

	if runtime.GOOS != "windows" {
		for _, path := range []string{directory, filepath.Join(directory, "go.stdout")} {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm()&0o077 != 0 {
				t.Errorf("evidence path %q is not private: mode=%#o", path, info.Mode().Perm())
			}
		}
	}

	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writePrivate(filepath.Join(blocked, "child"), []byte("no")); err == nil {
		t.Fatal("writePrivate accepted a file as a parent directory")
	}
	if err := writeResult(blocked, "go", result); err == nil {
		t.Fatal("writeResult accepted an uncreatable evidence directory")
	}
	preserved, err := os.ReadFile(blocked) // #nosec G304 -- reread the test-owned sentinel inside t.TempDir.
	if err != nil || string(preserved) != "preserve" {
		t.Fatalf("blocked target changed: %q err=%v", preserved, err)
	}
}

func TestMustJSONAndPrivateExclusiveWriter(t *testing.T) {
	encoded := mustJSON(map[string]string{"status": "failed"})
	if !json.Valid(bytes.TrimSpace(encoded)) || len(encoded) == 0 || encoded[len(encoded)-1] != '\n' {
		t.Fatalf("mustJSON did not produce valid newline-terminated JSON: %q", encoded)
	}

	path := filepath.Join(t.TempDir(), "preflight.runner.stderr")
	original := []byte("exact stderr bytes\n")
	if err := writeNewPrivate(path, original); err != nil {
		t.Fatalf("writeNewPrivate: %v", err)
	}
	got, err := os.ReadFile(path) // #nosec G304 -- test-owned exclusive-writer fixture inside t.TempDir.
	if err != nil || !bytes.Equal(got, original) {
		t.Fatalf("exclusive evidence bytes = %q, err=%v", got, err)
	}
	if err := writeNewPrivate(path, []byte("overwrite")); err == nil {
		t.Fatal("writeNewPrivate overwrote existing evidence")
	}
	got, err = os.ReadFile(path) // #nosec G304 -- reread the same test-owned fixture to prove overwrite refusal.
	if err != nil || !bytes.Equal(got, original) {
		t.Fatalf("exclusive writer changed retained bytes: %q, err=%v", got, err)
	}
}

func TestMustJSONPanicsForUnsupportedValues(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("mustJSON accepted a value encoding/json cannot represent")
		}
	}()
	mustJSON(make(chan int))
}

func TestPreflightFailureReceiptPreservesRawStreamsAndReportsPartialIdentity(t *testing.T) {
	evidence := filepath.Join(t.TempDir(), "evidence")
	if err := os.Mkdir(evidence, 0o700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "not-a-git-checkout")
	stdout := []byte("runner stdout\x00\n")
	stderr := []byte("capture failed\n")
	captureErr := errors.New("pinned source unavailable")
	goBinary := filepath.Join(t.TempDir(), "missing-go-binary")
	rustBinary := filepath.Join(t.TempDir(), "missing-rust-binary")

	if err := retainPreflightFailure(evidence, goBinary, rustBinary, root, "", report{CaseCount: 4}, captureErr, stdout, stderr); err != nil {
		t.Fatalf("retainPreflightFailure: %v", err)
	}
	for name, want := range map[string][]byte{
		"preflight.runner.stdout": stdout,
		"preflight.runner.stderr": stderr,
	} {
		got, err := os.ReadFile(filepath.Join(evidence, name)) // #nosec G304 -- fixed stream names inside the test-owned evidence directory.
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s = %q err=%v; want exact bytes %q", name, got, err, want)
		}
	}
	content, err := os.ReadFile(filepath.Join(evidence, "preflight.failure.json")) // #nosec G304 -- fixed test-owned failure receipt.
	if err != nil {
		t.Fatal(err)
	}
	var receipt preflightFailure
	if err := json.Unmarshal(content, &receipt); err != nil {
		t.Fatalf("decode partial-identity receipt: %v", err)
	}
	if receipt.Status != "preflight_failed" || receipt.Phase != "capture_preflight_before_first_case_result" ||
		receipt.Error != captureErr.Error() || receipt.ExitCode != 1 || receipt.DeclaredCaseCount != 4 ||
		receipt.ExecutedCaseCount != 0 || receipt.NativeCaptureClaimed {
		t.Fatalf("preflight receipt misstates failure or execution: %#v", receipt)
	}
	if receipt.StdoutBytes != len(stdout) || receipt.StdoutSHA256 != coverageSHA256(stdout) ||
		receipt.StderrBytes != len(stderr) || receipt.StderrSHA256 != coverageSHA256(stderr) {
		t.Fatalf("preflight stream hashes do not match retained files: %#v", receipt)
	}
	if receipt.GoBinaryError == "" || receipt.RustBinaryError == "" || len(receipt.IdentityErrors) == 0 ||
		len(receipt.GoSource.Missing) == 0 || len(receipt.RustSource.Missing) == 0 ||
		len(receipt.HarnessSource.Missing) == 0 || len(receipt.GoCoreKitMissing) != len(corekitSourceFiles) {
		t.Fatalf("receipt hid incomplete source/binary identity: %#v", receipt)
	}

	if err := retainPreflightFailure(evidence, "", "", root, "", report{}, nil, nil, nil); err == nil || !strings.Contains(err.Error(), "without a capture error") {
		t.Fatalf("nil capture error result = %v", err)
	}
	if err := retainPreflightFailure(filepath.Join(t.TempDir(), "missing-evidence"), "", "", root, "", report{}, captureErr, nil, nil); err == nil || !strings.Contains(err.Error(), "inspect preflight evidence directory") {
		t.Fatalf("missing evidence directory result = %v", err)
	}
	fileEvidence := filepath.Join(t.TempDir(), "evidence-file")
	if err := os.WriteFile(fileEvidence, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := retainPreflightFailure(fileEvidence, "", "", root, "", report{}, captureErr, nil, nil); err == nil || !strings.Contains(err.Error(), "not a real directory") {
		t.Fatalf("non-directory evidence target result = %v", err)
	}
}

func TestInspectBinariesAndBuildSetting(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	identity, err := inspectGoBinary(executable)
	if err != nil {
		t.Fatalf("inspectGoBinary(test executable): %v", err)
	}
	absolute, err := filepath.Abs(executable)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(absolute) // #nosec G304 -- hash the current test executable returned by os.Executable.
	if err != nil {
		t.Fatal(err)
	}
	if identity.Path != absolute || identity.SHA256 != coverageSHA256(content) || identity.GoVersion == "" || identity.BuildPath == "" {
		t.Fatalf("Go executable identity = %#v", identity)
	}
	fileIdentity, err := inspectFileBinary(executable)
	if err != nil || fileIdentity.Path != absolute || fileIdentity.SHA256 != identity.SHA256 {
		t.Fatalf("file binary identity = %#v, err=%v", fileIdentity, err)
	}

	info := &buildinfo.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "abc123"}}}
	if got := buildSetting(info, "vcs.revision"); got != "abc123" {
		t.Fatalf("buildSetting found %q, want abc123", got)
	}
	if got := buildSetting(info, "missing.setting"); got != "" {
		t.Fatalf("buildSetting missing value = %q, want empty", got)
	}

	invalid := filepath.Join(t.TempDir(), "not-a-go-binary")
	if err := os.WriteFile(invalid, []byte("not Go build info"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectGoBinary(invalid); err == nil || !strings.Contains(strings.ToLower(err.Error()), "read go oracle build info") {
		t.Fatalf("invalid Go binary result = %v", err)
	}
	if _, err := inspectFileBinary(filepath.Join(t.TempDir(), "missing-binary")); err == nil {
		t.Fatal("inspectFileBinary accepted a missing executable")
	}

	if got, want := digest([]byte("hash me")), coverageSHA256([]byte("hash me")); got != want {
		t.Fatalf("digest = %q, want SHA-256 %q", got, want)
	}
	if errorString(nil) != "" || errorString(errors.New("capture error")) != "capture error" {
		t.Fatal("errorString did not preserve nil and non-nil error behavior")
	}
}

func newCoverageGitRepo(t *testing.T, branch, origin string) string {
	t.Helper()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	mustCoverageGit(t, root, "init", "-b", branch)
	mustCoverageGit(t, root, "config", "user.name", "Coverage Test")
	mustCoverageGit(t, root, "config", "user.email", "coverage@example.invalid")
	coverageWriteFile(t, root, "source.txt", "initial source\n")
	mustCoverageGit(t, root, "add", "source.txt")
	mustCoverageGit(t, root, "commit", "-m", "fixture")
	if origin != "" {
		mustCoverageGit(t, root, "remote", "add", "origin", origin)
	}
	return root
}

func mustCoverageGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	output, err := git(root, args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return output
}

func coverageWriteFile(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func coverageSHA256(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func TestPreflightReceiptKeepsAvailableBinaryAndModuleIdentity(t *testing.T) {
	root := newCoverageGitRepo(t, "candidate", originURL)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	identity, err := inspectGoBinary(executable)
	if err != nil {
		t.Fatal(err)
	}
	module := t.TempDir()
	for _, name := range corekitSourceFiles {
		coverageWriteFile(t, module, name, "partial source fixture\n")
	}
	evidence := t.TempDir()
	stderr := []byte("source rejected\x00\xff\n")
	captureErr := errors.New("source identity rejected before execution")
	// A test executable is inspectable, but is not the pinned production oracle.
	// Its available identity must survive without turning the failure into parity.
	if err := retainPreflightFailure(evidence, executable, executable, root, module, report{}, captureErr, nil, stderr); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(evidence, "preflight.failure.json")) // #nosec G304 -- fixed receipt in the test-owned directory.
	if err != nil {
		t.Fatal(err)
	}
	var receipt preflightFailure
	if err := json.Unmarshal(content, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.GoBinary != identity || receipt.GoBinaryError != "" || receipt.RustBinaryError != "" || receipt.RustBinary.SHA256 != identity.SHA256 {
		t.Fatalf("available executable identity was lost: %#v", receipt)
	}
	if receipt.Head != mustCoverageGit(t, root, "rev-parse", "HEAD") || receipt.Branch != "candidate" || receipt.Origin != originURL || receipt.GitStatus != "" {
		t.Fatalf("checkout identity was lost: %#v", receipt)
	}
	if receipt.NativeCaptureClaimed || receipt.ExecutedCaseCount != 0 || receipt.DeclaredCaseCount != 0 || receipt.Error != captureErr.Error() || receipt.ExitCode != 1 {
		t.Fatalf("preflight failure was misclassified: %#v", receipt)
	}
	if len(receipt.GoCoreKitFiles) != len(corekitSourceFiles) || len(receipt.GoCoreKitMissing) != 0 {
		t.Fatalf("available module files were not retained: %#v", receipt)
	}
	for _, name := range corekitSourceFiles {
		if receipt.GoCoreKitFiles[name] != coverageSHA256([]byte("partial source fixture\n")) {
			t.Errorf("wrong retained source hash for %s", name)
		}
	}
	if !strings.Contains(strings.Join(receipt.IdentityErrors, "\n"), "CoreKit source: unexpected CoreKit binary identity") {
		t.Fatalf("unpinned executable was not diagnosed: %v", receipt.IdentityErrors)
	}
	if err := retainPreflightFailure(evidence, executable, executable, root, module, report{}, captureErr, nil, nil); err == nil {
		t.Fatal("a repeated failure overwrote retained evidence")
	}
	retained, err := os.ReadFile(filepath.Join(evidence, "preflight.runner.stderr")) // #nosec G304 -- reread the same test-owned failure stream.
	if err != nil || !bytes.Equal(retained, stderr) || receipt.StderrSHA256 != coverageSHA256(stderr) {
		t.Fatalf("original failure stream changed: bytes=%v err=%v", retained, err)
	}
}
