package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

const (
	phasePrefix   = "SYMDESK_INDEX_STATUS_PHASE "
	timeoutPrefix = "SYMDESK_INDEX_STATUS_TIMEOUT "
)

type indexStatusRequest struct {
	Documents     bool
	State         string
	VaultOverride string
	Timeout       time.Duration
	JSON          bool
}

type indexStatusPhaseReporter func(phase string)

var (
	indexStatusRun           = runIndexStatusChild
	indexStatusWorkerCmd     = defaultIndexStatusWorkerCmd
	indexStatusOnWorkerStart func(pid int)
)

func defaultIndexStatusWorkerCmd(ctx context.Context, req indexStatusRequest) (*exec.Cmd, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve executable: %w", err)
	}
	var args []string
	if req.VaultOverride != "" {
		args = append(args, "--vault", req.VaultOverride)
	}
	args = append(args, "index", "status", "--worker", "--timeout", req.Timeout.String())
	if req.Documents {
		args = append(args, "--documents")
	}
	if req.State != "" {
		args = append(args, "--state", req.State)
	}
	if req.JSON {
		args = append(args, "--json")
	}
	cmd := exec.Command(exe, args...) // #nosec G204 -- exe is os.Executable and args are allowlisted status flags.
	return cmd, nil
}

func runIndexStatusChild(
	ctx context.Context,
	req indexStatusRequest,
	report indexStatusPhaseReporter,
) ([]byte, error) {
	cmd, err := indexStatusWorkerCmd(ctx, req)
	if err != nil {
		return nil, err
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Stdin = nil

	prepareIndexStatusWorker(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	if indexStatusOnWorkerStart != nil && cmd.Process != nil {
		indexStatusOnWorkerStart(cmd.Process.Pid)
	}

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	var runErr error
	var timedOut bool
	select {
	case runErr = <-waitCh:
	case <-ctx.Done():
		killIndexStatusWorker(cmd)
		<-waitCh // always join the wait goroutine
		runErr = ctx.Err()
		timedOut = true
	}

	lastPhase, hasTimeoutMarker := parseIndexStatusStderr(stderr.Bytes())
	if report != nil {
		report(lastPhase)
	}

	if timedOut || errors.Is(runErr, context.DeadlineExceeded) || errors.Is(runErr, context.Canceled) {
		return nil, &IndexStatusTimeoutError{
			Phase:   lastPhase,
			Timeout: req.Timeout,
			Err:     runErr,
		}
	}

	if hasTimeoutMarker {
		return nil, &IndexStatusTimeoutError{
			Phase:   lastPhase,
			Timeout: req.Timeout,
			Err:     context.DeadlineExceeded,
		}
	}

	if runErr != nil {
		cleanStderr := extractCleanStderr(stderr.Bytes())
		if cleanStderr != "" {
			return nil, errors.New(cleanStderr)
		}
		return nil, runErr
	}

	return stdout.Bytes(), nil
}

func parseIndexStatusStderr(stderr []byte) (lastPhase string, isTimeout bool) {
	scanner := bufio.NewScanner(bytes.NewReader(stderr))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, phasePrefix) {
			lastPhase = strings.TrimSpace(strings.TrimPrefix(line, phasePrefix))
		} else if strings.HasPrefix(line, timeoutPrefix) {
			lastPhase = strings.TrimSpace(strings.TrimPrefix(line, timeoutPrefix))
			isTimeout = true
		}
	}
	if lastPhase == "" {
		lastPhase = "worker startup"
	}
	return lastPhase, isTimeout
}

func extractCleanStderr(stderr []byte) string {
	var lines []string
	scanner := bufio.NewScanner(bytes.NewReader(stderr))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, phasePrefix) && !strings.HasPrefix(line, timeoutPrefix) && line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

