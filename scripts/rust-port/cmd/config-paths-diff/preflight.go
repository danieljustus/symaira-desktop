package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// retainPreflightFailure records the producer process's exact failure streams
// and the source inventories available before any CLI case was executed.
func retainPreflightFailure(
	evidenceDir, goBinary, rustBinary, repoRoot, corekitDir string,
	capture report, captureErr error, stdout, stderr []byte,
) error {
	if captureErr == nil {
		return errors.New("preflight receipt requested without a capture error")
	}
	directory, err := filepath.Abs(evidenceDir)
	if err != nil {
		return fmt.Errorf("resolve preflight evidence directory: %w", err)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return fmt.Errorf("inspect preflight evidence directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("preflight evidence path is not a real directory: %s", directory)
	}

	root := resolvedPath(repoRoot)
	head := capture.Head
	var identityErrors []string
	if head == "" {
		if value, err := git(root, "rev-parse", "HEAD"); err == nil {
			head = value
		} else {
			identityErrors = append(identityErrors, "HEAD: "+err.Error())
		}
	}
	receipt := preflightFailure{
		SchemaVersion:        1,
		Status:               "preflight_failed",
		Phase:                "source_preflight",
		Error:                captureErr.Error(),
		ExitCode:             1,
		Worktree:             root,
		GoSource:             observeSourceIdentity(root, foundation, goSourceFiles),
		RustSource:           observeSourceIdentity(root, head, rustSourceFiles),
		HarnessSource:        observeSourceIdentity(root, head, harnessSourceFiles),
		GoCoreKitInputs:      append([]string(nil), corekitSourceFiles...),
		GoCoreKitFiles:       make(map[string]string, len(corekitSourceFiles)),
		GoRunner:             runtime.Version(),
		HostOS:               runtime.GOOS,
		HostArch:             runtime.GOARCH,
		IdentityErrors:       identityErrors,
		DeclaredCaseCount:    capture.CaseCount,
		ExecutedCaseCount:    capture.ExecutedCount,
		NativeCaptureClaimed: false,
		StdoutFile:           "preflight.runner.stdout",
		StdoutBytes:          len(stdout),
		StdoutSHA256:         digest(stdout),
		StderrFile:           "preflight.runner.stderr",
		StderrBytes:          len(stderr),
		StderrSHA256:         digest(stderr),
	}
	if capture.CaseCount != 0 {
		receipt.Phase = "capture_preflight_before_first_case_result"
	}
	if value, err := git(root, "remote", "get-url", "origin"); err == nil {
		receipt.Origin = value
	} else {
		receipt.IdentityErrors = append(receipt.IdentityErrors, "origin: "+err.Error())
	}
	if value, err := git(root, "branch", "--show-current"); err == nil {
		receipt.Branch = value
	} else {
		receipt.IdentityErrors = append(receipt.IdentityErrors, "branch: "+err.Error())
	}
	if value, err := git(root, "rev-parse", "HEAD"); err == nil {
		receipt.Head = value
	} else {
		receipt.IdentityErrors = append(receipt.IdentityErrors, "HEAD: "+err.Error())
	}
	if value, err := git(root, "status", "--porcelain=v1", "--untracked-files=all"); err == nil {
		receipt.GitStatus = value
	} else {
		receipt.IdentityErrors = append(receipt.IdentityErrors, "status: "+err.Error())
	}

	if goBinary != "" {
		if value, err := inspectGoBinary(goBinary); err == nil {
			receipt.GoBinary = value
			corekitDirectory := resolvedPath(corekitDir)
			receipt.GoCoreKit.Directory = corekitDirectory
			receipt.GoCoreKit.Path = "github.com/danieljustus/symaira-corekit"
			receipt.GoCoreKit.Version = value.CoreKitVersion
			receipt.GoCoreKit.ModuleSum = value.CoreKitModuleSum
			if manifest, err := moduleManifest(corekitDirectory, value); err == nil {
				receipt.GoCoreKit = manifest
				receipt.GoCoreKitFiles = manifest.Files
			} else {
				receipt.IdentityErrors = append(receipt.IdentityErrors, "CoreKit source: "+err.Error())
			}
		} else {
			receipt.GoBinaryError = err.Error()
		}
	}
	if rustBinary != "" {
		if value, err := inspectFileBinary(rustBinary); err == nil {
			receipt.RustBinary = value
		} else {
			receipt.RustBinaryError = err.Error()
		}
	}
	if runnerPath, err := os.Executable(); err == nil {
		if value, err := inspectGoBinary(runnerPath); err == nil {
			receipt.HarnessBinary = value
		} else {
			receipt.HarnessBinaryError = err.Error()
		}
	} else {
		receipt.HarnessBinaryError = err.Error()
	}
	corekitDirectory := resolvedPath(corekitDir)
	if receipt.GoCoreKit.Directory == "" {
		receipt.GoCoreKit.Directory = corekitDirectory
		receipt.GoCoreKit.Path = "github.com/danieljustus/symaira-corekit"
	}
	for _, relative := range corekitSourceFiles {
		if corekitDirectory == "" {
			receipt.GoCoreKitMissing = append(receipt.GoCoreKitMissing, relative)
			continue
		}
		content, readErr := os.ReadFile(filepath.Join(corekitDirectory, filepath.FromSlash(relative)))
		if readErr != nil {
			receipt.GoCoreKitMissing = append(receipt.GoCoreKitMissing, relative)
			continue
		}
		receipt.GoCoreKitFiles[relative] = digest(content)
	}

	content, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return fmt.Errorf("encode preflight failure receipt: %w", err)
	}
	content = append(content, '\n')
	if err := writeNewPrivate(filepath.Join(directory, receipt.StdoutFile), stdout); err != nil {
		return err
	}
	if err := writeNewPrivate(filepath.Join(directory, receipt.StderrFile), stderr); err != nil {
		return err
	}
	if err := writeNewPrivate(filepath.Join(directory, "preflight.failure.json"), content); err != nil {
		return err
	}
	return nil
}

func observeSourceIdentity(root, commit string, inputs []string) sourceIdentity {
	identity := sourceIdentity{
		Commit: commit,
		Inputs: append([]string(nil), inputs...),
		Files:  make(map[string]string, len(inputs)),
	}
	if root == "" {
		identity.InventoryErrors = append(identity.InventoryErrors, "worktree path is empty")
		return identity
	}
	if tree, err := git(root, "rev-parse", commit+"^{tree}"); err == nil {
		identity.Tree = tree
	} else {
		identity.InventoryErrors = append(identity.InventoryErrors, "source tree: "+err.Error())
	}
	for _, relative := range inputs {
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			identity.Missing = append(identity.Missing, relative)
			identity.InventoryErrors = append(identity.InventoryErrors, fmt.Sprintf("read %s: %v", relative, err))
			continue
		}
		identity.Files[relative] = digest(content)
	}
	return identity
}

func resolvedPath(path string) string {
	if path == "" {
		return ""
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		return resolved
	}
	return absolute
}

func writeNewPrivate(path string, content []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create preflight evidence %s: %w", filepath.Base(path), err)
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return fmt.Errorf("write preflight evidence %s: %w", filepath.Base(path), err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync preflight evidence %s: %w", filepath.Base(path), err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close preflight evidence %s: %w", filepath.Base(path), err)
	}
	return nil
}