func runIndexStatusInProcess(
	ctx context.Context,
	req indexStatusRequest,
	report indexStatusPhaseReporter,
) ([]byte, error) {
	if report == nil {
		report = func(string) {}
	}
	report("worker startup")
	if err := ctx.Err(); err != nil {
		return nil, indexStatusPhaseError(ctx, req.Timeout, "worker startup", err)
	}

	if req.Documents {
		report("resolve vault root")
		if err := ctx.Err(); err != nil {
			return nil, indexStatusPhaseError(ctx, req.Timeout, "resolve vault root", err)
		}
		vRoot, err := vault.ResolveVaultRoot(req.VaultOverride, cfg)
		if err != nil {
			return nil, indexStatusPhaseError(ctx, req.Timeout, "resolve vault root", err)
		}
		if err := ctx.Err(); err != nil {
			return nil, indexStatusPhaseError(ctx, req.Timeout, "resolve vault root", err)
		}

		report("open sidecar database")
		db, err := indexStatusSidecarOpenFunc(vRoot)
		if err != nil {
			return nil, indexStatusPhaseError(ctx, req.Timeout, "open sidecar database", err)
		}
		defer closeWithWarning("sidecar database", db.Close)
		if err := ctx.Err(); err != nil {
			return nil, indexStatusPhaseError(ctx, req.Timeout, "open sidecar database", err)
		}

		report("document status listing")
		rows, err := indexStatusDocumentsFunc(ctx, db, sidecar.IndexState(req.State))
		if err != nil {
			return nil, indexStatusPhaseError(ctx, req.Timeout, "document status listing", err)
		}
		if err := ctx.Err(); err != nil {
			return nil, indexStatusPhaseError(ctx, req.Timeout, "document status listing", err)
		}

		return formatIndexStatusResult(rows, req.JSON)
	}

	report("retrieval status")
	if err := ctx.Err(); err != nil {
		return nil, indexStatusPhaseError(ctx, req.Timeout, "retrieval status", err)
	}
	status, err := indexStatusRetrievalFunc(ctx)
	if err != nil {
		return nil, indexStatusPhaseError(ctx, req.Timeout, "retrieval status", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, indexStatusPhaseError(ctx, req.Timeout, "retrieval status", err)
	}

	hasVault := req.VaultOverride != "" || (cfg != nil && cfg.Vault != "")
	if hasVault {
		report("resolve vault root")
		if err := ctx.Err(); err != nil {
			return nil, indexStatusPhaseError(ctx, req.Timeout, "resolve vault root", err)
		}
		vRoot, resolveErr := vault.ResolveVaultRoot(req.VaultOverride, cfg)
		if resolveErr != nil {
			return nil, indexStatusPhaseError(ctx, req.Timeout, "resolve vault root", resolveErr)
		}
		if err := ctx.Err(); err != nil {
			return nil, indexStatusPhaseError(ctx, req.Timeout, "resolve vault root", err)
		}

		report("vault counting")
		vaultCount := 0
		if walkErr := indexStatusVaultWalkFunc(ctx, vRoot, func(path string) error {
			vaultCount++
			return nil
		}); walkErr != nil {
			return nil, indexStatusPhaseError(ctx, req.Timeout, "vault counting", walkErr)
		}
		if err := ctx.Err(); err != nil {
			return nil, indexStatusPhaseError(ctx, req.Timeout, "vault counting", err)
		}
		status.VaultDocumentCount = &vaultCount
	}

	return formatIndexStatusResult(status, req.JSON)
}

func formatIndexStatusResult(data any, isJSON bool) ([]byte, error) {
	if isJSON {
		b, err := json.Marshal(data)
		if err != nil {
			return nil, err
		}
		return append(b, '\n'), nil
	}
	return []byte(fmt.Sprintf("%+v\n", data)), nil
}

func indexStatusPhaseError(
	ctx context.Context,
	timeout time.Duration,
	phase string,
	err error,
) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || (ctx != nil && ctx.Err() != nil) {
		errToReport := err
		if errToReport == nil || (!errors.Is(errToReport, context.DeadlineExceeded) && !errors.Is(errToReport, context.Canceled)) {
			if ctx != nil && ctx.Err() != nil {
				errToReport = ctx.Err()
			}
		}
		return &IndexStatusTimeoutError{
			Phase:   phase,
			Timeout: timeout,
			Err:     errToReport,
		}
	}
	return err
}
